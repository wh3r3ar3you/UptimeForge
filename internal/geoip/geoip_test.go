package geoip

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strconv"
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

func TestShortISP(t *testing.T) {
	for in, want := range map[string]string{
		"MTS PJSC":            "mts",
		"Rostelecom OJSC":     "rostelecom",
		`LLC "Beeline"`:       "beeline",
		"T2 Mobile LLC":       "t2-mobile",
		"":                    "",
		"Acme Ltd":            "acme",
		"Acme JSC":            "acme",
		"OOO Romashka":        "romashka",
		"AO Telecom":          "telecom",
		"Acme Inc.":           "acme",
		"Acme GmbH":           "acme",
		"Hetzner Online B.V.": "hetzner-online",
		"LLC":                 "llc",
	} {
		if got := ShortISP(in); got != want {
			t.Errorf("ShortISP(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakeASN answers from its file: "<number> <organization>".
type fakeASN struct {
	num uint
	org string
}

func (f fakeASN) ASN(netip.Addr) (uint, string, error) { return f.num, f.org, nil }
func (f fakeASN) Close() error                         { return nil }

func openFakeASN(path string) (asnTable, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	num, org, _ := strings.Cut(strings.TrimSpace(string(b)), " ")
	n, err := strconv.ParseUint(num, 10, 32)
	if err != nil {
		return nil, err
	}
	return fakeASN{uint(n), org}, nil
}

func TestNetworkLookup(t *testing.T) {
	var asnHits atomic.Int32
	asnOK := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/country/"):
			w.Write(gz("ru"))
		case asnOK:
			asnHits.Add(1)
			w.Write(gz("8359 MTS PJSC"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	d := &DB{
		Dir:     dir,
		URL:     func(m time.Time) string { return srv.URL + "/country/" + m.Format("2006-01") },
		ASNURL:  func(m time.Time) string { return srv.URL + "/asn/" + m.Format("2006-01") },
		Now:     func() time.Time { return now },
		open:    openFake,
		openASN: openFakeASN,
	}
	ctx := context.Background()
	ip := netip.MustParseAddr("203.0.113.7")

	got, err := d.Network(ctx, ip)
	want := Network{IP: ip, Country: "ru", ASN: "as8359", Org: "MTS PJSC"}
	if err != nil || got != want {
		t.Fatalf("Network = %+v, %v; want %+v", got, err, want)
	}
	if _, err := os.Stat(dir + "/dbip-asn-2026-11.mmdb"); err != nil {
		t.Fatalf("the ASN database is kept next to the country one: %v", err)
	}
	if got, _ := d.Network(ctx, netip.MustParseAddr("::ffff:203.0.113.7")); got.ASN != "as8359" {
		t.Fatalf("a mapped IPv4 address: %+v", got)
	}
	if asnHits.Load() != 1 {
		t.Fatalf("a fresh ASN database is not downloaded again: %d", asnHits.Load())
	}

	for _, s := range []string{"10.0.0.1", "192.168.1.1", "127.0.0.1", "169.254.1.1", "0.0.0.0", "::1", "fd00::1"} {
		if _, err := d.Network(ctx, netip.MustParseAddr(s)); err == nil {
			t.Errorf("%s: a private address must be an error", s)
		}
	}
	if _, err := d.Network(ctx, netip.Addr{}); err == nil {
		t.Error("the zero address must be an error")
	}
}

// The two databases fail independently.
func TestNetworkPartial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/country/") {
			w.Write(gz("de"))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	d := &DB{
		Dir:     t.TempDir(),
		URL:     func(m time.Time) string { return srv.URL + "/country/x" },
		ASNURL:  func(m time.Time) string { return srv.URL + "/asn/x" },
		open:    openFake,
		openASN: openFakeASN,
	}
	ctx := context.Background()
	ip := netip.MustParseAddr("203.0.113.7")
	n, err := d.Network(ctx, ip)
	if err != nil || n.Country != "de" || n.ASN != "" || n.Org != "" {
		t.Fatalf("no ASN database: country must still answer: %+v %v", n, err)
	}
	if cc, err := d.Country(ctx, ip); err != nil || cc != "de" {
		t.Fatalf("Country: %q %v", cc, err)
	}

	// Both down: an error.
	srv2 := httptest.NewServer(http.NotFoundHandler())
	defer srv2.Close()
	d2 := &DB{Dir: t.TempDir(), URL: func(time.Time) string { return srv2.URL }, ASNURL: func(time.Time) string { return srv2.URL }, open: openFake, openASN: openFakeASN}
	if _, err := d2.Network(ctx, ip); err == nil {
		t.Fatal("nothing known must be an error")
	}
}

// With LookupOnly a lookup never downloads (it would within one agent's few
// seconds); Prepare does, in its own context, and is not held back by the
// hour a failed lookup download waits: its caller has its own backoff.
func TestLookupOnlyAndPrepare(t *testing.T) {
	var hits atomic.Int32
	var up atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !up.Load() {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/country/") {
			w.Write(gz("ru"))
			return
		}
		w.Write(gz("8359 MTS PJSC"))
	}))
	defer srv.Close()
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	d := &DB{
		Dir:        t.TempDir(),
		URL:        func(m time.Time) string { return srv.URL + "/country/" + m.Format("2006-01") },
		ASNURL:     func(m time.Time) string { return srv.URL + "/asn/" + m.Format("2006-01") },
		Now:        func() time.Time { return now },
		LookupOnly: true,
		open:       openFake,
		openASN:    openFakeASN,
	}
	ctx := context.Background()
	ip := netip.MustParseAddr("203.0.113.7")
	if _, err := d.Network(ctx, ip); err == nil {
		t.Fatal("no database yet must be an error")
	}
	if hits.Load() != 0 {
		t.Fatalf("a lookup downloaded: %d requests", hits.Load())
	}
	if err := d.Prepare(ctx); err == nil || hits.Load() == 0 {
		t.Fatalf("Prepare against a dead server: err %v, %d requests", err, hits.Load())
	}
	up.Store(true)
	if err := d.Prepare(ctx); err != nil {
		t.Fatalf("Prepare right after a failure must try again: %v", err)
	}
	got, err := d.Network(ctx, ip)
	if want := (Network{IP: ip, Country: "ru", ASN: "as8359", Org: "MTS PJSC"}); err != nil || got != want {
		t.Fatalf("Network = %+v, %v; want %+v", got, err, want)
	}
	h := hits.Load()
	if err := d.Prepare(ctx); err != nil || hits.Load() != h {
		t.Fatalf("fresh databases are not downloaded again: %v, %d requests", err, hits.Load()-h)
	}
	// A month and more on, a lookup still answers from the stale files and
	// does not download; Prepare refreshes.
	now = now.Add(40 * 24 * time.Hour)
	if got, err := d.Network(ctx, ip); err != nil || got.ASN != "as8359" || hits.Load() != h {
		t.Fatalf("stale lookup: %+v %v, %d requests", got, err, hits.Load()-h)
	}
	if err := d.Prepare(ctx); err != nil || hits.Load() == h {
		t.Fatalf("Prepare must refresh stale databases: %v", err)
	}
}
