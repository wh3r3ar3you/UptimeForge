package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/app"
	"github.com/wh3r3ar3you/uptimeforge/internal/remnawave"
	"github.com/wh3r3ar3you/uptimeforge/internal/store/pgtest"
)

// syncBuffer is a log sink tests can read while the hub writes to it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// The first admin can only be created with the setup token: without it
// anyone who reaches a fresh hub first would own it.
func TestSetupNeedsToken(t *testing.T) {
	_, srv, _ := startHub(t)
	c := newClient(t, srv.URL)
	var state map[string]any
	c.do("GET", "/api/v1/auth/state", nil, &state)
	if state["setup_required"] != true || state["setup_token_required"] != true {
		t.Fatalf("a fresh hub must ask for setup with a token: %v", state)
	}
	creds := map[string]string{"username": "eve", "password": "s3cret-pass"}
	if code := c.do("POST", "/api/v1/auth/setup", creds, nil); code != 403 {
		t.Fatalf("setup without a token: %d", code)
	}
	creds["setup_token"] = "wrong-token"
	var res map[string]string
	if code := c.do("POST", "/api/v1/auth/setup", creds, &res); code != 403 || res["error"] == "" {
		t.Fatalf("setup with a wrong token: %d %v", code, res)
	}
	creds["setup_token"] = testSetupToken
	if code := c.do("POST", "/api/v1/auth/setup", creds, nil); code != 200 {
		t.Fatalf("setup with the token: %d", code)
	}
	c.do("GET", "/api/v1/auth/state", nil, &state)
	if state["setup_required"] != false {
		t.Fatalf("after setup: %v", state)
	}
}

// Without UF_SETUP_TOKEN the hub makes one up and prints it to its log, once
// and only while there are no users.
func TestSetupTokenGeneratedAndLogged(t *testing.T) {
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	a, err := app.New(app.Options{DatabaseURL: pgtest.URL(t), DataDir: t.TempDir()}, log)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	srv := httptest.NewServer(New(a, Options{}).Handler())
	t.Cleanup(func() { srv.Close(); cancel(); <-done })

	m := regexp.MustCompile(`setup_token=([A-Za-z0-9_-]{20,})`).FindStringSubmatch(logs.String())
	if m == nil {
		t.Fatalf("no setup token in the log:\n%s", logs.String())
	}
	c := newClient(t, srv.URL)
	if code := c.do("POST", "/api/v1/auth/setup", map[string]string{"username": "leo", "password": "s3cret-pass", "setup_token": m[1]}, nil); code != 200 {
		t.Fatalf("setup with the logged token: %d", code)
	}
}

// Setup is atomic: many requests racing on a fresh hub make one admin.
func TestSetupParallelCreatesOneAdmin(t *testing.T) {
	a, srv, _ := startHub(t)
	const n = 8
	codes := make(chan int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := newClient(t, srv.URL)
			codes <- c.do("POST", "/api/v1/auth/setup", map[string]string{"username": fmt.Sprintf("u%d", i), "password": "s3cret-pass", "setup_token": testSetupToken}, nil)
		}()
	}
	wg.Wait()
	close(codes)
	ok := 0
	for code := range codes {
		switch code {
		case 200:
			ok++
		case 409:
		default:
			t.Errorf("unexpected status %d", code)
		}
	}
	users, err := a.Store.Users(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok != 1 || len(users) != 1 {
		t.Fatalf("%d setups succeeded, %d users exist", ok, len(users))
	}
}

// setupAdmin creates the first admin on a hub from startHub.
func setupAdmin(c *client) {
	c.t.Helper()
	if code := c.do("POST", "/api/v1/auth/setup", map[string]string{"username": "leo", "password": "s3cret-pass", "setup_token": testSetupToken}, nil); code != 200 {
		c.t.Fatalf("setup: %d", code)
	}
}

// viewerClient signs in a new viewer on a hub whose admin is c.
func viewerClient(c *client) *client {
	c.t.Helper()
	if code := c.do("POST", "/api/v1/users", map[string]string{"username": "viewer", "password": "viewer-pass", "role": "viewer"}, nil); code != 201 {
		c.t.Fatalf("create viewer: %d", code)
	}
	v := newClient(c.t, c.base)
	if code := v.do("POST", "/api/v1/auth/login", map[string]string{"username": "viewer", "password": "viewer-pass"}, nil); code != 200 {
		c.t.Fatalf("viewer login: %d", code)
	}
	return v
}

// A viewer only looks: acknowledging, muting or closing an incident would
// silence alerts for everyone.
func TestViewerCannotSilenceIncidents(t *testing.T) {
	_, srv, _ := startHub(t)
	admin := newClient(t, srv.URL)
	setupAdmin(admin)
	v := viewerClient(admin)
	for _, p := range []string{"ack", "mute", "resolve"} {
		if code := v.do("POST", "/api/v1/incidents/x/"+p, map[string]int{"minutes": 60}, nil); code != 403 {
			t.Errorf("viewer %s: %d", p, code)
		}
	}
	// Admins may, within a week for a mute.
	if code := admin.do("POST", "/api/v1/incidents/x/mute", map[string]int{"minutes": 7*24*60 + 1}, nil); code != 400 {
		t.Errorf("mute longer than a week: %d", code)
	}
	if code := admin.do("POST", "/api/v1/incidents/x/ack", nil, nil); code == 403 {
		t.Errorf("admin ack: %d", code)
	}
}

// A new X-Forwarded-For per attempt (the test client is on loopback, a
// trusted proxy) must not buy more guesses at one account.
func TestLoginLimitPerUsername(t *testing.T) {
	_, srv, _ := startHub(t)
	c := newClient(t, srv.URL)
	setupAdmin(c)
	last := 0
	for i := range 15 {
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/auth/login", strings.NewReader(`{"username":"LEO","password":"wrong-pass"}`))
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.100.%d", i+1))
		resp, err := c.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		last = resp.StatusCode
	}
	if last != 429 {
		t.Fatalf("expected 429 after repeated failures on one account from many addresses, got %d", last)
	}
}

// The install script carries the hub's address; a Host header with shell in
// it must never reach the script.
func TestInstallScriptRefusesBadHost(t *testing.T) {
	_, srv, _ := startHub(t)
	for _, h := range []string{"$(touch /tmp/pwned)", "hub.example.com;id", "a`id`", "x' y"} {
		req, _ := http.NewRequest("GET", srv.URL+"/install.sh", nil)
		req.Header.Set("X-Forwarded-Host", h)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 200 || strings.Contains(string(body), h) {
			t.Errorf("host %q: %d\n%s", h, resp.StatusCode, body)
		}
	}
	resp, err := http.Get(srv.URL + "/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "default_hub='"+srv.URL+"'") {
		t.Fatalf("a good host must be quoted into the script: %d\n%s", resp.StatusCode, body)
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"https://hub.example.com": "'https://hub.example.com'",
		"a'b":                     `'a'\''b'`,
		"$(id)":                   "'$(id)'",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("%q: got %s want %s", in, got, want)
		}
	}
}

// startHubWith is startHub with the server options given.
func startHubWith(t *testing.T, opt Options) (*Server, *httptest.Server) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := app.New(app.Options{DatabaseURL: pgtest.URL(t), DataDir: t.TempDir()}, log)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	if opt.SetupToken == "" {
		opt.SetupToken = testSetupToken
	}
	s := New(a, opt)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); cancel(); <-done })
	return s, srv
}

func get(t *testing.T, h http.Handler, remote, path string, hdr map[string]string) int {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	r.RemoteAddr = remote
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

// /metrics names every check, probe and target: without UF_METRICS_TOKEN it
// answers only scrapers on this host or the private network, never through
// a reverse proxy; with the token it wants the token.
func TestMetricsAccess(t *testing.T) {
	s, _ := startHubWith(t, Options{})
	h := s.Handler()
	for _, c := range []struct {
		remote string
		hdr    map[string]string
		want   int
	}{
		{"127.0.0.1:5000", nil, 200},
		{"172.18.0.7:5000", nil, 200}, // VictoriaMetrics on the compose network
		{"203.0.113.5:5000", nil, 404},
		{"127.0.0.1:5000", map[string]string{"X-Forwarded-For": "203.0.113.5"}, 404}, // the public through a proxy
		{"172.18.0.1:5000", map[string]string{"X-Real-IP": "203.0.113.5"}, 404},
	} {
		if got := get(t, h, c.remote, "/metrics", c.hdr); got != c.want {
			t.Errorf("%s %v: %d, want %d", c.remote, c.hdr, got, c.want)
		}
	}

	s, _ = startHubWith(t, Options{MetricsToken: "metrics-token-0123456789"})
	h = s.Handler()
	for _, c := range []struct {
		remote string
		hdr    map[string]string
		want   int
	}{
		{"127.0.0.1:5000", nil, 401},
		{"203.0.113.5:5000", map[string]string{"Authorization": "Bearer wrong"}, 401},
		{"203.0.113.5:5000", map[string]string{"Authorization": "Bearer metrics-token-0123456789"}, 200},
	} {
		if got := get(t, h, c.remote, "/metrics", c.hdr); got != c.want {
			t.Errorf("token set, %s %v: %d, want %d", c.remote, c.hdr, got, c.want)
		}
	}
}

// Every answer carries a CSP that allows only the hub's own scripts, so
// markup that slips into the page cannot run; HSTS once the hub is on https.
func TestSecurityHeaders(t *testing.T) {
	ui := fstest.MapFS{
		"index.html":       {Data: []byte("<!doctype html><title>UptimeForge</title>")},
		"assets/app-1.js":  {Data: []byte("console.log(1)")},
		"assets/app-1.css": {Data: []byte("body{}")},
	}
	s, _ := startHubWith(t, Options{UI: ui})
	h := s.Handler()
	for _, path := range []string{"/", "/checks/web", "/assets/app-1.js", "/api/v1/auth/state", "/install.sh"} {
		r := httptest.NewRequest("GET", path, nil)
		r.RemoteAddr = "203.0.113.5:1234"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		csp := w.Header().Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'self'", "script-src 'self'", "object-src 'none'", "frame-ancestors 'none'", "base-uri 'none'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: CSP %q lacks %q", path, csp, want)
			}
		}
		_, script, _ := strings.Cut(csp, "script-src")
		script, _, _ = strings.Cut(script, ";")
		if strings.Contains(csp, "unsafe-eval") || strings.Contains(script, "unsafe-inline") {
			t.Errorf("%s: scripts must not run inline: %q", path, csp)
		}
		if w.Header().Get("Strict-Transport-Security") != "" {
			t.Errorf("%s: HSTS over plain http", path)
		}
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:1234" // the reverse proxy
	r.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if !strings.Contains(w.Header().Get("Strict-Transport-Security"), "max-age=") {
		t.Errorf("no HSTS over https: %v", w.Header())
	}
}

// A tab left open across an upgrade asks for chunks that are gone: it must
// get a 404 it can recover from, not index.html served as JavaScript.
func TestMissingAssetIs404(t *testing.T) {
	ui := fstest.MapFS{
		"index.html":      {Data: []byte("<!doctype html><title>UptimeForge</title>")},
		"assets/app-1.js": {Data: []byte("console.log(1)")},
	}
	s, _ := startHubWith(t, Options{UI: ui})
	h := s.Handler()
	for path, want := range map[string]int{"/assets/app-1.js": 200, "/assets/app-0.js": 404, "/assets/": 404, "/checks/web": 200, "/": 200} {
		if got := get(t, h, "203.0.113.5:1234", path, nil); got != want {
			t.Errorf("%s: %d, want %d", path, got, want)
		}
	}
}

// URL parameters of params-driven checks (real browser, WebSocket,
// Globalping HTTP) are shown to viewers like an HTTP check's URL: host
// visible, login, secret query values and token-like path segments hidden.
func TestViewerDoesNotSeeURLParamSecrets(t *testing.T) {
	_, srv, _ := startHub(t)
	admin := newClient(t, srv.URL)
	setupAdmin(admin)
	for _, c := range []map[string]any{
		{"id": "shop", "type": "real-browser", "params": map[string]string{"url": "https://shop.example.com/s/Zx9Yw8Vu7Ts6Rq5P/status"}},
		{"id": "ws", "type": "websocket-upgrade", "params": map[string]string{"url": "wss://ws.example.com/socket/AbCdEf0123456789xyz?room=Room0123456789abc"}},
		{"id": "gp", "type": "globalping", "params": map[string]string{"subtype": "http", "target": "https://gp.example.com/hooks/Gp0123456789abcdef"}},
	} {
		var res map[string]any
		if code := admin.do("POST", "/api/v1/checks", c, &res); code != 201 {
			t.Fatalf("create check %s: %d %v", c["id"], code, res)
		}
	}
	v := viewerClient(admin)
	for _, p := range []string{"/api/v1/checks/shop", "/api/v1/checks/ws", "/api/v1/checks/gp", "/api/v1/checks"} {
		var raw json.RawMessage
		v.do("GET", p, nil, &raw)
		for _, sec := range []string{"Zx9Yw8Vu7Ts6Rq5P", "AbCdEf0123456789xyz", "Room0123456789abc", "Gp0123456789abcdef"} {
			if strings.Contains(string(raw), sec) {
				t.Errorf("viewer reads %q from %s", sec, p)
			}
		}
		if p == "/api/v1/checks/ws" && !strings.Contains(string(raw), "ws.example.com") {
			t.Errorf("the host must stay visible: %s", raw)
		}
	}
	var raw json.RawMessage
	admin.do("GET", "/api/v1/checks/shop", nil, &raw)
	if !strings.Contains(string(raw), "Zx9Yw8Vu7Ts6Rq5P") {
		t.Errorf("admins see the URL as is: %s", raw)
	}
}

// Viewers see nodes and users online, not how the panel reaches a node: no
// proxy URL, no provider login page, only the public (inbound/outbound) IPs.
func TestRedactRemnawaveForViewers(t *testing.T) {
	proxy, login := "http://admin:pw@10.0.0.1:3128", "https://my.hoster.example/login"
	in := app.RemnawaveView{Configured: true, Nodes: []remnawave.Node{{
		UUID: "a", Name: "de", Address: "de.example.com", ProxyURL: &proxy,
		IPs:      []remnawave.NodeIP{{IP: "203.0.113.1", Status: "INBOUND"}, {IP: "10.8.0.1", Status: "MANAGEMENT"}, {IP: "203.0.113.2", Status: "OUTBOUND"}},
		Provider: &remnawave.Provider{Name: "Hoster", LoginURL: &login},
	}}}
	out := redactRemnawave(in)
	b, _ := json.Marshal(out)
	for _, sec := range []string{"admin:pw", "3128", "hoster.example/login", "10.8.0.1"} {
		if strings.Contains(string(b), sec) {
			t.Errorf("viewer sees %q: %s", sec, b)
		}
	}
	for _, keep := range []string{"203.0.113.1", "203.0.113.2", "Hoster", "de.example.com"} {
		if !strings.Contains(string(b), keep) {
			t.Errorf("viewer must still see %q: %s", keep, b)
		}
	}
	if in.Nodes[0].ProxyURL == nil || len(in.Nodes[0].IPs) != 3 || in.Nodes[0].Provider.LoginURL == nil {
		t.Fatal("the cached view must not change")
	}
}

// The hub serves checksums of the agents it hands out, so the installer can
// tell a swapped or truncated download; a checksums.txt shipped next to the
// binaries is served as is.
func TestDownloadChecksums(t *testing.T) {
	dir := t.TempDir()
	bins := map[string]string{"uptimeforge-agent_linux_amd64": "amd64 agent", "uptimeforge-agent_linux_mipsle": "mipsle agent"}
	for name, body := range bins {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644)
	s, _ := startHubWith(t, Options{AgentDir: dir})
	h := s.Handler()
	fetch := func() (int, string) {
		r := httptest.NewRequest("GET", "/download/checksums.txt", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	code, body := fetch()
	if code != 200 {
		t.Fatalf("checksums: %d", code)
	}
	for name, content := range bins {
		sum := sha256.Sum256([]byte(content))
		if !strings.Contains(body, hex.EncodeToString(sum[:])+"  "+name+"\n") {
			t.Errorf("no checksum line for %s:\n%s", name, body)
		}
	}
	if strings.Contains(body, "notes.txt") {
		t.Errorf("only agent binaries are listed:\n%s", body)
	}
	// A binary replaced on disk gets its new checksum.
	os.WriteFile(filepath.Join(dir, "uptimeforge-agent_linux_amd64"), []byte("new amd64 agent, longer"), 0o644)
	_, body = fetch()
	sum := sha256.Sum256([]byte("new amd64 agent, longer"))
	if !strings.Contains(body, hex.EncodeToString(sum[:])) {
		t.Errorf("stale checksum after the binary changed:\n%s", body)
	}
	// Shipped with the release: served as is.
	os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte("abc  uptimeforge-agent_linux_amd64\n"), 0o644)
	if _, body = fetch(); body != "abc  uptimeforge-agent_linux_amd64\n" {
		t.Errorf("shipped checksums.txt not served: %q", body)
	}
}

// The release signature of checksums.txt is served byte for byte next to it,
// so an agent can verify an update it downloads from the hub.
func TestDownloadChecksumsSignature(t *testing.T) {
	dir := t.TempDir()
	s, _ := startHubWith(t, Options{AgentDir: dir})
	h := s.Handler()
	get := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/download/checksums.txt.sig", nil))
		return w
	}
	if w := get(); w.Code != 404 {
		t.Fatalf("no signature shipped: %d", w.Code)
	}
	sig := "c2lnbmF0dXJl\n"
	os.WriteFile(filepath.Join(dir, "checksums.txt.sig"), []byte(sig), 0o644)
	w := get()
	if w.Code != 200 || w.Body.String() != sig {
		t.Fatalf("signature: %d %q", w.Code, w.Body.String())
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q", cc)
	}
	// No agents dir: nothing to serve.
	s2, _ := startHubWith(t, Options{})
	w = httptest.NewRecorder()
	s2.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/download/checksums.txt.sig", nil))
	if w.Code != 404 {
		t.Errorf("no agents dir: %d", w.Code)
	}
}

// The installer checks the binary against the checksums and falls back to
// the GitHub release of the hub's version when the hub has no binary.
func TestInstallScriptVerifiesChecksum(t *testing.T) {
	s, _ := startHubWith(t, Options{})
	r := httptest.NewRequest("GET", "/install.sh", nil)
	r.Host = "hub.example.com"
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	script := w.Body.String()
	for _, want := range []string{"checksums.txt", "sha256", ReleasesURL, "checksum mismatch", "plain http"} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q", want)
		}
	}
	if strings.Contains(script, "__") {
		t.Errorf("a placeholder was left in the script")
	}
	if sh, err := exec.LookPath("sh"); err == nil {
		f := filepath.Join(t.TempDir(), "install.sh")
		os.WriteFile(f, []byte(script), 0o644)
		if out, err := exec.Command(sh, "-n", f).CombinedOutput(); err != nil {
			t.Fatalf("script does not parse: %v\n%s", err, out)
		}
	}
}

// Over plain http the token and the agent binary cross the network in the
// clear: every command says so loudly.
func TestInstallWarnsAboutPlainHTTP(t *testing.T) {
	s := &Server{}
	in := s.installFor("http://hub.example.com:8080", "tok-en_123")
	if in.Warning == "" {
		t.Fatal("no warning for a plain http hub")
	}
	for name, cmd := range map[string]string{"script": in.Script, "docker": in.Docker, "binary": in.Binary, "compose_cmd": in.ComposeCmd} {
		if !strings.Contains(cmd, "WARNING") {
			t.Errorf("%s does not warn: %s", name, cmd)
		}
	}
	if in := s.installFor("https://hub.example.com", "tok-en_123"); in.Warning != "" || strings.Contains(in.Script, "WARNING") {
		t.Fatalf("https needs no warning: %+v", in)
	}
}

// A stolen session must not be enough to take over the account: changing
// your own password needs the current one. An admin resetting someone
// else's password does not.
func TestOwnPasswordNeedsCurrent(t *testing.T) {
	_, srv, _ := startHub(t)
	admin := newClient(t, srv.URL)
	setupAdmin(admin)
	v := viewerClient(admin)
	var me struct {
		User struct{ ID int64 } `json:"user"`
	}
	v.do("GET", "/api/v1/auth/state", nil, &me)
	path := fmt.Sprintf("/api/v1/users/%d/password", me.User.ID)
	if code := v.do("PUT", path, map[string]string{"password": "new-pass-123"}, nil); code != 403 {
		t.Fatalf("without the current password: %d", code)
	}
	if code := v.do("PUT", path, map[string]string{"password": "new-pass-123", "current_password": "wrong-pass"}, nil); code != 403 {
		t.Fatalf("with a wrong current password: %d", code)
	}
	if code := v.do("PUT", path, map[string]string{"password": "new-pass-123", "current_password": "viewer-pass"}, nil); code != 200 {
		t.Fatalf("with the current password: %d", code)
	}
	// The UI's form sends it as "current". (The change ended the sessions.)
	v.do("POST", "/api/v1/auth/login", map[string]string{"username": "viewer", "password": "new-pass-123"}, nil)
	if code := v.do("PUT", path, map[string]string{"password": "newer-pass-123", "current": "new-pass-123"}, nil); code != 200 {
		t.Fatalf("with the current password as \"current\": %d", code)
	}
	if code := admin.do("PUT", path, map[string]string{"password": "reset-pass-123"}, nil); code != 200 {
		t.Fatalf("admin reset: %d", code)
	}
}

// A short admin token can be guessed: the hub refuses to start with one.
func TestCheckAdminToken(t *testing.T) {
	if err := CheckAdminToken(""); err != nil {
		t.Fatalf("no token is fine: %v", err)
	}
	if err := CheckAdminToken("secret"); err == nil || !strings.Contains(err.Error(), "UF_ADMIN_TOKEN") {
		t.Fatalf("a short token must be refused with a clear message: %v", err)
	}
	if err := CheckAdminToken(strings.Repeat("a1", 16)); err != nil {
		t.Fatalf("32 characters: %v", err)
	}
}

// Guessing the admin bearer token is throttled per address like logins.
func TestBearerGuessesThrottled(t *testing.T) {
	_, srv := startHubWith(t, Options{})
	try := func(tok string) int {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/meta", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	last := 0
	for i := range 15 {
		last = try(fmt.Sprintf("guess-%d", i))
	}
	if last != 429 {
		t.Fatalf("expected 429 after repeated wrong bearer tokens, got %d", last)
	}
}

// The pulse bars cache must not hold its lock while it queries: one slow
// window would stall every other. Requests for the same window share one
// query.
func TestBarsCacheSingleFlight(t *testing.T) {
	var c barsCache
	release := make(chan struct{})
	var slowCalls atomic.Int32
	slow := func() (any, error) {
		slowCalls.Add(1)
		<-release
		return "slow", nil
	}
	results := make(chan any, 3)
	for range 3 {
		go func() {
			v, _ := c.get(context.Background(), "90d/48", slow)
			results <- v
		}()
	}
	// Another window answers while the first one is still being computed.
	done := make(chan any)
	go func() {
		v, _ := c.get(context.Background(), "1h/48", func() (any, error) { return "fast", nil })
		done <- v
	}()
	select {
	case v := <-done:
		if v != "fast" {
			t.Fatalf("got %v", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a slow window blocked another one")
	}
	close(release)
	for range 3 {
		if v := <-results; v != "slow" {
			t.Fatalf("got %v", v)
		}
	}
	if n := slowCalls.Load(); n != 1 {
		t.Fatalf("one window was computed %d times at once", n)
	}
	// Cached now.
	v, _ := c.get(context.Background(), "90d/48", func() (any, error) { t.Fatal("recomputed a fresh entry"); return nil, nil })
	if v != "slow" {
		t.Fatalf("got %v", v)
	}
}

// A masked secret with a new destination is the form's fault, not the
// panel's: 400 with a message saying to enter it again.
func TestTestEndpointsRefuseMaskedSecretForNewHost(t *testing.T) {
	_, srv, _ := startHub(t)
	c := newClient(t, srv.URL)
	setupAdmin(c)
	var res map[string]string
	if code := c.do("POST", "/api/v1/settings/remnawave/test", map[string]string{"url": "https://evil.example.com", "token": "••••••••"}, &res); code != 400 || !strings.Contains(res["error"], "enter it again") {
		t.Fatalf("remnawave test: %d %v", code, res)
	}
	if code := c.do("POST", "/api/v1/settings/telegram/chats", map[string]string{"token": "••••••••", "api_url": "https://evil.example.com"}, &res); code != 400 || !strings.Contains(res["error"], "enter it again") {
		t.Fatalf("telegram chats: %d %v", code, res)
	}
}
