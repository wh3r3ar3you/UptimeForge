package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/wh3r3ar3you/uptimeforge/internal/checks"
	"github.com/wh3r3ar3you/uptimeforge/internal/proto"
	"github.com/wh3r3ar3you/uptimeforge/internal/update"
	"github.com/wh3r3ar3you/uptimeforge/internal/version"
)

// SourceIP binds the hub connection: an address the host has works, one it
// does not have cannot be used, and no SourceIP leaves Go's default client.
func TestHubClientSourceIP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	if (&Agent{}).hubClient() != nil {
		t.Fatal("no source IP must keep the default client")
	}
	ok := (&Agent{cfg: Config{SourceIP: "127.0.0.1"}}).hubClient()
	if _, err := ok.Get(srv.URL); err != nil {
		t.Fatalf("bound to a local address: %v", err)
	}
	bad := (&Agent{cfg: Config{SourceIP: "192.0.2.1"}}).hubClient() // TEST-NET-1, never ours
	if _, err := bad.Get(srv.URL); err == nil {
		t.Fatal("an address the host does not have must not be usable")
	}
}

// Auto source: after a failed dial the agent moves through the host's public
// addresses without waiting long, and after a full round goes back to the
// kernel's choice at the normal pace. A pinned address or "off" never moves.
func TestNextSourceRotates(t *testing.T) {
	a := &Agent{local: func() []string { return []string{"198.51.100.1", "198.51.100.2"} }}
	var got []string
	var fresh []bool
	for range 4 {
		fresh = append(fresh, a.nextSource())
		got = append(got, sourceName(a.source()))
	}
	want := []string{"198.51.100.1", "198.51.100.2", "default address", "198.51.100.1"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("round: %v", got)
		}
	}
	if !fresh[0] || !fresh[1] || fresh[2] || !fresh[3] {
		t.Fatalf("untried flags: %v", fresh)
	}
	for _, cfg := range []string{"198.51.100.9", "off"} {
		p := &Agent{cfg: Config{SourceIP: cfg}, local: a.local}
		if p.nextSource() {
			t.Fatalf("%s must not rotate", cfg)
		}
		if cfg == "off" && p.hubClient() != nil {
			t.Fatal("off keeps the default client")
		}
	}
}

// A backlog of results after an outage goes out in messages the hub accepts
// (its read limit is a few MB), in order, and what failed to go is kept.
func TestFlushSplitsBacklog(t *testing.T) {
	a := &Agent{}
	for i := range bufferLimit {
		a.enqueue(checks.Result{CheckID: fmt.Sprintf("c%d", i), Status: checks.Down, Reason: "timeout",
			Error: strings.Repeat("e", 400), At: time.Now()})
	}
	var got []string
	var msgs int
	err := a.flush(func(msg []byte) error {
		if len(msg) > maxMessage {
			t.Fatalf("message of %d bytes, limit %d", len(msg), maxMessage)
		}
		var env proto.Envelope
		var rs proto.Results
		if json.Unmarshal(msg, &env) != nil || json.Unmarshal(env.Data, &rs) != nil {
			t.Fatal("bad message")
		}
		for _, r := range rs.Results {
			got = append(got, r.CheckID)
		}
		msgs++
		return nil
	})
	if err != nil || len(got) != bufferLimit || msgs < 2 {
		t.Fatalf("err %v, %d results in %d messages", err, len(got), msgs)
	}
	for i, id := range got {
		if id != fmt.Sprintf("c%d", i) {
			t.Fatalf("out of order at %d: %s", i, id)
		}
	}

	// The second message fails: everything from it on stays pending, in order.
	for i := range bufferLimit {
		a.enqueue(checks.Result{CheckID: fmt.Sprintf("c%d", i), Error: strings.Repeat("e", 400), At: time.Now()})
	}
	sent, calls := 0, 0
	err = a.flush(func(msg []byte) error {
		if calls++; calls == 2 {
			return errors.New("connection lost")
		}
		var env proto.Envelope
		var rs proto.Results
		json.Unmarshal(msg, &env)
		json.Unmarshal(env.Data, &rs)
		sent += len(rs.Results)
		return nil
	})
	if err == nil || sent+len(a.pending) != bufferLimit || a.pending[0].CheckID != fmt.Sprintf("c%d", sent) {
		t.Fatalf("err %v, sent %d, pending %d", err, sent, len(a.pending))
	}

	// A result JSON cannot carry is dropped, not a wedge for the rest.
	a.pending = nil
	a.enqueue(checks.Result{CheckID: "nan", LatencyMS: math.NaN(), At: time.Now()})
	a.enqueue(checks.Result{CheckID: "ok", At: time.Now()})
	got = nil
	if err := a.flush(func(msg []byte) error {
		var env proto.Envelope
		var rs proto.Results
		json.Unmarshal(msg, &env)
		json.Unmarshal(env.Data, &rs)
		for _, r := range rs.Results {
			got = append(got, r.CheckID)
		}
		return nil
	}); err != nil || len(got) != 1 || got[0] != "ok" || len(a.pending) != 0 {
		t.Fatalf("err %v, got %v, pending %d", err, got, len(a.pending))
	}
}

// Runs on demand are capped: a hub (or whoever holds it) cannot make the
// agent start a goroutine and an xray instance per message without limit.
func TestRunsOnDemandAreCapped(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var held []net.Conn
	var mu sync.Mutex
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, c)
			mu.Unlock()
		}
	}()
	defer func() {
		mu.Lock()
		for _, c := range held {
			c.Close()
		}
		mu.Unlock()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := New(ctx, Config{MetricsInterval: -1}, slog.New(slog.DiscardHandler))
	a.out = make(chan []byte, 64)
	slow := checks.Spec{ID: "slow", Type: "http", Timeout: checks.Duration(5 * time.Second), HTTP: &checks.HTTPParams{URL: "http://" + ln.Addr().String() + "/"}}
	for i := range maxRuns + 2 {
		a.startRun(ctx, proto.Run{ID: fmt.Sprint(i), Spec: slow})
	}
	busy := 0
	timeout := time.After(2 * time.Second)
	for busy < 2 {
		select {
		case msg := <-a.out:
			var env proto.Envelope
			var rr proto.RunResult
			json.Unmarshal(msg, &env)
			json.Unmarshal(env.Data, &rr)
			if rr.Result.Reason != "unavailable" {
				t.Fatalf("a slow run answered first: %+v", rr.Result)
			}
			busy++
		case <-timeout:
			t.Fatalf("%d runs refused, want 2 over the limit of %d", busy, maxRuns)
		}
	}
}

// The token and every assignment (VPN links, passwords) cross the hub
// connection: in clear text only on loopback or when asked for.
func TestHubURLMustBeEncrypted(t *testing.T) {
	for hub, ok := range map[string]bool{
		"https://hub.example.com":         true,
		"wss://hub.example.com/api/agent": true,
		"hub.example.com":                 true, // wss by default
		"http://127.0.0.1:8080":           true,
		"ws://localhost:8080":             true,
		"http://[::1]:8080":               true,
		"http://:8080":                    true,
		"http://0.0.0.0:8080":             true, // the demo listening everywhere dials itself
		"http://hub.example.com":          false,
		"ws://10.0.0.5:8080":              false,
		"http://localhost.evil.com":       false,
	} {
		err := checkHubURL(hub, false)
		if (err == nil) != ok {
			t.Errorf("%s: err %v, want ok=%v", hub, err, ok)
		}
		if err != nil && !strings.Contains(err.Error(), "UF_INSECURE=1") {
			t.Errorf("%s: the error does not say how to allow it: %v", hub, err)
		}
		if err := checkHubURL(hub, true); err != nil {
			t.Errorf("%s with insecure allowed: %v", hub, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	a := New(ctx, Config{HubURL: "http://hub.invalid", Token: "t", MetricsInterval: -1}, slog.New(slog.DiscardHandler))
	if err := a.Run(ctx); err == nil {
		t.Fatal("Run went on over plain http")
	}
}

// localAddrs never offers addresses that cannot reach the internet.
func TestLocalAddrsArePublic(t *testing.T) {
	for _, s := range localAddrs() {
		ip := net.ParseIP(s)
		if ip == nil || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			t.Fatalf("not public: %s", s)
		}
	}
}

// Assign.Metrics=false stops metrics and sysinfo; nil (an older hub) keeps
// sending them.
func TestAssignMetricsOff(t *testing.T) {
	off := false
	for name, flag := range map[string]*bool{"nil": nil, "false": &off} {
		t.Run(name, func(t *testing.T) {
			var seen sync.Map
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer c.CloseNow()
				msg, _ := proto.Encode(proto.TypeAssign, proto.Assign{Probe: "p", Metrics: flag})
				c.Write(r.Context(), websocket.MessageText, msg)
				for {
					_, data, err := c.Read(r.Context())
					if err != nil {
						return
					}
					var env proto.Envelope
					json.Unmarshal(data, &env)
					seen.Store(env.Type, true)
				}
			}))
			defer srv.Close()
			ctx, cancel := context.WithCancel(context.Background())
			a := New(ctx, Config{HubURL: srv.URL, Token: "t", MetricsInterval: 30 * time.Millisecond}, slog.New(slog.DiscardHandler))
			done := make(chan struct{})
			go func() { a.Run(ctx); close(done) }()
			has := func(k string) bool { _, ok := seen.Load(k); return ok }
			if flag == nil {
				deadline := time.Now().Add(5 * time.Second)
				for !(has(proto.TypeMetrics) && has(proto.TypeSysInfo)) && time.Now().Before(deadline) {
					time.Sleep(20 * time.Millisecond)
				}
				if !has(proto.TypeMetrics) || !has(proto.TypeSysInfo) {
					t.Fatal("metrics and sysinfo must flow without the flag")
				}
			} else {
				deadline := time.Now().Add(5 * time.Second)
				for !has(proto.TypeHello) && time.Now().Before(deadline) {
					time.Sleep(20 * time.Millisecond)
				}
				time.Sleep(500 * time.Millisecond) // many metrics periods
				if has(proto.TypeMetrics) || has(proto.TypeSysInfo) {
					t.Fatal("metrics or sysinfo sent after the hub switched them off")
				}
			}
			cancel()
			<-done
		})
	}
}

// updateHub is a fake hub: it assigns, sends TypeUpdate{version} when
// version is set, serves files under /download/ and reports what the agent
// says back.
type updateHub struct {
	srv     *httptest.Server
	results chan proto.UpdateResult
	hello   chan proto.Hello
	sysinfo chan struct{}
	mu      sync.Mutex
	fetched []string
	// checkResults are the check IDs of results received.
	checkResults []string
}

func newUpdateHub(t *testing.T, files map[string][]byte, version string) *updateHub {
	h := &updateHub{results: make(chan proto.UpdateResult, 4), hello: make(chan proto.Hello, 4), sysinfo: make(chan struct{}, 4)}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if name, ok := strings.CutPrefix(r.URL.Path, "/download/"); ok {
			h.mu.Lock()
			h.fetched = append(h.fetched, name)
			h.mu.Unlock()
			if r.Header.Get("Authorization") != "" {
				t.Errorf("the download was sent credentials")
			}
			b, ok := files[name]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Write(b)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		msg, _ := proto.Encode(proto.TypeAssign, proto.Assign{Probe: "p"})
		c.Write(r.Context(), websocket.MessageText, msg)
		if version != "" {
			msg, _ = proto.Encode(proto.TypeUpdate, proto.Update{Version: version})
			c.Write(r.Context(), websocket.MessageText, msg)
		}
		for {
			_, data, err := c.Read(r.Context())
			if err != nil {
				return
			}
			var env proto.Envelope
			json.Unmarshal(data, &env)
			switch env.Type {
			case proto.TypeHello:
				var hello proto.Hello
				json.Unmarshal(env.Data, &hello)
				h.hello <- hello
			case proto.TypeSysInfo:
				h.sysinfo <- struct{}{}
			case proto.TypeResults:
				var rs proto.Results
				json.Unmarshal(env.Data, &rs)
				h.mu.Lock()
				for _, r := range rs.Results {
					h.checkResults = append(h.checkResults, r.CheckID)
				}
				h.mu.Unlock()
			case proto.TypeUpdateResult:
				var res proto.UpdateResult
				json.Unmarshal(env.Data, &res)
				h.results <- res
			}
		}
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *updateHub) downloads() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.fetched...)
}

// signedRelease is checksums.txt of release 0.9.0, its signature and the
// agent binary for this platform, signed with priv.
func signedRelease(priv ed25519.PrivateKey, bin []byte) map[string][]byte {
	name := update.AssetName(runtime.GOOS, update.ReleaseArch())
	sum := sha256.Sum256(bin)
	sums := []byte(hex.EncodeToString(sum[:]) + "  " + name + "\n" +
		strings.Repeat("0", 64) + "  " + update.ArchiveName("0.9.0", runtime.GOOS, update.ReleaseArch()) + "\n")
	return map[string][]byte{"checksums.txt": sums, "checksums.txt.sig": update.Sign(priv, sums), name: bin}
}

func TestAgentHandlesUpdate(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	_, stranger, _ := ed25519.GenerateKey(rand.Reader)
	keys := []ed25519.PublicKey{pub}
	bin := []byte("agent 0.9.0")

	t.Run("installs and restarts", func(t *testing.T) {
		hub := newUpdateHub(t, signedRelease(priv, bin), "0.9.0")
		dir := t.TempDir()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		a := New(ctx, Config{HubURL: hub.srv.URL, Token: "t", MetricsInterval: -1, StateDir: dir, Launcher: true, LauncherBuiltin: "0.1.0", UpdateKeys: keys}, slog.New(slog.DiscardHandler))
		// A result waiting for the next batch tick (not pushed at once) must
		// still reach the hub before the restart closes the connection.
		a.enqueue(checks.Result{CheckID: "pending", At: time.Now()})
		<-a.notify
		began := time.Now()
		if err := a.Run(ctx); !errors.Is(err, ErrRestart) {
			t.Fatalf("Run returned %v, want ErrRestart", err)
		}
		// Sent by the restart itself, not by waiting for the batch tick.
		if took := time.Since(began); took >= batchEvery {
			t.Fatalf("restart took %v", took)
		}
		hub.mu.Lock()
		sent := strings.Join(hub.checkResults, ",")
		hub.mu.Unlock()
		if sent != "pending" {
			t.Fatalf("results delivered before the restart: %q", sent)
		}
		if h := <-hub.hello; !h.Launcher {
			t.Fatal("hello does not say the agent runs under the launcher")
		}
		cur, _ := os.ReadFile(filepath.Join(dir, "current"))
		if strings.TrimSpace(string(cur)) != "0.9.0" {
			t.Fatalf("current = %q", cur)
		}
		exe := "uptimeforge-agent"
		if runtime.GOOS == "windows" {
			exe += ".exe"
		}
		got, err := os.ReadFile(filepath.Join(dir, "bin", "0.9.0", exe))
		if err != nil || string(got) != string(bin) {
			t.Fatalf("installed binary %q %v", got, err)
		}
		select {
		case res := <-hub.results:
			t.Fatalf("a successful update reported %+v", res)
		default:
		}
	})

	for name, tc := range map[string]struct {
		files      map[string][]byte
		launcher   bool
		builtin    string // the launcher's own version
		want       string
		noDownload bool
		running    string // this agent's version, if not the test binary's
	}{
		"bad signature":    {signedRelease(stranger, bin), true, "0.1.0", "signature", false, ""},
		"files missing":    {map[string][]byte{}, true, "0.1.0", "404", false, ""},
		"without launcher": {signedRelease(priv, bin), false, "", "not running under the launcher", true, ""},
		// The launcher would never select it: installing it means exit 75
		// into the same binary, again and again.
		"dev launcher":        {signedRelease(priv, bin), true, "dev", "development build cannot update", true, ""},
		"same as builtin":     {signedRelease(priv, bin), true, "0.9.0", "version not newer than the installed agent", true, ""},
		"older than builtin":  {signedRelease(priv, bin), true, "1.0.0", "version not newer than the installed agent", true, ""},
		"launcher of unknown": {signedRelease(priv, bin), true, "", "development build cannot update", true, ""},
		// The launcher would run 0.9.0 over its builtin 0.1.0, but this agent
		// already runs 1.0.0 (installed earlier): 0.9.0 is a downgrade.
		"older than running": {signedRelease(priv, bin), true, "0.1.0", "version not newer than the running agent", true, "1.0.0"},
	} {
		t.Run(name, func(t *testing.T) {
			if tc.running != "" {
				saved := version.Version
				version.Version = tc.running
				defer func() { version.Version = saved }()
			}
			hub := newUpdateHub(t, tc.files, "0.9.0")
			dir := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			a := New(ctx, Config{HubURL: hub.srv.URL, Token: "t", MetricsInterval: -1, StateDir: dir, Launcher: tc.launcher, LauncherBuiltin: tc.builtin, UpdateKeys: keys}, slog.New(slog.DiscardHandler))
			done := make(chan error, 1)
			go func() { done <- a.Run(ctx) }()
			select {
			case res := <-hub.results:
				if res.Version != "0.9.0" || !strings.Contains(res.Error, tc.want) {
					t.Fatalf("result %+v, want an error with %q", res, tc.want)
				}
				if host := strings.TrimPrefix(hub.srv.URL, "http://"); strings.Contains(res.Error, host) {
					t.Fatalf("the error names the hub: %s", res.Error)
				}
			case err := <-done:
				t.Fatalf("Run returned %v", err)
			case <-time.After(10 * time.Second):
				t.Fatal("no update result")
			}
			if tc.noDownload && len(hub.downloads()) != 0 {
				t.Fatalf("downloaded an update it must refuse: %v", hub.downloads())
			}
			select {
			case err := <-done:
				t.Fatalf("the agent stopped after a failed update: %v", err)
			case <-time.After(300 * time.Millisecond):
			}
			if _, err := os.Stat(filepath.Join(dir, "current")); err == nil {
				t.Fatal("current written by a failed update")
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatalf("Run returned %v", err)
			}
		})
	}

	t.Run("own version", func(t *testing.T) {
		hub := newUpdateHub(t, signedRelease(priv, bin), version.Version)
		ctx, cancel := context.WithCancel(context.Background())
		a := New(ctx, Config{HubURL: hub.srv.URL, Token: "t", MetricsInterval: -1, StateDir: t.TempDir(), Launcher: true, LauncherBuiltin: "0.1.0", UpdateKeys: keys}, slog.New(slog.DiscardHandler))
		done := make(chan error, 1)
		go func() { done <- a.Run(ctx) }()
		<-hub.hello
		time.Sleep(300 * time.Millisecond)
		cancel()
		if err := <-done; err != nil || len(hub.downloads()) != 0 || len(hub.results) != 0 {
			t.Fatalf("Run %v, downloads %v, %d results", err, hub.downloads(), len(hub.results))
		}
	})
}

// Under the launcher the agent marks its version as connected once the hub
// has assigned it work; outside the launcher it leaves the state dir alone.
func TestAgentWritesConnectedMarker(t *testing.T) {
	for _, launcher := range []bool{true, false} {
		t.Run(fmt.Sprint("launcher=", launcher), func(t *testing.T) {
			hub := newUpdateHub(t, nil, "")
			dir := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			a := New(ctx, Config{HubURL: hub.srv.URL, Token: "t", MetricsInterval: time.Hour, StateDir: dir, Launcher: launcher}, slog.New(slog.DiscardHandler))
			done := make(chan error, 1)
			go func() { done <- a.Run(ctx) }()
			defer func() { cancel(); <-done }()
			select {
			case <-hub.sysinfo: // sent after the assignment was handled
			case <-time.After(10 * time.Second):
				t.Fatal("no sysinfo")
			}
			_, err := os.Stat(filepath.Join(dir, update.ConnectedMarker(version.Version)))
			if launcher && err != nil {
				t.Fatalf("no connected marker: %v", err)
			}
			if !launcher && err == nil {
				t.Fatal("marker written outside the launcher")
			}
		})
	}
}

// Config.Hostname replaces the machine's name in the hello (the demo's
// volunteers must not show the name of the machine it runs on).
func TestHelloHostname(t *testing.T) {
	for _, want := range []string{"home-nas", ""} {
		hub := newUpdateHub(t, nil, "")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		a := New(ctx, Config{HubURL: hub.srv.URL, Token: "t", MetricsInterval: -1, StateDir: t.TempDir(), Hostname: want}, slog.New(slog.DiscardHandler))
		go a.Run(ctx)
		hello := <-hub.hello
		cancel()
		if want == "" {
			want, _ = os.Hostname()
		}
		if hello.Hostname != want {
			t.Errorf("hello hostname = %q, want %q", hello.Hostname, want)
		}
	}
}
