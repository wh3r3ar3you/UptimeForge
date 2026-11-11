package store

import (
	"context"
	"fmt"
	"time"
)

// UsersRetention keeps two weeks of users online per Remnawave node: enough
// to see what normal looks like on any weekday.
const UsersRetention = 14 * 24 * time.Hour

const nodeUsersMigration = `
CREATE TABLE node_users (
	node TEXT NOT NULL, ts TIMESTAMPTZ NOT NULL, users INTEGER NOT NULL, PRIMARY KEY (node, ts));
CREATE INDEX node_users_ts ON node_users (ts);
`

// WriteNodeUsers stores one panel poll: users online per node id.
func (s *Store) WriteNodeUsers(ctx context.Context, at time.Time, users map[string]int) error {
	if len(users) == 0 {
		return nil
	}
	nodes, counts := make([]string, 0, len(users)), make([]int64, 0, len(users))
	for n, c := range users {
		nodes, counts = append(nodes, n), append(counts, int64(c))
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO node_users (node, ts, users)
		SELECT n, $2, c FROM unnest($1::text[], $3::bigint[]) AS u(n, c)
		ON CONFLICT (node, ts) DO UPDATE SET users = excluded.users`, nodes, at.Truncate(time.Second), counts)
	return err
}

// UsersSample is one stored poll of one node.
type UsersSample struct {
	Node  string
	At    time.Time
	Users int
}

// NodeUsersSince returns every stored poll since a time, oldest first: the
// history a restarted hub needs to judge drops right away.
func (s *Store) NodeUsersSince(ctx context.Context, since time.Time) ([]UsersSample, error) {
	ctx, done, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	rows, err := s.db.QueryContext(ctx, `SELECT node, ts, users FROM node_users WHERE ts >= $1 ORDER BY ts, node`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsersSample
	for rows.Next() {
		var x UsersSample
		if err := rows.Scan(&x.Node, &x.At, &x.Users); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// UsersPoint is one chart bucket of users online on a node.
type UsersPoint struct {
	TS  int64   `json:"t"`
	Avg float64 `json:"avg"`
	Min int     `json:"min"`
	Max int     `json:"max"`
}

// NodeUsersSeries buckets users online per node for charts.
func (s *Store) NodeUsersSeries(ctx context.Context, from, to time.Time, maxPoints int) (map[string][]UsersPoint, error) {
	step := bucketStep(from, to, 30, maxPoints)
	ctx, done, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT node, EXTRACT(EPOCH FROM %s)::bigint AS b, AVG(users), MIN(users), MAX(users)
		FROM node_users WHERE ts >= $1 AND ts < $2 GROUP BY node, b ORDER BY node, b`, bucket("ts", step)), from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]UsersPoint{}
	for rows.Next() {
		var node string
		var p UsersPoint
		if err := rows.Scan(&node, &p.TS, &p.Avg, &p.Min, &p.Max); err != nil {
			return nil, err
		}
		out[node] = append(out[node], p)
	}
	return out, rows.Err()
}

// UsersOnline is the largest and the mean total of users online across
// nodes, one total per poll, over [from, to).
func (s *Store) UsersOnline(ctx context.Context, from, to time.Time) (peak, avg int, err error) {
	var p, a float64
	ctx, done, err := s.read(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer done()
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(t), 0), COALESCE(AVG(t), 0) FROM
		(SELECT SUM(users) AS t FROM node_users WHERE ts >= $1 AND ts < $2 GROUP BY ts) x`, from, to).Scan(&p, &a)
	return int(p), int(a), err
}
