package api

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/hub"
	"github.com/wh3r3ar3you/uptimeforge/internal/model"
	"github.com/wh3r3ar3you/uptimeforge/internal/notify"
	"github.com/wh3r3ar3you/uptimeforge/internal/remnawave"
	"github.com/wh3r3ar3you/uptimeforge/internal/store"
)

// Locations is a status page of VPN locations built from the panel's nodes.
type Locations struct {
	Mode   string     `json:"mode,omitempty"` // country (default), node, country_expand
	Items  []Location `json:"items,omitempty"`
	Uptime bool       `json:"uptime,omitempty"` // show the 90-day bar
}

// Location is how one panel node shows to clients. Nodes not listed follow
// the listed ones in the panel's order.
type Location struct {
	Node    string `json:"node"`              // Remnawave node UUID
	Name    string `json:"name,omitempty"`    // as clients see it; never the panel's name
	Country string `json:"country,omitempty"` // overrides the panel's country code
	Hidden  bool   `json:"hidden,omitempty"`
}

var locationModes = map[string]bool{"": true, "country": true, "node": true, "country_expand": true}

type publicNode struct {
	Name    string `json:"name,omitempty"`
	Country string `json:"country"`
	Status  string `json:"status"`
}

// publicLocation is a row of the public page: a country or a node. Name is
// empty when the admin gave none; the page then shows the country's name.
type publicLocation struct {
	Flag    string       `json:"flag"`
	Country string       `json:"country"`
	Name    string       `json:"name,omitempty"`
	Status  string       `json:"status"`
	Uptime  *float64     `json:"uptime_90d,omitempty"`
	Days    []UptimeDay  `json:"days,omitempty"`
	Nodes   []publicNode `json:"nodes,omitempty"`
	nodes   []locNode
}

type locNode struct {
	n      remnawave.Node
	name   string
	status string
}

// nodeStatus says how a panel node is to its clients: the panel lost it or
// its agent is offline → outage; its users were cut off (a ban) → degraded.
// The admin's reachability checks do not count: SSH closed or ICMP filtered
// on a node says nothing about the VPN on it.
func nodeStatus(n remnawave.Node, snap hub.Status) string {
	status := "operational"
	for _, inc := range snap.Incidents {
		if inc.Key != "rw:"+n.UUID {
			continue
		}
		switch inc.Kind {
		case model.KindNodeDown:
			return "outage"
		case model.KindUsersDrop:
			status = "degraded"
		}
	}
	for _, p := range snap.Probes {
		if !p.Online && (hub.NodeKey(p.Name) == hub.NodeKey(n.Name) || hub.NodeKey(p.ID) == hub.NodeKey(n.Name)) {
			return "outage"
		}
	}
	return status
}

// rollup is a set of statuses as one: all up is up, none up is down, the
// rest is degraded; unknowns count only when nothing else is known.
func rollup(statuses []string) string {
	var up, known int
	for _, s := range statuses {
		if s == "unknown" {
			continue
		}
		known++
		if s == "operational" {
			up++
		}
	}
	switch {
	case known == 0:
		return "unknown"
	case up == known:
		return "operational"
	case up == 0 && !slices.Contains(statuses, "degraded"):
		return "outage"
	}
	return "degraded"
}

// buildLocations lays the panel's nodes out as the page's rows and gives the
// page's overall status. Hidden, disabled and unknown nodes are left out.
func buildLocations(cfg Locations, nodes []remnawave.Node, snap hub.Status) (string, []publicLocation) {
	byUUID := map[string]remnawave.Node{}
	for _, n := range nodes {
		if !n.IsDisabled {
			byUUID[n.UUID] = n
		}
	}
	var list []locNode
	countries := map[string]string{}
	seen := map[string]bool{}
	add := func(l Location) {
		n, ok := byUUID[l.Node]
		if !ok || seen[l.Node] {
			return
		}
		seen[l.Node] = true
		if l.Hidden {
			return
		}
		countries[n.UUID] = strings.ToUpper(cmp.Or(l.Country, n.CountryCode))
		list = append(list, locNode{n: n, name: l.Name, status: nodeStatus(n, snap)})
	}
	for _, l := range cfg.Items {
		add(l)
	}
	for _, n := range nodes {
		add(Location{Node: n.UUID})
	}
	var rows []publicLocation
	if cfg.Mode == "node" {
		for _, ln := range list {
			c := countries[ln.n.UUID]
			rows = append(rows, publicLocation{Flag: notify.Flag(c), Country: c, Name: ln.name, Status: ln.status, nodes: []locNode{ln}})
		}
	} else {
		at := map[string]int{}
		for _, ln := range list {
			c := countries[ln.n.UUID]
			i, ok := at[c]
			if !ok {
				i, at[c] = len(rows), len(rows)
				rows = append(rows, publicLocation{Flag: notify.Flag(c), Country: c})
			}
			rows[i].nodes = append(rows[i].nodes, ln)
		}
		for i := range rows {
			var st []string
			for _, ln := range rows[i].nodes {
				st = append(st, ln.status)
				if cfg.Mode == "country_expand" {
					rows[i].Nodes = append(rows[i].Nodes, publicNode{Name: ln.name, Country: rows[i].Country, Status: ln.status})
				}
			}
			rows[i].Status = rollup(st)
		}
	}
	var all []string
	for _, r := range rows {
		all = append(all, r.Status)
	}
	if len(rows) == 0 {
		return "unknown", rows
	}
	return rollup(all), rows
}

// locationDays is a node's last days by its own incidents: the panel lost
// it or its agent went offline → down; a ban → degraded. Overlapping
// incidents count once.
func locationDays(incs []model.Incident, start, now time.Time, days int) []UptimeDay {
	out := make([]UptimeDay, 0, days)
	for d := range days {
		ds := start.AddDate(0, 0, d)
		de := minTime(ds.AddDate(0, 0, 1), now)
		day := UptimeDay{Day: ds.Format(time.DateOnly)}
		var down, partial [][2]time.Time
		for _, inc := range incs {
			end := inc.Resolved
			if end.IsZero() {
				end = now
			}
			a, b := maxTime(inc.Started, ds), minTime(end, de)
			if !b.After(a) {
				continue
			}
			day.Count++
			// In the words of the checks' bars: a ban is partial, the rest down.
			kind := model.KindDown
			if cmp.Or(inc.Worst, inc.Kind) == model.KindUsersDrop {
				kind = model.KindPartial
			}
			if verdictRank[kind] > verdictRank[day.Worst] {
				day.Worst = kind
			}
			if kind == model.KindPartial {
				partial = append(partial, [2]time.Time{a, b})
			} else {
				down = append(down, [2]time.Time{a, b})
			}
		}
		day.Down, day.Degraded = int64(union(down).Seconds()), int64(union(partial).Seconds())
		if span := de.Sub(ds); span > 0 {
			u := max(0, 1-float64(day.Down)/span.Seconds())
			day.Up = &u
		}
		out = append(out, day)
	}
	return out
}

// union is the total length of a set of intervals.
func union(iv [][2]time.Time) time.Duration {
	slices.SortFunc(iv, func(a, b [2]time.Time) int { return a[0].Compare(b[0]) })
	var total time.Duration
	var end time.Time
	for _, x := range iv {
		if x[0].After(end) {
			end = x[0]
		}
		if x[1].After(end) {
			total += x[1].Sub(end)
			end = x[1]
		}
	}
	return total
}

// fillUptime adds each row's 90-day bar: a country's day is the mean of its
// nodes' days.
func (s *Server) fillUptime(ctx context.Context, rows []publicLocation, probes []hub.ProbeStatus) error {
	loc := s.app.Hub.Location()
	now := time.Now().In(loc)
	const days = 90
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -(days - 1))
	keysOf := func(n remnawave.Node) []string {
		keys := []string{"rw:" + n.UUID}
		for _, p := range probes {
			if hub.NodeKey(p.Name) == hub.NodeKey(n.Name) || hub.NodeKey(p.ID) == hub.NodeKey(n.Name) {
				keys = append(keys, "probe:"+p.ID)
			}
		}
		return keys
	}
	var keys []string
	for _, r := range rows {
		for _, ln := range r.nodes {
			keys = append(keys, keysOf(ln.n)...)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	incs, err := s.app.Store.Incidents(ctx, store.IncidentFilter{Keys: keys, From: start, To: now})
	if err != nil {
		return fmt.Errorf("location uptime: %w", err)
	}
	byKey := map[string][]model.Incident{}
	for _, inc := range incs {
		if k := cmp.Or(inc.Worst, inc.Kind); k == model.KindNodeDown || k == model.KindUsersDrop || k == model.KindProbeOffline {
			byKey[inc.Key] = append(byKey[inc.Key], inc)
		}
	}
	for i := range rows {
		var per [][]UptimeDay
		for _, ln := range rows[i].nodes {
			var mine []model.Incident
			for _, k := range keysOf(ln.n) {
				mine = append(mine, byKey[k]...)
			}
			per = append(per, locationDays(mine, start, now, days))
		}
		rows[i].Days = meanDays(per)
		var sum float64
		var n int
		for _, d := range rows[i].Days {
			if d.Up != nil {
				sum, n = sum+*d.Up, n+1
			}
		}
		if n > 0 {
			u := sum / float64(n)
			rows[i].Uptime = &u
		}
	}
	return nil
}

// meanDays merges several nodes' days into one row: uptime is the mean, the
// worst kind and the incident count add up.
func meanDays(per [][]UptimeDay) []UptimeDay {
	if len(per) == 1 {
		return per[0]
	}
	out := slices.Clone(per[0])
	for d := range out {
		var sum float64
		out[d].Count, out[d].Down, out[d].Degraded, out[d].Worst = 0, 0, 0, ""
		for _, days := range per {
			x := days[d]
			sum += *x.Up
			out[d].Count += x.Count
			out[d].Down += x.Down
			out[d].Degraded += x.Degraded
			if verdictRank[x.Worst] > verdictRank[out[d].Worst] {
				out[d].Worst = x.Worst
			}
		}
		u := sum / float64(len(per))
		out[d].Up = &u
	}
	return out
}

// publicLocations is the public answer of a locations page.
func (s *Server) publicLocations(ctx context.Context, p StatusPage) (map[string]any, error) {
	cfg := Locations{}
	if p.Locations != nil {
		cfg = *p.Locations
	}
	view := s.app.Remnawave()
	snap := s.app.Hub.Snapshot()
	overall, rows := buildLocations(cfg, view.Nodes, snap)
	if view.Error != "" || time.Since(view.Fetched) > 5*time.Minute {
		// The panel has not answered lately: what it said before is stale.
		overall = "unknown"
		for i := range rows {
			rows[i].Status = "unknown"
			for j := range rows[i].Nodes {
				rows[i].Nodes[j].Status = "unknown"
			}
		}
	}
	if cfg.Uptime {
		if err := s.fillUptime(ctx, rows, snap.Probes); err != nil {
			return nil, err
		}
	}
	if rows == nil {
		rows = []publicLocation{}
	}
	return map[string]any{"title": p.Title, "description": p.Description, "kind": "locations", "mode": cmp.Or(cfg.Mode, "country"),
		"status": overall, "locations": rows, "updated": time.Now()}, nil
}
