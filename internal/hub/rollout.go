package hub

import (
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
// 10%, then everyone. A stage goes out only when every agent of the previous
// one is back on the new version and has run on it for rolloutSoak. An agent
// not back within updateWait of being told to update (still gone, or back on
// its old version because the launcher rolled the update back) halts the
// rollout and raises update:agents until someone resumes it.
//
// The state lives in memory: a restarted hub starts over, except that an
// open update:agents incident for the same version keeps the rollout halted.

const (
	rolloutKey = "update:agents"
	// rolloutName prefixes the target version in the incident's name; a
	// restarted hub reads the version back from it.
	rolloutName     = "uptimeforge-agent "
	rolloutSoak     = 10 * time.Minute
	updateWait      = 10 * time.Minute
	maxUpdateErrLen = 300
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
	InFlight []string `json:"in_flight"` // told to update, not back on Version yet
	// Failed agents refused the update or did not come back; they are not
	// sent this version again.
	Failed []RolloutFailure `json:"failed"`
	Halted bool             `json:"halted"`
	// NeedReinstall agents are older and do not run under the launcher, so
	// they cannot update themselves.
	NeedReinstall []string `json:"need_reinstall"`
}

type RolloutFailure struct {
	Probe string `json:"probe"`
	Error string `json:"error"`
}

type rollout struct {
	init       bool   // the state was set up for target (and a restored halt adopted)
	target     string // the version being rolled out
	stage      int
	population int                  // agents in the rollout when it started
	members    []string             // told to update in the current stage
	back       map[string]bool      // members back on the target
	lastBack   time.Time            // when the last of them came back
	inFlight   map[string]time.Time // told to update, not back yet: when
	stragglers map[string]bool      // in flight past updateWait: why the rollout halted
	failed     map[string]string    // never sent this target again: why
	halted     bool
}

func newRollout(target string) rollout {
	return rollout{init: true, target: target, back: map[string]bool{}, inFlight: map[string]time.Time{},
		stragglers: map[string]bool{}, failed: map[string]string{}}
}

// rolloutOn says what the hub rolls out: nothing for a dev build or when
// updates are off. Callers hold h.mu.
func (h *Hub) rolloutOn() bool {
	return update.IsRelease(h.version) && h.cfg.AgentUpdateMode() != AgentUpdateOff
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
		case !update.IsRelease(v) || update.Compare(v, r.target) > 0:
		case !p.hello.Launcher:
			reinstall = append(reinstall, id)
		case r.inFlight[id].IsZero() && r.failed[id] == "":
			candidates = append(candidates, id)
		}
	}
	return updated, candidates, reinstall
}

// rolloutStep moves the rollout on: notes who came back, halts on who did
// not, sends the next stage when it is due. Callers hold h.mu.
func (h *Hub) rolloutStep(now time.Time) {
	if !h.rolloutOn() {
		if h.roll.init && h.roll.target == "" && h.incidents[rolloutKey] == nil {
			return
		}
		h.roll = newRollout("") // off, or a dev build: nothing is rolled out or pending
		h.rolloutIncident(now)
		return
	}
	if !h.roll.init || h.roll.target != h.version {
		h.roll = newRollout(h.version)
		h.adoptHalt(now)
	}
	r := &h.roll
	for id, sent := range r.inFlight {
		p := h.probes[id]
		switch {
		case p == nil || p.cfg.Virtual(): // deleted: nothing to wait for
			delete(r.inFlight, id)
			delete(r.stragglers, id)
		case p.online && p.hello.Version == r.target:
			delete(r.inFlight, id)
			delete(r.stragglers, id)
			if slices.Contains(r.members, id) {
				r.back[id], r.lastBack = true, now
			}
		case now.Sub(sent) >= updateWait:
			r.stragglers[id], r.halted = true, true
		}
	}
	for id := range r.failed {
		if h.probes[id] == nil {
			delete(r.failed, id)
		}
	}
	h.rolloutIncident(now)
	if r.halted || now.Before(h.started.Add(startupGrace)) {
		return // halted, or agents are still reconnecting after the hub start
	}

	updated, candidates, _ := h.rolloutView()
	switch {
	case r.stage >= 3:
		h.sendUpdates(candidates, now) // the rollout is open: whoever turns up old
	case len(r.inFlight) > 0:
		// the stage is not back yet
	case len(r.back) == 0:
		// Nothing proven at this stage yet: it is the first, or all its
		// agents refused the update. (Again) as many as the stage takes.
		if len(candidates) == 0 {
			return
		}
		if r.stage == 0 {
			r.stage, r.population = 1, len(candidates)+updated
		}
		h.startStage(candidates, now)
	case now.Sub(r.lastBack) >= rolloutSoak && len(candidates) > 0:
		r.stage++
		h.startStage(candidates, now)
	}
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
	r.members, r.back = h.sendUpdates(candidates[:min(n, len(candidates))], now), map[string]bool{}
}

// sendUpdates tells agents to install the target, through the same queue as
// every other message to them, and returns those it reached.
func (h *Hub) sendUpdates(ids []string, now time.Time) []string {
	r := &h.roll
	msg, err := proto.Encode(proto.TypeUpdate, proto.Update{Version: r.target})
	if err != nil {
		h.log.Error("encode update", "err", err)
		return nil
	}
	var sent []string
	for _, id := range ids {
		if p := h.probes[id]; p.conn != nil && p.conn.send(msg) {
			r.inFlight[id] = now
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
		if h.incidents[rolloutKey] != nil {
			h.transition(rolloutKey, model.KindOK, "", nil, meta{}, now)
		}
		return
	}
	var lines []model.ProbeLine
	for _, id := range slices.Sorted(maps.Keys(r.stragglers)) {
		p := h.probes[id]
		l := h.probeLine(p, false, "update_missing", fmt.Sprintf("not back on %s within %s of the update", r.target, updateWait))
		if p.online {
			l.Reason, l.Error = "update_rolled_back", fmt.Sprintf("back on %s: the update to %s was rolled back", p.hello.Version, r.target)
		}
		lines = append(lines, l)
	}
	h.transition(rolloutKey, model.KindAgentUpdate, "", lines, meta{name: rolloutName + r.target, typ: "update", severity: "warning"}, now)
}

// adoptHalt takes over the halt of a hub run before a restart: its open
// incident names the stragglers. An incident about another version is over.
func (h *Hub) adoptHalt(now time.Time) {
	inc := h.incidents[rolloutKey]
	if inc == nil {
		return
	}
	r := &h.roll
	if strings.TrimPrefix(inc.Name, rolloutName) == r.target {
		for _, l := range inc.Lines {
			if h.probes[l.Probe] != nil {
				r.inFlight[l.Probe], r.stragglers[l.Probe], r.halted = now.Add(-updateWait), true, true
			}
		}
	}
	// Whatever was not adopted (another version, deleted agents) is closed
	// or updated by the step's rolloutIncident.
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
	st.InFlight = append(st.InFlight, slices.Sorted(maps.Keys(r.inFlight))...)
	for _, id := range slices.Sorted(maps.Keys(r.failed)) {
		st.Failed = append(st.Failed, RolloutFailure{Probe: id, Error: r.failed[id]})
	}
	st.NeedReinstall = append(st.NeedReinstall, reinstall...)
	return st
}

// ResumeRollout lifts a halt. The agents that did not come back are not sent
// the version again; the incident closes and the stages go on.
func (h *Hub) ResumeRollout() {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := &h.roll
	if !r.halted {
		return
	}
	for id := range r.stragglers {
		delete(r.inFlight, id)
		r.failed[id] = fmt.Sprintf("not back on %s within %s of the update", r.target, updateWait)
	}
	r.stragglers, r.halted = map[string]bool{}, false
	h.log.Info("agent update resumed", "version", r.target)
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
	r.failed[probeID] = msg
	delete(r.inFlight, probeID)
	delete(r.stragglers, probeID)
	h.log.Warn("agent refused the update", "probe", probeID, "version", r.target, "err", msg)
	h.rolloutStep(h.now())
}
