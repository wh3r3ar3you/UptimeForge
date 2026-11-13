// Package hub owns probes, assigns them checks, turns their results into
// verdicts and incidents, and hands incidents to notifiers. Its config can be
// replaced at runtime (Apply) when the UI changes something.
package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/checks"
	"github.com/wh3r3ar3you/uptimeforge/internal/model"
	"github.com/wh3r3ar3you/uptimeforge/internal/proto"
	"github.com/wh3r3ar3you/uptimeforge/internal/runner"
	"github.com/wh3r3ar3you/uptimeforge/internal/sysmon"
	"github.com/wh3r3ar3you/uptimeforge/internal/version"
	"github.com/wh3r3ar3you/uptimeforge/internal/xray"
)

const (
	tickEvery       = 5 * time.Second
	localMetricsInt = 15 * time.Second
)

// Recorder persists what the hub sees. Calls happen under the hub lock, so
// implementations must only enqueue.
type Recorder interface {
	Results(probe string, rs []checks.Result)
	System(probe string, m proto.Metrics)
	Incident(inc model.Incident)
	Mutes(m map[string]time.Time)
}

type Hub struct {
	log   *slog.Logger
	httpc *http.Client

	mu         sync.Mutex
	ctx        context.Context // set by Run
	cfg        *Config
	users      map[string]*usersTrack // Remnawave users online per node, for the drop alert
	nodes      []NodeUsers            // the panel's enabled nodes at the last poll, for dependencies
	holdUntil  map[string]time.Time   // checks with parents wait this long before alerting
	downAt     map[string]time.Time   // when an incident became a kind that takes its children down
	nodesSeen  bool                   // the panel's nodes arrived at least once since the start
	seen       map[string]bool        // probes that ever connected, across restarts
	started    time.Time
	usersTotal []userSample // users online across the fleet, same polls
	loc        *time.Location
	notifiers  []model.Notifier
	rec        Recorder
	enrich     func(*model.Incident)
	probes     map[string]*probe
	probeOrder []string
	checks     map[string]*check
	checkOrder []string
	subs       map[string][]CheckConfig
	subWatch   map[string]*subWatch
	incidents  map[string]*model.Incident // open ones, by key
	mutes      map[string]time.Time       // by incident key
	lastTick   time.Time

	pmu     sync.Mutex
	pending map[string]chan checks.Result // run-once requests waiting for agents

	// now and liveTicker are the clock of live metrics (live.go); tests
	// replace them.
	now        func() time.Time
	liveTicker func(time.Duration) (<-chan time.Time, func())

	// version is the hub's own release, which agents are updated to
	// (rollout.go); tests replace it.
	version string
	roll    rollout

	// OnAgentAddr is told the public address of every agent that connects,
	// outside the hub lock; the app looks up its network and calls
	// SetNetwork. Set it before the hub serves agents.
	OnAgentAddr func(probeID string, ip netip.Addr)
}

type probe struct {
	cfg        *ProbeConfig
	via        json.RawMessage
	conn       *agentConn
	runner     *runner.Runner
	stopLocal  context.CancelFunc
	online     bool
	since      time.Time
	hello      proto.Hello
	sysinfo    *proto.SysInfo
	metrics    *proto.Metrics
	metricsAt  time.Time
	noNet      bool
	lastAssign []byte
	host       map[string]*hostState // threshold timers per metric
	addr       netip.Addr            // the agent's public address at its last connect
	network    *NetworkView          // what is known about addr (SetNetwork)

	// Live metrics (live.go).
	watchers  map[chan proto.Metrics]struct{}
	stopLive  func()        // ends the renewal loop; nil without watchers
	liveUntil time.Time     // the host was asked for live points until then
	storedAt  time.Time     // the last point that went to storage
	agg       liveAgg       // the points since then
	liveWake  chan struct{} // wakes the hub's own collector (local probe)
}

type check struct {
	cfg    CheckConfig
	probes []string
	state  map[string]*pcState
	kind   model.Kind
	region string
	// parents are the incident keys whose being open explains this check
	// failing (see parentsOf); deps is how they combine.
	parents []string
	deps    depSet
}

// pcState is one check on one probe. A probe counts as failing a check only
// after Retries consecutive failures; one success clears it.
type pcState struct {
	last  checks.Result
	at    time.Time
	fails int
	down  bool
}

func New(cfg *Config, log *slog.Logger) (*Hub, error) {
	h := &Hub{
		started:   time.Now(),
		seen:      map[string]bool{},
		log:       log,
		httpc:     &http.Client{Timeout: 30 * time.Second},
		probes:    map[string]*probe{},
		checks:    map[string]*check{},
		subs:      map[string][]CheckConfig{},
		subWatch:  map[string]*subWatch{},
		incidents: map[string]*model.Incident{},
		holdUntil: map[string]time.Time{},
		downAt:    map[string]time.Time{},
		mutes:     map[string]time.Time{},
		pending:   map[string]chan checks.Result{},
		now:       time.Now,
		version:   version.Version,
		liveTicker: func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		},
	}
	return h, h.Apply(cfg)
}

func (h *Hub) AddNotifier(n model.Notifier) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notifiers = append(h.notifiers, n)
}

// SetNotifiers replaces all notifiers (settings changed in the UI).
func (h *Hub) SetNotifiers(ns []model.Notifier) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notifiers = ns
}

// SetEnricher adds context to incidents before they are announced (e.g.
// users online on the node). It runs under the hub lock and must not block.
func (h *Hub) SetEnricher(f func(*model.Incident)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.enrich = f
}

func (h *Hub) SetRecorder(r Recorder) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rec = r
}

func (h *Hub) Location() *time.Location {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.loc
}

func (h *Hub) Config() *Config {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cfg
}

// Restore loads incidents that were open and mutes from before a restart.
func (h *Hub) Restore(open []model.Incident, mutes map[string]time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range open {
		inc := open[i]
		h.incidents[inc.Key] = &inc
		// The probe's flag must match its open incident, or the next good
		// baseline sees "no change" and the incident never resolves.
		if kind, id, _ := strings.Cut(inc.Key, ":"); kind == "probe-net" && h.probes[id] != nil {
			h.probes[id].noNet = true
		}
	}
	for k, v := range mutes {
		h.mutes[k] = v
	}
}

// Apply validates cfg and swaps it in: probes are added, changed or removed,
// checks are reassigned, subscriptions restarted when their settings change.
// Probe and check state survives for everything that still exists.
func (h *Hub) Apply(cfg *Config) error {
	if err := cfg.Normalize(); err != nil {
		return err
	}
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return err
	}
	vias := map[string]json.RawMessage{}
	for _, pc := range cfg.Probes {
		if pc.Via != "" {
			l, err := xray.ParseLink(pc.Via)
			if err != nil {
				return fmt.Errorf("probe %s: %w", pc.ID, err)
			}
			vias[pc.ID], _ = json.Marshal(l.Outbound)
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg, h.loc = cfg, loc
	now := time.Now()
	seen := map[string]bool{}
	h.probeOrder = h.probeOrder[:0]
	for i := range cfg.Probes {
		pc := &cfg.Probes[i]
		seen[pc.ID] = true
		h.probeOrder = append(h.probeOrder, pc.ID)
		p := h.probes[pc.ID]
		if p == nil {
			p = &probe{since: now}
			h.probes[pc.ID] = p
		}
		tokenChanged := p.cfg != nil && p.cfg.TokenHash != pc.TokenHash
		p.cfg, p.via = pc, vias[pc.ID]
		if !liveCapable(pc) {
			h.closeWatchers(p) // turned checker or via: nothing of ours to watch any more
		}
		keepNetworkLabels(p)
		if pc.Virtual() {
			if p.conn != nil {
				p.conn.cancel()
				p.conn = nil // so the agent's detach does not mark the virtual probe offline
			}
			h.startVirtual(p)
		} else {
			h.stopVirtual(p, now)
			if tokenChanged && p.conn != nil {
				p.conn.cancel() // token rotated: the old agent must reconnect with the new one
			}
		}
	}
	for id, p := range h.probes {
		if seen[id] {
			continue
		}
		h.stopVirtual(p, now)
		if p.conn != nil {
			p.conn.cancel()
		}
		h.closeWatchers(p)
		delete(h.probes, id)
	}
	h.rebuild()
	h.syncSubscriptions()
	return nil
}

// startVirtual launches the in-process runner (and host metrics for local
// probes). Before Run there is no context yet; Run starts them.
func (h *Hub) startVirtual(p *probe) {
	if h.ctx == nil || p.runner != nil {
		return
	}
	pid := p.cfg.ID
	p.runner = runner.New(h.ctx, h.log.With("probe", pid), func(r checks.Result) { h.Ingest(pid, []checks.Result{r}) })
	now := time.Now()
	p.online, p.since, p.lastAssign = true, now, nil
	h.transition("probe:"+pid, model.KindOK, "", nil, meta{}, now) // it may have been an offline agent
	if p.cfg.Local {
		ctx, cancel := context.WithCancel(h.ctx)
		p.stopLocal = cancel
		p.liveWake = make(chan struct{}, 1)
		go h.localMetrics(ctx, pid, p.liveWake)
	}
}

func (h *Hub) stopVirtual(p *probe, now time.Time) {
	if p.runner == nil {
		return
	}
	p.runner.Stop()
	p.runner = nil
	if p.stopLocal != nil {
		p.stopLocal()
		p.stopLocal, p.liveWake = nil, nil
	}
	p.online, p.since, p.lastAssign = false, now, nil
}

// localMetrics gives the hub's own host a row in the servers view. Like an
// agent it samples every second while its live window lasts; wake says the
// window opened or was extended.
func (h *Hub) localMetrics(ctx context.Context, probeID string, wake <-chan struct{}) {
	c := sysmon.New(nil)
	info := c.Info(ctx)
	h.mu.Lock()
	if p := h.probes[probeID]; p != nil {
		p.sysinfo = &info
	}
	h.mu.Unlock()
	every := h.localNext(probeID)
	t := time.NewTimer(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
			if next := h.localNext(probeID); next < every { // a renewal of a running window changes nothing
				every = next
				t.Reset(every)
			}
		case <-t.C:
			h.IngestMetrics(probeID, c.Collect(ctx))
			every = h.localNext(probeID)
			t.Reset(every)
		}
	}
}

// Run starts virtual probes, subscription watchers and the evaluation loop.
func (h *Hub) Run(ctx context.Context) error {
	h.mu.Lock()
	h.ctx = ctx
	for _, id := range h.probeOrder {
		if p := h.probes[id]; p.cfg.Virtual() {
			h.startVirtual(p)
		}
	}
	h.rebuild()
	h.syncSubscriptions()
	h.mu.Unlock()

	go h.heartbeat(ctx)

	t := time.NewTicker(tickEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			h.mu.Lock()
			for _, p := range h.probes {
				h.stopVirtual(p, time.Now())
				h.closeWatchers(p) // live streams and their renewals end with the hub
			}
			for _, w := range h.subWatch {
				w.cancel()
			}
			h.mu.Unlock()
			return nil
		case <-t.C:
			h.tick()
		}
	}
}

// startupGrace is how long agents get to reconnect after the hub starts
// before they count as offline.
const startupGrace = 3 * time.Minute

// MarkSeen records probes known to have connected before (they have stored
// metrics), so an agent that does not come back after a hub restart is not
// reported as one that never connected.
func (h *Hub) MarkSeen(ids []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, id := range ids {
		h.seen[id] = true
	}
}

// offlineAfter is how long probe p may be disconnected before it counts as
// offline. Callers hold h.mu.
func (h *Hub) offlineAfter(p *probe) time.Duration {
	limit := h.cfg.OfflineGrace.D()
	if !p.since.After(h.started) {
		// Not back since the hub started: after a restart every agent
		// reconnects at once, with backoff, so give them a moment.
		limit = max(limit, startupGrace)
	}
	return limit
}

func (h *Hub) tick() {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	h.lastTick = now
	for _, id := range h.probeOrder {
		p := h.probes[id]
		// A checker is a volunteer's machine: its owner switching it off is no incident.
		if !p.online && !p.cfg.Checker() && now.Sub(p.since) > h.offlineAfter(p) {
			note := "never"
			if p.hello.Version != "" || h.seen[id] {
				note = p.since.Format(time.RFC3339)
			}
			h.transition("probe:"+id, model.KindProbeOffline, "", []model.ProbeLine{h.probeLine(p, false, "offline", "")},
				meta{name: p.cfg.Name, typ: "probe", severity: "warning", note: note}, now)
		}
		if p.online {
			h.updateProbeHealth(p, now) // verdicts are re-evaluated below anyway
		}
	}
	h.evaluateAll(now)
	h.rolloutStep(h.now())

	for _, inc := range h.incidents {
		if !inc.Notified && !h.holdBack(inc, now) {
			h.emit(model.Event{Type: model.EventOpen, Incident: *inc})
		}
	}
	if re := h.cfg.Alerts.RemindEvery; re != nil && re.D() > 0 {
		for _, inc := range h.incidents {
			if inc.Severity == model.Critical && inc.AckBy == "" && inc.Notified &&
				!now.Before(inc.MutedUntil) && now.Sub(inc.LastNotified) >= re.D() {
				h.emit(model.Event{Type: model.EventRemind, Incident: *inc})
			}
		}
	}
	changed := false
	for k, until := range h.mutes {
		if now.After(until) {
			delete(h.mutes, k)
			changed = true
		}
	}
	if changed && h.rec != nil {
		h.rec.Mutes(cloneMutes(h.mutes))
	}
}

func cloneMutes(m map[string]time.Time) map[string]time.Time {
	out := make(map[string]time.Time, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// heartbeat pings the dead man's switch while the evaluation loop is alive.
func (h *Hub) heartbeat(ctx context.Context) {
	for {
		h.mu.Lock()
		url, every := h.cfg.Heartbeat.URL, h.cfg.Heartbeat.Interval.D()
		healthy := time.Since(h.lastTick) < 3*tickEvery
		h.mu.Unlock()
		if url != "" && healthy {
			if req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil); err != nil {
				h.log.Warn("heartbeat url is invalid", "err", err)
			} else if resp, err := h.httpc.Do(req); err != nil {
				h.log.Warn("heartbeat failed", "err", err)
			} else {
				resp.Body.Close()
			}
		}
		if every <= 0 {
			every = time.Minute
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}
