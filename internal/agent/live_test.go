package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/wh3r3ar3you/uptimeforge/internal/proto"
)

// A live command makes the agent send metrics at its interval until the TTL
// runs out, then fall back to the normal one; a repeated live extends it.
func TestLiveMetrics(t *testing.T) {
	var mu sync.Mutex
	var points []time.Time
	connCh := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		msg, _ := proto.Encode(proto.TypeAssign, proto.Assign{Probe: "p"})
		c.Write(r.Context(), websocket.MessageText, msg)
		connCh <- c
		for {
			_, data, err := c.Read(r.Context())
			if err != nil {
				return
			}
			var env proto.Envelope
			json.Unmarshal(data, &env)
			if env.Type == proto.TypeMetrics {
				mu.Lock()
				points = append(points, time.Now())
				mu.Unlock()
			}
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	a := New(ctx, Config{HubURL: srv.URL, Token: "t", MetricsInterval: 15 * time.Second, MinLive: 10 * time.Millisecond}, slog.New(slog.DiscardHandler))
	a.sys.Collect(ctx) // the first sample is slow (primes counters); keep it out of the timing
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	var conn *websocket.Conn
	select {
	case conn = <-connCh:
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not connect")
	}
	count := func(since time.Time) int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, p := range points {
			if p.After(since) {
				n++
			}
		}
		return n
	}
	live := func() {
		msg, _ := proto.Encode(proto.TypeLive, proto.Live{IntervalMS: 100, TTLMS: 500})
		if err := conn.Write(ctx, websocket.MessageText, msg); err != nil {
			t.Fatal(err)
		}
	}

	t0 := time.Now()
	live()
	time.Sleep(450 * time.Millisecond)
	if n := count(t0); n < 3 {
		t.Fatalf("live: %d points in 450ms, want >= 3", n)
	}

	// Let the TTL run out and in-flight points land, then expect silence.
	time.Sleep(400 * time.Millisecond)
	t1 := time.Now()
	time.Sleep(400 * time.Millisecond)
	if n := count(t1); n != 0 {
		t.Fatalf("%d points after the live TTL expired, want 0", n)
	}

	// A second live, renewed before it expires, outlasts a single TTL.
	t2 := time.Now()
	live()
	time.Sleep(300 * time.Millisecond)
	live()
	time.Sleep(450 * time.Millisecond) // t2+750ms, past the first TTL
	if n := count(t2.Add(600 * time.Millisecond)); n < 1 {
		t.Fatalf("renewed live went quiet after the first TTL (%d points)", n)
	}
}

// Below 500 ms in production: the interval is raised; MinLive is the test hook.
func TestLiveIntervalClamp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := New(ctx, Config{MetricsInterval: 15 * time.Second}, slog.New(slog.DiscardHandler))
	a.setLive(proto.Live{IntervalMS: 10, TTLMS: 1000})
	if got, _ := a.liveState(); got != 500*time.Millisecond {
		t.Fatalf("interval %v, want 500ms", got)
	}
	a.setLive(proto.Live{IntervalMS: 2000, TTLMS: 1000})
	if got, _ := a.liveState(); got != 2*time.Second {
		t.Fatalf("interval %v, want 2s", got)
	}
}

// A metrics-off agent ignores live.
func TestLiveIgnoredWhenMetricsOff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := New(ctx, Config{MetricsInterval: 15 * time.Second}, slog.New(slog.DiscardHandler))
	a.metricsOff.Store(true)
	a.setLive(proto.Live{IntervalMS: 1000, TTLMS: 1000})
	if _, until := a.liveState(); !until.IsZero() {
		t.Fatal("live accepted while metrics are off")
	}
}
