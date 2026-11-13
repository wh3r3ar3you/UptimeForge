package hub

import (
	"bytes"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/checks"
	"github.com/wh3r3ar3you/uptimeforge/internal/model"
	"github.com/wh3r3ar3you/uptimeforge/internal/proto"
)

// rebuild recomputes the effective check list and pushes assignments.
// Callers hold h.mu.
func (h *Hub) rebuild() {
	var effective []CheckConfig
	for i, b := range h.cfg.Baseline {
		effective = append(effective, h.baselineCheck(fmt.Sprintf("_baseline/%d", i), b, nil))
	}
	for _, id := range h.probeOrder {
		for i, b := range h.probes[id].cfg.Baseline {
			effective = append(effective, h.baselineCheck(fmt.Sprintf("_baseline/%s/%d", id, i), b, []string{id}))
		}
	}
	for _, c := range h.cfg.Checks {
		if c.Paused {
			continue
		}
		if c.Xray != nil && c.Xray.Subscription != "" {
			effective = append(effective, h.subs[c.ID]...)
			continue
		}
		effective = append(effective, h.cfg.withDefaults(c))
	}

	next := map[string]*check{}
	h.checkOrder = h.checkOrder[:0]
	for _, c := range effective {
		c.carriesSecrets = checks.CarriesSecrets(&c.Spec)
		ch := h.checks[c.ID]
		if ch == nil {
			ch = &check{state: map[string]*pcState{}, kind: model.KindUnknown}
		}
		ch.cfg = c
		ch.probes = ch.probes[:0]
		for _, pid := range h.probeOrder {
			p := h.probes[pid]
			if c.baseline && len(p.cfg.Baseline) > 0 && len(c.Probes) == 0 {
				continue // probe has its own baseline
			}
			if c.runsOn(p.cfg) {
				ch.probes = append(ch.probes, pid)
			}
		}
		for pid := range ch.state {
			if !contains(ch.probes, pid) {
				delete(ch.state, pid)
			}
		}
		next[c.ID] = ch
		h.checkOrder = append(h.checkOrder, c.ID)
	}
	h.checks = next
	h.computeParents()
	now := time.Now()
	for key, inc := range h.incidents { // incidents of checks/probes removed from config
		if !h.orphaned(key) {
			continue
		}
		inc.Note = "removed"
		h.transition(key, model.KindOK, "", nil, meta{}, now)
	}
	h.forgetClosed(h.orphaned)
	h.pushAssignments()
}

func (h *Hub) orphaned(key string) bool {
	configured := func(id string) bool {
		for _, c := range h.cfg.Checks {
			if c.ID == id && !c.Paused {
				return true
			}
		}
		return false
	}
	if kind, id, ok := strings.Cut(key, ":"); ok {
		switch kind {
		case "probe", "probe-net":
			return h.probes[id] == nil
		case "sub":
			return !configured(id)
		case "rw":
			return h.cfg.Remnawave == nil || h.cfg.Remnawave.DropRule().Off
		case "host":
			pid, metric, _ := strings.Cut(id, ":")
			return h.probes[pid] == nil || !h.cfg.hostRuleOn(metric)
		}
		return false
	}
	if ch := h.checks[key]; ch != nil {
		return len(ch.probes) == 0 // nobody runs it any more, so nobody could ever confirm a recovery
	}
	if parent, _, child := strings.Cut(key, "/"); child && configured(parent) {
		_, fetched := h.subs[parent]
		return fetched // until the subscription loads after a restart, keep it
	}
	return true
}

func (h *Hub) baselineCheck(id string, s checks.Spec, only []string) CheckConfig {
	s.ID = id
	if s.Interval == 0 {
		s.Interval = checks.Duration(30 * time.Second)
	}
	if s.Timeout == 0 {
		s.Timeout = checks.Duration(5 * time.Second)
	}
	var sel []string
	for _, p := range only {
		sel = append(sel, "id="+p)
	}
	return CheckConfig{Spec: s, Name: id, Probes: sel, Retries: 2, MinFailing: 1, baseline: true}
}

func (h *Hub) assignmentFor(p *probe) proto.Assign {
	a := proto.Assign{Probe: p.cfg.ID, Via: p.via, Concurrency: p.cfg.Concurrency, Checks: []checks.Spec{}}
	if p.cfg.Checker() {
		a.Metrics = new(bool) // a checker's host is not ours to watch
	}
	for _, id := range h.checkOrder {
		ch := h.checks[id]
		for _, pid := range ch.probes {
			if pid == p.cfg.ID {
				a.Checks = append(a.Checks, ch.cfg.Spec)
				break
			}
		}
	}
	return a
}

func (h *Hub) pushAssignments() {
	for _, id := range h.probeOrder {
		p := h.probes[id]
		a := h.assignmentFor(p)
		msg, err := proto.Encode(proto.TypeAssign, a)
		if err != nil {
			h.log.Error("encode assignment", "err", err)
			continue
		}
		if bytes.Equal(msg, p.lastAssign) {
			continue
		}
		switch {
		case p.runner != nil:
			if err := p.runner.Apply(a); err != nil {
				h.log.Error("virtual probe upstream proxy is broken", "probe", id, "err", err)
			}
		case p.conn != nil:
			p.conn.sendAssign(msg)
		default:
			continue // sent on connect
		}
		p.lastAssign = msg
	}
}

const (
	// maxResultsPerBatch caps one results message: the agent's whole buffer
	// (internal/agent bufferLimit). The newest are kept.
	maxResultsPerBatch = 5000
	// agentReadLimit caps one agent WebSocket message. A full buffer of
	// results with 500-character errors is about 4 MB; the agent sends it
	// in one message and does not clip errors, hence the headroom.
	agentReadLimit = 8 << 20
	// maxResultAge is the oldest result that is still taken: older ones,
	// and absurd ages, are dropped rather than placed at a made-up time.
	maxResultAge = time.Hour

	maxReasonLen  = 64
	maxErrorLen   = 1024
	maxMapEntries = 32
	maxInfoKeyLen = 64
	maxInfoValLen = 512
)

// measuredAt places a measurement reported `age` seconds old. A negative age
// counts as now: a result from the future would lock out every honest one
// until then. ok is false for one older than maxResultAge or unreadable.
func measuredAt(now time.Time, age float64) (time.Time, bool) {
	if math.IsNaN(age) || age > maxResultAge.Seconds() {
		return time.Time{}, false
	}
	if age <= 0 {
		return now, true
	}
	return now.Add(-time.Duration(age * float64(time.Second))), true
}

// sanitize caps what an agent may put in a result's strings and maps.
func sanitize(r *checks.Result) {
	r.Reason = clip(r.Reason, maxReasonLen)
	r.Error = clip(r.Error, maxErrorLen)
	oversize := len(r.Info) > maxMapEntries
	for k, v := range r.Info {
		oversize = oversize || len(k) > maxInfoKeyLen || len(v) > maxInfoValLen
	}
	if oversize {
		info := make(map[string]string, maxMapEntries)
		for _, k := range slices.Sorted(maps.Keys(r.Info)) {
			if len(info) == maxMapEntries {
				break
			}
			info[clip(k, maxInfoKeyLen)] = clip(r.Info[k], maxInfoValLen)
		}
		r.Info = info
	}
	if len(r.Phases) > maxMapEntries {
		phases := make(map[string]float64, maxMapEntries)
		for _, k := range slices.Sorted(maps.Keys(r.Phases)) {
			if len(phases) == maxMapEntries {
				break
			}
			phases[clip(k, maxInfoKeyLen)] = r.Phases[k]
		}
		r.Phases = phases
	}
}

// Ingest records results reported by a probe.
func (h *Hub) Ingest(probeID string, results []checks.Result) {
	if len(results) > maxResultsPerBatch {
		results = results[len(results)-maxResultsPerBatch:]
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.probes[probeID]
	if p == nil {
		return
	}
	now := time.Now()
	touched := map[*check]bool{}
	baseline := false
	var recorded []checks.Result
	for _, r := range results {
		ch := h.checks[r.CheckID]
		if ch == nil || !contains(ch.probes, probeID) {
			continue
		}
		at, ok := measuredAt(now, r.Age)
		if !ok {
			continue
		}
		sanitize(&r)
		st := ch.state[probeID]
		if st == nil {
			st = &pcState{}
			ch.state[probeID] = st
		}
		if at.Before(st.at) {
			continue
		}
		r.At = at
		st.last, st.at = r, at
		if !ch.cfg.baseline {
			recorded = append(recorded, r)
		}
		if r.Status == checks.Up {
			st.fails, st.down = 0, false
		} else {
			st.fails++
			st.down = st.fails >= ch.cfg.Retries
		}
		if ch.cfg.baseline {
			baseline = true
		} else {
			touched[ch] = true
		}
	}
	if h.rec != nil && len(recorded) > 0 {
		h.rec.Results(probeID, recorded)
	}
	if baseline && h.updateProbeHealth(p, now) {
		h.evaluateAll(now)
		return
	}
	for ch := range touched {
		h.evaluate(ch, now)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func staleAfter(c *CheckConfig) time.Duration {
	return max(3*c.Interval.D(), 3*time.Minute)
}

// updateProbeHealth flips the probe's "no internet" flag from its baseline
// checks. Returns true when the flag changed.
func (h *Hub) updateProbeHealth(p *probe, now time.Time) bool {
	fresh, up := 0, 0
	pending := false
	var lastErr string
	for _, id := range h.checkOrder {
		ch := h.checks[id]
		if !ch.cfg.baseline || !contains(ch.probes, p.cfg.ID) {
			continue
		}
		st := ch.state[p.cfg.ID]
		if st == nil || st.at.Before(p.since) {
			// Nothing measured since it came up: what it saw before a
			// reconnect is no news either way.
			pending = pending || now.Sub(p.since) <= staleAfter(&ch.cfg)
			continue
		}
		if now.Sub(st.at) > staleAfter(&ch.cfg) {
			continue
		}
		fresh++
		if !st.down {
			up++
		} else if lastErr == "" {
			lastErr = ch.cfg.Spec.Target() + ": " + st.last.Error
		}
	}
	if fresh == 0 && pending {
		return false // the first baseline results after a restart or connect are on their way
	}
	noNet := fresh > 0 && up == 0
	if noNet == p.noNet {
		return false
	}
	p.noNet = noNet
	kind := model.KindOK
	if noNet {
		kind = model.KindProbeNoNet
	}
	h.transition("probe-net:"+p.cfg.ID, kind, "", []model.ProbeLine{h.probeLine(p, false, "baseline", lastErr)},
		meta{name: p.cfg.Name, typ: "probe", severity: "warning"}, now)
	return true
}

func (h *Hub) probeLine(p *probe, ok bool, reason, err string) model.ProbeLine {
	return model.ProbeLine{Probe: p.cfg.ID, Name: p.cfg.Name, Region: p.cfg.Labels[h.cfg.RegionLabel], OK: ok, Reason: reason, Error: err}
}

func (h *Hub) evaluateAll(now time.Time) {
	for _, id := range h.checkOrder {
		if ch := h.checks[id]; !ch.cfg.baseline {
			h.evaluate(ch, now)
		}
	}
}

func (h *Hub) evaluate(ch *check, now time.Time) {
	var lines []model.ProbeLine
	expected := 0
	for _, pid := range ch.probes {
		p := h.probes[pid]
		st := ch.state[pid]
		fresh := st != nil && now.Sub(st.at) <= staleAfter(&ch.cfg)
		if !p.online || p.noNet || !fresh {
			if !fresh && h.awaited(p, ch, now) {
				expected++
			}
			continue
		}
		l := h.probeLine(p, !st.down, st.last.Reason, st.last.Error)
		l.LatencyMS, l.Bytes = st.last.LatencyMS, st.last.Bytes
		if l.OK {
			l.Reason, l.Error = "", ""
		}
		lines = append(lines, l)
	}
	okSilent, failSilent := h.silentWitnesses(ch, lines)
	if len(lines) > 0 {
		// A probe that saw it working and went silent still counts as
		// working: fewer probes answering is no evidence of an escalation.
		lines = append(lines, okSilent...)
	}
	ch.kind, ch.region = classifyExpecting(lines, ch.cfg.MinFailing, expected)
	if len(failSilent) > 0 {
		if ch.kind == model.KindOK {
			ch.kind = model.KindUnknown
		}
		// Failing witnesses do not make the verdict, but they stay on the
		// incident so a later update does not forget them.
		lines = append(lines, failSilent...)
	}
	h.transition(ch.cfg.ID, ch.kind, ch.region, lines, meta{
		name: ch.cfg.Name, group: ch.cfg.Group, tags: ch.cfg.Tags, typ: ch.cfg.Type, target: ch.cfg.target, severity: ch.cfg.Severity,
	}, now)
}

// awaited says probe p has not reported ch since it (or the hub) came up and
// still may: it is connected and its first results are on the way, or it has
// not reconnected since the hub started and is not confirmed offline yet.
// Such a probe is expected, not absent: after a restart agents come back one
// by one, and the first one's view is not everybody's.
func (h *Hub) awaited(p *probe, ch *check, now time.Time) bool {
	if p.noNet {
		return false
	}
	if st := ch.state[p.cfg.ID]; st != nil && !st.at.Before(p.since) {
		return false // it has reported since it came up; stale is stale
	}
	if p.online {
		return now.Sub(p.since) <= staleAfter(&ch.cfg)
	}
	return h.incidents["probe:"+p.cfg.ID] == nil && now.Sub(p.since) <= h.offlineAfter(p)
}

// silentWitnesses returns the last lines of probes that are on the open
// incident and dropped out since (offline, no internet, stale), split into
// those that saw the check working and those that saw it failing. The
// incident stays open while a failing one is silent: "the only probe that
// saw it went silent" is not a recovery, and neither is a hiccup elsewhere
// clearing up in the meantime. And a working one going silent does not turn
// "blocked in ru" into "down".
func (h *Hub) silentWitnesses(ch *check, lines []model.ProbeLine) (ok, failing []model.ProbeLine) {
	inc := h.incidents[ch.cfg.ID]
	if inc == nil {
		return nil, nil
	}
	present := map[string]bool{}
	for _, l := range lines {
		present[l.Probe] = true
	}
	for _, l := range inc.Lines {
		if present[l.Probe] || !contains(ch.probes, l.Probe) {
			continue
		}
		if l.OK {
			ok = append(ok, l)
		} else {
			failing = append(failing, l)
		}
	}
	return ok, failing
}

type meta struct {
	name, group, typ, target, severity, note string
	tags                                     []string
	host                                     *model.HostValue
	drop                                     *model.UsersDrop
}

// transition moves the incident for key according to the new verdict.
func (h *Hub) transition(key string, kind model.Kind, region string, lines []model.ProbeLine, m meta, now time.Time) {
	inc := h.incidents[key]
	switch {
	case kind == model.KindUnknown:
		return
	case kind == model.KindOK:
		h.recovered(key)
		if inc == nil {
			return
		}
		delete(h.holdUntil, key)
		delete(h.downAt, key)
		inc.Resolved, inc.Updated = now, now
		if len(lines) > 0 {
			inc.Lines = lines
		}
		inc.Host = mergeHost(inc.Host, m.host)
		inc.Drop = mergeDrop(inc.Drop, m.drop)
		delete(h.incidents, key)
		h.persist(inc)
		h.emit(model.Event{Type: model.EventResolve, Incident: *inc})
		return
	}

	sev := severityFor(kind, m.severity)
	if inc == nil && h.closedByHand(key) {
		return // closed by hand and not recovered since: stay quiet
	}
	if inc == nil {
		inc = &model.Incident{
			ID: newID(), Key: key, Name: m.name, Group: m.group, Tags: m.tags, Type: m.typ, Target: m.target, Note: m.note,
			Kind: kind, Worst: kind, Region: region, Severity: sev, Lines: lines, Started: now, Updated: now,
			Host: mergeHost(nil, m.host), Drop: mergeDrop(nil, m.drop),
		}
		if until, ok := h.mutes[key]; ok && now.Before(until) && !until.Equal(untilRecovered) {
			inc.MutedUntil = until
		}
		inc.TrackPeak()
		if h.enrich != nil {
			h.enrich(inc)
		}
		h.markDown(key, model.KindOK, kind, now)
		h.incidents[key] = inc
		h.persist(inc)
		if !h.holdBack(inc, now) {
			h.emit(model.Event{Type: model.EventOpen, Incident: *inc})
		}
		return
	}

	changed := inc.Kind != kind || inc.Region != region || inc.Severity != sev || !slicesEqual(failingSet(inc.Lines), failingSet(lines))
	escalated := kind.Rank() > inc.Kind.Rank() || (sev == model.Critical && inc.Severity != model.Critical)
	detail := !sameLines(inc.Lines, lines)
	peak := [2]int{inc.PeakFailing, inc.PeakTotal}
	host, drop := inc.Host, inc.Drop
	inc.Lines = lines
	inc.TrackPeak()
	inc.Host = mergeHost(inc.Host, m.host)
	inc.Drop = mergeDrop(inc.Drop, m.drop)
	if !changed {
		// Same verdict, maybe news for the record: another error, a new
		// peak of the metric, fewer users left.
		if detail || peak != [2]int{inc.PeakFailing, inc.PeakTotal} ||
			(inc.Host != nil && (host == nil || host.Peak != inc.Host.Peak)) ||
			(inc.Drop != nil && (drop == nil || drop.Low != inc.Drop.Low || drop.Before != inc.Drop.Before)) {
			h.persist(inc)
		}
		return
	}
	h.markDown(key, inc.Kind, kind, now)
	inc.Kind, inc.Region, inc.Severity, inc.Updated, inc.Note = kind, region, sev, now, m.note
	if m.tags != nil {
		inc.Tags = m.tags
	}
	if kind.Rank() > inc.Worst.Rank() {
		inc.Worst = kind
	}
	if h.enrich != nil {
		h.enrich(inc)
	}
	h.persist(inc)
	if !h.holdBack(inc, now) {
		h.emit(model.Event{Type: model.EventUpdate, Incident: *inc, Escalated: escalated})
	}
}

// settling holds back a fresh partial outage for about one check interval.
// Probes confirm failures at different moments, so the first one to cross
// its retries would otherwise trigger "partial" seconds before the rest turn
// it into "blocked" or "down": two pings for one event. Down and blocked are
// announced at once.
func (h *Hub) settling(inc *model.Incident, now time.Time) bool {
	ch := h.checks[inc.Key]
	if inc.Notified || inc.Kind != model.KindPartial || ch == nil {
		return false
	}
	window := min(max(ch.cfg.Interval.D(), 30*time.Second), 2*time.Minute)
	return now.Sub(inc.Started) < window
}

// sameLines says two sets of probe lines tell the same story: the same
// probes, each as working or failing for the same reason and error.
// Latency and bytes move with every result and are not news.
func sameLines(a, b []model.ProbeLine) bool {
	return slices.EqualFunc(a, b, func(x, y model.ProbeLine) bool {
		return x.Probe == y.Probe && x.OK == y.OK && x.Reason == y.Reason && x.Error == y.Error
	})
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// emit applies notification policy (mutes, quiet hours) and fans out.
// Callers hold h.mu; notifiers must not block.
func (h *Hub) emit(ev model.Event) {
	inc := h.incidents[ev.Incident.Key]
	if inc == nil { // resolved: already removed from the map
		inc = &ev.Incident
	}
	now := time.Now()
	muted := now.Before(inc.MutedUntil)
	quiet := inQuietHours(h.cfg.Alerts.QuietHours, now.In(h.loc))

	switch ev.Type {
	case model.EventOpen, model.EventUpdate:
		if muted {
			return
		}
		if !inc.Notified {
			ev.Type, ev.Escalated = model.EventOpen, false
		}
		inc.Notified = true
		if ev.Type == model.EventOpen || ev.Escalated {
			inc.LastNotified = now
		}
		ev.Silent = quiet && inc.Severity != model.Critical
	case model.EventResolve:
		if !inc.Notified {
			return
		}
		ev.Silent = quiet || muted
	case model.EventRemind:
		inc.LastNotified = now
	case model.EventRefresh:
		if !inc.Notified {
			return
		}
		ev.Silent = true
	}
	ev.Incident = cloneIncident(inc)
	h.persist(inc)
	for _, n := range h.notifiers {
		n.Notify(ev)
	}
}

// persist hands a copy of the incident to the recorder.
func (h *Hub) persist(inc *model.Incident) {
	if h.rec == nil {
		return
	}
	h.rec.Incident(cloneIncident(inc))
}

// cloneIncident is a copy of inc that shares nothing mutable with it: what
// leaves the hub lock (notifiers, the recorder, snapshots) is read while the
// hub keeps changing the original.
func cloneIncident(inc *model.Incident) model.Incident {
	c := *inc
	c.Lines = slices.Clone(inc.Lines)
	c.Affected = slices.Clone(inc.Affected)
	c.Tags = slices.Clone(inc.Tags)
	if inc.Host != nil {
		v := *inc.Host
		c.Host = &v
	}
	if inc.Drop != nil {
		v := *inc.Drop
		c.Drop = &v
	}
	return c
}
