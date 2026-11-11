package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Bar is one bucket of a check's pulse: how many results it had across all
// probes and how many were up. Worst is filled by the API from incidents.
type Bar struct {
	N     int    `json:"n"`
	Up    int    `json:"up"`
	Worst string `json:"worst,omitempty"`
}

// CheckBars splits [from, to) into equal buckets and counts every check's
// results in each, in one query. A subscription config ("sub/cfg") has its
// own bars, and the subscription ("sub") gets the sum of its configs'. Each
// gets exactly buckets bars; an empty bucket is a zero Bar.
func (s *Store) CheckBars(ctx context.Context, from, to time.Time, buckets int) (map[string][]Bar, error) {
	if buckets <= 0 || !to.After(from) {
		return map[string][]Bar{}, nil
	}
	step := to.Sub(from).Seconds() / float64(buckets)
	table, _ := pickTable(from, to, "samples", "samples_5m", "samples_1h")
	count, up := "COUNT(*)", "SUM(s.up::int)"
	if table != "samples" {
		count, up = "SUM(s.n)", "SUM(s.n_up)"
	}
	q := fmt.Sprintf(`SELECT x.check_id, LEAST(FLOOR(EXTRACT(EPOCH FROM s.ts - $1) / $3)::int, $4 - 1) AS b, %s, %s
		FROM %s s JOIN series x ON x.id = s.series
		WHERE s.ts >= $1 AND s.ts < $2
		GROUP BY 1, 2`, count, up, table)
	ctx, done, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	rows, err := s.db.QueryContext(ctx, q, from, to, step, buckets)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]Bar{}
	for rows.Next() {
		var id string
		var b, n, nUp int
		if err := rows.Scan(&id, &b, &n, &nUp); err != nil {
			return nil, err
		}
		add := func(id string) {
			bars := out[id]
			if bars == nil {
				bars = make([]Bar, buckets)
				out[id] = bars
			}
			bars[b].N += n
			bars[b].Up += nUp
		}
		add(id)
		if sub, _, ok := strings.Cut(id, "/"); ok {
			add(sub)
		}
	}
	return out, rows.Err()
}
