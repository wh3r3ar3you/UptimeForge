package hub

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	xcore "github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf/serial"

	"github.com/wh3r3ar3you/uptimeforge/internal/agent"
	"github.com/wh3r3ar3you/uptimeforge/internal/notify"
)

// TestEndToEndRegionalBlock wires a real hub, a virtual probe behind a
// simulated DPI box, a local probe and a WebSocket agent, and checks the whole
// path: VLESS check -> 16 KB stall -> "blocked in RU" -> Telegram -> ack ->
// recovery.
func TestEndToEndRegionalBlock(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end")
	}
	const uuid = "b831381d-6324-4d53-ad4f-8cda48b30811"
	vlessPort := startVLESS(t, uuid)
	target := startHTTPTarget(t)
	dpi := startDPI(t, vlessPort)
	baseline := startTCPEcho(t)
	tg := startFakeTelegram(t)

	cfgYAML := fmt.Sprintf(`
timezone: Europe/Moscow
lang: ru
baseline:
  - type: tcp
    tcp: {address: %q}
probes:
  - id: ru-home
    name: Дом · Москва
    via: socks5://%s
    labels: {country: ru}
  - id: de-hetzner
    name: Hetzner
    local: true
    labels: {country: de}
  - id: nl-agent
    name: DC · NL
    token: nl-agent-token-0123456789
    probing: true
    labels: {country: nl}
checks:
  - id: node-de
    name: de-node-1
    group: VPN nodes
    type: xray
    interval: 2s
    timeout: 6s
    retries: 2
    xray:
      link: "vless://%s@127.0.0.1:%d?type=tcp&security=none#de-node-1"
      url: %q
      download_url: %q
      heavy_every: 1
      stall_timeout: 1s
      egress: false
telegram:
  token: "123:abc"
  chat_id: -1001
  api_url: %q
`, baseline, dpi.addr, uuid, vlessPort, target+"/204", target+"/down", tg.srv.URL)
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, []byte(cfgYAML), 0o600)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = t.TempDir()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if testing.Verbose() {
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	h, err := New(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	hubDone, tgDone := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { cancel(); <-hubDone; <-tgDone }) // before TempDir removal

	tgn, err := notify.NewTelegram(notify.TelegramConfig{Token: "123:abc", ChatID: -1001, APIURL: tg.srv.URL, DataDir: cfg.DataDir},
		&notify.Formatter{Lang: "ru", Loc: h.Location()}, log)
	if err != nil {
		t.Fatal(err)
	}
	tgn.SetActions(h)
	h.AddNotifier(tgn)
	go func() { tgn.Run(ctx); close(tgDone) }()
	go func() { h.Run(ctx); close(hubDone) }()

	srv := httptest.NewServer(h.Handler())
	defer srv.Close()
	go agent.New(ctx, agent.Config{HubURL: srv.URL, Token: "nl-agent-token-0123456789"}, log).Run(ctx)

	// 1. DPI on: RU fails with a stall, DE and NL work -> blocked in RU.
	open := tg.waitFor(t, 45*time.Second, "sendMessage", "Блок в RU")
	for _, want := range []string{"de-node-1", "🇷🇺", "Дом · Москва", "обрыв после", "ТСПУ", "<td>🇩🇪", "<td>🇳🇱", "<td>✅", "VLESS · 127.0.0.1"} {
		if !strings.Contains(open.text, want) {
			t.Errorf("incident message lacks %q:\n%s", want, open.text)
		}
	}
	t.Logf("incident message:\n%s", open.text)

	// 2. Someone presses ✅ in the chat.
	tg.press("ack:" + open.incidentID())
	tg.waitFor(t, 10*time.Second, "editMessageText", "Принял: @leo")

	// 3. DPI off -> recovery edits the message and replies.
	dpi.on.Store(false)
	resolved := tg.waitFor(t, 30*time.Second, "editMessageText", "Восстановлено")
	tg.waitFor(t, 10*time.Second, "sendMessage", "Восстановилось")
	t.Logf("resolved message:\n%s", resolved.text)

	// 4. Read-side endpoints.
	body := get(t, srv.URL+"/metrics", "")
	for _, want := range []string{`uptimeforge_check_verdict{check="node-de"`, `uptimeforge_probe_online{probe="nl-agent",region="nl"} 1`} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics lacks %s", want)
		}
	}
	st := h.Snapshot()
	if len(st.Checks) != 1 || st.Checks[0].Verdict != "ok" || len(st.Probes) != 3 {
		t.Errorf("unexpected status: %+v", st)
	}
	if de := st.Probes[1]; de.SysInfo == nil && de.Metrics == nil {
		// local probes report host facts once their collector has started
		t.Logf("local probe has no host info yet: %+v", de)
	}
}

func get(t *testing.T, url, token string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func startVLESS(t *testing.T, uuid string) int {
	t.Helper()
	port := freePort(t)
	cfg := fmt.Sprintf(`{"log":{"loglevel":"none"},
	  "inbounds":[{"listen":"127.0.0.1","port":%d,"protocol":"vless","settings":{"clients":[{"id":%q}],"decryption":"none"}}],
	  "outbounds":[{"protocol":"freedom"}]}`, port, uuid)
	pb, err := serial.LoadJSONConfig(strings.NewReader(cfg))
	if err != nil {
		t.Fatal(err)
	}
	inst, err := xcore.New(pb)
	if err != nil {
		t.Fatal(err)
	}
	if err := inst.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { inst.Close() })
	return port
}

func freePort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func startHTTPTarget(t *testing.T) string {
	mux := http.NewServeMux()
	mux.HandleFunc("/204", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/down", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "131072")
		w.Write(make([]byte, 131072))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func startTCPEcho(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	return ln.Addr().String()
}

// dpi is a SOCKS5 proxy that, while on, freezes server->client traffic to one
// port after 16 KB — how volume-based DPI treats VPN connections.
type dpi struct {
	addr string
	port int
	on   atomic.Bool
}

func startDPI(t *testing.T, port int) *dpi {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	d := &dpi{addr: ln.Addr().String(), port: port}
	d.on.Store(true)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go d.handle(c)
		}
	}()
	return d
}

func (d *dpi) handle(c net.Conn) {
	defer c.Close()
	buf := make([]byte, 262)
	if _, err := io.ReadFull(c, buf[:2]); err != nil {
		return
	}
	io.ReadFull(c, buf[:buf[1]])
	c.Write([]byte{5, 0})
	if _, err := io.ReadFull(c, buf[:4]); err != nil {
		return
	}
	var host string
	switch buf[3] {
	case 1:
		io.ReadFull(c, buf[:4])
		host = net.IP(buf[:4]).String()
	case 3:
		io.ReadFull(c, buf[:1])
		n := int(buf[0])
		io.ReadFull(c, buf[:n])
		host = string(buf[:n])
	case 4:
		io.ReadFull(c, buf[:16])
		host = net.IP(buf[:16]).String()
	}
	io.ReadFull(c, buf[:2])
	port := int(binary.BigEndian.Uint16(buf[:2]))
	up, err := net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer up.Close()
	c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	clientDone := make(chan struct{})
	go func() { io.Copy(up, c); close(clientDone) }()
	if port == d.port && d.on.Load() {
		io.Copy(c, io.LimitReader(up, 16<<10))
		<-clientDone // hold the connection open, silently
		return
	}
	io.Copy(c, up)
}

type tgCall struct {
	method string
	text   string
	params map[string]any
}

// incidentID extracts the id from the message's inline keyboard.
func (c tgCall) incidentID() string {
	b, _ := json.Marshal(c.params["reply_markup"])
	s := string(b)
	i := strings.Index(s, `"ack:`)
	if i < 0 {
		return ""
	}
	s = s[i+5:]
	return s[:strings.Index(s, `"`)]
}

type fakeTelegram struct {
	srv     *httptest.Server
	mu      sync.Mutex
	calls   []tgCall
	pending []map[string]any
	nextID  int
	nextUpd int
}

func startFakeTelegram(t *testing.T) *fakeTelegram {
	f := &fakeTelegram{nextID: 100}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		var p map[string]any
		json.NewDecoder(r.Body).Decode(&p)
		f.mu.Lock()
		defer f.mu.Unlock()
		switch method {
		case "getUpdates":
			ups := f.pending
			f.pending = nil
			if ups == nil {
				ups = []map[string]any{}
				f.mu.Unlock()
				time.Sleep(200 * time.Millisecond)
				f.mu.Lock()
			}
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": ups})
			return
		case "sendMessage", "sendRichMessage":
			f.nextID++
			text := cardText(p)
			f.calls = append(f.calls, tgCall{"sendMessage", text, p})
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": f.nextID}})
			return
		}
		f.calls = append(f.calls, tgCall{method, cardText(p), p})
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// cardText is a message's text whether it was sent plain or as a rich message.
func cardText(p map[string]any) string {
	if rich, ok := p["rich_message"].(map[string]any); ok {
		html, _ := rich["html"].(string)
		return html
	}
	text, _ := p["text"].(string)
	return text
}

func (f *fakeTelegram) press(data string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextUpd++
	f.pending = append(f.pending, map[string]any{
		"update_id": f.nextUpd,
		"callback_query": map[string]any{
			"id": "cb", "data": data,
			"from":    map[string]any{"id": 42, "username": "leo"},
			"message": map[string]any{"message_id": 1, "chat": map[string]any{"id": -1001}},
		},
	})
}

func (f *fakeTelegram) waitFor(t *testing.T, timeout time.Duration, method, substr string) tgCall {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		for _, c := range f.calls {
			if c.method == method && strings.Contains(c.text, substr) {
				f.mu.Unlock()
				return c
			}
		}
		f.mu.Unlock()
		time.Sleep(100 * time.Millisecond)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var seen []string
	for _, c := range f.calls {
		seen = append(seen, c.method+": "+c.text)
	}
	t.Fatalf("no %s containing %q within %s; calls:\n%s", method, substr, timeout, strings.Join(seen, "\n---\n"))
	return tgCall{}
}
