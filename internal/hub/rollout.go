package hub

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/model"
	"github.com/wh3r3ar3you/uptimeforge/internal/proto"
	"github.com/wh3r3ar3you/uptimeforge/internal/update"
)

// The hub updates its agents to its own version in stages: one agent, then
// 10%, then everyone. An agent is told a version once per hub run. The next
// stage goes out only when every agent of the current one is online on the
// new version and has run on it for rolloutSoak without a break. An agent of
// the stage that does not get there halts the rollout and raises
// update:agents until someone resumes it: one not back on the new version
// within updateWait of the update or of going missing, or one back on an
// older version (the launcher rolled the update back).
//
// The state lives in memory: a restarted hub starts over, except that an
// open update:agents incident for the same version keeps the rollout halted.

const (
	rolloutKey = "update:agents"
	// rolloutName prefixes the target version in the incident's name; a
	// restarted hub reads the version back from it.
	rolloutName      = "uptimeforge-agent "
	rolloutSoak      = 10 * time.Minute
	updateWait       = 10 * time.Minute
	rolloutFlapLimit = time.Hour
	maxUpdateErrLen  = 300

	// Reasons on the incident's lines.
	reasonUpdateMissing    = "update_missing"     // not back on the new version
	reasonUpdateRolledBack = "update_rolled_back" // back on an older version

	// Notes on a closed incident: why it closed without the agents coming back.
	noteRolloutResumed = "resumed"        // someone resumed the rollout (ResolvedBy)
	noteRolloutStopped = "update_stopped" // updates turned off, or the hub is another version now
)

// RolloutStatus is the rollout as the UI shows it. Names are probe IDs.
type RolloutStatus struct {
	Version string `json:"version"` // the hub's version, what agents are updated to
	Mode    string `json:"mode"`    // all, checkers, off
	// Stage is 1 (one agent), 2 (10%), 3 (everyone); 0 before the first.
	Stage int `json:"stage"`
	// Updated agents are connected on Version; Pending ones are connected,
	// can update and have not been told to yet.
	Updated  int      `json:"updated"`
	Pending  int      `json:"pending"`
	InFlight []string `json:"in_flight"` // told to update, not on Version now
	// Failed agents refused the update or did not come back; they are not
	// sent this version again.
	Failed []RolloutFailure `json:"failed"`
	Halted bool             `json:"halted"`
	// Done says there is nothing left to do: agents are on Version, none is
	// pending, in flight or being watched, whatever Stage it took. Agents
	// in Failed or NeedReinstall may remain.
	Done bool `json:"done"`
	// NeedReinstall agents are older and do not run under the launcher, so
	// they cannot update themselves.
	NeedReinstall []string `json:"need_reinstall"`
}

type RolloutFailure struct {
	Probe string `json:"probe"`
	Error string `json:"error"`
}

// watch follows an agent told to update until it has proven the version.
type watch struct {
	sent        time.Time
	on          time.Time // on the target without a break since; zero when not
	wasOn       bool      // was on the target since the send
	proven      bool      // ran a clean rolloutSoak on the target at least once
	reconnected bool      // connected again since the send
	missing     time.Time // off the target since: the send, or when it dropped off
}

type rollout struct {
	init       bool   // the state was set up for target (and a restored halt adopted)
	target     string // the version being rolled out
	stage      int
	population int      // agents in the rollout when it started
	members    []string // told to update in the current stage
	stageDone  bool     // the current stage proved the version
	told       map[string]bool
	watching   map[string]*watch
	stragglers map[string]string // why the rollout halted: probe → reason
	failed     map[string]string // never sent this target again: why
	left       map[string]bool   // moved to a dev build or a newer release
	halted     bool
	// settled says how each straggler of the open incident stopped being
	// one (outcome*); the close says so.
	settled map[string]string
}

// How a straggler stops being one.
const (
	outcomeBack    = "back"    // on the new version for good
	outcomeRemoved = "removed" // deleted from the config
	outcomeLeft    = "left"    // moved to a dev build or a newer release
	outcomeRefused = "refused" // said it would not install the update
)

func newRollout(target string) rollout {
	return rollout{init: true, target: target, told: map[string]bool{}, watching: map[string]*watch{},
		stragglers: map[string]string{}, failed: map[string]string{}, left: map[string]bool{}, settled: map[string]string{}}
}

// settle notes how a straggler stopped being one; others are not noted.
func (r *rollout) settle(id, how string) {
	if _, ok := r.stragglers[id]; ok {
		delete(r.stragglers, id)
		r.settled[id] = how
	}
}

// closeNote is the closed incident's note: none when every straggler came
// back on the new version, else what became of them.
func closeNote(settled map[string]string) string {
	hows := map[string]bool{}
	for _, how := range settled {
		hows[how] = true
	}
	switch {
	case len(hows) == 0, len(hows) == 1 && hows[outcomeBack]:
		return ""
	case len(hows) > 1:
		return "update_settled"
	case hows[outcomeRemoved]:
		return "removed"
	case hows[outcomeLeft]:
		return "update_left"
	}
	return "update_refused"
}

// rolloutOn says whether the hub rolls anything out: not a dev build, and
// updates not off. Callers hold h.mu.
func (h *Hub) rolloutOn() bool {
	return update.IsRelease(h.version) && h.cfg.AgentUpdateMode() != AgentUpdateOff
}

// older says v is a release before the target.
func (r *rollout) older(v string) bool {
	return update.IsRelease(v) && update.Compare(v, r.target) < 0
}

func (r *rollout) onTarget(p *probe) bool {
	return p.online && p.conn != nil && p.hello.Version == r.target
}

// rolloutView sorts the connected agents in scope: on the target, ready to
// be told, or too old to update themselves. Agents on a newer or a
// non-release version are left alone. Callers hold h.mu.
func (h *Hub) rolloutView() (updated int, candidates, reinstall []string) {
	r := &h.roll
	checkersOnly := h.cfg.AgentUpdateMode() == AgentUpdateCheckers
	for _, id := range slices.Sorted(maps.Keys(h.probes)) {
		p := h.probes[id]
		if p.cfg.Virtual() || !p.online || p.conn == nil || (checkersOnly && !p.cfg.Checker()) {
			continue
		}
		v := p.hello.Version
		switch {
		case v == r.target:
			updated++
		case !r.older(v):
		case !p.hello.Launcher:
			reinstall = append(reinstall, id)
		case !r.told[id]:
			candidates = append(candidates, id)
		}
	}
	return updated, candidates, reinstall
}

// rolloutAttached notes that an agent connected, before the step judges it:
// one told to update and back on its old version rolled the update back.
// Callers hold h.mu.
func (h *Hub) rolloutAttached(id string) {
	if w := h.roll.watching[id]; w != nil {
		w.reconnected = true
	}
}

// rolloutStep moves the rollout on: follows the agents told to update, halts
// on those that do not make it, sends the next stage when it is due.
// Callers hold h.mu.
func (h *Hub) rolloutStep(now time.Time) {
	if !h.rolloutOn() {
		if !h.roll.init || h.roll.target != "" {
			h.roll = newRollout("") // off, or a dev build: nothing is rolled out or pending
		}
		h.closeRollout(noteRolloutStopped, "", now)
		return
	}
	if !h.roll.init || h.roll.target != h.version {
		h.roll = newRollout(h.version)
		h.adoptHalt(now)
	}
	r := &h.roll
	for id, w := range r.watching {
		h.follow(id, w, now)
	}
	for id := range r.told { // failed and left agents were told too
		if p := h.probes[id]; p == nil || p.cfg.Virtual() {
			delete(r.told, id) // deleted: no longer part of the rollout
			delete(r.failed, id)
			delete(r.left, id)
		}
	}
	h.rolloutIncident(now)
	if r.halted || now.Before(h.started.Add(startupGrace)) {
		return // halted, or agents are still reconnecting after the hub start
	}

	updated, candidates, _ := h.rolloutView()
	if r.stage >= 3 {
		h.sendUpdates(candidates, now) // the rollout is open: whoever turns up old
		return
	}
	if !r.stageDone {
		active := h.activeMembers()
		if len(active) == 0 {
			// Nothing proven at this stage: it is the first, or all its
			// agents refused the update or left. (Again) as many as it takes.
			if len(candidates) == 0 {
				return
			}
			if r.stage == 0 {
				r.stage, r.population = 1, len(candidates)+updated
			}
			h.startStage(candidates, now)
			return
		}
		for _, id := range active {
			w := r.watching[id]
			if w == nil || w.on.IsZero() || now.Sub(w.on) < rolloutSoak || !r.onTarget(h.probes[id]) {
				return // not every agent of the stage is on the target and soaked
			}
		}
		for _, id := range active {
			delete(r.watching, id) // proven: what it does from now on is its own business
		}
		r.stageDone = true
	}
	if len(candidates) > 0 {
		r.stage++
		h.startStage(candidates, now)
	}
}

// follow judges one agent told to update. Callers hold h.mu.
func (h *Hub) follow(id string, w *watch, now time.Time) {
	r := &h.roll
	p := h.probes[id]
	switch {
	case p == nil || p.cfg.Virtual(): // deleted: nothing to wait for
		delete(r.watching, id)
		r.settle(id, outcomeRemoved)
		return
	case r.onTarget(p):
		if w.on.IsZero() {
			w.on = now
		}
		w.wasOn, w.missing = true, time.Time{}
		soaked := now.Sub(w.on) >= rolloutSoak
		w.proven = w.proven || soaked
		if !w.proven && now.Sub(w.sent) >= rolloutFlapLimit {
			// Back again, but never for a clean soak since the update: it
			// keeps dying on the new version. One that proved the version
			// and blips later is just back.
			r.stragglers[id], r.halted = reasonUpdateMissing, true
			return
		}
		r.settle(id, outcomeBack)
		if r.stage >= 3 && soaked {
			delete(r.watching, id) // proven
		}
		return
	case p.online && !r.older(p.hello.Version):
		// A dev build or a newer release: someone else took the agent over.
		delete(r.watching, id)
		r.settle(id, outcomeLeft)
		r.left[id] = true
		return
	}
	w.on = time.Time{}
	if w.missing.IsZero() {
		w.missing = now
	}
	switch {
	case p.online && w.wasOn:
		// It ran the new version and is back on an older one: rolled back.
		r.stragglers[id] = reasonUpdateRolledBack
	case now.Sub(w.missing) < updateWait && (w.proven || now.Sub(w.sent) < rolloutFlapLimit):
		return
	case p.online && w.reconnected:
		// Back on its old version without ever reaching the new one.
		r.stragglers[id] = reasonUpdateRolledBack
	default:
		r.stragglers[id] = reasonUpdateMissing
	}
	r.halted = true
}

// activeMembers are the agents of the current stage still in the rollout:
// not failed, not gone, not moved to another version.
func (h *Hub) activeMembers() []string {
	r := &h.roll
	var out []string
	for _, id := range r.members {
		if p := h.probes[id]; p != nil && !p.cfg.Virtual() && r.failed[id] == "" && !r.left[id] {
			out = append(out, id)
		}
	}
	return out
}

// startStage tells the current stage's share of candidates to update.
func (h *Hub) startStage(candidates []string, now time.Time) {
	r := &h.roll
	n := len(candidates)
	switch r.stage {
	case 1:
		n = 1
	case 2:
		n = max(1, (r.population+9)/10)
	}
	r.members, r.stageDone = h.sendUpdates(candidates[:min(n, len(candidates))], now), false
}

// sendUpdates tells agents to install the target, through the same queue as
// every other message to them, and returns those it reached. Each agent is
// told a version once.
func (h *Hub) sendUpdates(ids []string, now time.Time) []string {
	r := &h.roll
	msg, err := proto.Encode(proto.TypeUpdate, proto.Update{Version: r.target})
	if err != nil {
		h.log.Error("encode update", "err", err)
		return nil
	}
	var sent []string
	for _, id := range ids {
		if p := h.probes[id]; !r.told[id] && p.conn != nil && p.conn.send(msg) {
			r.told[id] = true
			r.watching[id] = &watch{sent: now, missing: now}
			sent = append(sent, id)
			h.log.Info("agent told to update", "probe", id, "from", p.hello.Version, "to", r.target, "stage", r.stage)
		}
	}
	return sent
}

// rolloutIncident keeps update:agents open exactly while there are
// stragglers. Callers hold h.mu.
func (h *Hub) rolloutIncident(now time.Time) {
	r := &h.roll
	if len(r.stragglers) == 0 {
		h.closeRollout(closeNote(r.settled), "", now)
		clear(r.settled)
		return
	}
	var lines []model.ProbeLine
	for _, id := range slices.Sorted(maps.Keys(r.stragglers)) {
		p := h.probes[id]
		l := h.probeLine(p, false, r.stragglers[id], fmt.Sprintf("not back on %s within %s", r.target, updateWait))
		if l.Reason == reasonUpdateRolledBack {
			l.Error = fmt.Sprintf("back on %s: the update to %s was rolled back", p.hello.Version, r.target)
		}
		lines = append(lines, l)
	}
	h.transition(rolloutKey, model.KindAgentUpdate, "", lines, meta{name: rolloutName + r.target, typ: "update", severity: "warning"}, now)
}

// closeRollout closes update:agents, saying why when it is not because the
// agents came back.
func (h *Hub) closeRollout(note, by string, now time.Time) {
	inc := h.incidents[rolloutKey]
	if inc == nil {
		h.recovered(rolloutKey) // closed by hand: the next halt is news again
		return
	}
	inc.Note, inc.ResolvedBy = note, by
	h.transition(rolloutKey, model.KindOK, "", nil, meta{}, now)
}

// adoptHalt takes over the halt of a hub run before a restart: its open
// incident names the stragglers. An incident about another version is over.
func (h *Hub) adoptHalt(now time.Time) {
	inc := h.incidents[rolloutKey]
	if inc == nil {
		return
	}
	r := &h.roll
	if strings.TrimPrefix(inc.Name, rolloutName) != r.target {
		h.closeRollout(noteRolloutStopped, "", now)
		return
	}
	for _, l := range inc.Lines {
		if h.probes[l.Probe] != nil {
			past := now.Add(-updateWait)
			r.told[l.Probe] = true
			r.watching[l.Probe] = &watch{sent: past, missing: past, wasOn: l.Reason == reasonUpdateRolledBack}
			r.stragglers[l.Probe], r.halted = l.Reason, true
		}
	}
	// Deleted agents are left out; the step's rolloutIncident updates or
	// closes the incident.
}

// Rollout is the rollout's state, a copy safe to hand out.
func (h *Hub) Rollout() RolloutStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := RolloutStatus{Version: h.version, Mode: h.cfg.AgentUpdateMode(),
		InFlight: []string{}, Failed: []RolloutFailure{}, NeedReinstall: []string{}} // "[]", not null, for the UI
	if !h.rolloutOn() || h.roll.target != h.version {
		return st
	}
	r := &h.roll
	updated, candidates, reinstall := h.rolloutView()
	st.Stage, st.Updated, st.Pending, st.Halted = r.stage, updated, len(candidates), r.halted
	for _, id := range slices.Sorted(maps.Keys(r.watching)) {
		if p := h.probes[id]; p == nil || !r.onTarget(p) {
			st.InFlight = append(st.InFlight, id)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(r.failed)) {
		st.Failed = append(st.Failed, RolloutFailure{Probe: id, Error: r.failed[id]})
	}
	st.NeedReinstall = append(st.NeedReinstall, reinstall...)
	st.Done = updated > 0 && len(candidates) == 0 && len(r.watching) == 0 && !r.halted
	return st
}

// ResumeRollout lifts a halt; by is who did it. The agents that did not come
// back are not sent the version again; the incident closes and the stages go
// on.
func (h *Hub) ResumeRollout(by string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := &h.roll
	if !r.halted {
		return
	}
	for id, reason := range r.stragglers {
		delete(r.watching, id)
		r.failed[id] = fmt.Sprintf("not back on %s within %s of the update", r.target, updateWait)
		if reason == reasonUpdateRolledBack {
			r.failed[id] = fmt.Sprintf("the update to %s was rolled back", r.target)
		}
	}
	r.stragglers, r.halted = map[string]string{}, false
	clear(r.settled)
	h.log.Info("agent update resumed", "version", r.target, "by", by)
	h.closeRollout(noteRolloutResumed, cmp.Or(by, "admin"), h.now())
	h.rolloutStep(h.now())
}

// updateResult is an agent saying it did not install an update: it is not
// sent that version again. A refusal (a bad signature, a failed download)
// leaves the agent on its working version, so it does not halt the rollout.
func (h *Hub) updateResult(probeID string, res proto.UpdateResult) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := &h.roll
	if !r.init || res.Version == "" || res.Version != r.target || h.probes[probeID] == nil {
		return
	}
	msg := clip(strings.TrimSpace(res.Error), maxUpdateErrLen)
	if msg == "" {
		msg = "update failed"
	}
	r.failed[probeID], r.told[probeID] = msg, true // told or not (a restart while it installed): not again
	delete(r.watching, probeID)
	r.settle(probeID, outcomeRefused)
	h.log.Warn("agent refused the update", "probe", probeID, "version", r.target, "err", msg)
	h.rolloutStep(h.now())
}
