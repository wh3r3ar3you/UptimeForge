package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/model"
)

// incidentsEndsMigration indexes when an incident ends (infinity while open),
// so "overlaps a window" does not walk the whole history.
const incidentsEndsMigration = `
CREATE INDEX incidents_ends ON incidents ((COALESCE(resolved, 'infinity'::timestamptz)));
`

// SaveIncident upserts an incident; called on open, every change and resolve.
func (s *Store) SaveIncident(ctx context.Context, inc model.Incident) error {
	b, err := json.Marshal(inc)
	if err != nil {
		return err
	}
	var resolved any
	if !inc.Resolved.IsZero() {
		resolved = inc.Resolved
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO incidents (id, key, kind, severity, started, resolved, data)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (id) DO UPDATE SET kind = excluded.kind, severity = excluded.severity,
			resolved = excluded.resolved, data = excluded.data`,
		inc.ID, inc.Key, string(inc.Worst), string(inc.Severity), inc.Started, resolved, cleanJSON(b))
	return err
}

type IncidentFilter struct {
	Open   bool     // only unresolved
	Keys   []string // only these incident keys (check ids, probe:<id>, ...)
	Prefix string   // keys starting with this, e.g. a subscription "canary/"
	From   time.Time
	To     time.Time
	Limit  int
}

// Incidents lists newest first. With From/To it returns incidents that
// overlap the window, which is what uptime calculations need.
func (s *Store) Incidents(ctx context.Context, f IncidentFilter) ([]model.Incident, error) {
	q, args := incidentsQuery(f)
	ctx, done, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Incident{} // "[]", not null, for the API
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var inc model.Incident
		if err := json.Unmarshal([]byte(data), &inc); err != nil {
			return nil, err
		}
		out = append(out, inc)
	}
	return out, rows.Err()
}

func incidentsQuery(f IncidentFilter) (string, []any) {
	q := `SELECT data FROM incidents WHERE TRUE`
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if f.Open {
		q += ` AND resolved IS NULL`
	}
	if len(f.Keys) > 0 {
		in := make([]string, len(f.Keys))
		for i, k := range f.Keys {
			in[i] = arg(k)
		}
		q += ` AND key IN (` + strings.Join(in, ",") + `)`
	}
	if f.Prefix != "" {
		q += ` AND key LIKE ` + arg(strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(f.Prefix)+"%") + ` ESCAPE '\'`
	}
	if !f.To.IsZero() {
		q += ` AND started < ` + arg(f.To)
	}
	if !f.From.IsZero() { // still open, or resolved after From: incidents_ends answers it
		q += ` AND COALESCE(resolved, 'infinity'::timestamptz) >= ` + arg(f.From)
	}
	q += ` ORDER BY started DESC`
	if f.Limit > 0 {
		q += ` LIMIT ` + arg(f.Limit)
	}
	return q, args
}
