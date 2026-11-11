package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/model"
	"github.com/wh3r3ar3you/uptimeforge/internal/store/pgtest"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(pgtest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestObjectsAndSettings(t *testing.T) {
	s, ctx := open(t), context.Background()
	type probe struct{ Name string }
	if err := s.PutObject(ctx, "probe", "b", probe{"B"}); err != nil {
		t.Fatal(err)
	}
	s.PutObject(ctx, "probe", "a", probe{"A"})
	s.PutObject(ctx, "probe", "b", probe{"B2"}) // update keeps position
	objs, _ := s.Objects(ctx, "probe")
	if len(objs) != 2 || objs[0].ID != "b" || string(objs[0].Data) != `{"Name": "B2"}` {
		t.Fatalf("objects: %+v", objs)
	}
	if err := s.DeleteObject(ctx, "probe", "zzz"); err != ErrNotFound {
		t.Fatalf("delete missing: %v", err)
	}
	var v map[string]int
	if found, _ := s.GetSetting(ctx, "x", &v); found {
		t.Fatal("unexpected setting")
	}
	s.PutSetting(ctx, "x", map[string]int{"a": 1})
	if found, _ := s.GetSetting(ctx, "x", &v); !found || v["a"] != 1 {
		t.Fatal("setting roundtrip")
	}
}

func TestUsersAndSessions(t *testing.T) {
	s, ctx := open(t), context.Background()
	if _, err := s.CreateUser(ctx, "leo", "short", RoleAdmin); err == nil {
		t.Fatal("short password accepted")
	}
	u, err := s.CreateUser(ctx, "leo", "correct horse", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, "LEO", "wrong password"); err != ErrBadCredentials {
		t.Fatal("wrong password accepted")
	}
	if _, err := s.Authenticate(ctx, "nobody", "whatever1"); err != ErrBadCredentials {
		t.Fatal("unknown user accepted")
	}
	if _, err := s.Authenticate(ctx, "Leo", "correct horse"); err != nil {
		t.Fatal(err)
	}
	tok, _ := s.CreateSession(ctx, u.ID, time.Hour)
	if got, err := s.SessionUser(ctx, tok); err != nil || got.Username != "leo" {
		t.Fatalf("session: %v %+v", err, got)
	}
	s.SetPassword(ctx, u.ID, "another password") // logs out everywhere
	if _, err := s.SessionUser(ctx, tok); err != ErrNotFound {
		t.Fatal("session survived a password change")
	}
}

func TestSeriesRollupAndQueries(t *testing.T) {
	s, ctx := open(t), context.Background()
	now := time.Now().Truncate(time.Hour).Add(30 * time.Minute)
	var samples []Sample
	for i := range 120 { // two hours, one per minute, every 10th failing
		at := now.Add(-time.Duration(120-i) * time.Minute)
		samples = append(samples,
			Sample{Check: "node", Probe: "ru", At: at, Up: i%10 != 0, Latency: 100, TTFB: 80, Reason: "timeout"},
			Sample{Check: "node", Probe: "de", At: at, Up: true, Latency: 20, BPS: 1e6})
	}
	if err := s.WriteSamples(ctx, samples); err != nil {
		t.Fatal(err)
	}
	if err := s.Rollup(ctx, now); err != nil {
		t.Fatal(err)
	}

	series, err := s.CheckSeries(ctx, "node", now.Add(-2*time.Hour), now, 300)
	if err != nil {
		t.Fatal(err)
	}
	if len(series["ru"]) != 120 || *series["de"][0].Lat != 20 || series["de"][0].BPS == nil {
		t.Fatalf("raw series: %d points", len(series["ru"]))
	}
	// Failed runs don't drag latency to the timeout value.
	for _, p := range series["ru"] {
		if p.Up == 0 && p.Lat != nil {
			t.Fatal("latency recorded for a failure")
		}
	}

	// Rollups answer long ranges; ratios must match the raw data.
	long, err := s.CheckSeries(ctx, "node", now.Add(-20*24*time.Hour), now, 300)
	if err != nil {
		t.Fatal(err)
	}
	var n, up float64
	for _, p := range long["ru"] {
		n += float64(p.N)
		up += p.Up * float64(p.N)
	}
	if n != 120 || up != 108 {
		t.Fatalf("hourly rollup lost data: n=%v up=%v", n, up)
	}

	av, _ := s.Availability(ctx, now.Add(-24*time.Hour))
	for _, a := range av {
		if a.Probe == "ru" && (a.N != 120 || a.Up != 0.9 || a.Lat != 100) {
			t.Fatalf("availability: %+v", a)
		}
	}
}

func TestSysSeries(t *testing.T) {
	s, ctx := open(t), context.Background()
	now := time.Now()
	var xs []SysSample
	for i := range 60 {
		xs = append(xs, SysSample{Probe: "de", At: now.Add(-time.Duration(60-i) * time.Minute), CPU: float64(i), RX: 1000, MemTotal: 8e9, MemUsed: 4e9})
	}
	s.WriteSys(ctx, xs)
	s.Rollup(ctx, now)
	pts, err := s.SysSeries(ctx, "de", now.Add(-time.Hour), now, 30)
	if err != nil || len(pts) == 0 || pts[0].MemTotal != 8e9 || pts[0].RX != 1000 {
		t.Fatalf("sys series: %v %+v", err, pts)
	}
	week, _ := s.SysSeries(ctx, "de", now.Add(-7*24*time.Hour), now, 300)
	if len(week) == 0 {
		t.Fatal("5m rollup empty")
	}
}

func TestIncidentHistory(t *testing.T) {
	s, ctx := open(t), context.Background()
	t0 := time.Now().Add(-48 * time.Hour)
	s.SaveIncident(ctx, model.Incident{ID: "a", Key: "canary/de-1", Kind: model.KindDown, Worst: model.KindDown, Started: t0, Resolved: t0.Add(time.Hour)})
	s.SaveIncident(ctx, model.Incident{ID: "b", Key: "node", Kind: model.KindPartial, Worst: model.KindPartial, Started: time.Now().Add(-time.Hour)})
	open, _ := s.Incidents(ctx, IncidentFilter{Open: true})
	if len(open) != 1 || open[0].ID != "b" {
		t.Fatalf("open: %+v", open)
	}
	sub, _ := s.Incidents(ctx, IncidentFilter{Prefix: "canary/"})
	if len(sub) != 1 {
		t.Fatal("prefix filter")
	}
	recent, _ := s.Incidents(ctx, IncidentFilter{From: time.Now().Add(-24 * time.Hour)})
	if len(recent) != 1 || recent[0].ID != "b" {
		t.Fatalf("overlap filter: %+v", recent)
	}
}

// Text from remote servers can carry NUL. jsonb and text columns refuse it, and
// that must not cost us the incident.
func TestNULFromRemoteText(t *testing.T) {
	s, ctx := open(t), context.Background()
	inc := model.Incident{ID: "i1", Key: "c", Name: "c", Worst: model.KindDown, Severity: model.Critical, Started: time.Now(),
		Lines: []model.ProbeLine{{Probe: "p", Error: "bad\x00byte \\u0000 stays"}}}
	if err := s.SaveIncident(ctx, inc); err != nil {
		t.Fatal(err)
	}
	got, err := s.Incidents(ctx, IncidentFilter{Open: true})
	if err != nil || len(got) != 1 || got[0].Lines[0].Error != "badbyte \\u0000 stays" {
		t.Fatalf("%v %+v", err, got)
	}
	if err := s.WriteSamples(ctx, []Sample{{Check: "c", Probe: "p", At: time.Now(), Reason: "x\x00y"}}); err != nil {
		t.Fatal(err)
	}
}

func TestUsernameIgnoresCase(t *testing.T) {
	s, ctx := open(t), context.Background()
	if _, err := s.CreateUser(ctx, "Leo", "password123", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "leo", "password123", RoleAdmin); err == nil || err.Error() != "user already exists" {
		t.Fatalf("got %v", err)
	}
	if _, err := s.Authenticate(ctx, "LEO", "password123"); err != nil {
		t.Fatal(err)
	}
}

// One batch can hold a series twice in a second, and many rows must arrive whole.
func TestWriteBatches(t *testing.T) {
	s, ctx := open(t), context.Background()
	now := time.Now().Truncate(time.Second)
	var batch []Sample
	for i := range 500 {
		batch = append(batch, Sample{Check: "c", Probe: "p", At: now.Add(time.Duration(-i) * time.Second), Up: true, Latency: 10})
	}
	batch = append(batch, Sample{Check: "c", Probe: "p", At: now, Up: false, Reason: "timeout"}) // the last one wins
	if err := s.WriteSamples(ctx, batch); err != nil {
		t.Fatal(err)
	}
	var n, up int
	if err := s.db.QueryRow(`SELECT COUNT(*), COUNT(*) FILTER (WHERE up) FROM samples`).Scan(&n, &up); err != nil || n != 500 || up != 499 {
		t.Fatalf("%v: %d rows, %d up", err, n, up)
	}
	sys := []SysSample{{Probe: "p", At: now, CPU: 1}, {Probe: "p", At: now, CPU: 2}, {Probe: "q", At: now, CPU: 3}}
	if err := s.WriteSys(ctx, sys); err != nil {
		t.Fatal(err)
	}
	var cpu float64
	if err := s.db.QueryRow(`SELECT cpu FROM sys WHERE probe = 'p'`).Scan(&cpu); err != nil || cpu != 2 {
		t.Fatalf("%v: cpu %v", err, cpu)
	}
}

// Detail from new agents reaches the charts from raw rows and from rollups; a
// bucket with old rows averages the detail over the rows that carried it.
func TestSysDetail(t *testing.T) {
	s, ctx := open(t), context.Background()
	now := time.Now().Truncate(time.Hour).Add(-2 * time.Hour)
	var xs []SysSample
	for i := range 60 {
		x := SysSample{Probe: "de", At: now.Add(time.Duration(i) * time.Minute), CPU: 50, MemTotal: 8e9}
		if i%2 == 0 { // every other row comes from an old agent
			x.Ext = &SysExt{CPUSteal: float64(10 + i%4), CPUIowait: 3, DiskRead: 1000, TCP: 400}
		}
		xs = append(xs, x)
	}
	if err := s.WriteSys(ctx, xs); err != nil {
		t.Fatal(err)
	}
	if err := s.Rollup(ctx, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	raw, err := s.SysSeries(ctx, "de", now, now.Add(time.Hour), 1)
	if err != nil || len(raw) != 1 {
		t.Fatalf("raw: %v %+v", err, raw)
	}
	if x := raw[0].X; x["cpu_steal"] != 11 || x["cpu_steal_max"] != 12 || x["cpu_iowait"] != 3 || x["tcp"] != 400 {
		t.Fatalf("raw detail: %+v", x)
	}
	week, err := s.SysSeries(ctx, "de", now.Add(-6*24*time.Hour), now.Add(time.Hour), 2000)
	if err != nil || len(week) == 0 {
		t.Fatalf("rollup: %v", err)
	}
	var steal, peak float64
	for _, p := range week {
		if v, ok := p.X["cpu_steal"]; ok {
			steal, peak = v, max(peak, p.X["cpu_steal_max"])
		}
	}
	if steal != 11 || peak != 12 {
		t.Fatalf("rollup steal %v peak %v: %+v", steal, peak, week)
	}
	old, _ := s.SysSeries(ctx, "nope", now, now.Add(time.Hour), 10)
	if len(old) != 0 {
		t.Fatal("unknown probe")
	}
}

func TestSparks(t *testing.T) {
	s, ctx := open(t), context.Background()
	now := time.Now().Truncate(time.Minute)
	var xs []SysSample
	for i := range 30 {
		xs = append(xs, SysSample{Probe: "a", At: now.Add(-time.Duration(i) * time.Minute), CPU: 10, RX: 100},
			SysSample{Probe: "b", At: now.Add(-time.Duration(i) * time.Minute), CPU: 20, TX: 5})
	}
	if err := s.WriteSys(ctx, xs); err != nil {
		t.Fatal(err)
	}
	sp, err := s.Sparks(ctx, now.Add(-time.Hour), now.Add(time.Minute), 300)
	if err != nil || len(sp) != 2 || sp["a"].CPU[0] != 10 || sp["b"].TX[0] != 5 || len(sp["a"].T) < 6 || len(sp["a"].T) > 8 {
		t.Fatalf("%v %+v", err, sp)
	}
}

// Users online per node: stored per poll, read back for the detector in
// order, and bucketed for charts with min and max.
func TestNodeUsers(t *testing.T) {
	s, ctx := open(t), context.Background()
	now := time.Now().Truncate(time.Minute)
	for i := range 20 {
		at := now.Add(time.Duration(i-20) * 30 * time.Second)
		if err := s.WriteNodeUsers(ctx, at, map[string]int{"pl": 800 - i*30, "swe": 880}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.NodeUsersSince(ctx, now.Add(-5*time.Minute))
	if err != nil || len(rows) != 20 || !rows[0].At.Before(rows[len(rows)-1].At) {
		t.Fatalf("since: %v %d", err, len(rows))
	}
	series, err := s.NodeUsersSeries(ctx, now.Add(-time.Hour), now.Add(time.Minute), 1)
	if err != nil || len(series["pl"]) == 0 {
		t.Fatalf("series: %v %+v", err, series)
	}
	lo, hi := 1<<30, 0 // a bucket edge may split the polls: judge across buckets
	for _, p := range series["pl"] {
		lo, hi = min(lo, p.Min), max(hi, p.Max)
	}
	if hi != 800 || lo != 800-19*30 || series["swe"][0].Avg != 880 {
		t.Fatalf("series: %+v", series)
	}
}

// A probe that ever sent metrics counts as seen, whatever table they sit in.
func TestProbesSeen(t *testing.T) {
	s, ctx := open(t), context.Background()
	if err := s.WriteSys(ctx, []SysSample{{Probe: "de", At: time.Now(), CPU: 1}}); err != nil {
		t.Fatal(err)
	}
	seen, err := s.ProbesSeen(ctx)
	if err != nil || len(seen) != 1 || seen[0] != "de" {
		t.Fatalf("%v %v", err, seen)
	}
}

// A batch that fails after its series were created must not leave their ids
// in the cache: the transaction rolled them back, and every later batch of
// that series would fail on the foreign key until a restart.
func TestFailedBatchDoesNotPoisonSeriesCache(t *testing.T) {
	s, ctx := open(t), context.Background()
	now := time.Now().Truncate(time.Second)
	bad := []Sample{
		{Check: "c", Probe: "p", At: now, Up: true, Latency: 10},
		{Check: "c", Probe: "p", At: time.Date(300000, 1, 1, 0, 0, 0, 0, time.UTC), Up: true}, // out of timestamptz range
	}
	if err := s.WriteSamples(ctx, bad); err == nil {
		t.Fatal("a sample out of range was accepted")
	}
	if err := s.WriteSamples(ctx, []Sample{{Check: "c", Probe: "p", At: now, Up: true, Latency: 10}}); err != nil {
		t.Fatalf("the next batch failed: %v", err)
	}
}

// The recorder retries a batch the database could not take now, and drops
// one it will never take: the same rows fail the same way forever.
func TestPermanentErrors(t *testing.T) {
	s, ctx := open(t), context.Background()
	err := s.WriteSamples(ctx, []Sample{{Check: "c", Probe: "p", At: time.Date(300000, 1, 1, 0, 0, 0, 0, time.UTC)}})
	if !Permanent(err) {
		t.Fatalf("out of range value counted as transient: %v", err)
	}
	dead, cancel := context.WithCancel(ctx)
	cancel()
	err = s.WriteSamples(dead, []Sample{{Check: "c", Probe: "p", At: time.Now()}})
	if err == nil || Permanent(err) {
		t.Fatalf("cancelled write counted as permanent: %v", err)
	}
	if Permanent(nil) {
		t.Fatal("nil is not an error")
	}
}

// A short window long ago reads the rollups: raw rows of that time are gone.
func TestOldShortWindowReadsRollups(t *testing.T) {
	s, ctx := open(t), context.Background()
	from := time.Now().Add(-5 * 24 * time.Hour).Truncate(time.Hour)
	var xs []Sample
	var sys []SysSample
	for i := range 360 { // six hours, one a minute
		at := from.Add(time.Duration(i) * time.Minute)
		xs = append(xs, Sample{Check: "c", Probe: "p", At: at, Up: true, Latency: 10})
		sys = append(sys, SysSample{Probe: "p", At: at, CPU: 5})
	}
	if err := s.WriteSamples(ctx, xs); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteSys(ctx, sys); err != nil {
		t.Fatal(err)
	}
	// Roll up as the hub did back then, then let today's retention drop the raw rows.
	if err := s.Rollup(ctx, from.Add(7*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.Rollup(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	pts, err := s.CheckSeries(ctx, "c", from, from.Add(6*time.Hour), 300)
	if err != nil || len(pts["p"]) == 0 {
		t.Fatalf("check series of an old window is empty: %v %+v", err, pts)
	}
	hs, err := s.SysSeries(ctx, "p", from, from.Add(6*time.Hour), 300)
	if err != nil || len(hs) == 0 {
		t.Fatalf("host series of an old window is empty: %v %+v", err, hs)
	}
}

// A rollup that failed must not forget what it had to roll up: the next one
// covers the same samples.
func TestFailedRollupKeepsDirty(t *testing.T) {
	s, ctx := open(t), context.Background()
	now := time.Now()
	at := now.Add(-2 * time.Hour).Truncate(5 * time.Minute)
	if err := s.WriteSamples(ctx, []Sample{{Check: "c", Probe: "p", At: at, Up: true, Latency: 10}}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteSys(ctx, []SysSample{{Probe: "p", At: at, CPU: 5}}); err != nil {
		t.Fatal(err)
	}
	dead, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.Rollup(dead, now); err == nil {
		t.Fatal("rollup with a cancelled context succeeded")
	}
	if err := s.Rollup(ctx, now); err != nil {
		t.Fatal(err)
	}
	var n, sn int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM samples_5m WHERE ts = $1`, at).Scan(&n); err != nil || n != 1 {
		t.Fatalf("samples_5m after a failed rollup: %v %d rows", err, n)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sys_5m WHERE ts = $1`, at).Scan(&sn); err != nil || sn != 1 {
		t.Fatalf("sys_5m after a failed rollup: %v %d rows", err, sn)
	}
}

// A late sample near the raw-retention edge must not rewrite an hour whose
// raw rows are already half purged.
func TestRollupSkipsPartlyPurgedHour(t *testing.T) {
	s, ctx := open(t), context.Background()
	h := time.Now().Truncate(time.Hour).Add(-72 * time.Hour)
	var xs []Sample
	var sys []SysSample
	for i := range 60 {
		xs = append(xs, Sample{Check: "c", Probe: "p", At: h.Add(time.Duration(i) * time.Minute), Up: true, Latency: 10})
		sys = append(sys, SysSample{Probe: "p", At: h.Add(time.Duration(i) * time.Minute), CPU: 5})
	}
	if err := s.WriteSamples(ctx, xs); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteSys(ctx, sys); err != nil {
		t.Fatal(err)
	}
	if err := s.Rollup(ctx, h.Add(61*time.Minute)); err != nil {
		t.Fatal(err)
	}
	later := h.Add(RawRetention + 30*time.Minute)
	if err := s.Rollup(ctx, later); err != nil { // purges the first half of hour h
		t.Fatal(err)
	}
	late := h.Add(45 * time.Minute)
	if err := s.WriteSamples(ctx, []Sample{{Check: "c2", Probe: "p", At: late, Up: true}}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteSys(ctx, []SysSample{{Probe: "q", At: late, CPU: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Rollup(ctx, later); err != nil {
		t.Fatal(err)
	}
	var n, sn int
	if err := s.db.QueryRow(`SELECT s.n FROM samples_1h s JOIN series x ON x.id = s.series WHERE x.check_id = 'c' AND s.ts = $1`, h).Scan(&n); err != nil || n != 60 {
		t.Fatalf("hour at the retention edge rewritten: %v n=%d", err, n)
	}
	if err := s.db.QueryRow(`SELECT n FROM sys_1h WHERE probe = 'p' AND ts = $1`, h).Scan(&sn); err != nil || sn != 60 {
		t.Fatalf("host hour at the retention edge rewritten: %v n=%d", err, sn)
	}
}

// Rollups average latency over the runs that measured it, not over every
// successful run: a check type without latency must not halve the line.
func TestRollupLatencyOverMeasuredRuns(t *testing.T) {
	s, ctx := open(t), context.Background()
	from := time.Now().Add(-5 * 24 * time.Hour).Truncate(time.Hour)
	var xs []Sample
	for i := range 60 {
		lat := 100.0
		if i%2 == 0 {
			lat = 0 // up, but nothing measured
		}
		xs = append(xs, Sample{Check: "c", Probe: "p", At: from.Add(time.Duration(i) * time.Minute), Up: true, Latency: lat})
	}
	if err := s.WriteSamples(ctx, xs); err != nil {
		t.Fatal(err)
	}
	if err := s.Rollup(ctx, from.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	pts, err := s.CheckSeries(ctx, "c", from, from.Add(time.Hour), 1)
	if err != nil || len(pts["p"]) != 1 || pts["p"][0].Lat == nil || *pts["p"][0].Lat != 100 {
		t.Fatalf("rollup latency: %v %+v", err, pts)
	}
	av, err := s.Availability(ctx, from)
	if err != nil || len(av) != 1 || av[0].Lat != 100 {
		t.Fatalf("availability latency: %v %+v", err, av)
	}
}

// The overlap filter (incidents still open or resolved after From) must be
// answerable from an index: history grows forever.
func TestIncidentOverlapUsesIndex(t *testing.T) {
	s, ctx := open(t), context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SET enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	q, args := incidentsQuery(IncidentFilter{From: time.Now().Add(-24 * time.Hour)})
	rows, err := conn.QueryContext(ctx, `EXPLAIN `+q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		rows.Scan(&line)
		plan.WriteString(line + "\n")
	}
	// An index condition on the end of the incident, not a walk over every
	// row (a seq scan or a full scan of the started index with a filter).
	if p := plan.String(); strings.Contains(p, "Seq Scan") || !strings.Contains(p, "Index Cond: (COALESCE(resolved") {
		t.Fatalf("the overlap filter scans the whole table:\n%s", p)
	}
}

// Heavy UI reads take a bounded share of the pool and give up after a
// deadline, so a burst of them cannot starve the recorder's writes.
func TestUIReadsAreBounded(t *testing.T) {
	s, ctx := open(t), context.Background()
	for range cap(s.reads) { // every read slot busy
		s.reads <- struct{}{}
	}
	short, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	if _, err := s.CheckSeries(short, "c", time.Now().Add(-time.Hour), time.Now(), 10); err == nil {
		t.Fatal("a read ran with every read slot taken")
	}
	if err := s.WriteSamples(ctx, []Sample{{Check: "c", Probe: "p", At: time.Now(), Up: true}}); err != nil {
		t.Fatalf("writes starved by reads: %v", err)
	}
	for range cap(s.reads) {
		<-s.reads
	}
	old := readTimeout
	readTimeout = time.Nanosecond
	defer func() { readTimeout = old }()
	if _, err := s.Incidents(ctx, IncidentFilter{}); err == nil {
		t.Fatal("a read outlived its deadline")
	}
}

// Hourly rollups equal an aggregate of the raw rows, whatever they are built
// from.
func TestHourlyRollupMatchesRaw(t *testing.T) {
	s, ctx := open(t), context.Background()
	now := time.Now().Truncate(time.Hour).Add(-3 * time.Hour)
	var xs []Sample
	var sys []SysSample
	for i := range 150 {
		at := now.Add(time.Duration(i) * time.Minute)
		xs = append(xs, Sample{Check: "c", Probe: "p", At: at, Up: i%7 != 0, Latency: float64(i % 3 * 10), TTFB: float64(i % 2 * 5), BPS: float64(i % 4 * 100)})
		x := SysSample{Probe: "p", At: at, CPU: float64(i % 50), RX: float64(i), MemTotal: 8e9}
		if i%3 != 0 {
			x.Ext = &SysExt{CPUSteal: float64(i % 9), TCP: 7}
		}
		sys = append(sys, x)
	}
	if err := s.WriteSamples(ctx, xs); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteSys(ctx, sys); err != nil {
		t.Fatal(err)
	}
	if err := s.Rollup(ctx, now.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var diff int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM (
		(SELECT series::text, date_bin('1 hour', ts, TIMESTAMPTZ 'epoch'), COUNT(*), SUM(up::int), SUM(latency), MIN(latency), MAX(latency),
			SUM(ttfb), COUNT(ttfb), SUM(bps), COUNT(bps), COUNT(latency) FROM samples GROUP BY 1, 2
		EXCEPT SELECT series::text, ts, n, n_up, lat_sum, lat_min, lat_max, ttfb_sum, ttfb_n, bps_sum, bps_n, lat_n FROM samples_1h)
		UNION ALL
		(SELECT probe, date_bin('1 hour', ts, TIMESTAMPTZ 'epoch'), COUNT(*), SUM(cpu), MAX(cpu), SUM(rx), MAX(rx), MAX(mem_total),
			COUNT(cpu_user), SUM(cpu_steal), MAX(cpu_steal), SUM(tcp) FROM sys GROUP BY 1, 2
		EXCEPT SELECT probe, ts, n, cpu_sum, cpu_max, rx_sum, rx_max, mem_total, n2, cpu_steal_sum, cpu_steal_max, tcp_sum FROM sys_1h)
	) d`).Scan(&diff); err != nil || diff != 0 {
		t.Fatalf("hourly rollup differs from raw: %v, %d rows", err, diff)
	}
}

// Rollups need date_bin (PostgreSQL 14): an older server is refused at
// startup with a clear message, not with a rollup error every minute.
func TestServerVersionCheck(t *testing.T) {
	if err := checkServerVersion(130011); err == nil || !strings.Contains(err.Error(), "PostgreSQL 14") {
		t.Fatalf("13.11 accepted: %v", err)
	}
	for _, v := range []int{140000, 160004} {
		if err := checkServerVersion(v); err != nil {
			t.Fatalf("%d refused: %v", v, err)
		}
	}
	open(t) // the test server itself passes
}

// Migration 2 is a literal now; the schema must still carry every column
// sysCols names, raw and rolled up.
func TestSchemaHasEverySysCol(t *testing.T) {
	s, ctx := open(t), context.Background()
	has := func(table, col string) bool {
		var n int
		s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2`, table, col).Scan(&n)
		return n == 1
	}
	for _, c := range sysCols {
		if !has("sys", c.name) {
			t.Errorf("sys.%s missing", c.name)
		}
		for _, tb := range []string{"sys_5m", "sys_1h"} {
			if !has(tb, c.name+"_sum") || (c.max && !has(tb, c.name+"_max")) || !has(tb, "n2") {
				t.Errorf("%s: %s rollup columns missing", tb, c.name)
			}
		}
	}
}

// Traffic over a day is rate times time from the 5-minute rollups.
func TestTrafficSince(t *testing.T) {
	s, ctx := open(t), context.Background()
	now := time.Now().Truncate(5 * time.Minute)
	var xs []SysSample
	for i := range 20 { // 10 minutes at 1000 B/s in, 500 B/s out, 30 s apart
		xs = append(xs, SysSample{Probe: "de", At: now.Add(-time.Duration(i) * 30 * time.Second), RX: 1000, TX: 500})
	}
	if err := s.WriteSys(ctx, xs); err != nil {
		t.Fatal(err)
	}
	if err := s.Rollup(ctx, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	tr, err := s.TrafficSince(ctx, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	// Three 5-minute buckets carry samples (the window straddles them): 3 x 300 s.
	if d := tr["de"]; d.RX < 600_000 || d.RX > 900_000 || d.TX < 300_000 || d.TX > 450_000 {
		t.Fatalf("traffic: %+v", d)
	}
}
