package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/wh3r3ar3you/uptimeforge/internal/store/pgtest"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/wh3r3ar3you/uptimeforge/internal/agent"
	"github.com/wh3r3ar3you/uptimeforge/internal/app"
	"github.com/wh3r3ar3you/uptimeforge/internal/geoip"
	"github.com/wh3r3ar3you/uptimeforge/internal/hub"
	"github.com/wh3r3ar3you/uptimeforge/internal/model"
	"github.com/wh3r3ar3you/uptimeforge/internal/proto"
	"github.com/wh3r3ar3you/uptimeforge/internal/store"
	"gopkg.in/yaml.v3"
)

type client struct {
	t    *testing.T
	base string
	http *http.Client
}

func (c *client) do(method, path string, body any, out any) int {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if method != http.MethodGet {
		req.Header.Set("Origin", c.base)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			c.t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, raw)
		}
	}
	return resp.StatusCode
}

// testSetupToken is the UF_SETUP_TOKEN of hubs started by startHub.
const testSetupToken = "test-setup-token-0123456789"

func startHub(t *testing.T) (*app.App, *httptest.Server, context.Context) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := app.New(app.Options{DatabaseURL: pgtest.URL(t), DataDir: t.TempDir()}, log)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	srv := httptest.NewServer(New(a, Options{SetupToken: testSetupToken}).Handler())
	t.Cleanup(func() { srv.Close(); cancel(); <-done })
	return a, srv, ctx
}

func newClient(t *testing.T, base string) *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: t, base: base, http: &http.Client{Jar: jar, Timeout: 60 * time.Second}}
}

func TestAPIFlow(t *testing.T) {
	a, srv, ctx := startHub(t)
	c := newClient(t, srv.URL)

	var state map[string]any
	c.do("GET", "/api/v1/auth/state", nil, &state)
	if state["setup_required"] != true {
		t.Fatal("fresh hub must ask for setup")
	}
	if code := c.do("GET", "/api/v1/overview", nil, nil); code != 401 {
		t.Fatalf("overview without session: %d", code)
	}
	if code := c.do("POST", "/api/v1/auth/setup", map[string]string{"username": "leo", "password": "s3cret-pass", "setup_token": testSetupToken}, nil); code != 200 {
		t.Fatalf("setup: %d", code)
	}
	if code := c.do("POST", "/api/v1/auth/setup", map[string]string{"username": "eve", "password": "s3cret-pass", "setup_token": testSetupToken}, nil); code != 409 {
		t.Fatalf("second setup must be refused: %d", code)
	}

	// Cross-site writes are rejected even with a valid session.
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/checks", strings.NewReader(`{}`))
	req.Header.Set("Origin", "https://evil.example")
	if resp, _ := c.http.Do(req); resp.StatusCode != 403 {
		t.Fatalf("CSRF not blocked: %d", resp.StatusCode)
	}

	// A fresh install has the hub itself as a probe; add an agent probe.
	var created struct {
		Install *Install `json:"install"`
	}
	if code := c.do("POST", "/api/v1/probes", map[string]any{"id": "home", "name": "Дом", "probing": true, "labels": map[string]string{"country": "ru"}}, &created); code != 201 {
		t.Fatalf("create probe: %d", code)
	}
	if created.Install == nil || len(created.Install.Token) < 20 || !strings.Contains(created.Install.Script, "/install.sh") {
		t.Fatalf("install info: %+v", created.Install)
	}
	go agent.New(ctx, agent.Config{HubURL: srv.URL, Token: created.Install.Token, MetricsInterval: time.Second}, a.Log).Run(ctx)

	// A TCP check against a local listener, checked from both probes.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	check := map[string]any{"id": "web", "name": "Web", "type": "tcp", "interval": "1s", "tcp": map[string]string{"address": ln.Addr().String()}}
	if code := c.do("POST", "/api/v1/checks", check, nil); code != 201 {
		t.Fatalf("create check: %d", code)
	}
	if code := c.do("POST", "/api/v1/checks", map[string]any{"id": "bad", "type": "tcp"}, nil); code != 400 {
		t.Fatalf("invalid check accepted: %d", code)
	}

	var ov struct {
		Counts map[string]int `json:"counts"`
		Checks []CheckRow     `json:"checks"`
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		c.do("GET", "/api/v1/overview", nil, &ov)
		if ov.Counts["ok"] == 1 && ov.Counts["probes_online"] == 2 && len(ov.Checks) == 1 && len(ov.Checks[0].Probes) == 2 &&
			ov.Checks[0].Probes[0].Status != "" && ov.Checks[0].Probes[1].Status != "" {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if ov.Counts["ok"] != 1 || ov.Counts["probes_online"] != 2 {
		t.Fatalf("overview never settled: %+v", ov.Counts)
	}

	// The agent reported host metrics.
	var probes struct {
		Probes []ProbeRow `json:"probes"`
	}
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		c.do("GET", "/api/v1/probes", nil, &probes)
		if len(probes.Probes) == 2 && probes.Probes[1].Metrics != nil && probes.Probes[1].SysInfo != nil {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if p := probes.Probes[1]; p.Metrics == nil || p.SysInfo == nil || p.Metrics.MemTotal == 0 || p.Config.TokenHash != "" {
		t.Fatalf("agent probe row: %+v", p)
	}

	// Run-once from both probes.
	var run struct {
		Rows []runRow `json:"rows"`
	}
	if code := c.do("POST", "/api/v1/tools/run", map[string]any{"target": ln.Addr().String()}, &run); code != 200 {
		t.Fatalf("run tool: %d", code)
	}
	if len(run.Rows) != 1 || len(run.Rows[0].Outcomes) != 2 {
		t.Fatalf("run rows: %+v", run.Rows)
	}
	for _, o := range run.Rows[0].Outcomes {
		if o.Result == nil || o.Result.Status != "up" {
			t.Fatalf("run outcome from %s: %+v", o.Probe, o)
		}
	}

	// History is written and queryable.
	time.Sleep(2500 * time.Millisecond)
	var series struct {
		Probes map[string][]json.RawMessage `json:"probes"`
	}
	c.do("GET", "/api/v1/checks/web/series?range=1h", nil, &series)
	if len(series.Probes["home"]) == 0 || len(series.Probes["hub"]) == 0 {
		t.Fatalf("series: %+v", series.Probes)
	}
	var days []UptimeDay
	c.do("GET", "/api/v1/checks/web/uptime?days=7", nil, &days)
	if len(days) != 7 || days[6].Up == nil || *days[6].Up != 1 {
		t.Fatalf("uptime days: %+v", days[6])
	}

	// Settings: secrets come back masked and survive a round trip.
	var st app.Settings
	c.do("GET", "/api/v1/settings", nil, &st)
	st.Config.Telegram = nil
	st.Config.Lang = "ru"
	if code := c.do("PUT", "/api/v1/settings", st.Config, &st); code != 200 || st.Config.Lang != "ru" {
		t.Fatalf("settings: %d %+v", code, st.Config.Lang)
	}

	// Viewers can look but not touch.
	c.do("POST", "/api/v1/users", map[string]string{"username": "viewer", "password": "viewer-pass", "role": "viewer"}, nil)
	v := newClient(t, srv.URL)
	if code := v.do("POST", "/api/v1/auth/login", map[string]string{"username": "viewer", "password": "viewer-pass"}, nil); code != 200 {
		t.Fatalf("viewer login: %d", code)
	}
	if code := v.do("GET", "/api/v1/checks", nil, nil); code != 200 {
		t.Fatalf("viewer list: %d", code)
	}
	if code := v.do("DELETE", "/api/v1/checks/web", nil, nil); code != 403 {
		t.Fatalf("viewer delete: %d", code)
	}

	// Public status page shows names and status only.
	c.do("PUT", "/api/v1/status-pages/main", map[string]any{"title": "Example VPN", "groups": []map[string]any{{"name": "Сайт", "checks": []string{"web"}}}}, nil)
	var pub map[string]any
	if code := newClient(t, srv.URL).do("GET", "/api/public/status/main", nil, &pub); code != 200 || pub["status"] != "operational" {
		t.Fatalf("public status: %d %+v", code, pub)
	}
	if b, _ := json.Marshal(pub); strings.Contains(string(b), "127.0.0.1") {
		t.Fatal("public status page leaks targets")
	}

	// Deleting the agent probe drops it from checks.
	if code := c.do("DELETE", "/api/v1/probes/home", nil, nil); code != 200 {
		t.Fatalf("delete probe: %d", code)
	}
	c.do("GET", "/api/v1/checks", nil, &ov)
	if len(ov.Checks[0].Probes) != 1 {
		t.Fatalf("deleted probe still assigned: %+v", ov.Checks[0].Probes)
	}
}

func TestLoginRateLimit(t *testing.T) {
	_, srv, _ := startHub(t)
	c := newClient(t, srv.URL)
	c.do("POST", "/api/v1/auth/setup", map[string]string{"username": "leo", "password": "s3cret-pass", "setup_token": testSetupToken}, nil)
	var last int
	for range 12 {
		last = c.do("POST", "/api/v1/auth/login", map[string]string{"username": "leo", "password": "wrong-pass"}, nil)
	}
	if last != 429 {
		t.Fatalf("expected 429 after repeated failures, got %d", last)
	}
}

func TestSpecFor(t *testing.T) {
	cases := map[string]string{
		"tg://proxy?server=1.2.3.4&port=443&secret=ee000102030405060708090a0b0c0d0e0f676f6f676c652e636f6d": "mtproto",
		"https://t.me/proxy?server=1.2.3.4&port=443&secret=dd000102030405060708090a0b0c0d0e0f":             "mtproto",
		"https://example.com/health":              "http",
		"vless://id@h.example:443?security=tls#x": "xray",
		"1.2.3.4:443": "tcp",
	}
	for in, want := range cases {
		s, err := SpecFor(in)
		if err != nil || s.Type != want {
			t.Errorf("%s: got %s %v, want %s", in, s.Type, err, want)
		}
		if err == nil {
			if err := s.Validate(); err != nil {
				t.Errorf("%s: %v", in, err)
			}
		}
	}
}

func TestClientIP(t *testing.T) {
	strict, err := ParseTrustedProxies("127.0.0.1, 10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	none, _ := ParseTrustedProxies("none")
	cases := []struct {
		trust          []netip.Prefix
		remote, xff, w string
	}{
		{nil, "203.0.113.5:1234", "1.1.1.1", "203.0.113.5"},              // direct: header ignored
		{nil, "127.0.0.1:1234", "6.6.6.6, 198.51.100.7", "198.51.100.7"}, // behind a proxy: the hop it appended
		{nil, "10.0.0.2:1234", "", "10.0.0.2"},
		// Two proxies: the inner one is trusted, so the hop before it counts.
		{nil, "127.0.0.1:1234", "6.6.6.6, 198.51.100.7, 172.18.0.5", "198.51.100.7"},
		// Only the configured proxies are trusted.
		{strict, "172.18.0.1:1234", "6.6.6.6", "172.18.0.1"},
		{strict, "127.0.0.1:1234", "6.6.6.6, 198.51.100.7, 10.1.2.3", "198.51.100.7"},
		{none, "127.0.0.1:1234", "6.6.6.6", "127.0.0.1"},
		// Garbage in the header stops the walk at the last hop understood.
		{nil, "127.0.0.1:1234", "6.6.6.6, not-an-ip", "127.0.0.1"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := newTrust(c.trust).clientIP(r); got != c.w {
			t.Errorf("%+v: got %s", c, got)
		}
	}
}

// Forwarded proto and host count only from a trusted proxy: anyone else
// could make the hub think it runs on https or on their host.
func TestForwardedHeadersOnlyFromTrustedProxies(t *testing.T) {
	tr := newTrust(nil)
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.5:1234"
	r.Host = "hub.example.com"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "evil.example.com")
	if tr.secure(r) || tr.host(r) != "hub.example.com" {
		t.Fatalf("untrusted forwarded headers used: %v %s", tr.secure(r), tr.host(r))
	}
	r.RemoteAddr = "127.0.0.1:1234"
	if !tr.secure(r) || tr.host(r) != "evil.example.com" {
		t.Fatalf("a trusted proxy's headers ignored: %v %s", tr.secure(r), tr.host(r))
	}
}

// IPv6 clients get a /64 each: one host owns the whole block.
func TestLimitKeyIPv6(t *testing.T) {
	if limitKey("2001:db8:1:2::1") != limitKey("2001:db8:1:2:ffff::9") {
		t.Fatal("addresses of one /64 must share a limit")
	}
	if limitKey("2001:db8:1:2::1") == limitKey("2001:db8:1:3::1") || limitKey("198.51.100.7") != "198.51.100.7" {
		t.Fatal("other networks keep their own limit")
	}
}

// Viewers must not read credentials: VPN links, subscription tokens,
// outbounds, MTProto secrets, HTTP headers, upstream proxy logins. What is
// checked and where (scheme, host, port) stays visible; admins see it all.
func TestViewerDoesNotSeeSecrets(t *testing.T) {
	_, srv, _ := startHub(t)
	admin := newClient(t, srv.URL)
	admin.do("POST", "/api/v1/auth/setup", map[string]string{"username": "leo", "password": "s3cret-pass", "setup_token": testSetupToken}, nil)
	const uuid = "b831381d-6324-4d53-ad4f-8cda48b30811"
	link := "vless://" + uuid + "@de1.example.com:443?type=tcp&security=reality&pbk=abc&sni=x.com&sid=0123abcd#de"
	for _, c := range []map[string]any{
		{"id": "de", "type": "xray", "xray": map[string]any{"link": link}},
		{"id": "sub", "type": "xray", "xray": map[string]any{"subscription": "http://127.0.0.1:1/sub-token-123"}},
		{"id": "nl", "type": "xray", "xray": map[string]any{"outbound": map[string]any{"protocol": "vless",
			"settings": map[string]any{"vnext": []any{map[string]any{"address": "nl.example.com", "port": 443,
				"users": []any{map[string]any{"id": uuid, "encryption": "none"}}}}}}}},
		{"id": "mt", "type": "mtproto", "mtproto": map[string]any{"link": "tg://proxy?server=203.0.113.9&port=443&secret=dd00112233445566778899aabbccddeeff"}},
		{"id": "db", "type": "postgres", "params": map[string]string{"host": "db.example.com", "port": "5432", "username": "u", "password": "db-pass-secret"}},
		{"id": "cron", "type": "push", "params": map[string]string{"token": "push-token-secret"}},
		{"id": "basic", "type": "http", "http": map[string]any{"url": "https://x.example.com/", "basic_password": "basic-pass-secret", "bearer_token": "bearer-secret"}},
		{"id": "api", "type": "http", "http": map[string]any{"url": "https://bot:http-pass@api.example.com/health",
			"headers": map[string]string{"Authorization": "Bearer header-token"}}},
	} {
		if code := admin.do("POST", "/api/v1/checks", c, nil); code != 201 {
			t.Fatalf("create check %s: %d", c["id"], code)
		}
	}
	if code := admin.do("POST", "/api/v1/probes", map[string]any{"id": "mob", "via": "socks5://user:proxy-pass@1.2.3.4:1080"}, nil); code != 201 {
		t.Fatalf("create probe: %d", code)
	}
	if code := admin.do("POST", "/api/v1/users", map[string]any{"username": "intern", "password": "viewer-pass", "role": "viewer"}, nil); code != 201 {
		t.Fatalf("create viewer: %d", code)
	}
	v := newClient(t, srv.URL)
	v.do("POST", "/api/v1/auth/login", map[string]string{"username": "intern", "password": "viewer-pass"}, nil)

	secrets := []string{uuid, "0123abcd", "sub-token-123", "00112233445566778899aabbccddeeff", "http-pass", "header-token", "proxy-pass",
		"db-pass-secret", "push-token-secret", "basic-pass-secret", "bearer-secret"}
	leaks := func(who string, path string, want bool) string {
		var raw json.RawMessage
		c := v
		if who == "admin" {
			c = admin
		}
		c.do("GET", path, nil, &raw)
		s := string(raw)
		for _, sec := range secrets {
			if strings.Contains(s, sec) && !want {
				t.Errorf("%s reads %q from %s", who, sec, path)
			}
		}
		return s
	}
	for _, p := range []string{"/api/v1/checks/de", "/api/v1/checks/sub", "/api/v1/checks/nl", "/api/v1/checks/mt",
		"/api/v1/checks/api", "/api/v1/checks/db", "/api/v1/checks/cron", "/api/v1/checks/basic", "/api/v1/checks", "/api/v1/probes", "/api/v1/overview", "/api/v1/incidents"} {
		leaks("viewer", p, false)
	}

	var got struct {
		Config struct {
			Xray struct{ Link string } `json:"xray"`
		} `json:"config"`
	}
	v.do("GET", "/api/v1/checks/de", nil, &got)
	if want := "vless://" + hidden + "@de1.example.com:443?type=tcp&security=reality&pbk=abc&sni=x.com&sid=" + hidden + "#de"; got.Config.Xray.Link != want {
		t.Errorf("viewer link %q, want %q", got.Config.Xray.Link, want)
	}
	var probes struct {
		Probes []struct {
			ID     string               `json:"id"`
			Config struct{ Via string } `json:"config"`
		} `json:"probes"`
	}
	v.do("GET", "/api/v1/probes", nil, &probes)
	for _, p := range probes.Probes {
		if p.ID == "mob" && p.Config.Via != "socks5://"+hidden+"@1.2.3.4:1080" {
			t.Errorf("viewer via %q", p.Config.Via)
		}
	}

	// Admins see everything as before.
	admin.do("GET", "/api/v1/checks/de", nil, &got)
	if got.Config.Xray.Link != link {
		t.Errorf("admin link %q", got.Config.Xray.Link)
	}
	if s := leaks("admin", "/api/v1/probes", true); !strings.Contains(s, "proxy-pass") {
		t.Error("admin must see the probe's upstream proxy")
	}
}

func TestRedactLink(t *testing.T) {
	ss := "ss://" + base64.StdEncoding.EncodeToString([]byte("aes-256-gcm:ss-pass@1.2.3.4:8388")) + "#ss"
	vmess := "vmess://" + base64.StdEncoding.EncodeToString([]byte(`{"add":"h.example.com","port":"443","id":"vm-uuid","ps":"vm"}`))
	for in, want := range map[string]string{
		"trojan://tr-pass@h.example.com:443?type=grpc&serviceName=x#t":        "trojan://" + hidden + "@h.example.com:443?type=grpc&serviceName=x#t",
		"hy2://hy-auth@h.example.com:443,2000-3000/?sni=x&obfs-password=op#h": "hy2://" + hidden + "@h.example.com:443,2000-3000/?sni=x&obfs-password=" + hidden + "#h",
		"ss://YWVzLTI1Ni1nY206cGFzcw@1.2.3.4:8388#ss":                         "ss://" + hidden + "@1.2.3.4:8388#ss",
		ss:                                  "ss://" + hidden + "@1.2.3.4:8388#ss",
		"http://u:p@proxy.example.com:3128": "http://" + hidden + "@proxy.example.com:3128",
		"https://api.example.com/health?token=t1&x=1":    "https://api.example.com/health?token=" + hidden + "&x=1",
		"VLESS · de1.example.com:443":                    "VLESS · de1.example.com:443",
		"tg://proxy?server=1.2.3.4&port=443&secret=ee00": "tg://proxy?server=1.2.3.4&port=443&secret=" + hidden,
		// A subscription page path is a credential; ordinary paths stay readable.
		"https://info.example.com/p_q7Xk2LmN9vRt4Z":               "https://info.example.com/" + hidden,
		"https://app.example.com/api/v1/branding/telegram-widget": "https://app.example.com/api/v1/branding/telegram-widget",
		"https://sub.example.com/sub/0f8e6c1d2b3a4f5e/vless":      "https://sub.example.com/sub/" + hidden + "/vless",
	} {
		if got := redactLink(in); got != want {
			t.Errorf("redactLink(%q) = %q, want %q", in, got, want)
		}
	}
	got := redactLink(vmess)
	b, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(got, "vmess://"))
	if strings.Contains(string(b), "vm-uuid") || !strings.Contains(string(b), "h.example.com") {
		t.Errorf("vmess: %s", b)
	}
	if got := redactSubscription("https://sub.example.com:8443/api/sub/abc?fmt=v2"); got != "https://sub.example.com:8443/"+hidden {
		t.Errorf("subscription: %q", got)
	}
}

// A hub with nothing open and no history used to send null lists, and the
// dashboard crashed on incidents.length.
func TestOverviewEmptyListsAreArrays(t *testing.T) {
	_, srv, _ := startHub(t)
	c := newClient(t, srv.URL)
	c.do("POST", "/api/v1/auth/setup", map[string]string{"username": "leo", "password": "s3cret-pass", "setup_token": testSetupToken}, nil)
	for _, path := range []string{"/api/v1/overview", "/api/v1/incidents"} {
		var got map[string]any
		c.do("GET", path, nil, &got)
		for k, v := range got {
			if v == nil {
				t.Errorf("%s: %s is null", path, k)
			}
		}
	}
}

// The install panel's compose file must be valid YAML that runs the agent
// against this hub: it is what people paste on their servers.
func TestInstallComposeFile(t *testing.T) {
	s := &Server{}
	in := s.installFor("https://hub.example.com", "tok-en_123")
	var f struct {
		Services map[string]struct {
			Image       string            `yaml:"image"`
			Restart     string            `yaml:"restart"`
			NetworkMode string            `yaml:"network_mode"`
			ReadOnly    bool              `yaml:"read_only"`
			MemLimit    string            `yaml:"mem_limit"`
			CapDrop     []string          `yaml:"cap_drop"`
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(in.Compose), &f); err != nil {
		t.Fatalf("compose is not valid YAML: %v\n%s", err, in.Compose)
	}
	a, ok := f.Services["uptimeforge-agent"]
	if !ok || !strings.HasPrefix(a.Image, "intoxxx/uptimeforge-agent:") || a.Restart != "unless-stopped" || a.NetworkMode != "host" {
		t.Fatalf("service: %+v", f)
	}
	if a.Environment["UF_HUB"] != "https://hub.example.com" || a.Environment["UF_TOKEN"] != "tok-en_123" {
		t.Fatalf("environment: %+v", a.Environment)
	}
	// It runs next to production services: no privileges, bounded resources.
	if !a.ReadOnly || a.MemLimit == "" || len(a.CapDrop) != 1 || a.CapDrop[0] != "ALL" {
		t.Fatalf("hardening: %+v", a)
	}
	// One paste sets it up: the command must carry the same file and start it.
	if !strings.Contains(in.ComposeCmd, in.Compose) || !strings.Contains(in.ComposeCmd, "docker compose up -d") {
		t.Fatalf("one-liner: %s", in.ComposeCmd)
	}
}

func TestPushEndpoint(t *testing.T) {
	_, srv, _ := startHub(t)
	admin := newClient(t, srv.URL)
	admin.do("POST", "/api/v1/auth/setup", map[string]string{"username": "leo", "password": "s3cret-pass", "setup_token": testSetupToken}, nil)
	const url = "/api/push/job-token-0123456789"
	anon := newClient(t, srv.URL)
	if code := anon.do("GET", url, nil, nil); code != 404 {
		t.Fatalf("a beat for an unknown token: %d", code)
	}
	if code := admin.do("POST", "/api/v1/checks", map[string]any{"id": "cron", "type": "push", "params": map[string]string{"token": "job-token-0123456789"}}, nil); code != 201 {
		t.Fatalf("create: %d", code)
	}
	if code := anon.do("GET", url+"?status=up&msg=ok&ping=12", nil, nil); code != 200 {
		t.Fatalf("beat without a session: %d", code)
	}
	if code := admin.do("DELETE", "/api/v1/checks/cron", nil, nil); code/100 != 2 {
		t.Fatalf("delete: %d", code)
	}
	if code := anon.do("GET", url, nil, nil); code != 404 {
		t.Fatalf("a deleted check's URL still works: %d", code)
	}
}

func TestMetaServesCatalog(t *testing.T) {
	_, srv, _ := startHub(t)
	admin := newClient(t, srv.URL)
	admin.do("POST", "/api/v1/auth/setup", map[string]string{"username": "leo", "password": "s3cret-pass", "setup_token": testSetupToken}, nil)
	var meta struct {
		Catalog []struct {
			Type string `json:"type"`
		} `json:"check_catalog"`
	}
	admin.do("GET", "/api/v1/meta", nil, &meta)
	if len(meta.Catalog) < 30 {
		t.Fatalf("catalog: %+v", meta.Catalog)
	}
}

// Zooming a chart asks for an explicit window; nonsense windows are refused.
func TestMetricsWindowAndSparklines(t *testing.T) {
	_, srv, _ := startHub(t)
	admin := newClient(t, srv.URL)
	admin.do("POST", "/api/v1/auth/setup", map[string]string{"username": "leo", "password": "s3cret-pass", "setup_token": testSetupToken}, nil)
	now := time.Now().Unix()
	ok := fmt.Sprintf("/api/v1/probes/local/metrics?from=%d&to=%d", now-600, now)
	var res struct{ From, To int64 }
	if code := admin.do("GET", ok, nil, &res); code != 200 || res.From != now-600 || res.To != now {
		t.Fatalf("window: %d %+v", code, res)
	}
	for _, bad := range []string{"from=10&to=20", "from=x&to=1", fmt.Sprintf("from=%d", now)} {
		if code := admin.do("GET", "/api/v1/probes/local/metrics?"+bad, nil, nil); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
	var sp map[string]any
	if code := admin.do("GET", "/api/v1/sparklines", nil, &sp); code != 200 {
		t.Fatalf("sparklines: %d", code)
	}
}

// The connection test talks to the panel with the typed token, reports what
// it found, fails clearly without a token, and is for admins only.
func TestRemnawaveConnectionTest(t *testing.T) {
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good-token" {
			http.Error(w, `{"message":"Unauthorized"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"response":[{"uuid":"a","name":"de","isConnected":true,"usersOnline":412},{"uuid":"b","name":"nl","isConnected":false,"usersOnline":0}]}`)
	}))
	defer panel.Close()
	_, srv, _ := startHub(t)
	admin := newClient(t, srv.URL)
	admin.do("POST", "/api/v1/auth/setup", map[string]string{"username": "leo", "password": "s3cret-pass", "setup_token": testSetupToken}, nil)
	var res struct{ Nodes, Online, Users int }
	if code := admin.do("POST", "/api/v1/settings/remnawave/test", map[string]string{"url": panel.URL, "token": "good-token"}, &res); code != 200 ||
		res.Nodes != 2 || res.Online != 1 || res.Users != 412 {
		t.Fatalf("good token: %d %+v", code, res)
	}
	if code := admin.do("POST", "/api/v1/settings/remnawave/test", map[string]string{"url": panel.URL, "token": "bad"}, nil); code != 502 {
		t.Fatalf("bad token: %d", code)
	}
	if code := admin.do("POST", "/api/v1/settings/remnawave/test", map[string]string{"url": panel.URL}, nil); code != 400 { // the form is missing it: not the panel's fault
		t.Fatalf("no token and none saved: %d", code)
	}
	admin.do("POST", "/api/v1/users", map[string]string{"username": "viewer", "password": "viewer-pass", "role": "viewer"}, nil)
	v := newClient(t, srv.URL)
	v.do("POST", "/api/v1/auth/login", map[string]string{"username": "viewer", "password": "viewer-pass"}, nil)
	if code := v.do("POST", "/api/v1/settings/remnawave/test", map[string]string{"url": panel.URL, "token": "good-token"}, nil); code != 403 {
		t.Fatalf("viewer: %d", code)
	}
}

// View settings are per user: saved, read back, kept apart from other users,
// and anything that is not a small JSON object is refused.
func TestUserPrefs(t *testing.T) {
	_, srv, _ := startHub(t)
	admin := newClient(t, srv.URL)
	admin.do("POST", "/api/v1/auth/setup", map[string]string{"username": "leo", "password": "s3cret-pass", "setup_token": testSetupToken}, nil)
	var got map[string]any
	if code := admin.do("GET", "/api/v1/me/prefs", nil, &got); code != 200 || len(got) != 0 {
		t.Fatalf("fresh prefs: %d %v", code, got)
	}
	prefs := map[string]any{"theme": "dracula", "density": "compact", "columns": []string{"cpu", "steal"}}
	if code := admin.do("PUT", "/api/v1/me/prefs", prefs, nil); code != 200 {
		t.Fatalf("save: %d", code)
	}
	admin.do("GET", "/api/v1/me/prefs", nil, &got)
	if got["theme"] != "dracula" || len(got["columns"].([]any)) != 2 {
		t.Fatalf("read back: %v", got)
	}
	if code := admin.do("PUT", "/api/v1/me/prefs", []int{1, 2}, nil); code != 400 {
		t.Fatalf("an array is not settings: %d", code)
	}
	if code := admin.do("PUT", "/api/v1/me/prefs", map[string]string{"x": strings.Repeat("a", 20000)}, nil); code != 400 {
		t.Fatalf("too large: %d", code)
	}
	admin.do("POST", "/api/v1/users", map[string]string{"username": "viewer", "password": "viewer-pass", "role": "viewer"}, nil)
	v := newClient(t, srv.URL)
	v.do("POST", "/api/v1/auth/login", map[string]string{"username": "viewer", "password": "viewer-pass"}, nil)
	var vp map[string]any
	if code := v.do("GET", "/api/v1/me/prefs", nil, &vp); code != 200 || len(vp) != 0 {
		t.Fatalf("another user's prefs leaked: %d %v", code, vp)
	}
}

// The pulse of every check in one call: fixed bucket count, the incident's
// kind marked in the buckets it covers, and bad parameters refused.
func TestCheckBarsEndpoint(t *testing.T) {
	a, srv, ctx := startHub(t)
	admin := newClient(t, srv.URL)
	admin.do("POST", "/api/v1/auth/setup", map[string]string{"username": "leo", "password": "s3cret-pass", "setup_token": testSetupToken}, nil)
	now := time.Now()
	at := now.Add(-90 * time.Minute)
	if err := a.Store.WriteSamples(ctx, []store.Sample{{Check: "x", Probe: "hub", At: at, Up: false}, {Check: "sub/cfg", Probe: "hub", At: at, Up: false}}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"x", "sub/cfg"} {
		inc := model.Incident{ID: "i-" + key, Key: key, Name: key, Kind: model.KindDown, Worst: model.KindDown, Started: at.Add(-time.Minute), Resolved: at.Add(time.Minute)}
		if err := a.Store.SaveIncident(ctx, inc); err != nil {
			t.Fatal(err)
		}
	}
	var res struct {
		Step int                    `json:"step_s"`
		Bars map[string][]store.Bar `json:"bars"`
	}
	if code := admin.do("GET", "/api/v1/checks/bars?window=24h&buckets=48", nil, &res); code != 200 {
		t.Fatalf("bars: %d", code)
	}
	bars := res.Bars["x"]
	if len(bars) != 48 || res.Step != 1800 {
		t.Fatalf("shape: %d bars, step %d", len(bars), res.Step)
	}
	var marked bool
	for _, b := range bars {
		if b.N == 1 && b.Up == 0 && b.Worst == "down" {
			marked = true
		}
	}
	if !marked {
		t.Fatalf("the failing bucket is not marked down: %+v", bars)
	}
	// A subscription's bar is as bad as its worst config.
	marked = false
	for _, b := range res.Bars["sub"] {
		if b.N == 1 && b.Worst == "down" {
			marked = true
		}
	}
	if !marked {
		t.Fatalf("the subscription bar is not marked down: %+v", res.Bars["sub"])
	}
	for _, bad := range []string{"window=2y", "buckets=5", "buckets=500"} {
		if code := admin.do("GET", "/api/v1/checks/bars?"+bad, nil, nil); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
}

// Bulk actions are admin-only and report what config.yaml kept.
func TestBulkChecksEndpoint(t *testing.T) {
	_, srv, _ := startHub(t)
	admin := newClient(t, srv.URL)
	admin.do("POST", "/api/v1/auth/setup", map[string]string{"username": "leo", "password": "s3cret-pass", "setup_token": testSetupToken}, nil)
	admin.do("POST", "/api/v1/checks", map[string]any{"id": "c1", "type": "tcp", "tcp": map[string]string{"address": "127.0.0.1:1"}}, nil)
	var res struct {
		OK      bool     `json:"ok"`
		Skipped []string `json:"skipped"`
	}
	if code := admin.do("POST", "/api/v1/checks/bulk", map[string]any{"ids": []string{"c1"}, "action": "tags", "add_tags": []string{"DE"}}, &res); code != 200 || !res.OK {
		t.Fatalf("bulk: %d %+v", code, res)
	}
	if code := admin.do("POST", "/api/v1/checks/bulk", map[string]any{"ids": []string{"c1"}, "action": "nope"}, nil); code != 400 {
		t.Fatalf("unknown action: %d", code)
	}
}

// The editor's preview renders a template on a sample, rich and as HTML.
func TestTemplatePreviewEndpoint(t *testing.T) {
	_, srv, _ := startHub(t)
	admin := newClient(t, srv.URL)
	admin.do("POST", "/api/v1/auth/setup", map[string]string{"username": "leo", "password": "s3cret-pass", "setup_token": testSetupToken}, nil)
	var res struct{ Rich, HTML, Error string }
	if code := admin.do("POST", "/api/v1/templates/preview", map[string]string{"lang": "ru", "key": "card", "text": "<p>{emoji} {name}</p>", "sample": "node_down"}, &res); code != 200 {
		t.Fatalf("preview: %d", code)
	}
	if res.Error != "" || !strings.HasPrefix(res.Rich, "<p>🔌 ") || res.HTML == "" {
		t.Fatalf("%+v", res)
	}
	admin.do("POST", "/api/v1/templates/preview", map[string]string{"lang": "ru", "key": "card", "text": "<b>{name}", "sample": "node_down"}, &res)
	if res.Error == "" {
		t.Fatal("a broken template must say why")
	}
	var info struct {
		Defaults     map[string]string `json:"defaults"`
		Placeholders []string          `json:"placeholders"`
	}
	if code := admin.do("GET", "/api/v1/templates", nil, &info); code != 200 || info.Defaults["card"] == "" || len(info.Placeholders) < 10 {
		t.Fatalf("templates: %d %+v", code, info)
	}
}

// The report can be previewed now, by admins only.
func TestReportPreviewEndpoint(t *testing.T) {
	_, srv, _ := startHub(t)
	admin := newClient(t, srv.URL)
	admin.do("POST", "/api/v1/auth/setup", map[string]string{"username": "leo", "password": "s3cret-pass", "setup_token": testSetupToken}, nil)
	var res struct{ Rich string }
	if code := admin.do("GET", "/api/v1/report/preview", nil, &res); code != 200 || !strings.Contains(res.Rich, "<h3>📊") {
		t.Fatalf("preview: %d %q", code, res.Rich)
	}
	if code := admin.do("POST", "/api/v1/report/send", nil, nil); code != 502 {
		t.Fatalf("no Telegram configured must be a clear error: %d", code)
	}
}

// A release hub installs the agent image of its own version; a dev build
// takes the latest.
func TestAgentImageFollowsHubVersion(t *testing.T) {
	if got := agentImageFor("0.1.0"); got != "intoxxx/uptimeforge-agent:0.1.0" {
		t.Fatalf("release: %q", got)
	}
	if got := agentImageFor("0.1.0-rc1"); got != "intoxxx/uptimeforge-agent:0.1.0-rc1" {
		t.Fatalf("rc: %q", got)
	}
	for _, v := range []string{"dev", "25e2505", "v0.1.0-3-gabc-dirty"} {
		if got := agentImageFor(v); got != "intoxxx/uptimeforge-agent:latest" {
			t.Fatalf("%s: %q", v, got)
		}
	}
}

// agentHello connects to the agent endpoint with token and extra headers and
// says hello, as an agent does.
func agentHello(t *testing.T, base, token string, hdr http.Header) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h := http.Header{"Authorization": {"Bearer " + token}}
	for k, v := range hdr {
		h[k] = v
	}
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/api/agent/ws", &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	msg, _ := proto.Encode(proto.TypeHello, proto.Hello{Proto: proto.Version, Version: "test"})
	if err := conn.Write(ctx, websocket.MessageText, msg); err != nil {
		t.Fatal(err)
	}
}

// The hub learns an agent's public address the way it learns a browser's: a
// trusted reverse proxy's X-Forwarded-For counts, anybody else's does not.
func TestAgentClientIPBehindProxy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		proxies []netip.Prefix
		want    string
	}{
		{"trusted proxy", nil, "203.0.113.7"},        // loopback is a trusted proxy by default
		{"untrusted", []netip.Prefix{}, "127.0.0.1"}, // UF_TRUSTED_PROXIES=none
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, err := app.New(app.Options{DatabaseURL: pgtest.URL(t), DataDir: t.TempDir()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			defer a.Store.Close()
			got := make(chan string, 1)
			a.Hub.OnAgentAddr = func(probe string, ip netip.Addr) { got <- probe + " " + ip.String() }
			token, err := a.SaveProbe(context.Background(), hub.ProbeConfig{ID: "home", Role: hub.RoleChecker}, true)
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(New(a, Options{SetupToken: testSetupToken, TrustedProxies: tc.proxies}).Handler())
			defer srv.Close()
			agentHello(t, srv.URL, token, http.Header{"X-Forwarded-For": {"203.0.113.7"}})
			select {
			case s := <-got:
				if s != "home "+tc.want {
					t.Fatalf("OnAgentAddr got %q, want %q", s, "home "+tc.want)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("OnAgentAddr was not called")
			}
		})
	}
}

// Where a probe point sits is for everyone; its address only for admins.
func TestViewerDoesNotSeeAgentIP(t *testing.T) {
	a, srv, _ := startHub(t)
	admin := newClient(t, srv.URL)
	setupAdmin(admin)
	if code := admin.do("POST", "/api/v1/probes", map[string]any{"id": "home", "role": "checker"}, nil); code != 201 {
		t.Fatalf("create probe: %d", code)
	}
	a.Hub.SetNetwork("home", geoip.Network{IP: netip.MustParseAddr("203.0.113.7"), Country: "ru", ASN: "as8359", Org: "MTS PJSC"})
	viewer := viewerClient(admin)
	type probes struct {
		Probes []struct {
			ID      string           `json:"id"`
			Network *hub.NetworkView `json:"network"`
		} `json:"probes"`
	}
	network := func(c *client, path string) (*hub.NetworkView, string) {
		var raw json.RawMessage
		c.do("GET", path, nil, &raw)
		var ps probes
		json.Unmarshal(raw, &ps)
		for _, p := range ps.Probes {
			if p.ID == "home" {
				return p.Network, string(raw)
			}
		}
		return nil, string(raw)
	}
	for _, path := range []string{"/api/v1/overview", "/api/v1/probes", "/api/v1/checks"} {
		n, raw := network(viewer, path)
		if n == nil || n.Country != "ru" || n.ISP != "mts" {
			t.Errorf("viewer %s: network %+v", path, n)
		}
		if strings.Contains(raw, "203.0.113.7") {
			t.Errorf("viewer reads the agent's address from %s", path)
		}
	}
	if n, _ := network(admin, "/api/v1/overview"); n == nil || n.IP != "203.0.113.7" {
		t.Errorf("admin overview: network %+v", n)
	}
}

// A systemd agent starts through the launcher so the hub can update it;
// routers keep running the agent itself (their state dir is on flash).
func TestInstallScriptUsesLauncher(t *testing.T) {
	s, _ := startHubWith(t, Options{})
	r := httptest.NewRequest("GET", "/install.sh", nil)
	r.Host = "hub.example.com"
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	script := w.Body.String()
	for _, want := range []string{"ExecStart=/usr/local/bin/uptimeforge-agent launch", "UF_STATE_DIR", "StateDirectory=uptimeforge-agent"} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q", want)
		}
	}
	_, noSystemd, ok := strings.Cut(script, "# No systemd")
	if !ok {
		t.Fatal("no OpenWrt/Entware branch in the script")
	}
	starts := 0
	for _, line := range strings.Split(noSystemd, "\n") {
		if strings.Contains(line, "start)") {
			starts++
			if strings.Contains(line, "launch") {
				t.Errorf("a router starts the agent through the launcher: %s", line)
			}
		}
	}
	if starts == 0 {
		t.Error("the init script has no start")
	}
	if sh, err := exec.LookPath("sh"); err == nil {
		f := filepath.Join(t.TempDir(), "install.sh")
		os.WriteFile(f, []byte(script), 0o644)
		if out, err := exec.Command(sh, "-n", f).CombinedOutput(); err != nil {
			t.Fatalf("script does not parse: %v\n%s", err, out)
		}
	}
}

// The launcher keeps installed updates under /state: every Docker form the
// hub hands out keeps it in a named volume, so an update survives a
// container recreate.
func TestComposeHasStateVolume(t *testing.T) {
	check := func(name, compose, mem string) {
		t.Helper()
		var f struct {
			Services map[string]struct {
				ReadOnly    bool              `yaml:"read_only"`
				MemLimit    string            `yaml:"mem_limit"`
				Environment map[string]string `yaml:"environment"`
				Volumes     []string          `yaml:"volumes"`
			} `yaml:"services"`
			Volumes map[string]any `yaml:"volumes"`
		}
		if err := yaml.Unmarshal([]byte(compose), &f); err != nil {
			t.Fatalf("%s: compose is not valid YAML: %v\n%s", name, err, compose)
		}
		a := f.Services["uptimeforge-agent"]
		if a.Environment["UF_STATE_DIR"] != "/state" {
			t.Errorf("%s: environment %+v", name, a.Environment)
		}
		if !slices.Contains(a.Volumes, "uptimeforge-agent-state:/state") {
			t.Errorf("%s: volumes %v", name, a.Volumes)
		}
		if _, ok := f.Volumes["uptimeforge-agent-state"]; !ok {
			t.Errorf("%s: no top-level volume: %+v", name, f.Volumes)
		}
		if !a.ReadOnly || a.MemLimit != mem {
			t.Errorf("%s: read_only %v, mem_limit %q, want %q", name, a.ReadOnly, a.MemLimit, mem)
		}
	}
	in := (&Server{}).installFor("https://hub.example.com", "tok-en_123")
	check("install", in.Compose, "512m")
	if !strings.Contains(in.Docker, "-v uptimeforge-agent-state:/state ") {
		t.Errorf("docker one-liner keeps no /state: %s", in.Docker)
	}

	_, rest, _ := strings.Cut(joinScriptText, "<<COMPOSE\n")
	compose, _, ok := strings.Cut(rest, "\nCOMPOSE\n")
	if !ok {
		t.Fatal("no compose heredoc in join.sh")
	}
	compose = strings.NewReplacer("$env_extra\n", "", "$IMAGE", "img", "$UF_HUB", "https://hub", "$CODE", "code").Replace(compose + "\n")
	check("join.sh", compose, "256m")
}

type fakeRollout struct {
	mu      sync.Mutex
	st      hub.RolloutStatus
	resumed []string
}

func (f *fakeRollout) Rollout() hub.RolloutStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st
}

func (f *fakeRollout) ResumeRollout(by string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumed = append(f.resumed, by)
	f.st.Halted = false
}

func (f *fakeRollout) resumedBy() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.resumed)
}

// The agent rollout is for admins: a viewer neither reads nor resumes it.
// An admin's resume is the hub's, under the admin's name.
func TestRolloutAPI(t *testing.T) {
	s, _ := startHubWith(t, Options{})
	fake := &fakeRollout{st: hub.RolloutStatus{Version: "0.2.0", Mode: "all", Stage: 2, Updated: 3,
		InFlight: []string{"edge"}, Failed: []hub.RolloutFailure{{Probe: "home", Error: "the update to 0.2.0 was rolled back"}},
		Halted: true, NeedReinstall: []string{"router"}, Unsupported: []string{"laptop"}, Blocked: "no signed agents on this hub"}}
	s.rollout = fake
	srv := httptest.NewServer(s.Handler()) // after the fake is in place
	defer srv.Close()
	admin := newClient(t, srv.URL)
	setupAdmin(admin)
	for _, p := range []map[string]any{{"id": "edge", "name": "Edge FI", "role": "checker"}, {"id": "home", "name": "Home", "role": "checker"},
		{"id": "laptop", "name": "Laptop", "role": "checker"}} {
		if code := admin.do("POST", "/api/v1/probes", p, nil); code != 201 {
			t.Fatalf("create probe %v: %d", p["id"], code)
		}
	}
	viewer := viewerClient(admin)
	if code := viewer.do("GET", "/api/v1/agents/rollout", nil, nil); code != 403 {
		t.Errorf("viewer status: %d", code)
	}
	if code := viewer.do("POST", "/api/v1/agents/rollout/resume", nil, nil); code != 403 {
		t.Errorf("viewer resume: %d", code)
	}
	if got := fake.resumedBy(); len(got) != 0 {
		t.Fatalf("a viewer resumed the rollout: %v", got)
	}

	type status struct {
		hub.RolloutStatus
		Names   map[string]string `json:"names"`
		Release bool              `json:"release"`
	}
	var st status
	if code := admin.do("GET", "/api/v1/agents/rollout", nil, &st); code != 200 {
		t.Fatalf("admin status: %d", code)
	}
	if st.Version != "0.2.0" || st.Stage != 2 || !st.Halted || len(st.Failed) != 1 || st.Failed[0].Error == "" ||
		!slices.Equal(st.InFlight, []string{"edge"}) || !slices.Equal(st.NeedReinstall, []string{"router"}) || !st.Release {
		t.Errorf("status %+v", st)
	}
	if st.Names["edge"] != "Edge FI" || st.Names["home"] != "Home" || st.Names["laptop"] != "Laptop" {
		t.Errorf("names %v", st.Names)
	}
	if st.Blocked != "no signed agents on this hub" || !slices.Equal(st.Unsupported, []string{"laptop"}) {
		t.Errorf("blocked %q, unsupported %v", st.Blocked, st.Unsupported)
	}
	if code := admin.do("POST", "/api/v1/agents/rollout/resume", nil, &st); code != 200 {
		t.Fatalf("admin resume: %d", code)
	}
	if got := fake.resumedBy(); !slices.Equal(got, []string{"leo"}) {
		t.Errorf("resumed by %v, want [leo]", got)
	}
	if st.Halted {
		t.Errorf("resume answers the status before the resume: %+v", st)
	}
}

// The hub learns from the agents dir what it can hand agents to update
// from: a signature, and a binary per platform. The answer follows the dir.
func TestAgentFilesForRollout(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("uptimeforge-agent_linux_amd64")
	write("checksums.txt")
	f := newAgentFiles(dir)
	if f.Signed() || !f.Has("uptimeforge-agent_linux_amd64") || f.Has("uptimeforge-agent_windows_amd64.exe") || f.Has("checksums.txt") {
		t.Fatal("unsigned dir with one linux agent read wrong")
	}
	write("checksums.txt.sig")
	write("uptimeforge-agent_windows_amd64.exe")
	f.expire()
	if !f.Signed() || !f.Has("uptimeforge-agent_windows_amd64.exe") {
		t.Fatal("the dir changed, the answer did not")
	}
	if missing := newAgentFiles(filepath.Join(dir, "nope")); missing.Signed() || missing.Has("uptimeforge-agent_linux_amd64") {
		t.Fatal("a missing dir serves something")
	}
}
