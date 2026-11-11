package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const prefsMigration = `ALTER TABLE users ADD COLUMN prefs JSONB NOT NULL DEFAULT '{}'::jsonb;`

// MaxPrefs bounds what a user may store: view settings, not data.
const MaxPrefs = 16 << 10

// Prefs returns a user's UI settings (columns, widths, density, sort, theme)
// as stored; the hub does not interpret them.
func (s *Store) Prefs(ctx context.Context, userID int64) (json.RawMessage, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT prefs FROM users WHERE id = $1`, userID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return json.RawMessage(`{}`), nil
	}
	return raw, err
}

// SetPrefs replaces a user's UI settings; they must be a JSON object.
func (s *Store) SetPrefs(ctx context.Context, userID int64, prefs json.RawMessage) error {
	if len(prefs) > MaxPrefs {
		return errors.New("settings are too large")
	}
	var obj map[string]any
	if err := json.Unmarshal(prefs, &obj); err != nil || obj == nil {
		return errors.New("settings must be a JSON object")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE users SET prefs = $2 WHERE id = $1`, userID, CleanJSON(prefs))
	return err
}

// Traffic is bytes moved over a window.
type Traffic struct {
	RX float64 `json:"rx"`
	TX float64 `json:"tx"`
}

// TrafficSince sums each probe's traffic since a time from the 5-minute
// rollups: average rate times the bucket length, so gaps (an agent offline)
// count as nothing rather than being stretched over.
func (s *Store) TrafficSince(ctx context.Context, since time.Time) (map[string]Traffic, error) {
	ctx, done, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	rows, err := s.db.QueryContext(ctx, `SELECT probe, SUM(rx_sum / n * 300), SUM(tx_sum / n * 300)
		FROM sys_5m WHERE ts >= $1 AND n > 0 GROUP BY probe`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Traffic{}
	for rows.Next() {
		var p string
		var rx, tx sql.NullFloat64
		if err := rows.Scan(&p, &rx, &tx); err != nil {
			return nil, err
		}
		out[p] = Traffic{rx.Float64, tx.Float64}
	}
	return out, rows.Err()
}
