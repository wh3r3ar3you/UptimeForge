package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/app"
	"github.com/wh3r3ar3you/uptimeforge/internal/hub"
	"github.com/wh3r3ar3you/uptimeforge/internal/store/pgtest"
	"github.com/wh3r3ar3you/uptimeforge/internal/version"
)

// logBuffer is a log sink tests can read while the hub writes to it.
type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// startHubLogged is startHub with the hub's log kept.
func startHubLogged(t *testing.T) (*app.App, *httptest.Server, *logBuffer) {
	t.Helper()
	buf := &logBuffer{}
	a, err := app.New(app.Options{DatabaseURL: pgtest.URL(t), DataDir: t.TempDir()}, slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	srv := httptest.NewServer(New(a, Options{SetupToken: testSetupToken}).Handler())
	t.Cleanup(func() { srv.Close(); cancel(); <-done })
	return a, srv, buf
}

type inviteResp struct {
	Invite  app.Invite `json:"invite"`
	Code    string     `json:"code"`
	Command string     `json:"command"`
}

type joinResp struct {
	ID    string `json:"id"`
	Token string `json:"token"`
}

func createInvite(t *testing.T, c *client, name string, trusted bool) inviteResp {
	t.Helper()
	var inv inviteResp
	if code := c.do("POST", "/api/v1/checkers/invites", map[string]any{"name": name, "trusted": trusted}, &inv); code != 201 {
		t.Fatalf("create invite: %d", code)
	}
	return inv
}

func join(c *client, code string, out any) int {
	c.t.Helper()
	return c.do("POST", "/api/agent/join", map[string]string{"code": code, "hostname": "box", "os": "linux", "arch": "amd64"}, out)
}

func TestInviteJoinFlow(t *testing.T) {
	a, srv, _ := startHubLogged(t)
	admin := newClient(t, srv.URL)
	setupAdmin(admin)

	inv := createInvite(t, admin, "Дом · МТС", false)
	if raw, err := base64.RawURLEncoding.DecodeString(inv.Code); err != nil || len(inv.Code) < 22 || len(raw) < 16 {
		t.Fatalf("code %q is not 128 bits of base64url (%v)", inv.Code, err)
	}
	if !strings.Contains(inv.Command, "/join.sh") || !strings.Contains(inv.Command, inv.Code) || !strings.HasPrefix(inv.Command, "curl -fsSL ") {
		t.Fatalf("command %q", inv.Command)
	}
	if inv.Invite.ID == "" || inv.Invite.Name != "Дом · МТС" || inv.Invite.Trusted || inv.Invite.Used ||
		inv.Invite.Expires.Sub(inv.Invite.Created) != 24*time.Hour {
		t.Fatalf("invite %+v", inv.Invite)
	}

	anon := newClient(t, srv.URL)
	var j joinResp
	if code := join(anon, inv.Code, &j); code != 200 || j.ID == "" || len(j.Token) < 20 {
		t.Fatalf("join: %d %+v", code, j)
	}
	var p *app.ProbeItem
	for _, x := range a.Probes() {
		if x.ID == j.ID {
			p = &x
		}
	}
	if p == nil || p.ID != "dom-mts" || p.Role != hub.RoleChecker || p.Trusted || p.Name != "Дом · МТС" {
		t.Fatalf("probe made by join: %+v", p)
	}
	if code := join(anon, inv.Code, nil); code != 410 {
		t.Fatalf("second join with the same code: %d, want 410", code)
	}

	// The token is an agent token.
	agentHello(t, srv.URL, j.Token, nil)
	deadline := time.Now().Add(10 * time.Second)
	for !probeOnline(a, j.ID) {
		if time.Now().After(deadline) {
			t.Fatal("the joined agent never came online")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Without a name the checker is called after its host; trusted carries over.
	inv = createInvite(t, admin, "", true)
	if code := join(anon, inv.Code, &j); code != 200 {
		t.Fatalf("join: %d", code)
	}
	p = nil
	for _, x := range a.Probes() {
		if x.ID == j.ID {
			p = &x
		}
	}
	if p == nil || p.Name != "box" || !p.Trusted || p.Role != hub.RoleChecker {
		t.Fatalf("probe from an unnamed trusted invite: %+v", p)
	}

	// Viewers have no business with invites.
	if code := viewerClient(admin).do("GET", "/api/v1/checkers/invites", nil, nil); code != 403 {
		t.Fatalf("viewer lists invites: %d", code)
	}
}

func probeOnline(a *app.App, id string) bool {
	for _, p := range a.Hub.Snapshot().Probes {
		if p.ID == id {
			return p.Online
		}
	}
	return false
}

func TestInviteExpiredAndRevoked(t *testing.T) {
	a, srv, _ := startHubLogged(t)
	admin := newClient(t, srv.URL)
	setupAdmin(admin)
	anon := newClient(t, srv.URL)

	old := createInvite(t, admin, "old", false)
	var stored map[string]any
	if err := a.Store.GetObject(context.Background(), "invite", old.Invite.ID, &stored); err != nil {
		t.Fatal(err)
	}
	stored["expires"] = time.Now().Add(-time.Minute).Format(time.RFC3339Nano)
	if err := a.Store.PutObject(context.Background(), "invite", old.Invite.ID, stored); err != nil {
		t.Fatal(err)
	}
	if code := join(anon, old.Code, nil); code != 410 {
		t.Fatalf("expired code: %d, want 410", code)
	}

	gone := createInvite(t, admin, "gone", false)
	if code := admin.do("DELETE", "/api/v1/checkers/invites/"+gone.Invite.ID, nil, nil); code != 200 {
		t.Fatalf("revoke: %d", code)
	}
	if code := join(anon, gone.Code, nil); code != 404 {
		t.Fatalf("revoked code: %d, want 404", code)
	}
	if code := admin.do("DELETE", "/api/v1/checkers/invites/"+gone.Invite.ID, nil, nil); code != 404 {
		t.Fatalf("revoking twice: %d, want 404", code)
	}
	if code := join(anon, "never-issued-code-0123456", nil); code != 404 {
		t.Fatalf("unknown code: %d, want 404", code)
	}
	for _, p := range a.Probes() {
		if p.Role == hub.RoleChecker {
			t.Fatalf("a refused join made a probe: %+v", p)
		}
	}
}

func TestJoinRateLimited(t *testing.T) {
	_, srv, _ := startHubLogged(t)
	anon := newClient(t, srv.URL)
	for i := range 10 {
		if code := join(anon, "wrong-code-0123456789ab", nil); code != 404 {
			t.Fatalf("attempt %d: %d, want 404", i+1, code)
		}
	}
	if code := join(anon, "wrong-code-0123456789ab", nil); code != 429 {
		t.Fatalf("11th attempt in a minute: %d, want 429", code)
	}
}

func TestInviteCodeNotLeaked(t *testing.T) {
	a, srv, logs := startHubLogged(t)
	admin := newClient(t, srv.URL)
	setupAdmin(admin)
	inv := createInvite(t, admin, "leak", false)
	spent := createInvite(t, admin, "spent", false)
	anon := newClient(t, srv.URL)
	if code := join(anon, spent.Code, nil); code != 200 {
		t.Fatalf("join: %d", code)
	}
	join(anon, spent.Code, nil) // refused: a refusal is logged

	var raw json.RawMessage
	if code := admin.do("GET", "/api/v1/checkers/invites", nil, &raw); code != 200 {
		t.Fatalf("list invites: %d", code)
	}
	var list struct {
		Invites []app.Invite `json:"invites"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Invites) != 2 {
		t.Fatalf("invites: %s", raw)
	}
	// The UI tells spent and untrusted invites apart without guessing.
	if !strings.Contains(string(raw), `"used":true`) || !strings.Contains(string(raw), `"used":false`) || !strings.Contains(string(raw), `"trusted":false`) {
		t.Fatalf("invites must always say used and trusted: %s", raw)
	}
	for _, code := range []string{inv.Code, spent.Code} {
		if strings.Contains(string(raw), code) {
			t.Fatalf("the invite list shows a code: %s", raw)
		}
		if strings.Contains(logs.String(), code) {
			t.Fatalf("the hub log holds a code:\n%s", logs.String())
		}
	}
	if !strings.Contains(logs.String(), "join refused") {
		t.Fatalf("a refused join is not logged:\n%s", logs.String())
	}
	objs, err := a.Store.Objects(context.Background(), "invite")
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		if strings.Contains(string(o.Data), inv.Code) || strings.Contains(string(o.Data), spent.Code) {
			t.Fatalf("the database holds a code: %s", o.Data)
		}
	}
}

func TestJoinScript(t *testing.T) {
	s, _ := startHubWith(t, Options{})
	r := httptest.NewRequest("GET", "/join.sh", nil)
	r.Host = "hub.example.com"
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/x-shellscript") {
		t.Fatalf("%d %q", w.Code, w.Header().Get("Content-Type"))
	}
	script := w.Body.String()
	for _, want := range []string{
		"default_hub='http://hub.example.com'", "get.docker.com", "compose.yml", "mem_limit: 256m", "cpus: 0.5",
		"read_only: true", "cap_drop", "network_mode: host", "restart: unless-stopped", "no-new-privileges",
		":/state", "UF_STATE_DIR", "UF_JOIN", "/install.sh", "up -d", "pull", "UF_INSECURE", "set -eu", agentImageFor(version.Version),
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q", want)
		}
	}
	if strings.Contains(script, "__") {
		t.Errorf("a placeholder was left in the script")
	}
	if sh, err := exec.LookPath("sh"); err == nil {
		f := filepath.Join(t.TempDir(), "join.sh")
		if err := os.WriteFile(f, []byte(script), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(sh, "-n", f).CombinedOutput(); err != nil {
			t.Fatalf("script does not parse: %v\n%s", err, out)
		}
	}
}

func TestJoinScriptRefusesBadHost(t *testing.T) {
	_, srv, _ := startHub(t)
	for _, h := range []string{"$(touch /tmp/pwned)", "hub.example.com;id", "a`id`", "x' y"} {
		req, _ := http.NewRequest("GET", srv.URL+"/join.sh", nil)
		req.Header.Set("X-Forwarded-Host", h)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 400 || strings.Contains(string(body), h) {
			t.Errorf("host %q: %d\n%s", h, resp.StatusCode, body)
		}
	}
}

// The binary fallback enrolls with the invite code instead of a token, and
// the agent gets a writable place to keep the token it receives.
func TestInstallScriptAcceptsJoin(t *testing.T) {
	for _, want := range []string{"UF_JOIN", "UF_STATE_DIR", "StateDirectory="} {
		if !strings.Contains(installScript, want) {
			t.Errorf("install script lacks %q", want)
		}
	}
	if strings.Contains(installScript, `[ -n "${UF_TOKEN:-}" ] || {`) {
		t.Errorf("install script still demands UF_TOKEN")
	}
}

// joinScriptFor fetches /join.sh from a hub and writes it to a file.
func joinScriptFor(t *testing.T) string {
	t.Helper()
	s, _ := startHubWith(t, Options{})
	r := httptest.NewRequest("GET", "/join.sh", nil)
	r.Host = "hub.example.com"
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	f := filepath.Join(t.TempDir(), "join.sh")
	if err := os.WriteFile(f, w.Body.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

// withoutCompose is a Docker engine with no compose plugin.
const withoutCompose = `[ "$1 $2" = "compose version" ] && exit 1; exit 0`

// runJoin runs the script with stub commands in front of PATH. Each stub
// appends its name and arguments to the returned log; docker behaves as
// dockerBody says, and everything that would install or download fails.
func runJoin(t *testing.T, dockerBody string, env []string, args ...string) (out string, code int, calls string) {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Log("no sh on this machine: behavioural join.sh test skipped")
		t.SkipNow()
	}
	script := joinScriptFor(t)
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	stub := func(name, body string) {
		src := "#!/bin/sh\necho \"" + name + " $*\" >> '" + filepath.ToSlash(log) + "'\n" + body + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stub("id", "echo 0")
	stub("docker", dockerBody)
	for _, n := range []string{"curl", "wget", "apt-get", "dnf", "yum", "apk"} {
		stub(n, "exit 1")
	}
	stub("systemctl", "exit 0")
	stub("sleep", "exit 0")
	cmd := exec.Command(sh, append([]string{script}, args...)...)
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"UF_DIR="+filepath.ToSlash(filepath.Join(dir, "agent")))
	cmd.Env = append(cmd.Env, env...)
	b, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	c, _ := os.ReadFile(log)
	return string(b), code, string(c)
}

var httpsHub = []string{"UF_HUB=https://hub.example.com"}

// A bad or missing code stops the script before it touches anything.
func TestJoinScriptRejectsBadCode(t *testing.T) {
	for _, args := range [][]string{{"a;b"}, {}} {
		out, code, calls := runJoin(t, withoutCompose, append([]string{"UF_JOIN="}, httpsHub...), args...)
		if code != 1 || calls != "" {
			t.Errorf("args %q: exit %d, calls %q\n%s", args, code, calls, out)
		}
	}
}

// Docker is on the machine but has no compose plugin: no package is installed
// and the engine is left alone (a package could replace docker-ce or add a
// second engine); the agent goes in as a binary instead.
func TestJoinScriptLeavesExistingDockerAlone(t *testing.T) {
	out, code, calls := runJoin(t, withoutCompose, httpsHub, "abc123")
	for _, bad := range []string{"get.docker.com", "apt-get", "dnf", "yum", "apk"} {
		if strings.Contains(calls, bad) {
			t.Errorf("%s was called with Docker present:\n%s\n%s", bad, calls, out)
		}
	}
	if code == 0 || !strings.Contains(calls, "install.sh") {
		t.Errorf("without compose it must fall back to the binary installer (exit %d):\n%s\n%s", code, calls, out)
	}
}

// The agent refuses plain http outside loopback without UF_INSECURE=1, so the
// script says so instead of installing an agent that never starts.
func TestJoinScriptRefusesPlainHTTP(t *testing.T) {
	out, code, calls := runJoin(t, withoutCompose, []string{"UF_HUB=http://hub.example.com", "UF_INSECURE="}, "abc123")
	if code != 1 || calls != "" || !strings.Contains(out, "UF_INSECURE=1") {
		t.Errorf("exit %d, calls %q\n%s", code, calls, out)
	}
}

// A failed pull (registry unreachable) must not send a machine that already
// has the image to the binary fallback: the container is started anyway.
func TestJoinScriptPullFailureIsNotFatal(t *testing.T) {
	docker := `case "$1 $2" in
  "compose pull") exit 1 ;;
  "inspect -f") echo "true false" ;;
esac
exit 0`
	out, code, calls := runJoin(t, docker, httpsHub, "abc123")
	if code != 0 || strings.Contains(calls, "install.sh") || !strings.Contains(calls, "docker compose up -d") {
		t.Errorf("exit %d\n%s\n%s", code, calls, out)
	}
}

// Without systemd (OpenWrt) the token must live on flash, not on tmpfs.
func TestInstallScriptKeepsStateOnFlash(t *testing.T) {
	if !strings.Contains(installScript, "STATE_DIR=/etc/uptimeforge-agent") {
		t.Errorf("init.d installs need a persistent state dir")
	}
}

// UF_INSECURE=1 reaches the binary agent only when the operator set it: in
// the systemd env file and in the init.d start line.
func TestInstallScriptPassesInsecureOnlyWhenSet(t *testing.T) {
	if !strings.Contains(installScript, `[ "${UF_INSECURE:-}" != 1 ] || printf 'UF_INSECURE=1\n'`) {
		t.Errorf("agent.env must carry UF_INSECURE=1 only when it is set")
	}
	if !strings.Contains(installScript, `[ "${UF_INSECURE:-}" != 1 ] || insecure="UF_INSECURE=1"`) ||
		!strings.Contains(installScript, "$insecure nohup") {
		t.Errorf("the init.d start line must carry UF_INSECURE=1 only when it is set")
	}
}
