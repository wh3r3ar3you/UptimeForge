// Package runner schedules assigned checks on a probe. Remote agents and the
// hub's own virtual probes use the same runner.
package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/checks"
	"github.com/wh3r3ar3you/uptimeforge/internal/proto"
	"github.com/wh3r3ar3you/uptimeforge/internal/xray"
)

const (
	DefaultConcurrency = 16
	// After a failure the check is repeated sooner, so an outage is confirmed
	// (retries) in seconds rather than intervals.
	MaxRetryInterval = 20 * time.Second
)

type Runner struct {
	ctx  context.Context
	out  func(checks.Result)
	log  *slog.Logger
	mu   sync.Mutex
	jobs map[string]*job
	sem  chan struct{}
	via  json.RawMessage
	dial func(ctx context.Context, network, addr string) (net.Conn, error)
	viaX *xray.Instance
	// viaErr is why via could not be started; nil when it runs or is unset.
	viaErr error
	deny   map[string]bool // check types refused on this probe
}

type job struct {
	spec   checks.Spec
	key    []byte
	cancel context.CancelFunc
}

func New(ctx context.Context, log *slog.Logger, out func(checks.Result)) *Runner {
	return &Runner{ctx: ctx, out: out, log: log, jobs: map[string]*job{},
		sem: make(chan struct{}, DefaultConcurrency), dial: (&net.Dialer{}).DialContext}
}

// Apply replaces the running set of checks. Unchanged checks keep their
// schedule and counters.
func (r *Runner) Apply(a proto.Assign) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if a.Concurrency > 0 && a.Concurrency != cap(r.sem) {
		r.sem = make(chan struct{}, a.Concurrency)
	}
	// A broken upstream proxy still gets the checks applied: they report down
	// with reason "via" instead of the probe going silent. The error is
	// returned once, when the proxy changes, for the caller to log.
	var viaErr error
	if !bytes.Equal(a.Via, r.via) {
		viaErr = r.setVia(a.Via)
		for id, j := range r.jobs { // every check depends on the dialer
			j.cancel()
			delete(r.jobs, id)
		}
	}

	want := map[string]bool{}
	for _, s := range a.Checks {
		want[s.ID] = true
		key, _ := json.Marshal(s)
		if j, ok := r.jobs[s.ID]; ok {
			if bytes.Equal(j.key, key) {
				continue
			}
			j.cancel()
		}
		ctx, cancel := context.WithCancel(r.ctx)
		j := &job{spec: s, key: key, cancel: cancel}
		r.jobs[s.ID] = j
		go r.loop(ctx, j)
	}
	for id, j := range r.jobs {
		if !want[id] {
			j.cancel()
			delete(r.jobs, id)
		}
	}
	return viaErr
}

func (r *Runner) setVia(via json.RawMessage) error {
	if r.viaX != nil {
		r.viaX.Close()
		r.viaX = nil
	}
	r.via, r.dial, r.viaErr = via, (&net.Dialer{}).DialContext, nil
	if len(via) == 0 {
		return nil
	}
	var ob xray.Outbound
	err := json.Unmarshal(via, &ob)
	if err == nil {
		var inst *xray.Instance
		if inst, err = xray.Start(nil, ob, ""); err == nil {
			r.viaX, r.dial = inst, inst.DialContext
			return nil
		}
	}
	// Never fall back to direct dialing: results would claim the proxy works.
	// xray-core quotes rejected keys and values; this text is logged.
	viaErr := errors.New("probe upstream proxy: " + checks.ScrubVia(err.Error(), via))
	r.viaErr = viaErr
	r.dial = func(context.Context, string, string) (net.Conn, error) { return nil, viaErr }
	return viaErr
}

// Deny makes the runner refuse these check types, whatever the hub assigns:
// the probe's owner keeps the last word over a compromised hub (docker,
// real-browser, system-service read or start things on this host).
func (r *Runner) Deny(types []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deny = map[string]bool{}
	for _, t := range types {
		if t = strings.TrimSpace(t); t != "" {
			r.deny[t] = true
		}
	}
}

// run runs a check unless its type is denied on this probe.
func (r *Runner) run(ctx context.Context, s *checks.Spec, env *checks.Env, deny map[string]bool) checks.Result {
	if deny[s.Type] {
		return checks.Result{CheckID: s.ID, Status: checks.Down, Reason: "unavailable", At: time.Now(),
			Error: fmt.Sprintf("check type %s is disabled on this probe (UF_DENY_TYPES)", s.Type)}
	}
	return checks.Run(ctx, s, env)
}

func (r *Runner) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, j := range r.jobs {
		j.cancel()
		delete(r.jobs, id)
	}
	if r.viaX != nil {
		r.viaX.Close()
		r.viaX = nil
	}
}

// maxSpread caps how far the first run of a check is pushed back.
const maxSpread = 30 * time.Second

// firstDelay spreads first runs so a probe does not fire everything at once,
// but within half a minute: spread over a whole hourly interval, a new,
// resumed or restarted check showed "unknown" for up to an hour.
func firstDelay(interval time.Duration) time.Duration {
	return time.Duration(rand.Int64N(int64(min(interval, maxSpread))))
}

func (r *Runner) loop(ctx context.Context, j *job) {
	interval := j.spec.Interval.D()
	if interval <= 0 {
		interval = checks.DefaultInterval
	}
	retry := min(interval, MaxRetryInterval)

	delay := firstDelay(interval)
	var seq int
	var escalated bool
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}

		r.mu.Lock()
		sem, dial, via, viaErr, deny := r.sem, r.dial, r.via, r.viaErr, r.deny
		r.mu.Unlock()

		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		start := time.Now()
		res := r.run(ctx, &j.spec, &checks.Env{Dial: dial, Via: via, ViaErr: viaErr, Seq: seq, Escalated: escalated}, deny)
		<-sem
		if ctx.Err() != nil {
			return
		}
		seq++
		if res.Heavy {
			escalated = res.Status == checks.Down
		}
		r.out(res)

		next := interval
		if res.Status == checks.Down {
			next = retry
		}
		delay = max(next-time.Since(start), time.Second)
	}
}

// RunOnce runs a check immediately through this probe's dialer, outside the
// schedule and the concurrency limit (interactive use from the UI).
func (r *Runner) RunOnce(ctx context.Context, s checks.Spec) checks.Result {
	r.mu.Lock()
	dial, via, viaErr, deny := r.dial, r.via, r.viaErr, r.deny
	r.mu.Unlock()
	return r.run(ctx, &s, &checks.Env{Dial: dial, Via: via, ViaErr: viaErr}, deny)
}
