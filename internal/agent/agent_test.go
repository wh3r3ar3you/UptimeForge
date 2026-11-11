package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/checks"
	"github.com/wh3r3ar3you/uptimeforge/internal/proto"
)

// SourceIP binds the hub connection: an address the host has works, one it
// does not have cannot be used, and no SourceIP leaves Go's default client.
func TestHubClientSourceIP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	if (&Agent{}).hubClient() != nil {
		t.Fatal("no source IP must keep the default client")
	}
	ok := (&Agent{cfg: Config{SourceIP: "127.0.0.1"}}).hubClient()
	if _, err := ok.Get(srv.URL); err != nil {
		t.Fatalf("bound to a local address: %v", err)
	}
	bad := (&Agent{cfg: Config{SourceIP: "192.0.2.1"}}).hubClient() // TEST-NET-1, never ours
	if _, err := bad.Get(srv.URL); err == nil {
		t.Fatal("an address the host does not have must not be usable")
	}
}

// Auto source: after a failed dial the agent moves through the host's public
// addresses without waiting long, and after a full round goes back to the
// kernel's choice at the normal pace. A pinned address or "off" never moves.
func TestNextSourceRotates(t *testing.T) {
	a := &Agent{local: func() []string { return []string{"198.51.100.1", "198.51.100.2"} }}
	var got []string
	var fresh []bool
	for range 4 {
		fresh = append(fresh, a.nextSource())
		got = append(got, sourceName(a.source()))
	}
	want := []string{"198.51.100.1", "198.51.100.2", "default address", "198.51.100.1"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("round: %v", got)
		}
	}
	if !fresh[0] || !fresh[1] || fresh[2] || !fresh[3] {
		t.Fatalf("untried flags: %v", fresh)
	}
	for _, cfg := range []string{"198.51.100.9", "off"} {
		p := &Agent{cfg: Config{SourceIP: cfg}, local: a.local}
		if p.nextSource() {
			t.Fatalf("%s must not rotate", cfg)
		}
		if cfg == "off" && p.hubClient() != nil {
			t.Fatal("off keeps the default client")
		}
	}
}

// A backlog of results after an outage goes out in messages the hub accepts
// (its read limit is a few MB), in order, and what failed to go is kept.
func TestFlushSplitsBacklog(t *testing.T) {
	a := &Agent{}
	for i := range bufferLimit {
		a.enqueue(checks.Result{CheckID: fmt.Sprintf("c%d", i), Status: checks.Down, Reason: "timeout",
			Error: strings.Repeat("e", 400), At: time.Now()})
	}
	var got []string
	var msgs int
	err := a.flush(func(msg []byte) error {
		if len(msg) > maxMessage {
			t.Fatalf("message of %d bytes, limit %d", len(msg), maxMessage)
		}
		var env proto.Envelope
		var rs proto.Results
		if json.Unmarshal(msg, &env) != nil || json.Unmarshal(env.Data, &rs) != nil {
			t.Fatal("bad message")
		}
		for _, r := range rs.Results {
			got = append(got, r.CheckID)
		}
		msgs++
		return nil
	})
	if err != nil || len(got) != bufferLimit || msgs < 2 {
		t.Fatalf("err %v, %d results in %d messages", err, len(got), msgs)
	}
	for i, id := range got {
		if id != fmt.Sprintf("c%d", i) {
			t.Fatalf("out of order at %d: %s", i, id)
		}
	}

	// The second message fails: everything from it on stays pending, in order.
	for i := range bufferLimit {
		a.enqueue(checks.Result{CheckID: fmt.Sprintf("c%d", i), Error: strings.Repeat("e", 400), At: time.Now()})
	}
	sent, calls := 0, 0
	err = a.flush(func(msg []byte) error {
		if calls++; calls == 2 {
			return errors.New("connection lost")
		}
		var env proto.Envelope
		var rs proto.Results
		json.Unmarshal(msg, &env)
		json.Unmarshal(env.Data, &rs)
		sent += len(rs.Results)
		return nil
	})
	if err == nil || sent+len(a.pending) != bufferLimit || a.pending[0].CheckID != fmt.Sprintf("c%d", sent) {
		t.Fatalf("err %v, sent %d, pending %d", err, sent, len(a.pending))
	}

	// A result JSON cannot carry is dropped, not a wedge for the rest.
	a.pending = nil
	a.enqueue(checks.Result{CheckID: "nan", LatencyMS: math.NaN(), At: time.Now()})
	a.enqueue(checks.Result{CheckID: "ok", At: time.Now()})
	got = nil
	if err := a.flush(func(msg []byte) error {
		var env proto.Envelope
		var rs proto.Results
		json.Unmarshal(msg, &env)
		json.Unmarshal(env.Data, &rs)
		for _, r := range rs.Results {
			got = append(got, r.CheckID)
		}
		return nil
	}); err != nil || len(got) != 1 || got[0] != "ok" || len(a.pending) != 0 {
		t.Fatalf("err %v, got %v, pending %d", err, got, len(a.pending))
	}
}

// Runs on demand are capped: a hub (or whoever holds it) cannot make the
// agent start a goroutine and an xray instance per message without limit.
func TestRunsOnDemandAreCapped(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var held []net.Conn
	var mu sync.Mutex
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, c)
			mu.Unlock()
		}
	}()
	defer func() {
		mu.Lock()
		for _, c := range held {
			c.Close()
		}
		mu.Unlock()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := New(ctx, Config{MetricsInterval: -1}, slog.New(slog.DiscardHandler))
	a.out = make(chan []byte, 64)
	slow := checks.Spec{ID: "slow", Type: "http", Timeout: checks.Duration(5 * time.Second), HTTP: &checks.HTTPParams{URL: "http://" + ln.Addr().String() + "/"}}
	for i := range maxRuns + 2 {
		a.startRun(ctx, proto.Run{ID: fmt.Sprint(i), Spec: slow})
	}
	busy := 0
	timeout := time.After(2 * time.Second)
	for busy < 2 {
		select {
		case msg := <-a.out:
			var env proto.Envelope
			var rr proto.RunResult
			json.Unmarshal(msg, &env)
			json.Unmarshal(env.Data, &rr)
			if rr.Result.Reason != "unavailable" {
				t.Fatalf("a slow run answered first: %+v", rr.Result)
			}
			busy++
		case <-timeout:
			t.Fatalf("%d runs refused, want 2 over the limit of %d", busy, maxRuns)
		}
	}
}

// The token and every assignment (VPN links, passwords) cross the hub
// connection: in clear text only on loopback or when asked for.
func TestHubURLMustBeEncrypted(t *testing.T) {
	for hub, ok := range map[string]bool{
		"https://hub.example.com":         true,
		"wss://hub.example.com/api/agent": true,
		"hub.example.com":                 true, // wss by default
		"http://127.0.0.1:8080":           true,
		"ws://localhost:8080":             true,
		"http://[::1]:8080":               true,
		"http://:8080":                    true,
		"http://0.0.0.0:8080":             true, // the demo listening everywhere dials itself
		"http://hub.example.com":          false,
		"ws://10.0.0.5:8080":              false,
		"http://localhost.evil.com":       false,
	} {
		err := checkHubURL(hub, false)
		if (err == nil) != ok {
			t.Errorf("%s: err %v, want ok=%v", hub, err, ok)
		}
		if err != nil && !strings.Contains(err.Error(), "UF_INSECURE=1") {
			t.Errorf("%s: the error does not say how to allow it: %v", hub, err)
		}
		if err := checkHubURL(hub, true); err != nil {
			t.Errorf("%s with insecure allowed: %v", hub, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	a := New(ctx, Config{HubURL: "http://hub.invalid", Token: "t", MetricsInterval: -1}, slog.New(slog.DiscardHandler))
	if err := a.Run(ctx); err == nil {
		t.Fatal("Run went on over plain http")
	}
}

// localAddrs never offers addresses that cannot reach the internet.
func TestLocalAddrsArePublic(t *testing.T) {
	for _, s := range localAddrs() {
		ip := net.ParseIP(s)
		if ip == nil || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			t.Fatalf("not public: %s", s)
		}
	}
}
