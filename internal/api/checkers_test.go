package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/app"
	"github.com/wh3r3ar3you/uptimeforge/internal/hub"
	"github.com/wh3r3ar3you/uptimeforge/internal/store/pgtest"
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
	for _, x := range a.Probes() {
		if x.ID == j.ID && (x.Name != "box" || !x.Trusted || x.Role != hub.RoleChecker) {
			t.Fatalf("probe from an unnamed trusted invite: %+v", x)
		}
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
	json.Unmarshal(raw, &list)
	if len(list.Invites) != 2 {
		t.Fatalf("invites: %s", raw)
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
