package geoip

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fake is a "database" whose file holds the one country it answers.
type fake struct{ cc string }

func (f fake) Country(netip.Addr) (string, error) { return f.cc, nil }
func (f fake) Close() error                       { return nil }

func openFake(path string) (table, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return fake{strings.TrimSpace(string(b))}, nil
}

func gz(s string) []byte {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write([]byte(s))
	w.Close()
	return b.Bytes()
}

// The database comes down on first use (last month's early in a month),
// is kept on disk, refreshed when a month old, and a failed refresh keeps
// the old one and does not hammer the server.
func TestDownloadAndRefresh(t *testing.T) {
	var hits atomic.Int32
	months := map[string]string{"2026-10": "de"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		cc, ok := months[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(gz(cc))
	}))
	defer srv.Close()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	d := &DB{Dir: dir, URL: func(m time.Time) string { return srv.URL + "/" + m.Format("2006-01") }, Now: func() time.Time { return now }, open: openFake}
	ip := netip.MustParseAddr("203.0.113.7")
	ctx := context.Background()

	if cc, err := d.Country(ctx, ip); err != nil || cc != "de" {
		t.Fatalf("first lookup: %q %v", cc, err)
	}
	if hits.Load() != 2 {
		t.Fatalf("November is not out yet, October must be fetched: %d requests", hits.Load())
	}
	d.Country(ctx, ip)
	if hits.Load() != 2 {
		t.Fatal("a fresh database is not downloaded again")
	}

	// A restarted hub uses the file on disk.
	d2 := &DB{Dir: dir, URL: d.URL, Now: d.Now, open: openFake}
	os.Chtimes(dir+"/dbip-country-2026-10.mmdb", now, now)
	if cc, _ := d2.Country(ctx, ip); cc != "de" || hits.Load() != 2 {
		t.Fatalf("restart: %q, %d requests", cc, hits.Load())
	}

	// A month on, the refresh fails: the old database still answers, and
	// the next lookups within the hour do not retry.
	now = now.Add(40 * 24 * time.Hour)
	delete(months, "2026-10")
	if cc, err := d.Country(ctx, ip); err != nil || cc != "de" {
		t.Fatalf("stale beats nothing: %q %v", cc, err)
	}
	h := hits.Load()
	d.Country(ctx, ip)
	if hits.Load() != h {
		t.Fatal("a failed download is not retried on every lookup")
	}
	// Then December comes out.
	now = now.Add(2 * time.Hour)
	months["2026-12"] = "nl"
	if cc, _ := d.Country(ctx, ip); cc != "nl" {
		t.Fatalf("refreshed: %q", cc)
	}
	if _, err := os.Stat(dir + "/dbip-country-2026-10.mmdb"); !os.IsNotExist(err) {
		t.Fatal("the old file is removed")
	}
}

func TestNoDatabase(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	d := &DB{Dir: t.TempDir(), URL: func(time.Time) string { return srv.URL }, open: openFake}
	if _, err := d.Country(context.Background(), netip.MustParseAddr("1.1.1.1")); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("no database must be an error saying why: %v", err)
	}
}
