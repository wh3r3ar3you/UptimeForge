package api

import (
	"maps"
	"testing"

	"github.com/wh3r3ar3you/uptimeforge/internal/hub"
)

// Every server gets its panel node: linked by hand, by name, or by the
// public address its agent connects from; one behind the hub's NAT, named
// apart and not linked, gets none.
func TestMatchNodes(t *testing.T) {
	nodes := []hub.NodeRef{
		{ID: "u-uk", Name: "provider-uk", Address: "uk.example.com", IPs: []string{"203.0.113.7"}},
		{ID: "u-msk", Name: "msk-node-new", Address: "msk.example.com"},
		{ID: "u-wl", Name: "de-wl(spare)", Address: "198.51.100.9"},
	}
	probes := []hub.ProbeStatus{
		{ID: "uk-1", Name: "uk-1", Network: &hub.NetworkView{IP: "203.0.113.7"}},
		{ID: "msk-1", Name: "msk-1", Network: &hub.NetworkView{IP: "192.168.1.1"}, RemnawaveNode: "u-msk"},
		{ID: "de-wl", Name: "de-wl", Network: &hub.NetworkView{IP: "192.168.1.1"}},
		{ID: "home", Name: "home", Network: &hub.NetworkView{IP: "192.168.1.1"}},
	}
	want := map[string]string{"uk-1": "u-uk", "msk-1": "u-msk", "de-wl": "u-wl"}
	if got := matchNodes(probes, nodes); !maps.Equal(got, want) {
		t.Fatalf("matchNodes = %v, want %v", got, want)
	}
	if got := matchNodes(probes, nil); len(got) != 0 {
		t.Fatalf("no panel, no nodes: %v", got)
	}
}
