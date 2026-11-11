package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/wh3r3ar3you/uptimeforge/internal/store/pgtest"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/checks"
	"github.com/wh3r3ar3you/uptimeforge/internal/hub"
	"github.com/wh3r3ar3you/uptimeforge/internal/model"
	"github.com/wh3r3ar3you/uptimeforge/internal/remnawave"
)

func newApp(t *testing.T, opt Options) *App {
	t.Helper()
	a, err := New(opt, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// Everything made in the UI must survive a restart unchanged.
func TestUIConfigSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	a := newApp(t, Options{DatabaseURL: pgtest.URL(t), DataDir: dir})
	sub := hub.CheckConfig{Spec: checks.Spec{ID: "canary", Type: "xray", Xray: &checks.XrayParams{
		Subscription: "https://sub.example.com/abc", UserAgent: "v2rayN/7.0", Filter: "DE|NL", HeavyEvery: 3,
	}}, Name: "Canary", Group: "VPN", Probes: []string{"country=ru"}, Retries: 3}
	if err := a.SaveCheck(ctx, sub, true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SaveProbe(ctx, hub.ProbeConfig{ID: "home", Name: "Дом", Labels: map[string]string{"country": "ru"}}, true); err != nil {
		t.Fatal(err)
	}
	a.Store.Close()

	b := newApp(t, Options{DatabaseURL: pgtest.URL(t), DataDir: dir})
	defer b.Store.Close()
	var got *hub.CheckConfig
	for _, c := range b.Checks() {
		if c.ID == "canary" {
			got = &c.CheckConfig
		}
	}
	if got == nil || got.Xray.Subscription != sub.Xray.Subscription || got.Xray.UserAgent != "v2rayN/7.0" ||
		got.Xray.Filter != "DE|NL" || got.Xray.HeavyEvery != 3 || got.Retries != 3 || got.Probes[0] != "country=ru" {
		t.Fatalf("check changed across restart: %+v", got)
	}
	probes := b.Probes()
	if len(probes) != 2 || probes[0].ID != "hub" || probes[1].TokenHash == "" {
		t.Fatalf("probes: %+v", probes)
	}
}

func TestConfigFileItemsAreReadOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`
probes:
  - {id: de, name: DE, local: true, labels: {country: de}}
checks:
  - {id: api, type: tcp, tcp: {address: "127.0.0.1:1"}}
telegram: {token: "1:x", chat_id: -100}
`), 0o600)
	a := newApp(t, Options{DatabaseURL: pgtest.URL(t), DataDir: dir, ConfigPath: path})
	defer a.Store.Close()
	ctx := context.Background()
	if err := a.DeleteCheck(ctx, "api"); err != ErrManaged {
		t.Fatalf("deleting a file check: %v", err)
	}
	if _, err := a.SaveProbe(ctx, hub.ProbeConfig{ID: "de", Local: true}, false); err != ErrManaged {
		t.Fatalf("editing a file probe: %v", err)
	}
	st := a.Settings()
	if !st.Managed[SectionTelegram] || st.Config.Telegram.Token != masked {
		t.Fatalf("telegram must be managed and masked: %+v", st)
	}
	// With a file that defines probes, the default "hub" probe isn't created.
	if ps := a.Probes(); len(ps) != 1 || !ps[0].Managed {
		t.Fatalf("probes: %+v", ps)
	}
}

func TestEnrichIncidentWithRemnawaveNode(t *testing.T) {
	a := newApp(t, Options{DatabaseURL: pgtest.URL(t), DataDir: t.TempDir()})
	defer a.Store.Close()
	nodes := []remnawave.Node{
		{Name: "de-node-1", Address: "de1.example.com", UsersOnline: 412},
		{Name: "nl", Address: "nl.example.com", IPs: []remnawave.NodeIP{{IP: "203.0.113.7"}}, UsersOnline: 90},
	}
	a.nodes.Store(&nodes)
	for target, want := range map[string]string{
		"VLESS · de1.example.com:443": "de-node-1",
		"203.0.113.7:443":             "nl",
		"https://nl.example.com/":     "nl",
		"VLESS · other.example:443":   "",
	} {
		inc := model.Incident{Target: target, Name: "x"}
		a.enrichIncident(&inc)
		if inc.Node != want {
			t.Errorf("%s: node %q, want %q", target, inc.Node, want)
		}
	}
	inc := model.Incident{Target: "VLESS · de1.example.com:443", Users: 500}
	a.enrichIncident(&inc)
	if inc.Users != 500 {
		t.Fatal("users must keep the highest count seen")
	}
}

// Saving settings from the UI must not write the masked secrets of sections
// that config.yaml pins.
func TestSaveSettingsKeepsPinnedSections(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("telegram: {token: \"1:real-secret\", chat_id: -100}\n"), 0o600)
	a := newApp(t, Options{DatabaseURL: pgtest.URL(t), DataDir: dir, ConfigPath: path})
	defer a.Store.Close()
	ctx := context.Background()
	st := a.Settings()
	st.Config.Lang = "ru"
	if err := a.SaveSettings(ctx, st.Config); err != nil {
		t.Fatal(err)
	}
	var stored hub.Config
	a.Store.GetSetting(ctx, "settings", &stored)
	if stored.Telegram != nil {
		t.Fatalf("pinned telegram section written to the database: %+v", stored.Telegram)
	}
	if got := a.Hub.Config().Telegram; got == nil || got.Token != "1:real-secret" {
		t.Fatalf("running config lost the file's telegram settings: %+v", got)
	}
}

// eventually polls cond until it holds or d passes.
func eventually(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func acceptAndClose(t *testing.T) string {
	t.Helper()
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

// Rebuilding notifiers (any telegram/webhook/lang/timezone/public_url change)
// must hand queued and mid-retry alerts over to the new instance.
func TestNotifierRebuildKeepsAlerts(t *testing.T) {
	var broken atomic.Bool
	broken.Store(true)
	var mu sync.Mutex
	var attempts, delivered []string
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		json.NewDecoder(r.Body).Decode(&p)
		text, _ := p["text"].(string)
		if rich, ok := p["rich_message"].(map[string]any); ok { // cards go out as rich messages
			text, _ = rich["html"].(string)
		}
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			io.WriteString(w, `{"ok":true,"result":[]}`)
		case broken.Load():
			attempts = append(attempts, text)
			io.WriteString(w, `{"ok":false,"error_code":502,"description":"Bad Gateway"}`)
		default:
			delivered = append(delivered, text)
			io.WriteString(w, `{"ok":true,"result":{"message_id":7}}`)
		}
	}))
	defer tg.Close()

	a := newApp(t, Options{DatabaseURL: pgtest.URL(t), DataDir: t.TempDir()})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	settings := hub.Config{Lang: "en", Baseline: []checks.Spec{{Type: "tcp", TCP: &checks.TCPParams{Address: acceptAndClose(t)}}},
		Telegram: &hub.Telegram{Token: "1:x", ChatID: -100, APIURL: tg.URL}}
	if err := a.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if err := a.SaveCheck(ctx, hub.CheckConfig{Spec: checks.Spec{ID: "api", Type: "tcp", Interval: checks.Duration(2 * time.Second),
		TCP: &checks.TCPParams{Address: "127.0.0.1:1"}}, Name: "API-DOWN", Retries: 1}, true); err != nil {
		t.Fatal(err)
	}
	if !eventually(20*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return len(attempts) > 0 }) {
		t.Fatal("the alert never reached the Telegram API")
	}

	// The alert is being retried; the admin changes the language meanwhile.
	settings.Lang = "ru"
	if err := a.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	broken.Store(false)
	if !eventually(20*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, d := range delivered {
			if strings.Contains(d, "API-DOWN") {
				return true
			}
		}
		return false
	}) {
		t.Fatal("the DOWN alert was lost when the notifiers were rebuilt")
	}
}

// A heartbeat URL pasted with a leading space used to crash the hub. It is
// trimmed, and the dead man's switch gets pinged.
func TestHeartbeatURLWithSpaces(t *testing.T) {
	var pings atomic.Int32
	hc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { pings.Add(1) }))
	defer hc.Close()
	a := newApp(t, Options{DatabaseURL: pgtest.URL(t), DataDir: t.TempDir()})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	if err := a.SaveSettings(ctx, hub.Config{Heartbeat: hub.Heartbeat{URL: " " + hc.URL + "/5f1c ", Interval: checks.Duration(time.Second)}}); err != nil {
		t.Fatal(err)
	}
	if !eventually(20*time.Second, func() bool { return pings.Load() > 0 }) {
		t.Fatal("the heartbeat URL was never pinged")
	}
	if !a.Hub.Healthy() {
		t.Fatal("hub is not healthy")
	}
}

// Probe label maps from config.yaml must not be shared between a.file and
// the running config: every compose+Normalize writes Labels["id"] into them
// while the API reads them (concurrent map write).
func TestProbeLabelsNotShared(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("probes:\n  - {id: de, name: DE, local: true, labels: {country: de, kind: dc}}\n"), 0o600)
	a := newApp(t, Options{DatabaseURL: pgtest.URL(t), DataDir: dir, ConfigPath: path})
	defer a.Store.Close()
	ctx := context.Background()
	if reflect.ValueOf(a.Hub.Config().Probes[0].Labels).Pointer() == reflect.ValueOf(a.file.Probes[0].Labels).Pointer() {
		t.Fatal("the running config shares the label map of config.yaml")
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { // what the API does on every /probes and /overview
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				json.Marshal(a.Hub.Snapshot().Probes)
			}
		}
	}()
	for i := range 50 {
		c := hub.CheckConfig{Spec: checks.Spec{ID: fmt.Sprintf("c%d", i%3), Type: "tcp", TCP: &checks.TCPParams{Address: "127.0.0.1:1"}}}
		if err := a.SaveCheck(ctx, c, i < 3); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	<-done
}

// A mistyped link must be refused when saving: stored, it would raise a false
// alert from every probe. It stays out of hub validation so that a bad check
// already in the database cannot stop the hub from starting.
func TestSaveCheckRefusesBadLink(t *testing.T) {
	a := newApp(t, Options{DatabaseURL: pgtest.URL(t), DataDir: t.TempDir()})
	defer a.Store.Close()
	ctx := context.Background()
	mk := func(link string) hub.CheckConfig {
		return hub.CheckConfig{Spec: checks.Spec{ID: "de", Type: "xray", Xray: &checks.XrayParams{Link: link}}}
	}
	var ve *ValidationError
	for _, link := range []string{"vless://broken-link", "vless://uuid@host:", "nonsense"} {
		if err := a.SaveCheck(ctx, mk(link), true); !errors.As(err, &ve) {
			t.Errorf("link %q: expected a validation error, got %v", link, err)
		}
	}
	if err := a.SaveCheck(ctx, mk("vless://b831381d-6324-4d53-ad4f-8cda48b30811@de1.example.com:443?type=tcp#de"), true); err != nil {
		t.Fatalf("a good link was refused: %v", err)
	}
}

// A node deleted from the panel takes its server and checks out of
// monitoring, but only once it has been gone for three polls, never on an
// empty answer or most nodes vanishing at once, and only what is its own.
func TestRemnawavePrune(t *testing.T) {
	a := newApp(t, Options{DatabaseURL: pgtest.URL(t), DataDir: t.TempDir()})
	defer a.Store.Close()
	ctx := context.Background()
	for _, c := range []hub.CheckConfig{
		{Spec: checks.Spec{ID: "c-tcp", Type: "tcp", TCP: &checks.TCPParams{Address: "c.example.com:443"}}, Name: "Something"},
		{Spec: checks.Spec{ID: "c", Type: "tcp", TCP: &checks.TCPParams{Address: "10.0.0.9:22"}}},
		{Spec: checks.Spec{ID: "other", Type: "tcp", TCP: &checks.TCPParams{Address: "b.example.com:443"}}},
	} {
		if err := a.SaveCheck(ctx, c, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.SaveProbe(ctx, hub.ProbeConfig{ID: "c-agent", Name: "C"}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SaveProbe(ctx, hub.ProbeConfig{ID: "b", Name: "B"}, true); err != nil {
		t.Fatal(err)
	}
	node := func(id, name, addr string) remnawave.Node { return remnawave.Node{UUID: id, Name: name, Address: addr} }
	a1, b1, c1 := node("1", "A", "a.example.com"), node("2", "B", "b.example.com"), node("3", "C (main)", "c.example.com")
	poll := func(nodes ...remnawave.Node) []remnawave.Node {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.trackDeleted(nodes)
	}
	poll(a1, b1, c1)
	if got := poll(); got != nil {
		t.Fatal("an empty answer deletes nothing")
	}
	for i := range 3 { // A alone: two of three gone at once is a broken answer
		if got := poll(a1); got != nil {
			t.Fatalf("poll %d: most nodes gone at once is not a deletion: %+v", i, got)
		}
	}
	if got := poll(a1, b1); got != nil {
		t.Fatal("one poll without C is not a deletion")
	}
	poll(a1, b1)
	got := poll(a1, b1)
	if len(got) != 1 || got[0].UUID != "3" {
		t.Fatalf("C gone for three polls: %+v", got)
	}
	if again := poll(a1, b1); again != nil {
		t.Fatal("a deleted node is reported once")
	}

	// Pruning pauses C's checks with a note instead of deleting them, and
	// keeps its server: an agent's token cannot be brought back.
	a.prune(ctx, got)
	paused := map[string]bool{}
	for _, c := range a.Checks() {
		paused[c.ID] = c.Paused && slices.Contains(c.Tags, nodeDeletedTag)
	}
	if !paused["c-tcp"] || !paused["c"] || paused["other"] {
		t.Errorf("C's checks paused with a note, others untouched: %v", paused)
	}
	probes := map[string]bool{}
	for _, p := range a.Probes() {
		probes[p.ID] = true
	}
	for _, want := range []string{"c-agent", "b", "hub"} {
		if !probes[want] {
			t.Errorf("server %s must stay: %v", want, probes)
		}
	}

	// C comes back (re-created under a new id): its checks run again.
	a.restoreNodes(ctx, []remnawave.Node{a1, b1, node("33", "C", "c.example.com")})
	for _, c := range a.Checks() {
		if c.Paused || slices.Contains(c.Tags, nodeDeletedTag) {
			t.Errorf("check %s still paused after its node came back: %+v", c.ID, c.Tags)
		}
	}
}

// A panel reinstall or a new panel URL replaces every node's id at once.
// The old ids must never count as deleted nodes: their names and addresses
// are on the panel, and pruning them would take the whole fleet's checks.
func TestRemnawaveReinstallPrunesNothing(t *testing.T) {
	a := &App{}
	node := func(id, name, addr string) remnawave.Node { return remnawave.Node{UUID: id, Name: name, Address: addr} }
	poll := func(nodes ...remnawave.Node) []remnawave.Node { return a.trackDeleted(nodes) }
	poll(node("1", "A", "a.example.com"), node("2", "B", "b.example.com"), node("3", "C", "c.example.com"))
	fresh := []remnawave.Node{node("11", "A", "a.example.com"), node("12", "B", "b.example.com"), node("13", "C", "c.example.com")}
	for i := range 6 {
		if got := poll(fresh...); got != nil {
			t.Fatalf("poll %d after the reinstall pruned %+v", i, got)
		}
	}
	// The guard is not stuck on the old ids: a real deletion still counts.
	var got []remnawave.Node
	for range pruneAfter {
		got = poll(fresh[:2]...)
	}
	if len(got) != 1 || got[0].UUID != "13" {
		t.Fatalf("C deleted after the reinstall: %+v", got)
	}
}

// A node deleted and created again under the same name or address keeps
// its checks, however long the gap between the two.
func TestRemnawaveRecreatedNodeKeepsChecks(t *testing.T) {
	a := &App{}
	node := func(id, name, addr string) remnawave.Node { return remnawave.Node{UUID: id, Name: name, Address: addr} }
	poll := func(nodes ...remnawave.Node) []remnawave.Node { return a.trackDeleted(nodes) }
	a1, b1 := node("1", "A", "a.example.com"), node("2", "B", "b.example.com")
	poll(a1, b1, node("3", "C", "c.example.com"), node("4", "D", "d.example.com"))
	poll(a1, b1)
	for i := range 5 { // C is back by name, D by address
		if got := poll(a1, b1, node("33", "C", "c2.example.com"), node("44", "D renamed", "d.example.com")); got != nil {
			t.Fatalf("poll %d pruned a re-created node: %+v", i, got)
		}
	}
}

// A secondary panel endpoint that fails must not stop node alerts: the node
// list feeds the users-drop and node-down detectors on its own, and the
// home-page numbers keep their last good value with the error shown.
func TestRemnawaveStatsFailureKeepsNodeAlerts(t *testing.T) {
	var nodeCalls atomic.Int32
	var bandwidthDown atomic.Bool
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/nodes/":
			nodeCalls.Add(1)
			io.WriteString(w, `{"response":[{"uuid":"u1","name":"DE-1","address":"de1.example.com","isConnected":true,"usersOnline":42}]}`)
		case "/api/system/stats":
			io.WriteString(w, `{"response":{}}`)
		case "/api/system/stats/bandwidth":
			if bandwidthDown.Load() {
				w.WriteHeader(http.StatusInternalServerError)
				io.WriteString(w, `{"message":"boom"}`)
				return
			}
			io.WriteString(w, `{"response":{}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer panel.Close()
	a := newApp(t, Options{DatabaseURL: pgtest.URL(t), DataDir: t.TempDir()})
	defer a.Store.Close()
	ctx := context.Background()
	if err := a.SaveSettings(ctx, hub.Config{Remnawave: &hub.Remnawave{URL: panel.URL, Token: "t"}}); err != nil {
		t.Fatal(err)
	}
	a.refreshRemnawave(ctx)
	if v := a.Remnawave(); v.Error != "" || v.Stats == nil || v.Stats.NodesOnline != 1 {
		t.Fatalf("healthy panel: %+v", v)
	}
	if n := nodeCalls.Load(); n != 1 {
		t.Errorf("the node list was fetched %d times in one poll", n)
	}

	bandwidthDown.Store(true)
	time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second))) // the next poll lands in its own second
	start := time.Now()
	a.refreshRemnawave(ctx)
	if !eventually(10*time.Second, func() bool {
		rows, _ := a.Store.NodeUsersSince(ctx, start.Truncate(time.Second))
		return len(rows) == 1 && rows[0].Users == 42
	}) {
		t.Error("users online were not observed while the stats endpoint failed")
	}
	v := a.Remnawave()
	if len(v.Nodes) != 1 || !strings.Contains(v.Error, "bandwidth") || v.Stats == nil || v.Stats.NodesTotal != 1 {
		t.Errorf("stats failure: nodes %d, error %q, stats %+v", len(v.Nodes), v.Error, v.Stats)
	}
}

type fakeGeo map[string]string

func (f fakeGeo) Country(_ context.Context, ip netip.Addr) (string, error) {
	return f[ip.String()], nil
}

// The country of a proxy comes from the address its link points at; a link
// that cannot be read is a validation error that does not repeat it.
func TestLocateProxy(t *testing.T) {
	a := newApp(t, Options{DatabaseURL: pgtest.URL(t), DataDir: t.TempDir()})
	defer a.Store.Close()
	a.geo = fakeGeo{"203.0.113.7": "de", "198.51.100.4": "nl"}
	ctx := context.Background()
	for link, want := range map[string]string{
		"socks5://user:secret@203.0.113.7:1080": "de",
		"vless://11111111-2222-3333-4444-555555555555@198.51.100.4:443?security=reality&sni=example.com&pbk=x&type=tcp#NL": "nl",
		"http://[::ffff:203.0.113.7]:3128": "de",
	} {
		loc, err := a.LocateProxy(ctx, link)
		if err != nil || loc.Country != want || loc.Source == "" {
			t.Errorf("%s: %+v %v", link, loc, err)
		}
	}
	_, err := a.LocateProxy(ctx, "garbage-with-secret-p4ss")
	var ve *ValidationError
	if !errors.As(err, &ve) || strings.Contains(err.Error(), "p4ss") {
		t.Fatalf("a bad link is a validation error without the link in it: %v", err)
	}
}

// Bulk actions touch the UI's checks and skip the ones config.yaml owns.
func TestBulkSkipsManaged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("checks:\n  - {id: file, type: tcp, tcp: {address: \"127.0.0.1:1\"}}\n"), 0o600)
	a := newApp(t, Options{DatabaseURL: pgtest.URL(t), DataDir: dir, ConfigPath: path})
	defer a.Store.Close()
	ctx := context.Background()
	for _, id := range []string{"ui1", "ui2"} {
		if err := a.SaveCheck(ctx, hub.CheckConfig{Spec: checks.Spec{ID: id, Type: "tcp", TCP: &checks.TCPParams{Address: "127.0.0.1:1"}}}, true); err != nil {
			t.Fatal(err)
		}
	}
	skipped, err := a.BulkChecks(ctx, BulkAction{IDs: []string{"ui1", "ui2", "file"}, Action: "pause"})
	if err != nil || !slices.Equal(skipped, []string{"file"}) {
		t.Fatalf("skipped %v, %v", skipped, err)
	}
	for _, c := range a.Checks() {
		if c.ID != "file" && !c.Paused {
			t.Fatalf("%s not paused", c.ID)
		}
	}
	if _, err := a.BulkChecks(ctx, BulkAction{IDs: []string{"ui1"}, Action: "group", Group: "VPN"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.BulkChecks(ctx, BulkAction{IDs: []string{"ui1"}, Action: "explode"}); err == nil {
		t.Fatal("unknown action accepted")
	}
}

// Bulk tags are normalised like a check's own and removal is exact.
func TestBulkTags(t *testing.T) {
	a := newApp(t, Options{DatabaseURL: pgtest.URL(t), DataDir: t.TempDir()})
	defer a.Store.Close()
	ctx := context.Background()
	if err := a.SaveCheck(ctx, hub.CheckConfig{Spec: checks.Spec{ID: "c", Type: "tcp", TCP: &checks.TCPParams{Address: "127.0.0.1:1"}}, Tags: []string{"old", "keep"}}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.BulkChecks(ctx, BulkAction{IDs: []string{"c"}, Action: "tags", AddTags: []string{" DE", "vless"}, RemoveTags: []string{"OLD"}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range a.Checks() {
		if c.ID == "c" && !slices.Equal(c.Tags, []string{"de", "keep", "vless"}) {
			t.Fatalf("tags: %v", c.Tags)
		}
	}
}

// A template saved in the UI applies to the next alert, without a restart.
func TestSavedTemplateAppliesToNextAlert(t *testing.T) {
	var mu sync.Mutex
	var delivered []string
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		json.NewDecoder(r.Body).Decode(&p)
		text, _ := p["text"].(string)
		if rich, ok := p["rich_message"].(map[string]any); ok {
			text, _ = rich["html"].(string)
		}
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			io.WriteString(w, `{"ok":true,"result":[]}`)
			return
		}
		mu.Lock()
		delivered = append(delivered, text)
		mu.Unlock()
		io.WriteString(w, `{"ok":true,"result":{"message_id":7}}`)
	}))
	defer tg.Close()

	a := newApp(t, Options{DatabaseURL: pgtest.URL(t), DataDir: t.TempDir()})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	settings := hub.Config{Lang: "en", Baseline: []checks.Spec{{Type: "tcp", TCP: &checks.TCPParams{Address: acceptAndClose(t)}}},
		Telegram: &hub.Telegram{Token: "1:x", ChatID: -100, APIURL: tg.URL}}
	if err := a.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	settings.Templates = map[string]map[string]string{"en": {"card": "<p>CUSTOM {name}</p>"}}
	if err := a.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if err := a.SaveCheck(ctx, hub.CheckConfig{Spec: checks.Spec{ID: "api", Type: "tcp", Interval: checks.Duration(2 * time.Second),
		TCP: &checks.TCPParams{Address: "127.0.0.1:1"}}, Name: "API-DOWN", Retries: 1}, true); err != nil {
		t.Fatal(err)
	}
	if !eventually(20*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, d := range delivered {
			if strings.Contains(d, "CUSTOM API-DOWN") {
				return true
			}
		}
		return false
	}) {
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("the saved template was not used: %q", delivered)
	}
}

// A template Telegram would refuse is not saved: a refused card is a lost alert.
func TestSaveTemplateRejectsBrokenHTML(t *testing.T) {
	a := newApp(t, Options{DatabaseURL: pgtest.URL(t), DataDir: t.TempDir()})
	defer a.Store.Close()
	st := a.Settings()
	cfg := st.Config
	cfg.Templates = map[string]map[string]string{"ru": {"card": "<b>{name}"}}
	err := a.SaveSettings(context.Background(), cfg)
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(err.Error(), "card") {
		t.Fatalf("broken template accepted: %v", err)
	}
	if a.Settings().Config.Templates != nil {
		t.Fatal("nothing must be saved")
	}
	cfg.Templates = map[string]map[string]string{"ru": {"card": "<p>{emoji} {name}</p>"}}
	if err := a.SaveSettings(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
}
