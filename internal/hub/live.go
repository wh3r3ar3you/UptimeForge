package hub

import (
	"errors"
	"sync"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/proto"
)

// Live metrics: while someone watches a server, its agent is asked for a
// point every second and every point is handed to the watchers. Storage
// keeps its usual rate.
const (
	liveInterval = time.Second
	liveTTL      = 15 * time.Second // the agent falls back to its own interval after this
	liveRenew    = 10 * time.Second // renewed well before the TTL runs out
	// liveStoreEvery is the usual metrics interval (agents and the hub's
	// own host): while live points flow, at most one per this is stored and
	// evaluated by host alerts, so neither the database nor alert timing
	// sees the live rate. Half a live interval of slack keeps jitter from
	// pushing a stored point to the next second.
	liveStoreEvery = 15*time.Second - liveInterval/2
	// liveGrace covers the agent's live window outlasting the hub's view
	// of it: the command arrives late, so its last live points do too.
	liveGrace  = 5 * time.Second
	liveBuffer = 16 // points a watcher may fall behind before it loses some
)

// ErrNoLive is Watch's answer for a probe that has no host of ours to
// watch: a checker (someone else's machine), a via probe (the hub's own
// host under another name), or one that does not exist.
var ErrNoLive = errors.New("no live metrics for this probe")

func liveCapable(pc *ProbeConfig) bool { return pc.Via == "" && !pc.Checker() }

// Watch streams probeID's host metrics as they arrive, a point a second
// while at least one watcher remains. The channel never blocks the agent: a
// watcher that falls behind loses points. The returned func unsubscribes and
// closes the channel; calling it again does nothing. The channel is also
// closed when the probe is removed or stops being live-capable.
func (h *Hub) Watch(probeID string) (<-chan proto.Metrics, func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.probes[probeID]
	if p == nil || !liveCapable(p.cfg) {
		return nil, nil, ErrNoLive
	}
	ch := make(chan proto.Metrics, liveBuffer)
	if p.watchers == nil {
		p.watchers = map[chan proto.Metrics]struct{}{}
	}
	p.watchers[ch] = struct{}{}
	if len(p.watchers) == 1 {
		h.renewLive(p)
		tick, stopTick := h.liveTicker(liveRenew)
		done := make(chan struct{})
		p.stopLive = func() { close(done) }
		go h.liveLoop(p, tick, stopTick, done)
	}
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.unwatch(p, ch)
		})
	}, nil
}

// unwatch drops one watcher; the last one stops the renewals. Callers hold h.mu.
func (h *Hub) unwatch(p *probe, ch chan proto.Metrics) {
	if _, ok := p.watchers[ch]; !ok {
		return // already closed with the probe
	}
	delete(p.watchers, ch)
	close(ch)
	if len(p.watchers) == 0 && p.stopLive != nil {
		p.stopLive()
		p.stopLive = nil
	}
}

// closeWatchers ends every stream of p (the probe is gone or can no longer
// be watched). Callers hold h.mu.
func (h *Hub) closeWatchers(p *probe) {
	for ch := range p.watchers {
		h.unwatch(p, ch)
	}
}

// liveLoop renews the live window every liveRenew until done is closed.
func (h *Hub) liveLoop(p *probe, tick <-chan time.Time, stopTick func(), done chan struct{}) {
	defer stopTick()
	for {
		select {
		case <-done:
			return
		case <-tick:
			h.mu.Lock()
			select {
			case <-done: // the last watcher left while this tick was in flight
			default:
				h.renewLive(p)
			}
			h.mu.Unlock()
		}
	}
}

// renewLive opens or extends p's live window: the agent is told so, the
// hub's own collector is woken. Callers hold h.mu.
func (h *Hub) renewLive(p *probe) {
	p.liveUntil = h.now().Add(liveTTL)
	if p.conn != nil {
		if msg, err := proto.Encode(proto.TypeLive, proto.Live{IntervalMS: int(liveInterval / time.Millisecond), TTLMS: int(liveTTL / time.Millisecond)}); err == nil {
			p.conn.send(msg) // a busy connection skips one renewal; the next comes in 10 s
		}
	}
	if p.liveWake != nil {
		select {
		case p.liveWake <- struct{}{}:
		default:
		}
	}
}

// fanOut hands a point to every watcher of p without waiting for any.
// Callers hold h.mu.
func fanOut(p *probe, m proto.Metrics) {
	for ch := range p.watchers {
		select {
		case ch <- m:
		default:
		}
	}
}

// storePoint says whether a point measured at goes to storage and host
// alerts. Outside a live window every point does, at whatever rate the
// agent has; inside one, at most one per liveStoreEvery. Callers hold h.mu.
func storePoint(p *probe, at time.Time) bool {
	if at.After(p.liveUntil.Add(liveGrace)) {
		return true
	}
	return at.Sub(p.storedAt) >= liveStoreEvery
}

// localNext is the wait before the hub's own host is sampled again: the
// live interval while its live window lasts, else the usual one.
func (h *Hub) localNext(probeID string) time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	if p := h.probes[probeID]; p != nil && !h.now().Add(liveInterval).After(p.liveUntil) {
		return liveInterval
	}
	return localMetricsInt
}
