// Package geoip says which country and which operator (ASN) an IP address
// belongs to, from the DB-IP IP-to-Country and IP-to-ASN Lite databases
// (CC BY 4.0, https://db-ip.com). The hub downloads them on first use and
// refreshes them monthly, so lookups never send an address anywhere.
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
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/oschwald/maxminddb-golang/v2"
)

// Attribution is what the license asks to show next to the results.
const Attribution = "IP Geolocation by DB-IP"

const (
	maxAge     = 35 * 24 * time.Hour // a monthly database, with a few days' slack
	retryAfter = time.Hour           // a failed download is not retried on every lookup
	maxSize    = 200 << 20           // the unpacked database is ~10 MB
)

// table is an opened country database.
type table interface {
	Country(ip netip.Addr) (string, error)
	Close() error
}

// asnTable is an opened ASN database: the autonomous system number (0 when
// unknown) and the organization that holds it.
type asnTable interface {
	ASN(ip netip.Addr) (uint, string, error)
	Close() error
}

// slot is one database file kept fresh. The country one and the ASN one are
// downloaded, refreshed and fail independently of each other.
type slot[T io.Closer] struct {
	prefix string // dbip-country, dbip-asn: names the file on disk

	mu      sync.Mutex
	t       T
	have    bool
	path    string
	at      time.Time // when the open file was downloaded
	failed  time.Time // last failed download
	lastErr error
}

// DB is safe for concurrent use. Dir is where the database files live.
type DB struct {
	Dir  string
	HTTP *http.Client
	// URL and ASNURL are where a month's country / ASN database is downloaded
	// from (tests point them elsewhere).
	URL    func(month time.Time) string
	ASNURL func(month time.Time) string
	Now    func() time.Time

	open    func(path string) (table, error)
	openASN func(path string) (asnTable, error)

	country slot[table]
	asn     slot[asnTable]
}

// Network is what is known about an address.
type Network struct {
	IP      netip.Addr
	Country string // lower-case ISO code, "" when unknown
	ASN     string // like "as8359", "" when unknown
	Org     string // the organization name as the database spells it
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

func (d *DB) asnURL(m time.Time) string {
	if d.ASNURL != nil {
		return d.ASNURL(m)
	}
	return "https://download.db-ip.com/free/dbip-asn-lite-" + m.Format("2006-01") + ".mmdb.gz"
}

// Country returns the lower-case ISO code of ip's country, "" when the
// database does not know it.
func (d *DB) Country(ctx context.Context, ip netip.Addr) (string, error) {
	t, err := load(ctx, d, &d.country, "dbip-country", d.url, d.countryOpener())
	if err != nil {
		return "", err
	}
	return t.Country(ip.Unmap())
}

// Network returns the country and the operator of ip. The two databases are
// independent: when one is unavailable the other half is still filled in and
// the error is nil (an empty field means unknown). An error means nothing
// could be found out: both databases failed, or the address is not public.
func (d *DB) Network(ctx context.Context, ip netip.Addr) (Network, error) {
	ip = ip.Unmap()
	n := Network{IP: ip}
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return n, fmt.Errorf("geoip: %s is not a public address", ip)
	}
	cc, cerr := d.Country(ctx, ip)
	n.Country = cc

	var aerr error
	if t, err := load(ctx, d, &d.asn, "dbip-asn", d.asnURL, d.asnOpener()); err != nil {
		aerr = err
	} else {
		var num uint
		if num, n.Org, aerr = t.ASN(ip); aerr == nil && num != 0 {
			n.ASN = "as" + strconv.FormatUint(uint64(num), 10)
		}
	}
	if cerr != nil && aerr != nil {
		return n, errors.Join(cerr, aerr)
	}
	return n, nil
}

// load returns the slot's database, downloading or refreshing it as needed.
func load[T io.Closer](ctx context.Context, d *DB, s *slot[T], prefix string, url func(time.Time) string, open func(string) (T, error)) (T, error) {
	var zero T
	s.mu.Lock()
	defer s.mu.Unlock()
	now := d.now()
	if !s.have {
		if p, at := d.newest(prefix); p != "" {
			if t, err := open(p); err == nil {
				s.t, s.have, s.path, s.at = t, true, p, at
			}
		}
	}
	if s.have && now.Sub(s.at) < maxAge {
		return s.t, nil
	}
	if !s.failed.IsZero() && now.Sub(s.failed) < retryAfter {
		if s.have {
			return s.t, nil // stale beats nothing
		}
		return zero, s.lastErr
	}
	p, err := d.download(ctx, now, prefix, url)
	if err == nil {
		var t T
		if t, err = open(p); err == nil {
			old, oldHave, oldPath := s.t, s.have, s.path
			s.t, s.have, s.path, s.at, s.failed = t, true, p, now, time.Time{}
			if oldHave && oldPath != p {
				old.Close()
				os.Remove(oldPath)
			}
			return t, nil
		}
		os.Remove(p)
	}
	s.failed, s.lastErr = now, fmt.Errorf("geoip database: %w", err)
	if s.have {
		return s.t, nil
	}
	return zero, s.lastErr
}

func (d *DB) countryOpener() func(string) (table, error) {
	if d.open != nil {
		return d.open
	}
	return openMMDB
}

func (d *DB) asnOpener() func(string) (asnTable, error) {
	if d.openASN != nil {
		return d.openASN
	}
	return openASNMMDB
}

// newest is the latest database of that kind already on disk.
func (d *DB) newest(prefix string) (string, time.Time) {
	files, _ := filepath.Glob(filepath.Join(d.Dir, prefix+"-*.mmdb"))
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
func (d *DB) download(ctx context.Context, now time.Time, prefix string, url func(time.Time) string) (string, error) {
	if err := os.MkdirAll(d.Dir, 0o755); err != nil {
		return "", err
	}
	client := d.HTTP
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	var last error
	for _, m := range []time.Time{now, now.AddDate(0, -1, 0)} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url(m), nil)
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
		p := filepath.Join(d.Dir, prefix+"-"+m.Format("2006-01")+".mmdb")
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

type asnMMDB struct{ r *maxminddb.Reader }

func openASNMMDB(path string) (asnTable, error) {
	r, err := maxminddb.Open(path)
	if err != nil {
		return nil, err
	}
	return asnMMDB{r}, nil
}

func (m asnMMDB) ASN(ip netip.Addr) (uint, string, error) {
	var rec struct {
		Number uint   `maxminddb:"autonomous_system_number"`
		Org    string `maxminddb:"autonomous_system_organization"`
	}
	if err := m.r.Lookup(ip).Decode(&rec); err != nil {
		return 0, "", err
	}
	return rec.Number, rec.Org, nil
}

func (m asnMMDB) Close() error { return m.r.Close() }

// legalForms are company-form words dropped from an operator's name.
var legalForms = map[string]bool{
	"llc": true, "ltd": true, "limited": true, "inc": true, "corp": true, "plc": true,
	"gmbh": true, "bv": true, "jsc": true, "pjsc": true, "ojsc": true, "cjsc": true,
	"ooo": true, "ao": true, "pao": true, "zao": true, "oao": true,
}

// ShortISP turns an organization name into a short label: lower case, no
// legal form, no punctuation ("MTS PJSC" is "mts", "T2 Mobile LLC" is
// "t2-mobile").
func ShortISP(org string) string {
	org = strings.ReplaceAll(strings.ToLower(org), ".", "") // "B.V." is "bv"
	words := strings.FieldsFunc(org, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	var keep []string
	for _, w := range words {
		if !legalForms[w] {
			keep = append(keep, w)
		}
	}
	if len(keep) == 0 {
		keep = words
	}
	return strings.Join(keep, "-")
}
