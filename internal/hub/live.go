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
	liveStoreEvery = localMetricsInt - liveInterval/2
	// liveSlowGap: a point this long after the previous one is not at the
	// live rate (an old agent, a lost renewal, an agent back from offline)
	// and is stored like any other.
	liveSlowGap = localMetricsInt / 2
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
	if p == nil || !liveCapable(p.cfg) || (h.ctx != nil && h.ctx.Err() != nil) { // or the hub has stopped
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

// storeDue says whether the points gathered since the last stored one go to
// storage and host alerts now; at is the newest, gap its distance from the
// one before. Every point does outside a live window (whatever rate the
// agent has) and every point not at the live rate does inside one; live
// points go at most once per liveStoreEvery. Callers hold h.mu.
func storeDue(p *probe, at time.Time, gap time.Duration) bool {
	return at.After(p.liveUntil.Add(liveGrace)) || gap >= liveSlowGap || at.Sub(p.storedAt) >= liveStoreEvery
}

// liveAgg gathers the points received since the last stored one, so what
// is stored and evaluated while live is what a usual-rate point would have
// been: rates averaged over the time the points cover, gauges the newest.
type liveAgg struct {
	n    int
	w    float64 // seconds covered by the points
	extW float64 // of them, by points that carry Ext
	sum  proto.Metrics
	ext  proto.HostExt
	last proto.Metrics
}

// add takes point m covering w seconds (since the point before it).
func (a *liveAgg) add(m proto.Metrics, w float64) {
	a.n++
	a.w += w
	a.sum.CPU += m.CPU * w
	a.sum.RX += m.RX * w
	a.sum.TX += m.TX * w
	if e := m.Ext; e != nil {
		a.extW += w
		s := &a.ext
		s.CPUUser += e.CPUUser * w
		s.CPUSystem += e.CPUSystem * w
		s.CPUIowait += e.CPUIowait * w
		s.CPUSteal += e.CPUSteal * w
		s.CPUIRQ += e.CPUIRQ * w
		s.DiskRead += e.DiskRead * w
		s.DiskWrite += e.DiskWrite * w
		s.DiskROps += e.DiskROps * w
		s.DiskWOps += e.DiskWOps * w
		s.DiskUtil += e.DiskUtil * w
		s.RXPkts += e.RXPkts * w
		s.TXPkts += e.TXPkts * w
		s.NetErr += e.NetErr * w
	}
	a.last = m
}

// result is the aggregate point: the newest one with its rate fields
// replaced by their averages. A single point is returned as it came.
func (a *liveAgg) result() proto.Metrics {
	m := a.last
	if a.n <= 1 || a.w <= 0 {
		return m
	}
	m.CPU, m.RX, m.TX = a.sum.CPU/a.w, a.sum.RX/a.w, a.sum.TX/a.w
	if m.Ext != nil && a.extW > 0 {
		e := *m.Ext // a copy: the original went to the watchers
		s, w := &a.ext, a.extW
		e.CPUUser, e.CPUSystem, e.CPUIowait = s.CPUUser/w, s.CPUSystem/w, s.CPUIowait/w
		e.CPUSteal, e.CPUIRQ = s.CPUSteal/w, s.CPUIRQ/w
		e.DiskRead, e.DiskWrite, e.DiskROps, e.DiskWOps, e.DiskUtil = s.DiskRead/w, s.DiskWrite/w, s.DiskROps/w, s.DiskWOps/w, s.DiskUtil/w
		e.RXPkts, e.TXPkts, e.NetErr = s.RXPkts/w, s.TXPkts/w, s.NetErr/w
		m.Ext = &e
	}
	return m
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
