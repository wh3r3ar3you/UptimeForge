package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/wh3r3ar3you/uptimeforge/internal/checks"
	"github.com/wh3r3ar3you/uptimeforge/internal/model"
	"github.com/wh3r3ar3you/uptimeforge/internal/store"
)

// fakeRecStore records what the recorder wrote, and fails while down.
type fakeRecStore struct {
	mu        sync.Mutex
	down      bool
	block     bool // writes wait for their context to end
	perm      bool // the database refuses the data itself
	samples   []store.Sample
	incidents []model.Incident
	mutes     []map[string]time.Time
}

func (f *fakeRecStore) fail(ctx context.Context) error {
	f.mu.Lock()
	down, block, perm := f.down, f.block, f.perm
	f.mu.Unlock()
	if perm {
		return &pgconn.PgError{Code: "22008", Message: "timestamp out of range"}
	}
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	if down {
		return errors.New("database is down")
	}
	return nil
}

func (f *fakeRecStore) WriteSamples(ctx context.Context, xs []store.Sample) error {
	if err := f.fail(ctx); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.samples = append(f.samples, xs...)
	return nil
}

func (f *fakeRecStore) WriteSys(ctx context.Context, xs []store.SysSample) error { return f.fail(ctx) }

func (f *fakeRecStore) SaveIncident(ctx context.Context, inc model.Incident) error {
	if err := f.fail(ctx); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.incidents = append(f.incidents, inc)
	return nil
}

func (f *fakeRecStore) PutSetting(ctx context.Context, key string, v any) error {
	if err := f.fail(ctx); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mutes = append(f.mutes, v.(map[string]time.Time))
	return nil
}

func (f *fakeRecStore) Rollup(ctx context.Context, now time.Time) error { return f.fail(ctx) }

func (f *fakeRecStore) setDown(v bool) { f.mu.Lock(); f.down = v; f.mu.Unlock() }

func quietLog() *slog.Logger { return slog.New(slog.DiscardHandler) }

// A batch that failed to write is retried, not dropped: a lost resolve
// would leave the incident open in the history forever.
func TestRecorderRetriesFailedBatch(t *testing.T) {
	r, st, ctx := newRecorder(), &fakeRecStore{down: true}, context.Background()
	now := time.Now()
	r.Results("p", []checks.Result{{CheckID: "c", At: now, Status: checks.Up}})
	r.Incident(model.Incident{ID: "i1", Key: "c", Started: now, Resolved: now})
	r.Mutes(map[string]time.Time{"c": now.Add(time.Hour)})
	r.flush(ctx, st, quietLog())

	st.setDown(false)
	r.flush(ctx, st, quietLog())
	if len(st.samples) != 1 || len(st.incidents) != 1 || st.incidents[0].Resolved.IsZero() || len(st.mutes) != 1 {
		t.Fatalf("after the database came back: %d samples, incidents %+v, %d mute saves", len(st.samples), st.incidents, len(st.mutes))
	}
}

// The latest state of an incident wins over a failed older one.
func TestRecorderRetryKeepsLatestState(t *testing.T) {
	r, st, ctx := newRecorder(), &fakeRecStore{down: true}, context.Background()
	now := time.Now()
	r.Incident(model.Incident{ID: "i1", Key: "c", Started: now})
	r.Mutes(map[string]time.Time{"old": now})
	r.flush(ctx, st, quietLog())
	r.Incident(model.Incident{ID: "i1", Key: "c", Started: now, Resolved: now.Add(time.Minute)})
	r.Mutes(map[string]time.Time{"new": now})

	st.setDown(false)
	r.flush(ctx, st, quietLog())
	r.flush(ctx, st, quietLog())
	if len(st.incidents) != 1 || st.incidents[0].Resolved.IsZero() {
		t.Fatalf("incident writes: %+v", st.incidents)
	}
	if len(st.mutes) != 1 || st.mutes[0]["new"].IsZero() {
		t.Fatalf("mute writes: %+v", st.mutes)
	}
}

// Rows the database refuses as such are dropped, not retried forever: one bad
// sample must not block every write behind it.
func TestRecorderDropsRefusedRows(t *testing.T) {
	r, st, ctx := newRecorder(), &fakeRecStore{perm: true}, context.Background()
	r.Results("p", []checks.Result{{CheckID: "c", At: time.Now(), Status: checks.Up}})
	r.Incident(model.Incident{ID: "i1", Key: "c", Started: time.Now()})
	r.flush(ctx, st, quietLog())
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.samples) != 0 || len(r.incidents) != 0 {
		t.Fatalf("refused rows kept for retry: %d samples, %d incidents", len(r.samples), len(r.incidents))
	}
}

// A database that hangs must not hang the recorder: every write has a deadline.
func TestRecorderWritesTimeOut(t *testing.T) {
	r, st := newRecorder(), &fakeRecStore{block: true}
	old := writeTimeout
	writeTimeout = 50 * time.Millisecond
	defer func() { writeTimeout = old }()
	r.Incident(model.Incident{ID: "i1", Key: "c", Started: time.Now()})
	done := make(chan struct{})
	go func() { r.flush(context.Background(), st, quietLog()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("flush hangs on a stuck database")
	}
}

// A full buffer says so instead of dropping samples silently.
func TestRecorderOverflowIsLogged(t *testing.T) {
	r, st := newRecorder(), &fakeRecStore{}
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	rs := make([]checks.Result, bufferCap+5)
	for i := range rs {
		rs[i] = checks.Result{CheckID: "c", At: time.Now(), Status: checks.Up}
	}
	r.Results("p", rs)
	r.flush(context.Background(), st, log)
	if !strings.Contains(buf.String(), "dropped") || !strings.Contains(buf.String(), "=5") {
		t.Fatalf("overflow not logged: %q", buf.String())
	}
}
