package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/wh3r3ar3you/uptimeforge/internal/hub"
	"github.com/wh3r3ar3you/uptimeforge/internal/proto"
)

// liveEventT is one `data:` event of the stream: the metrics point plus its
// time in unix milliseconds.
type liveEventT struct {
	proto.Metrics
	At int64 `json:"at"`
}

func TestLiveStream(t *testing.T) {
	a, srv, _ := startHub(t)
	admin := newClient(t, srv.URL)
	admin.do("POST", "/api/v1/auth/setup", map[string]string{"username": "leo", "password": "s3cret-pass", "setup_token": testSetupToken}, nil)
	admin.do("POST", "/api/v1/users", map[string]any{"username": "intern", "password": "viewer-pass", "role": "viewer"}, nil)
	var created struct {
		Install struct{ Token string } `json:"install"`
	}
	if code := admin.do("POST", "/api/v1/probes", map[string]any{"id": "srv"}, &created); code != 201 {
		t.Fatalf("create probe: %d", code)
	}
	if code := admin.do("POST", "/api/v1/probes", map[string]any{"id": "chk", "role": "checker"}, nil); code != 201 {
		t.Fatalf("create checker: %d", code)
	}
	viewer := newClient(t, srv.URL)
	viewer.do("POST", "/api/v1/auth/login", map[string]string{"username": "intern", "password": "viewer-pass"}, nil)

	// Not watchable: a checker, an unknown probe; and not for the anonymous.
	for _, id := range []string{"chk", "nope"} {
		if code := viewer.do("GET", "/api/v1/probes/"+id+"/live", nil, nil); code != 404 {
			t.Fatalf("live of %q: %d, want 404", id, code)
		}
	}
	if code := newClient(t, srv.URL).do("GET", "/api/v1/probes/srv/live", nil, nil); code != 401 {
		t.Fatalf("anonymous live: %d, want 401", code)
	}

	// The agent connects.
	actx, acancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer acancel()
	conn, _, err := websocket.Dial(actx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/agent/ws",
		&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + created.Install.Token}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	hello, _ := proto.Encode(proto.TypeHello, proto.Hello{Proto: proto.Version, Version: "test"})
	if err := conn.Write(actx, websocket.MessageText, hello); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/probes/srv/live", nil)
	resp, err := viewer.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("live: %d", resp.StatusCode)
	}
	for k, want := range map[string]string{"Content-Type": "text/event-stream", "Cache-Control": "no-store", "X-Accel-Buffering": "no"} {
		if got := resp.Header.Get(k); got != want {
			t.Fatalf("%s = %q, want %q", k, got, want)
		}
	}
	waitWatchers(t, a.Hub, "srv", 1)

	for i := range 3 {
		msg, _ := proto.Encode(proto.TypeMetrics, proto.Metrics{CPU: float64(10 + i), MemTotal: 1 << 30, Age: 2})
		if err := conn.Write(actx, websocket.MessageText, msg); err != nil {
			t.Fatal(err)
		}
	}
	sc := bufio.NewScanner(resp.Body)
	got := make(chan liveEventT, 8)
	go func() {
		for sc.Scan() {
			if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var ev liveEventT
				if json.Unmarshal([]byte(d), &ev) == nil {
					got <- ev
				}
			}
		}
		close(got)
	}()
	for i := range 3 {
		select {
		case ev := <-got:
			if ev.CPU != float64(10+i) {
				t.Fatalf("event %d: cpu %v", i, ev.CPU)
			}
			// at = receive time - age (2 s)
			if d := time.Since(time.UnixMilli(ev.At)); d < 2*time.Second || d > 10*time.Second {
				t.Fatalf("event %d: at is %v ago, want about 2s", i, d)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("no event %d", i)
		}
	}

	// The client goes away: the hub subscription is gone.
	cancel()
	waitWatchers(t, a.Hub, "srv", 0)
}

func waitWatchers(t *testing.T, h *hub.Hub, id string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.Watchers(id) != want {
		if time.Now().After(deadline) {
			t.Fatalf("Watchers(%q) = %d, want %d", id, h.Watchers(id), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The server's ReadTimeout is a deadline on the whole request; an event
// stream must outlive it.
func TestLiveStreamOutlivesReadTimeout(t *testing.T) {
	a, _, _ := startHub(t)
	api := New(a, Options{SetupToken: testSetupToken})
	srv := httptest.NewUnstartedServer(api.Handler())
	srv.Config.ReadTimeout = 300 * time.Millisecond
	srv.Start()
	defer srv.Close()

	admin := newClient(t, srv.URL)
	admin.do("POST", "/api/v1/auth/setup", map[string]string{"username": "leo", "password": "s3cret-pass", "setup_token": testSetupToken}, nil)
	var created struct {
		Install struct{ Token string } `json:"install"`
	}
	admin.do("POST", "/api/v1/probes", map[string]any{"id": "srv"}, &created)
	actx, acancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer acancel()
	conn, _, err := websocket.Dial(actx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/agent/ws",
		&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + created.Install.Token}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	hello, _ := proto.Encode(proto.TypeHello, proto.Hello{Proto: proto.Version, Version: "test"})
	conn.Write(actx, websocket.MessageText, hello)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/probes/srv/live", nil)
	resp, err := admin.http.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("live: %v %v", resp, err)
	}
	defer resp.Body.Close()
	waitWatchers(t, a.Hub, "srv", 1)
	time.Sleep(900 * time.Millisecond) // three read timeouts
	if n := a.Hub.Watchers("srv"); n != 1 {
		t.Fatalf("the stream was cut after the read timeout: %d watchers", n)
	}
	msg, _ := proto.Encode(proto.TypeMetrics, proto.Metrics{CPU: 7, MemTotal: 1})
	conn.Write(actx, websocket.MessageText, msg)
	line := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "data: ") {
				line <- sc.Text()
				return
			}
		}
		line <- ""
	}()
	select {
	case l := <-line:
		if l == "" {
			t.Fatal("stream ended after the read timeout")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event after the read timeout")
	}
}

// A quiet stream still says something, so proxies and browsers keep it open.
func TestLiveStreamKeepAlive(t *testing.T) {
	old := liveKeepAlive
	liveKeepAlive = 100 * time.Millisecond
	defer func() { liveKeepAlive = old }()
	a, srv, _ := startHub(t)
	admin := newClient(t, srv.URL)
	admin.do("POST", "/api/v1/auth/setup", map[string]string{"username": "leo", "password": "s3cret-pass", "setup_token": testSetupToken}, nil)
	admin.do("POST", "/api/v1/probes", map[string]any{"id": "srv"}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/probes/srv/live", nil)
	resp, err := admin.http.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("live: %v %v", resp, err)
	}
	defer resp.Body.Close()
	waitWatchers(t, a.Hub, "srv", 1)
	line := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), ":") {
				line <- sc.Text()
				return
			}
		}
	}()
	select {
	case l := <-line:
		if l != ": keep-alive" {
			t.Fatalf("comment %q", l)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no keep-alive")
	}
}
