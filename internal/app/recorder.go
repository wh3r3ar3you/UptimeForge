package app

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/checks"
	"github.com/wh3r3ar3you/uptimeforge/internal/model"
	"github.com/wh3r3ar3you/uptimeforge/internal/proto"
	"github.com/wh3r3ar3you/uptimeforge/internal/store"
)

// recorder buffers what the hub reports and writes it in batches, so the
// hub never waits for the disk. A batch the database refused goes back
// into the buffer and is retried with the next flush: a lost resolve
// would leave its incident open in the history for good.
type recorder struct {
	mu         sync.Mutex
	samples    []store.Sample
	sys        []store.SysSample
	incidents  map[string]model.Incident
	mutes      map[string]time.Time
	mutesSet   bool
	dropped    int // samples dropped from a full buffer since the last flush
	droppedSys int
}

const bufferCap = 200_000 // drop the oldest rather than grow without bound if the disk stalls

func newRecorder() *recorder {
	return &recorder{incidents: map[string]model.Incident{}}
}

func (r *recorder) Results(probe string, rs []checks.Result) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, x := range rs {
		r.samples = append(r.samples, store.Sample{
			Check: x.CheckID, Probe: probe, At: x.At, Up: x.Status == checks.Up,
			Latency: x.LatencyMS, TTFB: x.Phases["ttfb"], BPS: x.Throughput, Reason: x.Reason,
		})
	}
	r.trim()
}

func (r *recorder) System(probe string, m proto.Metrics) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sys = append(r.sys, store.SysSample{
		Probe: probe, At: time.Now().Add(-time.Duration(m.Age * float64(time.Second))),
		CPU: m.CPU, MemUsed: float64(m.MemUsed), MemTotal: float64(m.MemTotal),
		DiskUsed: float64(m.DiskUsed), DiskTotal: float64(m.DiskTotal),
		RX: m.RX, TX: m.TX, Load1: m.Load1, Load5: m.Load5, Load15: m.Load15, Ext: sysExt(m),
	})
	r.trim()
}

// trim drops the oldest samples over bufferCap and counts them, for flush to
// report. Caller holds r.mu.
func (r *recorder) trim() {
	if over := len(r.samples) - bufferCap; over > 0 {
		r.samples, r.dropped = r.samples[over:], r.dropped+over
	}
	if over := len(r.sys) - bufferCap; over > 0 {
		r.sys, r.droppedSys = r.sys[over:], r.droppedSys+over
	}
}

func (r *recorder) Incident(inc model.Incident) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.incidents[inc.ID] = inc
}

func (r *recorder) Mutes(m map[string]time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mutes, r.mutesSet = m, true
}

// recStore is what the recorder writes to: *store.Store.
type recStore interface {
	WriteSamples(ctx context.Context, samples []store.Sample) error
	WriteSys(ctx context.Context, samples []store.SysSample) error
	SaveIncident(ctx context.Context, inc model.Incident) error
	PutSetting(ctx context.Context, key string, v any) error
	Rollup(ctx context.Context, now time.Time) error
}

// A stuck database must not stall the recorder: each write, and each
// rollup, gives up after a while and is retried later.
var (
	writeTimeout  = 30 * time.Second
	rollupTimeout = 5 * time.Minute
)

func (r *recorder) run(ctx context.Context, st recStore, log *slog.Logger) {
	flush := time.NewTicker(2 * time.Second)
	defer flush.Stop()
	rollup := time.NewTicker(time.Minute)
	defer rollup.Stop()
	for {
		select {
		case <-ctx.Done():
			// Last writes before the store closes; a short outage gets a
			// couple more tries, as incidents left here are lost. Shutdown
			// waits for this at most writeTimeout.
			sctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
			defer cancel()
			for range 3 {
				if r.flush(sctx, st, log) || sctx.Err() != nil {
					break
				}
				select {
				case <-sctx.Done():
				case <-time.After(time.Second):
				}
			}
			if n := r.pendingIncidents(); n > 0 {
				log.Error("shutdown: incident updates not saved", "count", n)
			}
			return
		case <-flush.C:
			r.flush(ctx, st, log)
		case now := <-rollup.C:
			rctx, cancel := context.WithTimeout(ctx, rollupTimeout)
			if err := st.Rollup(rctx, now); err != nil {
				log.Error("rollup", "err", err)
			}
			cancel()
		}
	}
}

func (r *recorder) pendingIncidents() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.incidents)
}

// flush writes what is buffered and puts back what failed. It reports
// whether everything was written.
func (r *recorder) flush(ctx context.Context, st recStore, log *slog.Logger) bool {
	r.mu.Lock()
	samples, sys, incidents, mutes, mutesSet := r.samples, r.sys, r.incidents, r.mutes, r.mutesSet
	dropped, droppedSys := r.dropped, r.droppedSys
	r.samples, r.sys, r.incidents, r.mutes, r.mutesSet = nil, nil, map[string]model.Incident{}, nil, false
	r.dropped, r.droppedSys = 0, 0
	r.mu.Unlock()

	if dropped > 0 || droppedSys > 0 {
		log.Warn("recorder buffer full: the database is not keeping up, oldest samples dropped",
			"dropped", dropped, "dropped_host", droppedSys)
	}
	write := func(f func(ctx context.Context) error) error {
		wctx, cancel := context.WithTimeout(ctx, writeTimeout)
		defer cancel()
		return f(wctx)
	}
	ok := true
	// failed puts a batch back for the next flush, unless the database
	// refused the data itself: then the same rows would fail forever and
	// block everything behind them.
	failed := func(what string, err error, requeue func(), attrs ...any) {
		ok = false
		if store.Permanent(err) {
			log.Error(what+": refused by the database, dropped", append(attrs, "err", err)...)
			return
		}
		log.Error(what+", will retry", append(attrs, "err", err)...)
		r.mu.Lock()
		requeue()
		r.mu.Unlock()
	}
	if err := write(func(ctx context.Context) error { return st.WriteSamples(ctx, samples) }); err != nil {
		failed("write samples", err, func() { r.samples = append(samples, r.samples...); r.trim() }, "n", len(samples))
	}
	if err := write(func(ctx context.Context) error { return st.WriteSys(ctx, sys) }); err != nil {
		failed("write host metrics", err, func() { r.sys = append(sys, r.sys...); r.trim() }, "n", len(sys))
	}
	for _, inc := range incidents {
		if err := write(func(ctx context.Context) error { return st.SaveIncident(ctx, inc) }); err != nil {
			failed("save incident", err, func() {
				if _, newer := r.incidents[inc.ID]; !newer { // the latest state wins
					r.incidents[inc.ID] = inc
				}
			}, "id", inc.ID)
		}
	}
	if mutesSet {
		if err := write(func(ctx context.Context) error { return st.PutSetting(ctx, "mutes", mutes) }); err != nil {
			failed("save mutes", err, func() {
				if !r.mutesSet {
					r.mutes, r.mutesSet = mutes, true
				}
			})
		}
	}
	return ok
}

func sysExt(m proto.Metrics) *store.SysExt {
	e := m.Ext
	if e == nil {
		return nil
	}
	return &store.SysExt{
		CPUUser: e.CPUUser, CPUSystem: e.CPUSystem, CPUIowait: e.CPUIowait, CPUSteal: e.CPUSteal, CPUIRQ: e.CPUIRQ,
		MemCache: float64(e.MemCache), SwapUsed: float64(m.SwapUsed), SwapTotal: float64(e.SwapTotal),
		DiskRead: e.DiskRead, DiskWrite: e.DiskWrite, DiskROps: e.DiskROps, DiskWOps: e.DiskWOps, DiskUtil: e.DiskUtil,
		RXPkts: e.RXPkts, TXPkts: e.TXPkts, NetErr: e.NetErr,
		TCP: float64(m.TCP), Procs: float64(e.Procs), ProcsBlocked: float64(e.ProcsD),
	}
}
