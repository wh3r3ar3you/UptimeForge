// Package geoip says which country an IP address is in, from the DB-IP
// IP-to-Country Lite database (CC BY 4.0, https://db-ip.com). The hub
// downloads it on first use and refreshes it monthly, so lookups never send
// an address anywhere.
package geoip

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"
)

// Attribution is what the license asks to show next to the results.
const Attribution = "IP Geolocation by DB-IP"

const (
	maxAge     = 35 * 24 * time.Hour // a monthly database, with a few days' slack
	retryAfter = time.Hour           // a failed download is not retried on every lookup
	maxSize    = 200 << 20           // the unpacked database is ~10 MB
)

// table is an opened database.
type table interface {
	Country(ip netip.Addr) (string, error)
	Close() error
}

// DB is safe for concurrent use. Dir is where the database file lives.
type DB struct {
	Dir  string
	HTTP *http.Client
	// URL is where a month's database is downloaded from (tests point it elsewhere).
	URL func(month time.Time) string
	Now func() time.Time

	open func(path string) (table, error)

	mu      sync.Mutex
	t       table
	path    string
	at      time.Time // when the open file was downloaded
	failed  time.Time // last failed download
	lastErr error
}

func (d *DB) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *DB) url(m time.Time) string {
	if d.URL != nil {
		return d.URL(m)
	}
	return "https://download.db-ip.com/free/dbip-country-lite-" + m.Format("2006-01") + ".mmdb.gz"
}

// Country returns the lower-case ISO code of ip's country, "" when the
// database does not know it.
func (d *DB) Country(ctx context.Context, ip netip.Addr) (string, error) {
	t, err := d.table(ctx)
	if err != nil {
		return "", err
	}
	return t.Country(ip.Unmap())
}

func (d *DB) table(ctx context.Context) (table, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	if d.t == nil {
		if p, at := d.newest(); p != "" {
			if t, err := d.opener()(p); err == nil {
				d.t, d.path, d.at = t, p, at
			}
		}
	}
	if d.t != nil && now.Sub(d.at) < maxAge {
		return d.t, nil
	}
	if !d.failed.IsZero() && now.Sub(d.failed) < retryAfter {
		if d.t != nil {
			return d.t, nil // stale beats nothing
		}
		return nil, d.lastErr
	}
	p, err := d.download(ctx, now)
	if err == nil {
		var t table
		if t, err = d.opener()(p); err == nil {
			old, oldPath := d.t, d.path
			d.t, d.path, d.at, d.failed = t, p, now, time.Time{}
			if old != nil && oldPath != p {
				old.Close()
				os.Remove(oldPath)
			}
			return t, nil
		}
		os.Remove(p)
	}
	d.failed, d.lastErr = now, fmt.Errorf("geoip database: %w", err)
	if d.t != nil {
		return d.t, nil
	}
	return nil, d.lastErr
}

func (d *DB) opener() func(string) (table, error) {
	if d.open != nil {
		return d.open
	}
	return openMMDB
}

// newest is the latest database already on disk.
func (d *DB) newest() (string, time.Time) {
	files, _ := filepath.Glob(filepath.Join(d.Dir, "dbip-country-*.mmdb"))
	if len(files) == 0 {
		return "", time.Time{}
	}
	sort.Strings(files)
	p := files[len(files)-1]
	st, err := os.Stat(p)
	if err != nil {
		return "", time.Time{}
	}
	return p, st.ModTime()
}

// download fetches this month's database, or last month's early in a month
// before the new one is out, and unpacks it next to the old one.
func (d *DB) download(ctx context.Context, now time.Time) (string, error) {
	if err := os.MkdirAll(d.Dir, 0o755); err != nil {
		return "", err
	}
	client := d.HTTP
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	var last error
	for _, m := range []time.Time{now, now.AddDate(0, -1, 0)} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.url(m), nil)
		if err != nil {
			return "", err
		}
		resp, err := client.Do(req)
		if err != nil {
			last = err
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			last = fmt.Errorf("%s: HTTP %d", m.Format("2006-01"), resp.StatusCode)
			continue
		}
		p := filepath.Join(d.Dir, "dbip-country-"+m.Format("2006-01")+".mmdb")
		err = unpack(resp.Body, p)
		resp.Body.Close()
		if err != nil {
			return "", err
		}
		return p, nil
	}
	return "", last
}

func unpack(r io.Reader, path string) error {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dbip-*")
	if err != nil {
		return err
	}
	n, err := io.Copy(tmp, io.LimitReader(zr, maxSize+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxSize {
		err = errors.New("database too large")
	}
	if err != nil {
		os.Remove(tmp.Name())
		return err
	}
	os.Remove(path) // Windows will not rename over an existing file
	return os.Rename(tmp.Name(), path)
}

type mmdb struct{ r *maxminddb.Reader }

func openMMDB(path string) (table, error) {
	r, err := maxminddb.Open(path)
	if err != nil {
		return nil, err
	}
	return mmdb{r}, nil
}

func (m mmdb) Country(ip netip.Addr) (string, error) {
	var rec struct {
		Country struct {
			ISOCode string `maxminddb:"iso_code"`
		} `maxminddb:"country"`
	}
	if err := m.r.Lookup(ip).Decode(&rec); err != nil {
		return "", err
	}
	return strings.ToLower(rec.Country.ISOCode), nil
}

func (m mmdb) Close() error { return m.r.Close() }
