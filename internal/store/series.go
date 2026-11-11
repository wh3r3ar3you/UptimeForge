package store

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"strings"
	"time"
)

// Retention per resolution. Raw data feeds the last two days of charts; 5m
// and 1h rollups feed longer ranges and 90-day uptime.
const (
	RawRetention = 48 * time.Hour
	M5Retention  = 10 * 24 * time.Hour
	H1Retention  = 120 * 24 * time.Hour
)

// latNMigration counts the runs that measured latency per rollup bucket: an
// up run may carry none, and averaging over n_up drags the line down. Rows
// rolled up before it have lat_n NULL; reads fall back to n_up for them.
const latNMigration = `
ALTER TABLE samples_5m ADD COLUMN lat_n INTEGER;
ALTER TABLE samples_1h ADD COLUMN lat_n INTEGER;
`

type Sample struct {
	Check   string
	Probe   string
	At      time.Time
	Up      bool
	Latency float64 // ms; ignored for failures, which would only measure the timeout
	TTFB    float64
	BPS     float64
	Reason  string
}

type SysSample struct {
	Probe                string
	At                   time.Time
	CPU                  float64 // percent
	MemUsed, MemTotal    float64 // bytes
	DiskUsed, DiskTotal  float64
	RX, TX               float64 // bytes per second
	Load1, Load5, Load15 float64
	Ext                  *SysExt
}

// seriesID looks a series up in the cache, else creates it in tx. A new id
// goes to fresh, not the cache: it exists only once tx commits, and the
// caller merges fresh into the cache after that.
func (s *Store) seriesID(ctx context.Context, tx *sql.Tx, fresh map[[2]string]int64, check, probe string) (int64, error) {
	k := [2]string{check, probe}
	if id, ok := s.series[k]; ok {
		return id, nil
	}
	if id, ok := fresh[k]; ok {
		return id, nil
	}
	var id int64
	err := tx.QueryRowContext(ctx, `INSERT INTO series (check_id, probe_id) VALUES ($1, $2)
		ON CONFLICT (check_id, probe_id) DO UPDATE SET check_id = excluded.check_id RETURNING id`, check, probe).Scan(&id)
	if err != nil {
		return 0, err
	}
	fresh[k] = id
	return id, nil
}

// nullable turns a value into a NULL when zero says "not measured".
func nullable(v float64) *float64 {
	if v <= 0 {
		return nil
	}
	return &v
}

// WriteSamples sends the whole batch as arrays in one statement: a hub with many
// probes should not spend a round trip per row. Two samples of one series in the
// same second collapse into the last, as ON CONFLICT cannot touch a row twice.
func (s *Store) WriteSamples(ctx context.Context, samples []Sample) error {
	if len(samples) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	type key struct {
		series int64
		ts     int64
	}
	at := make(map[key]int, len(samples))
	var (
		ids         []int64
		times       []time.Time
		ups         []bool
		lats, ttfbs []*float64
		bpss        []*float64
		reasons     []*string
		oldest      int64
	)
	fresh := map[[2]string]int64{}
	for _, sm := range samples {
		id, err := s.seriesID(ctx, tx, fresh, sm.Check, sm.Probe)
		if err != nil {
			return err
		}
		ts := sm.At.Unix()
		var lat, ttfb *float64
		var reason *string
		if sm.Up {
			lat, ttfb = nullable(sm.Latency), nullable(sm.TTFB)
		} else {
			r := cleanText(sm.Reason)
			reason = &r
		}
		k := key{id, ts}
		if i, dup := at[k]; dup {
			ups[i], lats[i], ttfbs[i], bpss[i], reasons[i] = sm.Up, lat, ttfb, nullable(sm.BPS), reason
		} else {
			at[k] = len(ids)
			ids, times, ups = append(ids, id), append(times, time.Unix(ts, 0)), append(ups, sm.Up)
			lats, ttfbs, bpss, reasons = append(lats, lat), append(ttfbs, ttfb), append(bpss, nullable(sm.BPS)), append(reasons, reason)
		}
		if oldest == 0 || ts < oldest {
			oldest = ts
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO samples (series, ts, up, latency, ttfb, bps, reason)
		SELECT * FROM unnest($1::bigint[], $2::timestamptz[], $3::boolean[], $4::float8[], $5::float8[], $6::float8[], $7::text[])
		ON CONFLICT (series, ts) DO UPDATE SET up = excluded.up, latency = excluded.latency, ttfb = excluded.ttfb,
			bps = excluded.bps, reason = excluded.reason`, ids, times, ups, lats, ttfbs, bpss, reasons)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err // fresh ids die with the transaction
	}
	maps.Copy(s.series, fresh)
	if s.dirty == 0 || oldest < s.dirty {
		s.dirty = oldest
	}
	return nil
}

func (s *Store) WriteSys(ctx context.Context, samples []SysSample) error {
	if len(samples) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	type key struct {
		probe string
		ts    int64
	}
	last := make(map[key]SysSample, len(samples))
	oldest := int64(0)
	for _, m := range samples {
		ts := m.At.Unix()
		last[key{m.Probe, ts}] = m
		if oldest == 0 || ts < oldest {
			oldest = ts
		}
	}
	n := len(last)
	probes, times := make([]string, 0, n), make([]time.Time, 0, n)
	cols := make([][]float64, 10)
	ext := make([][]*float64, len(sysCols))
	for k, m := range last {
		probes, times = append(probes, k.probe), append(times, time.Unix(k.ts, 0))
		for i, v := range []float64{m.CPU, m.MemUsed, m.MemTotal, m.DiskUsed, m.DiskTotal, m.RX, m.TX, m.Load1, m.Load5, m.Load15} {
			cols[i] = append(cols[i], v)
		}
		var vs []float64
		if m.Ext != nil {
			vs = m.Ext.values()
		}
		for i := range sysCols {
			var v *float64
			if vs != nil {
				v = &vs[i]
			}
			ext[i] = append(ext[i], v)
		}
	}
	names := []string{"cpu", "mem_used", "mem_total", "disk_used", "disk_total", "rx", "tx", "load1", "load5", "load15"}
	for _, c := range sysCols {
		names = append(names, c.name)
	}
	args := []any{probes, times}
	casts := []string{"$1::text[]", "$2::timestamptz[]"}
	sets := make([]string, len(names))
	for i, c := range cols {
		args = append(args, c)
		casts = append(casts, fmt.Sprintf("$%d::float8[]", i+3))
	}
	for _, c := range ext {
		args = append(args, c)
		casts = append(casts, fmt.Sprintf("$%d::float8[]", len(casts)+1))
	}
	for i, nm := range names {
		sets[i] = nm + " = excluded." + nm
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO sys (probe, ts, %s) SELECT * FROM unnest(%s)
		ON CONFLICT (probe, ts) DO UPDATE SET %s`, strings.Join(names, ", "), strings.Join(casts, ", "), strings.Join(sets, ", ")), args...)
	if err != nil {
		return err
	}
	if s.sysDirty == 0 || oldest < s.sysDirty {
		s.sysDirty = oldest
	}
	return nil
}

// bucket rounds a timestamp column down to a step in seconds.
func bucket(col string, step int64) string {
	return fmt.Sprintf(`date_bin('%d seconds', %s, TIMESTAMPTZ 'epoch')`, step, col)
}

const sampleAgg = `COUNT(*), SUM(up::int), SUM(latency), MIN(latency), MAX(latency), SUM(ttfb), COUNT(ttfb), SUM(bps), COUNT(bps), COUNT(latency)`

const sysAgg = `COUNT(*), SUM(cpu), MAX(cpu), SUM(mem_used), MAX(mem_total), SUM(disk_used), MAX(disk_total),
	SUM(rx), MAX(rx), SUM(tx), MAX(tx), SUM(load1), SUM(load5), SUM(load15)`

// sampleAgg5m and sysAgg5m fold 5-minute buckets into bigger ones, column
// for column the same as sampleAgg and sysAgg over raw rows. A bucket from
// before lat_n existed counts its up runs as measured, as reads do.
const sampleAgg5m = `SUM(n), SUM(n_up), SUM(lat_sum), MIN(lat_min), MAX(lat_max), SUM(ttfb_sum), SUM(ttfb_n),
	SUM(bps_sum), SUM(bps_n), SUM(COALESCE(lat_n, n_up))`

const sysAgg5m = `SUM(n), SUM(cpu_sum), MAX(cpu_max), SUM(mem_sum), MAX(mem_total), SUM(disk_sum), MAX(disk_total),
	SUM(rx_sum), MAX(rx_max), SUM(tx_sum), MAX(tx_max), SUM(load1_sum), SUM(load5_sum), SUM(load15_sum)`

// Rollup refreshes 5-minute and hourly aggregates for everything written
// since the last call (plus the current buckets) and enforces retention.
func (s *Store) Rollup(ctx context.Context, now time.Time) (err error) {
	s.mu.Lock()
	dirty, sysDirty := s.dirty, s.sysDirty
	s.dirty, s.sysDirty = 0, 0
	s.mu.Unlock()
	defer func() {
		if err == nil {
			return
		}
		// What this run had to cover is still to be covered.
		s.mu.Lock()
		s.dirty, s.sysDirty = older(s.dirty, dirty), older(s.sysDirty, sysDirty)
		s.mu.Unlock()
	}()

	from := func(d int64) int64 {
		f := now.Add(-15 * time.Minute).Unix()
		if d != 0 && d < f {
			f = d
		}
		return f
	}
	// Raw rows older than edge are being purged, so a bucket that starts
	// before the first whole bucket after it may be rebuilt from what is left
	// of it: such buckets are only filled when missing, never rewritten.
	edge := now.Add(-RawRetention).Unix()
	f, sf := from(dirty), from(sysDirty)
	extCols, extRaw := sysRollupCols()
	extSet, extRolled := make([]string, len(extCols)), make([]string, len(extCols))
	for i, c := range extCols {
		extSet[i] = c + " = excluded." + c
		extRolled[i] = "SUM(" + c + ")"
		if strings.HasSuffix(c, "_max") {
			extRolled[i] = "MAX(" + c + ")"
		}
	}
	// 5-minute buckets come from raw rows; hourly ones from the 5-minute
	// buckets just refreshed, which is the same sum over 12 times fewer rows.
	steps := []struct {
		res                    int64
		samples, sys           string
		samplesSrc, sysSrc     string
		samplesAgg, sysAggExpr string
	}{
		{300, "samples_5m", "sys_5m", "samples", "sys", sampleAgg, sysAgg + ", " + strings.Join(extRaw, ", ")},
		{3600, "samples_1h", "sys_1h", "samples_5m", "sys_5m", sampleAgg5m, sysAgg5m + ", " + strings.Join(extRolled, ", ")},
	}
	for _, st := range steps {
		bf, sbf := time.Unix(f/st.res*st.res, 0), time.Unix(sf/st.res*st.res, 0)
		whole := time.Unix((edge+st.res-1)/st.res*st.res, 0)
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s
			(series, ts, n, n_up, lat_sum, lat_min, lat_max, ttfb_sum, ttfb_n, bps_sum, bps_n, lat_n)
			SELECT series, %s AS b, %s FROM %s WHERE ts >= $1 GROUP BY series, b
			ON CONFLICT (series, ts) DO UPDATE SET n = excluded.n, n_up = excluded.n_up, lat_sum = excluded.lat_sum,
				lat_min = excluded.lat_min, lat_max = excluded.lat_max, ttfb_sum = excluded.ttfb_sum, ttfb_n = excluded.ttfb_n,
				bps_sum = excluded.bps_sum, bps_n = excluded.bps_n, lat_n = excluded.lat_n
			WHERE excluded.ts >= $2`,
			st.samples, bucket("ts", st.res), st.samplesAgg, st.samplesSrc), bf, whole); err != nil {
			return fmt.Errorf("rollup %s: %w", st.samples, err)
		}
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s
			(probe, ts, n, cpu_sum, cpu_max, mem_sum, mem_total, disk_sum, disk_total, rx_sum, rx_max, tx_sum, tx_max,
			 load1_sum, load5_sum, load15_sum, %s)
			SELECT probe, %s AS b, %s FROM %s WHERE ts >= $1 GROUP BY probe, b
			ON CONFLICT (probe, ts) DO UPDATE SET n = excluded.n, cpu_sum = excluded.cpu_sum, cpu_max = excluded.cpu_max,
				mem_sum = excluded.mem_sum, mem_total = excluded.mem_total, disk_sum = excluded.disk_sum, disk_total = excluded.disk_total,
				rx_sum = excluded.rx_sum, rx_max = excluded.rx_max, tx_sum = excluded.tx_sum, tx_max = excluded.tx_max,
				load1_sum = excluded.load1_sum, load5_sum = excluded.load5_sum, load15_sum = excluded.load15_sum, %s
			WHERE excluded.ts >= $2`,
			st.sys, strings.Join(extCols, ", "), bucket("ts", st.res), st.sysAggExpr, st.sysSrc, strings.Join(extSet, ", ")), sbf, whole); err != nil {
			return fmt.Errorf("rollup %s: %w", st.sys, err)
		}
	}

	purge := []struct {
		table string
		keep  time.Duration
	}{
		{"samples", RawRetention}, {"samples_5m", M5Retention}, {"samples_1h", H1Retention},
		{"sys", RawRetention}, {"sys_5m", M5Retention}, {"sys_1h", H1Retention},
		{"node_users", UsersRetention},
	}
	for _, p := range purge {
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE ts < $1`, p.table), now.Add(-p.keep)); err != nil {
			return fmt.Errorf("purge %s: %w", p.table, err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires < $1`, now); err != nil {
		return fmt.Errorf("purge sessions: %w", err)
	}
	return nil
}

// older is the earlier of two dirty marks, where zero means none.
func older(a, b int64) int64 {
	if a == 0 || (b != 0 && b < a) {
		return b
	}
	return a
}

// Point is one chart bucket. Latency fields are averages of successful runs.
type Point struct {
	TS     int64    `json:"t"`
	N      int      `json:"n"`
	Up     float64  `json:"up"` // share of successful runs, 0..1
	Lat    *float64 `json:"lat,omitempty"`
	LatMin *float64 `json:"lat_min,omitempty"`
	LatMax *float64 `json:"lat_max,omitempty"`
	TTFB   *float64 `json:"ttfb,omitempty"`
	BPS    *float64 `json:"bps,omitempty"`
}

// pickTable chooses the finest table that still holds [from, to): retention
// is counted back from now, so a short window long ago reads a rollup.
func pickTable(from, to time.Time, raw, m5, h1 string) (string, int64) {
	span := max(time.Since(from), to.Sub(from))
	switch {
	case span <= RawRetention:
		return raw, 60
	case span <= M5Retention:
		return m5, 300
	}
	return h1, 3600
}

func bucketStep(from, to time.Time, minStep int64, maxPoints int) int64 {
	if maxPoints <= 0 {
		maxPoints = 300
	}
	step := int64(to.Sub(from).Seconds()) / int64(maxPoints)
	step = max(step, minStep)
	return (step + minStep - 1) / minStep * minStep
}

// CheckSeries returns chart points per probe for one check.
func (s *Store) CheckSeries(ctx context.Context, check string, from, to time.Time, maxPoints int) (map[string][]Point, error) {
	table, res := pickTable(from, to, "samples", "samples_5m", "samples_1h")
	step := bucketStep(from, to, res, maxPoints)
	var q string
	if table == "samples" {
		q = fmt.Sprintf(`SELECT x.probe_id, EXTRACT(EPOCH FROM %s)::bigint AS b, COUNT(*), SUM(s.up::int), AVG(s.latency), MIN(s.latency), MAX(s.latency),
			AVG(s.ttfb), AVG(s.bps)
			FROM samples s JOIN series x ON x.id = s.series
			WHERE x.check_id = $1 AND s.ts >= $2 AND s.ts < $3 GROUP BY x.probe_id, b ORDER BY b`, bucket("s.ts", step))
	} else {
		q = fmt.Sprintf(`SELECT x.probe_id, EXTRACT(EPOCH FROM %[1]s)::bigint AS b, SUM(s.n), SUM(s.n_up),
			SUM(s.lat_sum) / NULLIF(SUM(COALESCE(s.lat_n, s.n_up)), 0), MIN(s.lat_min), MAX(s.lat_max),
			SUM(s.ttfb_sum) / NULLIF(SUM(s.ttfb_n), 0), SUM(s.bps_sum) / NULLIF(SUM(s.bps_n), 0)
			FROM %[2]s s JOIN series x ON x.id = s.series
			WHERE x.check_id = $1 AND s.ts >= $2 AND s.ts < $3 GROUP BY x.probe_id, b ORDER BY b`, bucket("s.ts", step), table)
	}
	ctx, done, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	rows, err := s.db.QueryContext(ctx, q, check, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]Point{}
	for rows.Next() {
		var probe string
		var p Point
		var nUp int
		if err := rows.Scan(&probe, &p.TS, &p.N, &nUp, &p.Lat, &p.LatMin, &p.LatMax, &p.TTFB, &p.BPS); err != nil {
			return nil, err
		}
		if p.N > 0 {
			p.Up = float64(nUp) / float64(p.N)
		}
		out[probe] = append(out[probe], p)
	}
	return out, rows.Err()
}

// Availability is successful/total runs per check and probe since from.
type Availability struct {
	Check string  `json:"check"`
	Probe string  `json:"probe"`
	N     int     `json:"n"`
	Up    float64 `json:"up"`
	Lat   float64 `json:"lat"`
}

func (s *Store) Availability(ctx context.Context, from time.Time) ([]Availability, error) {
	table, _ := pickTable(from, time.Now(), "samples", "samples_5m", "samples_1h")
	var q string
	if table == "samples" {
		q = `SELECT x.check_id, x.probe_id, COUNT(*), SUM(s.up::int), COALESCE(AVG(s.latency), 0)
			FROM samples s JOIN series x ON x.id = s.series WHERE s.ts >= $1 GROUP BY x.check_id, x.probe_id`
	} else {
		q = fmt.Sprintf(`SELECT x.check_id, x.probe_id, SUM(s.n), SUM(s.n_up), COALESCE(SUM(s.lat_sum) / NULLIF(SUM(COALESCE(s.lat_n, s.n_up)), 0), 0)
			FROM %s s JOIN series x ON x.id = s.series WHERE s.ts >= $1 GROUP BY x.check_id, x.probe_id`, table)
	}
	ctx, done, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	rows, err := s.db.QueryContext(ctx, q, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Availability
	for rows.Next() {
		var a Availability
		var nUp int
		if err := rows.Scan(&a.Check, &a.Probe, &a.N, &nUp, &a.Lat); err != nil {
			return nil, err
		}
		if a.N > 0 {
			a.Up = float64(nUp) / float64(a.N)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

type SysPoint struct {
	TS        int64   `json:"t"`
	CPU       float64 `json:"cpu"`
	CPUMax    float64 `json:"cpu_max"`
	Mem       float64 `json:"mem"`
	MemTotal  float64 `json:"mem_total"`
	Disk      float64 `json:"disk"`
	DiskTotal float64 `json:"disk_total"`
	RX        float64 `json:"rx"`
	RXMax     float64 `json:"rx_max"`
	TX        float64 `json:"tx"`
	TXMax     float64 `json:"tx_max"`
	Load1     float64 `json:"load1"`
	Load5     float64 `json:"load5"`
	Load15    float64 `json:"load15"`
	// X holds the detail (sysCols, plus "<name>_max" peaks); keys are
	// missing where the agent did not send them.
	X map[string]float64 `json:"x,omitempty"`
}

func (s *Store) SysSeries(ctx context.Context, probe string, from, to time.Time, maxPoints int) ([]SysPoint, error) {
	table, res := pickTable(from, to, "sys", "sys_5m", "sys_1h")
	step := bucketStep(from, to, res, maxPoints)
	exprs, keys := sysReadExprs(table == "sys")
	extSQL := strings.Join(exprs, ", ")
	var q string
	if table == "sys" {
		q = fmt.Sprintf(`SELECT EXTRACT(EPOCH FROM %[1]s)::bigint AS b, AVG(cpu), MAX(cpu), AVG(mem_used), MAX(mem_total), AVG(disk_used), MAX(disk_total),
			AVG(rx), MAX(rx), AVG(tx), MAX(tx), AVG(load1), AVG(load5), AVG(load15), %[2]s
			FROM sys WHERE probe = $1 AND ts >= $2 AND ts < $3 GROUP BY b ORDER BY b`, bucket("ts", step), extSQL)
	} else {
		q = fmt.Sprintf(`SELECT EXTRACT(EPOCH FROM %[1]s)::bigint AS b, SUM(cpu_sum) / SUM(n), MAX(cpu_max), SUM(mem_sum) / SUM(n), MAX(mem_total),
			SUM(disk_sum) / SUM(n), MAX(disk_total), SUM(rx_sum) / SUM(n), MAX(rx_max), SUM(tx_sum) / SUM(n), MAX(tx_max),
			SUM(load1_sum) / SUM(n), SUM(load5_sum) / SUM(n), SUM(load15_sum) / SUM(n), %[3]s
			FROM %[2]s WHERE probe = $1 AND ts >= $2 AND ts < $3 GROUP BY b ORDER BY b`, bucket("ts", step), table, extSQL)
	}
	ctx, done, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	rows, err := s.db.QueryContext(ctx, q, probe, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SysPoint
	for rows.Next() {
		var p SysPoint
		ext := make([]sql.NullFloat64, len(keys))
		dst := []any{&p.TS, &p.CPU, &p.CPUMax, &p.Mem, &p.MemTotal, &p.Disk, &p.DiskTotal,
			&p.RX, &p.RXMax, &p.TX, &p.TXMax, &p.Load1, &p.Load5, &p.Load15}
		for i := range ext {
			dst = append(dst, &ext[i])
		}
		if err := rows.Scan(dst...); err != nil {
			return nil, err
		}
		for i, v := range ext {
			if v.Valid {
				if p.X == nil {
					p.X = make(map[string]float64, len(keys))
				}
				p.X[keys[i]] = v.Float64
			}
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// FirstSeen is the time of the oldest stored sample of a check, zero if none.
func (s *Store) FirstSeen(ctx context.Context, check string) (time.Time, error) {
	var ts sql.NullTime
	ctx, done, err := s.read(ctx)
	if err != nil {
		return time.Time{}, err
	}
	defer done()
	err = s.db.QueryRowContext(ctx, `SELECT MIN(t) FROM (
		SELECT MIN(s.ts) AS t FROM samples_1h s JOIN series x ON x.id = s.series WHERE x.check_id = $1
		UNION ALL SELECT MIN(s.ts) FROM samples s JOIN series x ON x.id = s.series WHERE x.check_id = $1) m`, check).Scan(&ts)
	if err != nil || !ts.Valid {
		return time.Time{}, err
	}
	return ts.Time, nil
}

// Spark is a small per-server trend for the dashboard cards.
type Spark struct {
	T   []int64   `json:"t"`
	CPU []float64 `json:"cpu"`
	RX  []float64 `json:"rx"`
	TX  []float64 `json:"tx"`
}

// Sparks reads every server's recent trend in one query: a dashboard of fifty
// cards must not cost fifty round trips.
func (s *Store) Sparks(ctx context.Context, from, to time.Time, step int64) (map[string]*Spark, error) {
	ctx, done, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT probe, EXTRACT(EPOCH FROM %s)::bigint AS b, AVG(cpu), AVG(rx), AVG(tx)
		FROM sys WHERE ts >= $1 AND ts < $2 GROUP BY probe, b ORDER BY probe, b`, bucket("ts", step)), from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*Spark{}
	for rows.Next() {
		var probe string
		var t int64
		var cpu, rx, tx sql.NullFloat64
		if err := rows.Scan(&probe, &t, &cpu, &rx, &tx); err != nil {
			return nil, err
		}
		sp := out[probe]
		if sp == nil {
			sp = &Spark{}
			out[probe] = sp
		}
		sp.T, sp.CPU, sp.RX, sp.TX = append(sp.T, t), append(sp.CPU, cpu.Float64), append(sp.RX, rx.Float64), append(sp.TX, tx.Float64)
	}
	return out, rows.Err()
}

// ProbesSeen lists probes that ever sent host metrics, that is, ever connected.
func (s *Store) ProbesSeen(ctx context.Context) ([]string, error) {
	ctx, done, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	rows, err := s.db.QueryContext(ctx, `SELECT probe FROM sys_1h UNION SELECT probe FROM sys_5m UNION SELECT probe FROM sys`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
