package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/hub"
	"github.com/wh3r3ar3you/uptimeforge/internal/model"
	"github.com/wh3r3ar3you/uptimeforge/internal/remnawave"
)

func panelNode(uuid, name, country, addr string) remnawave.Node {
	return remnawave.Node{UUID: uuid, Name: name, CountryCode: country, Address: addr, IsConnected: true}
}

func open(key string, kind model.Kind) model.Incident {
	return model.Incident{Key: key, Kind: kind, Worst: kind}
}

func byCountry(rows []publicLocation) map[string]string {
	out := map[string]string{}
	for _, r := range rows {
		out[r.Country] = r.Status
	}
	return out
}

// A country is up when all its nodes are, down when none is, and partly up
// otherwise. A node is down when the panel lost it or its agent is offline;
// a ban makes it partly up. SSH closed or ICMP filtered says nothing to
// clients, so the admin's reachability checks do not count.
func TestLocationsCountryRollup(t *testing.T) {
	nodes := []remnawave.Node{
		panelNode("de1", "de-node-a", "DE", "de1.example"), panelNode("de2", "de-node-b", "DE", "de2.example"), panelNode("de3", "de-node-c", "DE", "de3.example"),
		panelNode("nl1", "nl-1", "NL", "nl1.example"), panelNode("nl2", "nl-2", "NL", "nl2.example"),
		panelNode("ee1", "ee-1", "EE", "ee1.example"), panelNode("ee2", "ee-2", "EE", "ee2.example"),
		panelNode("fi1", "fi-1", "FI", "fi1.example"),
		panelNode("pl1", "pl-1", "PL", "pl1.example"),
	}
	snap := hub.Status{
		Incidents: []model.Incident{open("rw:de2", model.KindNodeDown), open("rw:ee1", model.KindNodeDown), open("rw:pl1", model.KindUsersDrop)},
		Probes:    []hub.ProbeStatus{{ID: "ee-2", Name: "ee-2", Online: false}, {ID: "nl-1", Name: "nl-1 (Amsterdam)", Online: true}},
		Checks:    []hub.CheckStatus{{ID: "fi", Type: "tcp", Target: "fi1.example:22", Verdict: model.KindDown}, {ID: "fi-ping", Type: "ping", Target: "fi1.example", Verdict: model.KindDown}, {ID: "nl", Type: "tcp", Target: "nl2.example:443", Verdict: model.KindOK}},
	}
	overall, rows := buildLocations(Locations{Mode: "country"}, nodes, snap)
	got := byCountry(rows)
	want := map[string]string{"DE": "degraded", "NL": "operational", "EE": "outage", "FI": "operational", "PL": "degraded"}
	for c, s := range want {
		if got[c] != s {
			t.Errorf("%s: %q, want %q", c, got[c], s)
		}
	}
	if len(rows) != len(want) || rows[0].Country != "DE" || rows[0].Flag != "🇩🇪" {
		t.Fatalf("rows: %+v", rows)
	}
	if overall != "degraded" {
		t.Fatalf("overall %q: some countries work", overall)
	}
	if overall, _ := buildLocations(Locations{}, nodes[3:5], snap); overall != "operational" {
		t.Fatalf("all up: %q", overall)
	}
}

// The public answer names a node only as the admin named it for clients:
// never by its panel name, address or UUID.
func TestLocationsHideDetails(t *testing.T) {
	nodes := []remnawave.Node{panelNode("uuid-de-1", "de-node-secret", "DE", "10.9.9.9"), panelNode("uuid-de-2", "de-node-two", "DE", "de2.internal")}
	nodes[0].IPs = []remnawave.NodeIP{{IP: "10.8.8.8"}}
	for _, mode := range []string{"country", "node", "country_expand"} {
		_, rows := buildLocations(Locations{Mode: mode, Items: []Location{{Node: "uuid-de-1", Name: "Франкфурт"}}}, nodes, hub.Status{})
		b, _ := json.Marshal(rows)
		for _, leak := range []string{"de-node-1", "10.9.9.9", "10.8.8.8", "uuid-", "internal"} {
			if strings.Contains(string(b), leak) {
				t.Errorf("%s: %q leaks in %s", mode, leak, b)
			}
		}
		if mode != "country" && !strings.Contains(string(b), "Франкфурт") {
			t.Errorf("%s: the client name is missing: %s", mode, b)
		}
	}
}

// A hidden node is not shown and does not count for its country.
func TestLocationsHidden(t *testing.T) {
	nodes := []remnawave.Node{panelNode("de1", "a", "DE", "a.example"), panelNode("de2", "b", "DE", "b.example")}
	snap := hub.Status{Incidents: []model.Incident{open("rw:de2", model.KindNodeDown)}}
	cfg := Locations{Items: []Location{{Node: "de2", Hidden: true}}}
	if _, rows := buildLocations(cfg, nodes, snap); len(rows) != 1 || rows[0].Status != "operational" {
		t.Fatalf("country: %+v", rows)
	}
	cfg.Mode = "node"
	if _, rows := buildLocations(cfg, nodes, snap); len(rows) != 1 || rows[0].Status != "operational" {
		t.Fatalf("nodes: %+v", rows)
	}
}

// A node deleted from the panel loses its row; the page still renders.
func TestLocationsNodeRemovedFromPanel(t *testing.T) {
	nodes := []remnawave.Node{panelNode("de1", "a", "DE", "a.example")}
	cfg := Locations{Mode: "node", Items: []Location{{Node: "gone-uuid", Name: "Ушедшая", Country: "NL"}, {Node: "de1", Name: "Берлин"}}}
	_, rows := buildLocations(cfg, nodes, hub.Status{})
	b, _ := json.Marshal(rows)
	if len(rows) != 1 || rows[0].Name != "Берлин" || strings.Contains(string(b), "gone") || strings.Contains(string(b), "Ушедшая") {
		t.Fatalf("rows: %s", b)
	}
}

// Rows follow the admin's order; nodes the admin has not placed yet follow
// in the panel's order, and disabled nodes are left out.
func TestLocationsOrder(t *testing.T) {
	nodes := []remnawave.Node{panelNode("a", "a", "DE", "a"), panelNode("b", "b", "NL", "b"), panelNode("c", "c", "EE", "c"), panelNode("d", "d", "FI", "d")}
	nodes[3].IsDisabled = true
	_, rows := buildLocations(Locations{Items: []Location{{Node: "c"}, {Node: "a"}}}, nodes, hub.Status{})
	var got []string
	for _, r := range rows {
		got = append(got, r.Country)
	}
	if strings.Join(got, ",") != "EE,DE,NL" {
		t.Fatalf("order: %v", got)
	}
}

// The public answer is cached for 30 seconds and dropped when the page is
// edited.
func TestPublicStatusCached(t *testing.T) {
	_, srv, _ := startHub(t)
	admin := newClient(t, srv.URL)
	admin.do("POST", "/api/v1/auth/setup", map[string]string{"username": "leo", "password": "s3cret-pass", "setup_token": testSetupToken}, nil)
	if code := admin.do("PUT", "/api/v1/status-pages/vpn", map[string]any{"title": "VPN", "kind": "locations"}, nil); code != 200 {
		t.Fatalf("put: %d", code)
	}
	var a, b struct {
		Title   string
		Updated string
	}
	public := newClient(t, srv.URL)
	public.do("GET", "/api/public/status/vpn", nil, &a)
	public.do("GET", "/api/public/status/vpn", nil, &b)
	if a.Updated == "" || a.Updated != b.Updated {
		t.Fatalf("not cached: %q %q", a.Updated, b.Updated)
	}
	var pages []map[string]any
	admin.do("GET", "/api/v1/status-pages", nil, &pages)
	if len(pages) != 1 || pages[0]["groups"] == nil {
		t.Fatalf("a page without groups lists them as [], not null: %v", pages)
	}
	admin.do("PUT", "/api/v1/status-pages/vpn", map[string]any{"title": "VPN 2", "kind": "locations"}, nil)
	public.do("GET", "/api/public/status/vpn", nil, &b)
	if b.Title != "VPN 2" {
		t.Fatalf("an edit must drop the cache: %q", b.Title)
	}
	if code := admin.do("PUT", "/api/v1/status-pages/vpn", map[string]any{"title": "VPN", "kind": "locations", "locations": map[string]any{"mode": "map"}}, nil); code != 400 {
		t.Fatalf("unknown mode: %d", code)
	}
}

// A node's day counts overlapping outages once (the panel lost it while its
// agent was offline), and a ban as degraded, not down.
func TestLocationDays(t *testing.T) {
	start := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	at := start.Add(10 * time.Hour)
	inc := func(kind model.Kind, from, d time.Duration) model.Incident {
		return model.Incident{Kind: kind, Worst: kind, Started: at.Add(from), Resolved: at.Add(from + d)}
	}
	days := locationDays([]model.Incident{
		inc(model.KindNodeDown, 0, time.Hour), inc(model.KindProbeOffline, 30*time.Minute, time.Hour), inc(model.KindUsersDrop, 5*time.Hour, 2*time.Hour),
	}, start, start.AddDate(0, 0, 2), 2)
	d := days[0]
	if d.Down != 5400 || d.Degraded != 7200 || d.Count != 3 || *d.Up != 1-5400.0/86400 {
		t.Fatalf("day: down %d degraded %d count %d up %v", d.Down, d.Degraded, d.Count, *d.Up)
	}
	if *days[1].Up != 1 {
		t.Fatalf("a clean day: %v", *days[1].Up)
	}
	m := meanDays([][]UptimeDay{days, locationDays(nil, start, start.AddDate(0, 0, 2), 2)})
	if *m[0].Up != (1+*d.Up)/2 || m[0].Worst != model.KindDown {
		t.Fatalf("mean: %v %s", *m[0].Up, m[0].Worst)
	}
}
