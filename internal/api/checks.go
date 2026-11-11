package api

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/app"
	"github.com/wh3r3ar3you/uptimeforge/internal/checks"
	"github.com/wh3r3ar3you/uptimeforge/internal/hub"
	"github.com/wh3r3ar3you/uptimeforge/internal/model"
	"github.com/wh3r3ar3you/uptimeforge/internal/store"
	"github.com/wh3r3ar3you/uptimeforge/internal/version"
)

// CheckRow is a check as lists and dashboards show it.
type CheckRow struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Group        string          `json:"group,omitempty"`
	Tags         []string        `json:"tags,omitempty"`
	Parents      []string        `json:"parents,omitempty"`
	ParentsAuto  bool            `json:"parents_auto,omitempty"`
	Type         string          `json:"type"`
	Target       string          `json:"target,omitempty"`
	Parent       string          `json:"parent,omitempty"`
	Interval     checks.Duration `json:"interval,omitempty"`
	Verdict      model.Kind      `json:"verdict"`
	Region       string          `json:"region,omitempty"`
	Paused       bool            `json:"paused,omitempty"`
	Managed      bool            `json:"managed,omitempty"`
	Subscription bool            `json:"subscription,omitempty"`
	Children     int             `json:"children,omitempty"`
	Probes       []ProbeCell     `json:"probes"`
	Uptime24h    *float64        `json:"uptime_24h,omitempty"`
	Latency24h   *float64        `json:"latency_24h,omitempty"`
	Incident     *model.Incident `json:"incident,omitempty"`
}

type ProbeCell struct {
	Probe     string    `json:"probe"`
	Failing   bool      `json:"failing"`
	Status    string    `json:"status,omitempty"`
	Latency   float64   `json:"latency,omitempty"`
	TTFB      float64   `json:"ttfb,omitempty"`
	BPS       float64   `json:"bps,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	Error     string    `json:"error,omitempty"`
	At        time.Time `json:"at,omitzero"`
	Uptime24h *float64  `json:"uptime_24h,omitempty"`
	DCms      string    `json:"dc_ms,omitempty"`   // MTProxy: "1:54 2:60", DC and milliseconds
	DCDown    string    `json:"dc_down,omitempty"` // MTProxy: "3,5", DCs that gave no answer
}

var verdictRank = map[model.Kind]int{model.KindUnknown: 0, model.KindOK: 1, model.KindPartial: 2, model.KindBlocked: 3, model.KindDown: 4}

func (s *Server) checkRows(ctx context.Context, snap hub.Status) ([]CheckRow, error) {
	av, err := s.app.Store.Availability(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	type agg struct{ n, up, latSum, latN float64 }
	perCheck := map[string]*agg{}
	perCell := map[[2]string]float64{}
	for _, a := range av {
		g := perCheck[a.Check]
		if g == nil {
			g = &agg{}
			perCheck[a.Check] = g
		}
		g.n += float64(a.N)
		g.up += a.Up * float64(a.N)
		if a.Lat > 0 {
			g.latSum += a.Lat * a.Up * float64(a.N)
			g.latN += a.Up * float64(a.N)
		}
		perCell[[2]string{a.Check, a.Probe}] = a.Up
	}
	open := map[string]*model.Incident{}
	for i := range snap.Incidents {
		open[snap.Incidents[i].Key] = &snap.Incidents[i]
	}
	status := map[string]hub.CheckStatus{}
	children := map[string][]hub.CheckStatus{}
	for _, c := range snap.Checks {
		status[c.ID] = c
		if c.Parent != "" {
			children[c.Parent] = append(children[c.Parent], c)
		}
	}

	row := func(c hub.CheckStatus) CheckRow {
		r := CheckRow{ID: c.ID, Name: c.Name, Group: c.Group, Tags: c.Tags, Parents: c.Parents, ParentsAuto: c.ParentsAuto, Type: c.Type, Target: c.Target, Parent: c.Parent,
			Interval: c.Interval, Verdict: c.Verdict, Region: c.Region, Incident: open[c.ID], Probes: []ProbeCell{}}
		if g := perCheck[c.ID]; g != nil && g.n > 0 {
			u := g.up / g.n
			r.Uptime24h = &u
			if g.latN > 0 {
				l := g.latSum / g.latN
				r.Latency24h = &l
			}
		}
		for _, p := range c.Probes {
			cell := ProbeCell{Probe: p.Probe, Failing: p.Failing, At: p.At}
			if p.Last != nil {
				cell.Status, cell.Latency, cell.Reason, cell.Error = string(p.Last.Status), p.Last.LatencyMS, p.Last.Reason, p.Last.Error
				cell.TTFB, cell.BPS = p.Last.Phases["ttfb"], p.Last.Throughput
				cell.DCms, cell.DCDown = p.Last.Info["dc_ms"], p.Last.Info["dc_down"]
			}
			if u, ok := perCell[[2]string{c.ID, p.Probe}]; ok {
				cell.Uptime24h = &u
			}
			r.Probes = append(r.Probes, cell)
		}
		return r
	}

	rows := []CheckRow{}
	for _, item := range s.app.Checks() {
		c := item.CheckConfig
		switch {
		case c.Xray != nil && c.Xray.Subscription != "":
			parent := CheckRow{ID: c.ID, Name: pick(c.Name, c.ID), Group: c.Group, Tags: c.Tags, Type: "subscription", Paused: c.Paused,
				Managed: item.Managed, Subscription: true, Verdict: model.KindUnknown, Probes: []ProbeCell{}, Incident: open["sub:"+c.ID]}
			kids := children[c.ID]
			parent.Children = len(kids)
			var n, up float64
			for _, k := range kids {
				if verdictRank[k.Verdict] > verdictRank[parent.Verdict] {
					parent.Verdict = k.Verdict
				}
				if g := perCheck[k.ID]; g != nil {
					n, up = n+g.n, up+g.up
				}
			}
			if n > 0 {
				u := up / n
				parent.Uptime24h = &u
			}
			if c.Paused {
				parent.Verdict = "paused"
			}
			rows = append(rows, parent)
			for _, k := range kids {
				r := row(k)
				r.Managed = item.Managed
				rows = append(rows, r)
			}
		case c.Paused:
			rows = append(rows, CheckRow{ID: c.ID, Name: pick(c.Name, c.ID), Group: c.Group, Tags: c.Tags, Type: c.Type,
				Interval: c.Interval, Verdict: "paused", Paused: true, Managed: item.Managed, Probes: []ProbeCell{}})
		default:
			if st, ok := status[c.ID]; ok {
				r := row(st)
				r.Managed = item.Managed
				rows = append(rows, r)
			}
		}
	}
	return rows, nil
}

func pick(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (s *Server) meta(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Hub.Config()
	writeJSON(w, http.StatusOK, map[string]any{
		"version":       version.Version,
		"time":          time.Now(),
		"timezone":      cfg.Timezone,
		"lang":          cfg.Lang,
		"region_label":  cfg.RegionLabel,
		"public_url":    s.hubURL(r),
		"check_types":   checks.Types(),
		"check_catalog": checks.Catalog(),
		"host_rules":    hub.DefaultHostRules(),
		"user":          userFrom(r.Context()),
	})
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	snap := s.app.Hub.Snapshot()
	rows, err := s.checkRows(ctx, snap)
	if err != nil {
		s.fail(w, err)
		return
	}
	counts := map[string]int{}
	for _, row := range rows {
		if row.Subscription {
			continue
		}
		counts["checks"]++
		counts[string(row.Verdict)]++
	}
	var rx, tx float64
	for _, p := range snap.Probes {
		counts["probes"]++
		if p.Online {
			counts["probes_online"]++
		}
		if p.Metrics != nil && time.Since(p.MetricsAt) < 2*time.Minute {
			rx, tx = rx+p.Metrics.RX, tx+p.Metrics.TX
		}
	}
	for _, inc := range snap.Incidents {
		if inc.CauseKey != "" {
			continue // folded into its parent's incident
		}
		counts["incidents"]++
		if inc.Severity == model.Critical {
			counts["critical"]++
		}
	}
	recent, err := s.app.Store.Incidents(ctx, store.IncidentFilter{Limit: 12})
	if err != nil {
		s.fail(w, err)
		return
	}
	if isViewer(r) {
		redactRows(rows)
		redactIncidents(snap.Incidents)
		redactIncidents(recent)
	}
	rw := s.app.Remnawave()
	rw.Nodes, rw.Usage = nil, nil
	writeJSON(w, http.StatusOK, map[string]any{
		"time":      snap.Time,
		"counts":    counts,
		"checks":    rows,
		"probes":    snap.Probes,
		"incidents": snap.Incidents,
		"recent":    recent,
		"traffic":   map[string]float64{"rx": rx, "tx": tx},
		"remnawave": rw,
	})
}

func (s *Server) listChecks(w http.ResponseWriter, r *http.Request) {
	snap := s.app.Hub.Snapshot()
	rows, err := s.checkRows(r.Context(), snap)
	if err != nil {
		s.fail(w, err)
		return
	}
	if isViewer(r) {
		redactRows(rows)
	}
	writeJSON(w, http.StatusOK, map[string]any{"checks": rows, "probes": snap.Probes})
}

// configFor finds the editable config behind a row: the check itself, or
// the subscription it was expanded from.
func (s *Server) configFor(id string) (hub.CheckConfig, bool, bool) {
	parent, _, child := strings.Cut(id, "/")
	for _, it := range s.app.Checks() {
		if it.ID == id || (child && it.ID == parent) {
			return it.CheckConfig, it.Managed, true
		}
	}
	return hub.CheckConfig{}, false, false
}

func (s *Server) getCheck(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	cfg, managed, ok := s.configFor(id)
	if !ok {
		s.fail(w, store.ErrNotFound)
		return
	}
	snap := s.app.Hub.Snapshot()
	rows, err := s.checkRows(ctx, snap)
	if err != nil {
		s.fail(w, err)
		return
	}
	viewer := isViewer(r)
	if viewer {
		cfg = redactCheck(cfg)
		redactRows(rows)
	}
	var row *CheckRow
	kids := []CheckRow{}
	for i := range rows {
		if rows[i].ID == id {
			row = &rows[i]
		}
		if rows[i].Parent == id {
			kids = append(kids, rows[i])
		}
	}
	keys := []string{id}
	if row != nil && row.Subscription {
		keys = append(keys, "sub:"+id)
	}
	f := store.IncidentFilter{Keys: keys, Limit: 50}
	if row != nil && row.Subscription {
		f = store.IncidentFilter{Prefix: id + "/", Limit: 50}
	}
	incs, err := s.app.Store.Incidents(ctx, f)
	if err != nil {
		s.fail(w, err)
		return
	}
	if viewer {
		redactIncidents(incs)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"check":     row,
		"config":    cfg,
		"managed":   managed,
		"children":  kids,
		"incidents": incs,
		"probes":    snap.Probes,
	})
}

func (s *Server) createCheck(w http.ResponseWriter, r *http.Request) {
	var c hub.CheckConfig
	if err := decode(r, &c); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.app.SaveCheck(r.Context(), c, true); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) updateCheck(w http.ResponseWriter, r *http.Request) {
	var c hub.CheckConfig
	if err := decode(r, &c); err != nil {
		s.fail(w, err)
		return
	}
	c.ID = r.PathValue("id")
	if err := s.app.SaveCheck(r.Context(), c, false); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) deleteCheck(w http.ResponseWriter, r *http.Request) {
	if err := s.app.DeleteCheck(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) pauseCheck(paused bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := s.app.SetPaused(r.Context(), r.PathValue("id"), paused); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

var ranges = map[string]time.Duration{
	"1h": time.Hour, "3h": 3 * time.Hour, "6h": 6 * time.Hour, "12h": 12 * time.Hour, "24h": 24 * time.Hour,
	"7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour, "90d": 90 * 24 * time.Hour,
}

// parseRange reads either a named range or an explicit from/to window in Unix
// seconds, which is what zooming into a chart asks for.
func parseRange(r *http.Request) (time.Time, time.Time, error) {
	q := r.URL.Query()
	if q.Get("from") != "" || q.Get("to") != "" {
		f, err1 := strconv.ParseInt(q.Get("from"), 10, 64)
		t, err2 := strconv.ParseInt(q.Get("to"), 10, 64)
		if err1 != nil || err2 != nil || t-f < 60 || t-f > 120*86400 {
			return time.Time{}, time.Time{}, errors.New("from and to must be Unix seconds, 1 minute to 120 days apart")
		}
		return time.Unix(f, 0), time.Unix(t, 0), nil
	}
	d, ok := ranges[r.URL.Query().Get("range")]
	if !ok {
		if r.URL.Query().Get("range") != "" {
			return time.Time{}, time.Time{}, errors.New("range must be one of 1h, 3h, 6h, 12h, 24h, 7d, 30d, 90d")
		}
		d = 24 * time.Hour
	}
	to := time.Now()
	return to.Add(-d), to, nil
}

func (s *Server) checkSeries(w http.ResponseWriter, r *http.Request) {
	from, to, err := parseRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	series, err := s.app.Store.CheckSeries(r.Context(), r.PathValue("id"), from, to, 240)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from.Unix(), "to": to.Unix(), "probes": series})
}

// UptimeDay is one bar of the uptime strip.
type UptimeDay struct {
	Day      string     `json:"day"`
	Up       *float64   `json:"up"` // nil: no data that day
	Worst    model.Kind `json:"worst,omitempty"`
	Down     int64      `json:"down_s"`     // down or blocked
	Degraded int64      `json:"degraded_s"` // partial
	Count    int        `json:"incidents"`
}

func (s *Server) checkUptime(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 || days > 365 {
		days = 90
	}
	out, err := s.uptimeDays(r.Context(), r.PathValue("id"), days)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// uptimeDays derives daily availability from verdict-level incidents, so it
// matches what was alerted rather than individual probe hiccups.
func (s *Server) uptimeDays(ctx context.Context, id string, days int) ([]UptimeDay, error) {
	loc := s.app.Hub.Location()
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	start := today.AddDate(0, 0, -(days - 1))
	keys := []string{id}
	f := store.IncidentFilter{Keys: keys, From: start, To: now}
	if strings.HasSuffix(id, "/") {
		f = store.IncidentFilter{Prefix: id, From: start, To: now}
	}
	incs, err := s.app.Store.Incidents(ctx, f)
	if err != nil {
		return nil, err
	}
	first, err := s.app.Store.FirstSeen(ctx, strings.TrimSuffix(id, "/"))
	if err != nil {
		return nil, err
	}
	out := make([]UptimeDay, 0, days)
	for d := 0; d < days; d++ {
		ds := start.AddDate(0, 0, d)
		de := ds.AddDate(0, 0, 1)
		if de.After(now) {
			de = now
		}
		day := UptimeDay{Day: ds.Format(time.DateOnly)}
		if first.IsZero() || de.Before(first) {
			out = append(out, day)
			continue
		}
		span := de.Sub(ds)
		if first.After(ds) {
			span = de.Sub(first)
		}
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
			if verdictRank[inc.Worst] > verdictRank[day.Worst] {
				day.Worst = inc.Worst
			}
			if inc.Worst == model.KindPartial {
				day.Degraded += int64(b.Sub(a).Seconds())
			} else {
				day.Down += int64(b.Sub(a).Seconds())
			}
		}
		u := 1.0
		if span > 0 {
			u = max(0, 1-float64(day.Down)/span.Seconds())
		}
		day.Up = &u
		out = append(out, day)
	}
	return out, nil
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// barsCache keeps each window's bars for barsTTL. The lock guards the maps
// only: queries run without it, one per window at a time, and requests for
// a window being computed wait for that answer.
type barsCache struct {
	mu   sync.Mutex
	at   map[string]time.Time
	data map[string]any
	busy map[string]*barsCall
}

type barsCall struct {
	done chan struct{}
	val  any
	err  error
}

const barsTTL = 30 * time.Second

func (c *barsCache) get(ctx context.Context, key string, fn func() (any, error)) (any, error) {
	c.mu.Lock()
	if d, ok := c.data[key]; ok && time.Since(c.at[key]) < barsTTL {
		c.mu.Unlock()
		return d, nil
	}
	if call := c.busy[key]; call != nil {
		c.mu.Unlock()
		select {
		case <-call.done:
			return call.val, call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if c.busy == nil {
		c.busy = map[string]*barsCall{}
	}
	call := &barsCall{done: make(chan struct{})}
	c.busy[key] = call
	c.mu.Unlock()

	call.val, call.err = fn()

	c.mu.Lock()
	delete(c.busy, key)
	if call.err == nil {
		if c.data == nil {
			c.data, c.at = map[string]any{}, map[string]time.Time{}
		}
		c.data[key], c.at[key] = call.val, time.Now()
	}
	c.mu.Unlock()
	close(call.done)
	return call.val, call.err
}

// checkBars is every check's pulse: the window in equal buckets, each with
// its result counts and the worst incident kind that covered it. Cached for
// 30 seconds: the page polls it and it reads every sample in the window.
func (s *Server) checkBars(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	win := cmp.Or(q.Get("window"), "24h")
	span, ok := ranges[win]
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Errorf("window must be one of 1h, 3h, 6h, 12h, 24h, 7d, 30d, 90d"))
		return
	}
	buckets := 48
	if v := q.Get("buckets"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 12 || n > 96 {
			writeError(w, http.StatusBadRequest, fmt.Errorf("buckets must be between 12 and 96"))
			return
		}
		buckets = n
	}
	key := fmt.Sprintf("%s/%d", win, buckets)
	// The query serves everyone waiting for this window: it must not stop
	// when the client that started it goes away.
	qctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Minute)
	defer cancel()
	out, err := s.bars.get(r.Context(), key, func() (any, error) { return s.computeBars(qctx, span, buckets) })
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// computeBars reads the bars of a window and marks incidents on them.
func (s *Server) computeBars(ctx context.Context, span time.Duration, buckets int) (any, error) {
	to := time.Now()
	from := to.Add(-span)
	bars, err := s.app.Store.CheckBars(ctx, from, to, buckets)
	if err != nil {
		return nil, err
	}
	incs, err := s.app.Store.Incidents(ctx, store.IncidentFilter{From: from, To: to})
	if err != nil {
		return nil, err
	}
	step := span / time.Duration(buckets)
	for _, inc := range incs {
		end := inc.Resolved
		if end.IsZero() {
			end = to
		}
		worst := cmp.Or(inc.Worst, inc.Kind)
		// A config's incident marks its subscription's bar too, and so does
		// the subscription failing to load ("sub:<id>").
		keys := []string{strings.TrimPrefix(inc.Key, "sub:")}
		if sub, _, ok := strings.Cut(inc.Key, "/"); ok {
			keys = append(keys, sub)
		}
		for _, k := range keys {
			b := bars[k]
			for i := max(0, int(inc.Started.Sub(from)/step)); b != nil && i < buckets && from.Add(time.Duration(i)*step).Before(end); i++ {
				if cur := model.Kind(b[i].Worst); worst.Rank() > cur.Rank() {
					b[i].Worst = string(worst)
				}
			}
		}
	}
	return map[string]any{"from": from.Unix(), "to": to.Unix(), "step_s": int(step.Seconds()), "bars": bars}, nil
}

// bulkChecks applies one action to many checks; config.yaml ones are skipped.
func (s *Server) bulkChecks(w http.ResponseWriter, r *http.Request) {
	var b app.BulkAction
	if err := decode(r, &b); err != nil {
		s.fail(w, err)
		return
	}
	skipped, err := s.app.BulkChecks(r.Context(), b)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "skipped": append([]string{}, skipped...)})
}
