package notify

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/model"
)

// An alert cut off mid-retry by a rebuild comes back from Undelivered and is
// delivered by the instance that adopts it, ahead of newer events.
func TestWebhookHandover(t *testing.T) {
	var broken atomic.Bool
	broken.Store(true)
	var mu sync.Mutex
	var attempts int
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p WebhookPayload
		json.NewDecoder(r.Body).Decode(&p)
		mu.Lock()
		defer mu.Unlock()
		if broken.Load() {
			attempts++
			w.WriteHeader(502)
			return
		}
		got = append(got, p.Incident.ID)
	}))
	defer srv.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	f := &Formatter{Lang: "en", Loc: time.UTC}

	old := NewWebhook(WebhookConfig{URL: srv.URL}, f, log)
	ctx, cancel := context.WithCancel(context.Background())
	go old.Run(ctx)
	old.Notify(model.Event{Type: model.EventOpen, Incident: model.Incident{ID: "first", Kind: model.KindDown}})
	old.Notify(model.Event{Type: model.EventOpen, Incident: model.Incident{ID: "second", Kind: model.KindDown}})
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		mu.Lock()
		n := attempts
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no delivery attempt")
		}
	}
	cancel()
	left := old.Undelivered()
	if len(left) != 2 || left[0].Incident.ID != "first" || left[1].Incident.ID != "second" {
		t.Fatalf("undelivered: %+v", left)
	}

	broken.Store(false)
	next := NewWebhook(WebhookConfig{URL: srv.URL}, f, log)
	next.Notify(model.Event{Type: model.EventOpen, Incident: model.Incident{ID: "third", Kind: model.KindDown}})
	next.Adopt(left)
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	go next.Run(ctx)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("delivered %v", got)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if got[0] != "first" || got[1] != "second" || got[2] != "third" {
		t.Fatalf("order: %v", got)
	}
}
