package app

import (
	"context"
	"testing"

	"github.com/wh3r3ar3you/uptimeforge/internal/hub"
	"github.com/wh3r3ar3you/uptimeforge/internal/store/pgtest"
)

// An agent that hangs up mid-join must not leave the exchange half done (a
// probe whose token nobody got, and a code that makes a second one): once
// it started, a join runs to the end.
func TestJoinSurvivesCancelledRequest(t *testing.T) {
	a := newApp(t, Options{DatabaseURL: pgtest.URL(t), DataDir: t.TempDir()})
	defer a.Store.Close()
	_, code, err := a.CreateInvite(context.Background(), "home", false)
	if err != nil {
		t.Fatal(err)
	}
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	id, token, err := a.Join(gone, code, "box", "linux", "amd64")
	if err != nil || id != "home" || token == "" {
		t.Fatalf("join on a cancelled request: id=%q token=%v err=%v", id, token != "", err)
	}
	if _, _, err := a.Join(context.Background(), code, "box", "linux", "amd64"); err != ErrInviteUsed {
		t.Fatalf("second join: %v, want ErrInviteUsed", err)
	}
	n := 0
	for _, p := range a.Probes() {
		if p.Role == hub.RoleChecker {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d checker probes, want 1", n)
	}
}
