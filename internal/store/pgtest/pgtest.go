// Package pgtest gives each test its own schema in a real PostgreSQL, so tests
// run in parallel and leave nothing behind. Point UF_TEST_DATABASE_URL at a
// throwaway server: docker compose -f deploy/compose.test.yml up -d
package pgtest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const Env = "UF_TEST_DATABASE_URL"

var (
	mu   sync.Mutex
	urls = map[testing.TB]string{}
)

// URL returns the same database URL for every call within one test, so a test
// can reopen the store and see what it wrote.
func URL(t testing.TB) string {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	if u, ok := urls[t]; ok {
		return u
	}
	base := os.Getenv(Env)
	if base == "" {
		t.Fatalf("%s is not set: tests need a real PostgreSQL (docker compose -f deploy/compose.test.yml up -d)", Env)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("%s: bad URL", Env)
	}
	b := make([]byte, 8)
	rand.Read(b)
	schema := "t_" + hex.EncodeToString(b)
	db, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		db.Close()
		t.Fatalf("create test schema: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		db.ExecContext(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		db.Close()
		mu.Lock()
		delete(urls, t)
		mu.Unlock()
	})
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	urls[t] = u.String()
	return urls[t]
}
