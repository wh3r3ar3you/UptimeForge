package checks

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func runSpec(t *testing.T, s *Spec) Result {
	t.Helper()
	if s.ID == "" {
		s.ID = "c"
	}
	if s.Timeout == 0 {
		s.Timeout = Duration(5 * time.Second)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return Run(context.Background(), s, &Env{})
}

func hostPort(t *testing.T, addr string) (string, string) {
	t.Helper()
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	return h, p
}

func wantUp(t *testing.T, r Result) {
	t.Helper()
	if r.Status != Up {
		t.Fatalf("want up: %+v", r)
	}
}

func wantDown(t *testing.T, r Result, reason string) {
	t.Helper()
	if r.Status != Down || r.Reason != reason {
		t.Fatalf("want down/%s: %+v", reason, r)
	}
}

// testCert is a self-signed certificate for local fake servers.
func testCert(t *testing.T) tls.Certificate {
	t.Helper()
	c, _, _ := testCertPEM(t)
	return c
}

func testCertPEM(t *testing.T) (tls.Certificate, string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fake"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	c, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	return c, certPEM, keyPEM
}

func TestCatalogCoversRegistry(t *testing.T) {
	seen := map[string]bool{}
	for _, info := range Catalog() {
		if info.Category == "" {
			t.Errorf("%s has no category", info.Type)
		}
		seen[info.Type] = true
	}
	for _, typ := range Types() {
		if !seen[typ] {
			t.Errorf("type %s is registered but not in the catalog", typ)
		}
	}
	presets := map[string]bool{}
	for _, info := range Catalog() {
		presets[info.Preset] = true
	}
	if !presets["keyword"] || !presets["json-query"] {
		t.Fatalf("presets missing: %v", presets)
	}
	for _, name := range []string{"push", "manual"} {
		if info, ok := TypeOf(name); !ok || !info.HubOnly {
			t.Errorf("%s must be hub only", name)
		}
	}
}

func TestMaskAndKeepSecrets(t *testing.T) {
	const mask = "•••"
	in := map[string]string{"host": "db", "port": "5432", "username": "u", "password": "hunter2"}
	masked := MaskParams("postgres", in, mask)
	if masked["password"] != mask || masked["host"] != "db" || in["password"] != "hunter2" {
		t.Fatalf("%v / %v", masked, in)
	}
	empty := MaskParams("postgres", map[string]string{"password": ""}, mask)
	if empty["password"] != "" {
		t.Fatalf("an unset secret must stay visibly unset: %v", empty)
	}
	if got := MaskParams("from-a-newer-agent", map[string]string{"x": "y"}, mask); got["x"] != mask {
		t.Fatalf("unknown type must mask everything: %v", got)
	}

	form := map[string]string{"host": "db2", "password": mask}
	KeepSecrets("postgres", form, in, mask)
	if form["password"] != "hunter2" || form["host"] != "db2" {
		t.Fatalf("%v", form)
	}
	fresh := map[string]string{"password": mask}
	KeepSecrets("postgres", fresh, nil, mask)
	if _, ok := fresh["password"]; ok {
		t.Fatalf("the mask must never be saved as a password: %v", fresh)
	}
}

func TestHTTPMaskSecrets(t *testing.T) {
	const mask = "•••"
	p := &HTTPParams{URL: "http://x", BasicPassword: "b", BearerToken: "t", TLSKey: "k", Body: "secret body"}
	m := p.MaskSecrets(mask)
	if m.BasicPassword != mask || m.BearerToken != mask || m.TLSKey != mask || m.Body != mask || p.BearerToken != "t" {
		t.Fatalf("%+v", m)
	}
	m.KeepSecrets(p, mask)
	if m.BasicPassword != "b" || m.BearerToken != "t" || m.TLSKey != "k" || m.Body != "secret body" {
		t.Fatalf("%+v", m)
	}
}

func TestValidateFromCatalog(t *testing.T) {
	bad := []Spec{
		{ID: "a", Type: "ntp"}, // missing host
		{ID: "a", Type: "ntp", Params: map[string]string{"host": "x", "port": "abc"}},
		{ID: "a", Type: "ntp", Params: map[string]string{"host": "x", "port": "70000"}},
		{ID: "a", Type: "ntp", Params: map[string]string{"host": "x", "max_offset": "-1"}},
		{ID: "a", Type: "ntp", Params: map[string]string{"host": "x", "nonsense": "1"}},
		{ID: "a", Type: "ntp", Params: map[string]string{"host": "http://user:pw@x"}},
		{ID: "a", Type: "smtp", Params: map[string]string{"host": "x", "smtp_security": "ssl3"}},
		{ID: "a", Type: "redis", Params: map[string]string{"host": "x", "tls": "maybe"}},
		{ID: "a", Type: "redis", Keyword: "x", Params: map[string]string{"host": "x"}}, // no text
		{ID: "a", Type: "tcp", Keyword: "x", TCP: &TCPParams{Address: "x:1"}},          // no text
		{ID: "a", Type: "postgres", Keyword: "x", Params: map[string]string{"host": "x", "username": "u"}},
		{ID: "a", Type: "websocket-upgrade", Params: map[string]string{"url": "ws://u:p@x/"}},
		{ID: "a", Type: "websocket-upgrade", Params: map[string]string{"url": "ws://x/?token=abc"}},
		{ID: "a", Type: "websocket-upgrade", Params: map[string]string{"url": "http://x/"}},
		{ID: "a", Type: "system-service", Params: map[string]string{"service": "--help"}},
		{ID: "a", Type: "system-service", Params: map[string]string{"service": "a b; rm -rf /"}},
		{ID: "a", Type: "pm2", Params: map[string]string{"process": "-x"}},
		{ID: "a", Type: "tailscale-ping", Params: map[string]string{"host": "--socket=/x"}},
		{ID: "a", Type: "docker", Params: map[string]string{"container": "../etc"}},
		{ID: "a", Type: "http", HTTP: &HTTPParams{URL: "http://x", AcceptedStatus: []string{"abc"}}},
		{ID: "a", Type: "http", HTTP: &HTTPParams{URL: "http://x", JSONPath: "$[", JSONExpect: "1"}},
		{ID: "a", Type: "http", HTTP: &HTTPParams{URL: "http://x", TLSCert: "only cert"}},
		{ID: "a", Type: "dns", DNS: &DNSParams{Name: "x", RecordType: "WKS"}},
	}
	for i := range bad {
		if err := bad[i].Validate(); err == nil {
			t.Errorf("#%d %s %v: accepted", i, bad[i].Type, bad[i].Params)
		}
	}
	good := []Spec{
		{ID: "a", Type: "ntp", Params: map[string]string{"host": "pool.ntp.org"}},
		{ID: "a", Type: "smtp", Keyword: "ESMTP", Params: map[string]string{"host": "x", "smtp_security": "starttls"}},
		{ID: "a", Type: "postgres", Keyword: "x", Params: map[string]string{"host": "x", "username": "u", "query": "select 1"}},
		{ID: "a", Type: "sftp", Params: map[string]string{"host": "x", "username": "u", "password": "p"}},
		{ID: "a", Type: "http", Keyword: "x", HTTP: &HTTPParams{URL: "http://x"}},
	}
	for i := range good {
		if err := good[i].Validate(); err != nil {
			t.Errorf("#%d %s: %v", i, good[i].Type, err)
		}
	}
	// A secret must not come back in a message that reaches the form.
	s := Spec{ID: "a", Type: "redis", Params: map[string]string{"host": "x", "password": "hunter2", "port": "hunter2"}}
	if err := s.Validate(); err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("%v", err)
	}
}

func TestTargetNeverHasSecrets(t *testing.T) {
	const secret = "hunter2"
	for _, info := range Catalog() {
		if info.target == nil {
			continue
		}
		p := map[string]string{}
		for _, f := range info.Fields {
			switch {
			case f.Secret, f.Name == "password":
				p[f.Name] = secret
			case f.Name == "url", f.Name == "nodes":
				p[f.Name] = "http://a.example/" + secret + "?x=" + secret
			case f.Name == "host", f.Name == "port":
				p[f.Name] = map[string]string{"host": "a.example", "port": "1"}[f.Name]
			}
		}
		s := Spec{Type: info.Type, Params: p}
		if got := s.Target(); strings.Contains(got, secret) {
			t.Errorf("%s target leaks: %s", info.Type, got)
		}
	}
	if got := (&Spec{Type: "redis", Params: map[string]string{"host": "::1"}}).Target(); got != "[::1]:6379" {
		t.Fatalf("%s", got)
	}
}

func TestScrubText(t *testing.T) {
	cases := []struct{ in, leak string }{
		{"dial postgres://bob:hunter2@db:5432/x failed", "hunter2"},
		{"parse oracle://bob:hunter%232@db/x: bad", "hunter%232"},
		{"connect error password=hunter2 host=db", "hunter2"},
		{"Authorization: Bearer", "Bearer"},
		{`token=abc123&x=1`, "abc123"},
		{"api_key: abc123", "abc123"},
	}
	for _, c := range cases {
		if got := scrubText(c.in, nil); strings.Contains(got, c.leak) {
			t.Errorf("%q -> %q", c.in, got)
		}
	}
	if got := scrubText("bad pass hunter#2 and hunter%232", []string{"hunter#2"}); strings.Contains(got, "hunter") {
		t.Errorf("%q", got)
	}
	if got := scrubText("connection refused", nil); got != "connection refused" {
		t.Errorf("%q", got)
	}
}

func TestPlainErrDropsURL(t *testing.T) {
	err := &url.Error{Op: "Get", URL: "http://x/?key=hunter2", Err: errors.New("connection refused")}
	if got := plainErr(fmt.Errorf("wrap: %w", err)).Error(); strings.Contains(got, "hunter2") {
		t.Fatal(got)
	}
}

// Whatever a driver says, Run must not let a param's secret through.
func TestRunScrubsSecretsFromResults(t *testing.T) {
	register("_leaky", func(_ context.Context, s *Spec, _ *Env) Result {
		return Result{Status: Down, Reason: "error", Error: "dsn user:" + s.Params["password"] + "@db",
			Info: map[string]string{"dsn": "password=" + s.Params["password"]}}
	})
	typeInfos["_leaky"] = TypeInfo{Type: "_leaky", Category: "web", Fields: []Field{fPassword}, target: targetNone}
	defer func() { delete(registry, "_leaky"); delete(typeInfos, "_leaky") }()
	r := Run(context.Background(), &Spec{ID: "x", Type: "_leaky", Params: map[string]string{"password": "hunter2"}}, &Env{})
	if strings.Contains(r.Error, "hunter2") || strings.Contains(r.Info["dsn"], "hunter2") {
		t.Fatalf("%+v", r)
	}
}

func TestKeywordOnSpec(t *testing.T) {
	register("_text", func(context.Context, *Spec, *Env) Result { return Result{Text: "hello world"} })
	typeInfos["_text"] = TypeInfo{Type: "_text", Category: "web", Keyword: true, target: targetNone}
	defer func() { delete(registry, "_text"); delete(typeInfos, "_text") }()
	run := func(kw string, invert bool) Result {
		return runSpec(t, &Spec{Type: "_text", Keyword: kw, KeywordInvert: invert})
	}
	wantUp(t, run("world", false))
	wantUp(t, run("absent", true))
	r := run("absent", false)
	wantDown(t, r, "keyword")
	if !strings.Contains(r.Error, "absent") {
		t.Fatalf("error must say what was expected: %+v", r)
	}
	wantDown(t, run("world", true), "keyword")
	if r := run("world", false); r.Text != "" {
		t.Fatalf("text must not leave Run: %q", r.Text)
	}
}

func TestReasonForDownIsKept(t *testing.T) {
	register("_down", func(context.Context, *Spec, *Env) Result {
		return Result{Status: Down, Reason: "refused", Error: "x", Text: "needle"}
	})
	typeInfos["_down"] = TypeInfo{Type: "_down", Category: "web", Keyword: true, target: targetNone}
	defer func() { delete(registry, "_down"); delete(typeInfos, "_down") }()
	wantDown(t, runSpec(t, &Spec{Type: "_down", Keyword: "other"}), "refused")
}

// closedPort returns an address nothing listens on.
func closedPort(t *testing.T) (string, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	ln.Close()
	return "127.0.0.1", strconv.Itoa(addr.Port)
}
