package runner

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/checks"
	"github.com/wh3r3ar3you/uptimeforge/internal/proto"
)

// An hourly check runs within half a minute of starting, not up to an hour
// later; short intervals still spread over the interval itself.
func TestFirstDelay(t *testing.T) {
	for range 1000 {
		if d := firstDelay(time.Hour); d < 0 || d >= maxSpread {
			t.Fatalf("hourly: first run after %v", d)
		}
		if d := firstDelay(10 * time.Second); d < 0 || d >= 10*time.Second {
			t.Fatalf("10s: first run after %v", d)
		}
	}
}

// Types the probe's owner denied are refused whatever the hub assigns, and
// say so instead of going silent.
func TestDeniedTypes(t *testing.T) {
	got := make(chan checks.Result, 4)
	r := New(context.Background(), slog.Default(), func(res checks.Result) { got <- res })
	defer r.Stop()
	r.Deny([]string{" tcp ", "docker"})
	r.Apply(proto.Assign{Checks: []checks.Spec{{ID: "a", Type: "tcp",
		Interval: checks.Duration(time.Second), TCP: &checks.TCPParams{Address: "127.0.0.1:1"}}}})
	select {
	case res := <-got:
		if res.CheckID != "a" || res.Status != checks.Down || res.Reason != "unavailable" || !strings.Contains(res.Error, "disabled") {
			t.Fatalf("got %+v, want a refused tcp check", res)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no result")
	}
	if res := r.RunOnce(context.Background(), checks.Spec{ID: "b", Type: "tcp", TCP: &checks.TCPParams{Address: "127.0.0.1:1"}}); res.Reason != "unavailable" {
		t.Fatalf("RunOnce: %+v", res)
	}
	if res := r.RunOnce(context.Background(), checks.Spec{ID: "c", Type: "dns", DNS: &checks.DNSParams{Name: "x.invalid", Server: "127.0.0.1:1"},
		Timeout: checks.Duration(time.Second)}); res.Reason == "unavailable" {
		t.Fatalf("an allowed type was refused: %+v", res)
	}
}

// A broken upstream proxy must not silence the probe: the checks still run
// and report down, so the hub sees the probe's state instead of stale data.
func TestBrokenViaReportsDown(t *testing.T) {
	got := make(chan checks.Result, 4)
	r := New(context.Background(), slog.Default(), func(res checks.Result) { got <- res })
	defer r.Stop()
	via := json.RawMessage(`{"protocol":"no-such-protocol","settings":{}}`)
	err := r.Apply(proto.Assign{Via: via, Checks: []checks.Spec{{ID: "a", Type: "tcp",
		Interval: checks.Duration(time.Second), TCP: &checks.TCPParams{Address: "127.0.0.1:1"}}}})
	if err == nil {
		t.Fatal("Apply hid the broken upstream proxy")
	}
	select {
	case res := <-got:
		if res.CheckID != "a" || res.Status != checks.Down || res.Reason != "via" {
			t.Fatalf("got %+v, want check a down with reason via", res)
		}
		if !strings.Contains(res.Error, "probe upstream proxy") {
			t.Fatalf("error %q does not name the upstream proxy", res.Error)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no result: the broken upstream proxy silenced the probe")
	}
	// The error is logged by the agent and the hub: the proxy's keys stay out.
	leaky := json.RawMessage(`{"protocol":"vless","settings":{"address":"127.0.0.1","port":1,"id":"11111111-2222-3333-4444-555555555555","encryption":"none"},` +
		`"streamSettings":{"security":"reality","realitySettings":{"publicKey":"VIAPBKVALUE","serverName":"a.com"}}}`)
	if err := r.Apply(proto.Assign{Via: leaky}); err == nil || strings.Contains(err.Error(), "VIAPBKVALUE") {
		t.Fatalf("Apply error %v", err)
	}
	r.Apply(proto.Assign{Via: via, Checks: []checks.Spec{{ID: "a", Type: "tcp",
		Interval: checks.Duration(time.Second), TCP: &checks.TCPParams{Address: "127.0.0.1:1"}}}})
	// A run from the UI says the same.
	if res := r.RunOnce(context.Background(), checks.Spec{ID: "b", Type: "tcp", TCP: &checks.TCPParams{Address: "127.0.0.1:1"}}); res.Reason != "via" {
		t.Fatalf("RunOnce: got %+v, want reason via", res)
	}
}
