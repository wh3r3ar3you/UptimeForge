package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/wh3r3ar3you/uptimeforge/internal/remnawave"
)

// A node listed by host name gets the addresses it resolves to, marked as
// resolved and without repeating the panel's own; a literal address is not
// looked up; the answer is cached, and a failed lookup keeps the last one.
func TestNodeAddrsResolve(t *testing.T) {
	var calls atomic.Int32
	fail := false
	c := nodeAddrs{lookup: func(_ context.Context, host string) ([]string, error) {
		calls.Add(1)
		if fail {
			return nil, errors.New("no such host")
		}
		if host != "uk.example.com" {
			t.Errorf("looked up %q", host)
		}
		return []string{"203.0.113.7", "203.0.113.8"}, nil
	}}
	fresh := func() []remnawave.Node {
		return []remnawave.Node{
			{Name: "uk", Address: "uk.example.com", IPs: []remnawave.NodeIP{{IP: "203.0.113.8", Status: "INBOUND"}}},
			{Name: "nl", Address: "198.51.100.9"},
		}
	}
	nodes := fresh()
	c.resolve(context.Background(), nodes)
	if got := nodeIPs(nodes[0]); len(got) != 2 || got[0] != "203.0.113.8" || got[1] != "203.0.113.7" || nodes[0].IPs[1].Status != resolvedIP {
		t.Fatalf("uk: %+v", nodes[0].IPs)
	}
	if len(nodes[1].IPs) != 0 {
		t.Fatalf("a literal address is not resolved: %+v", nodes[1].IPs)
	}
	nodes = fresh()
	c.resolve(context.Background(), nodes)
	if calls.Load() != 1 || len(nodes[0].IPs) != 2 {
		t.Fatalf("cached: %d lookups, %+v", calls.Load(), nodes[0].IPs)
	}
	c.mu.Lock()
	e := c.m["uk.example.com"]
	e.at = e.at.Add(-2 * addrTTL)
	c.m["uk.example.com"] = e
	c.mu.Unlock()
	fail = true
	nodes = fresh()
	c.resolve(context.Background(), nodes)
	if calls.Load() != 2 || len(nodes[0].IPs) != 2 {
		t.Fatalf("a failed lookup keeps the last answer: %d lookups, %+v", calls.Load(), nodes[0].IPs)
	}
}
