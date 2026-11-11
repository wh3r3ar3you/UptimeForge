package hub

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/checks"
	"github.com/wh3r3ar3you/uptimeforge/internal/model"
	"github.com/wh3r3ar3you/uptimeforge/internal/proto"
)

func newID() string {
	b := make([]byte, 5)
	rand.Read(b)
	return strings.ToLower(fmt.Sprintf("%x", b))
}

// Ack marks an incident as handled: no more reminders.
func (h *Hub) Ack(id, by string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	inc := h.byID(id)
	if inc == nil {
		return fmt.Errorf("incident is already resolved")
	}
	inc.AckBy = by
	h.persist(inc) // emit persists only announced incidents; this one may still be settling
	h.emit(model.Event{Type: model.EventRefresh, Incident: *inc})
	return nil
}

// Mute silences an incident's key (the check or probe) for d.
func (h *Hub) Mute(id string, d time.Duration, by string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	inc := h.byID(id)
	if inc == nil {
		return fmt.Errorf("incident is already resolved")
	}
	until := time.Now().Add(d)
	h.mutes[inc.Key] = until
	inc.MutedUntil, inc.MutedBy = until, by
	if h.rec != nil {
		h.rec.Mutes(cloneMutes(h.mutes))
	}
	h.persist(inc) // emit persists only announced incidents; this one may still be settling
	h.emit(model.Event{Type: model.EventRefresh, Incident: *inc})
	return nil
}

// untilRecovered marks a key closed by hand: nothing is raised for it until
// it recovers, then the mark goes. It lives with the mutes, so it survives a
// restart the same way.
var untilRecovered = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

// Resolve closes an open incident by hand ("it's fixed, stop telling me").
// If the thing is in fact still broken, no new incident opens until it has
// recovered and broken again.
func (h *Hub) Resolve(id, by string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	inc := h.byID(id)
	if inc == nil {
		return fmt.Errorf("incident is already resolved")
	}
	key := inc.Key
	inc.ResolvedBy, inc.Note = by, "manual"
	downAt, wasDown := h.downAt[key]
	h.transition(key, model.KindOK, "", nil, meta{}, time.Now())
	h.mutes[key] = untilRecovered // after the close, which would lift it as a recovery
	if wasDown {
		h.downAt[key] = downAt // still explains its children while it stays down
	}
	if h.rec != nil {
		h.rec.Mutes(cloneMutes(h.mutes))
	}
	return nil
}

// closedByHand reports a key under a manual resolve that has not recovered yet.
func (h *Hub) closedByHand(key string) bool { return h.mutes[key].Equal(untilRecovered) }

// recovered lifts a manual resolve once the thing is fine again.
func (h *Hub) recovered(key string) {
	if h.closedByHand(key) {
		delete(h.mutes, key)
		delete(h.downAt, key)
		if h.rec != nil {
			h.rec.Mutes(cloneMutes(h.mutes))
		}
	}
}

// forgetClosed drops the manual-close marks of keys that are gone (a check,
// probe or node removed): nothing can recover them any more, and a new
// thing reusing the id must not start silenced.
func (h *Hub) forgetClosed(gone func(key string) bool) {
	changed := false
	for key := range h.mutes {
		if h.closedByHand(key) && gone(key) {
			delete(h.mutes, key)
			delete(h.downAt, key)
			changed = true
		}
	}
	if changed && h.rec != nil {
		h.rec.Mutes(cloneMutes(h.mutes))
	}
}

func (h *Hub) byID(id string) *model.Incident {
	for _, inc := range h.incidents {
		if inc.ID == id {
			return inc
		}
	}
	return nil
}

// IngestMetrics stores the latest host snapshot of a probe.
func (h *Hub) IngestMetrics(probeID string, m proto.Metrics) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.probes[probeID]
	if p == nil {
		return
	}
	at, ok := measuredAt(time.Now(), m.Age)
	if !ok || at.Before(p.metricsAt) {
		return
	}
	p.metrics, p.metricsAt = &m, at
	if h.rec != nil {
		h.rec.System(probeID, m)
	}
	h.evalHost(p, m, at)
}

// RunOutcome is one probe's answer to a run-once request.
type RunOutcome struct {
	Probe  string         `json:"probe"`
	Name   string         `json:"name"`
	Region string         `json:"region,omitempty"`
	Result *checks.Result `json:"result,omitempty"`
	Error  string         `json:"error,omitempty"`
}

// RunOnce runs spec right now on the given probes (all online ones if empty)
// and waits for every answer: the "check this config from there" tool.
func (h *Hub) RunOnce(ctx context.Context, spec checks.Spec, probeIDs []string) []RunOutcome {
	if spec.ID == "" {
		spec.ID = "_run"
	}
	timeout := spec.Timeout.D()
	if timeout <= 0 {
		timeout = 25 * time.Second
		spec.Timeout = checks.Duration(timeout)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout+10*time.Second)
	defer cancel()

	h.mu.Lock()
	if len(probeIDs) == 0 {
		probeIDs = append(probeIDs, h.probeOrder...)
	}
	type target struct {
		out RunOutcome
		run func() (checks.Result, error)
	}
	var targets []target
	for _, id := range probeIDs {
		p := h.probes[id]
		if p == nil {
			continue
		}
		t := target{out: RunOutcome{Probe: id, Name: p.cfg.Name, Region: p.cfg.Labels[h.cfg.RegionLabel]}}
		switch {
		case p.runner != nil:
			r := p.runner
			t.run = func() (checks.Result, error) { return r.RunOnce(ctx, spec), nil }
		case p.conn != nil:
			conn := p.conn
			t.run = func() (checks.Result, error) { return h.runOnAgent(ctx, conn, spec) }
		default:
			t.out.Error = "offline"
		}
		targets = append(targets, t)
	}
	h.mu.Unlock()

	out := make([]RunOutcome, len(targets))
	done := make(chan struct{})
	for i, t := range targets {
		out[i] = t.out
		if t.run == nil {
			continue
		}
		go func(i int, run func() (checks.Result, error)) {
			defer func() { done <- struct{}{} }()
			res, err := run()
			if err != nil {
				out[i].Error = err.Error()
				return
			}
			out[i].Result = &res
		}(i, t.run)
	}
	for _, t := range targets {
		if t.run != nil {
			<-done
		}
	}
	return out
}

func (h *Hub) runOnAgent(ctx context.Context, conn *agentConn, spec checks.Spec) (checks.Result, error) {
	id := newID()
	ch := make(chan checks.Result, 1)
	h.pmu.Lock()
	h.pending[id] = ch
	h.pmu.Unlock()
	defer func() {
		h.pmu.Lock()
		delete(h.pending, id)
		h.pmu.Unlock()
	}()
	msg, err := proto.Encode(proto.TypeRun, proto.Run{ID: id, Spec: spec})
	if err != nil {
		return checks.Result{}, err
	}
	if !conn.send(msg) {
		return checks.Result{}, fmt.Errorf("agent is busy")
	}
	select {
	case r := <-ch:
		return r, nil
	case <-ctx.Done():
		return checks.Result{}, fmt.Errorf("no answer from the agent")
	}
}

func (h *Hub) runResult(rr proto.RunResult) {
	h.pmu.Lock()
	ch := h.pending[rr.ID]
	h.pmu.Unlock()
	if ch != nil {
		select {
		case ch <- rr.Result:
		default:
		}
	}
}
