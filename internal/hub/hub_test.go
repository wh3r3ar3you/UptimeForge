package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/wh3r3ar3you/uptimeforge/internal/agent"
	"github.com/wh3r3ar3you/uptimeforge/internal/checks"
	"github.com/wh3r3ar3you/uptimeforge/internal/geoip"
	"github.com/wh3r3ar3you/uptimeforge/internal/model"
	"github.com/wh3r3ar3you/uptimeforge/internal/proto"
	"github.com/wh3r3ar3you/uptimeforge/internal/xray"
)

type recorder struct {
	mu     sync.Mutex
	events []model.Event
}

func (r *recorder) Notify(ev model.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recorder) find(typ model.EventType, key string) *model.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.events {
		if r.events[i].Type == typ && r.events[i].Incident.Key == key {
			return &r.events[i]
		}
	}
	return nil
}

func testHub(t *testing.T) (*Hub, *recorder) {
	t.Helper()
	cfg := &Config{
		DataDir:  t.TempDir(),
		Baseline: []checks.Spec{{Type: "tcp", TCP: &checks.TCPParams{Address: "77.88.8.8:53"}}},
		Probes: []ProbeConfig{
			{ID: "ru-a", Token: "token-ru-a-0123456789", Probing: true, Labels: map[string]string{"country": "ru"}},
			{ID: "ru-b", Token: "token-ru-b-0123456789", Probing: true, Labels: map[string]string{"country": "ru"}},
			{ID: "de", Token: "token-de-00123456789", Probing: true, Labels: map[string]string{"country": "de"}},
		},
		Checks: []CheckConfig{{Spec: checks.Spec{ID: "node", Type: "tcp", TCP: &checks.TCPParams{Address: "node.example:443"}}}},
	}
	if err := cfg.normalize(); err != nil {
		t.Fatal(err)
	}
	h, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	h.AddNotifier(rec)
	h.mu.Lock()
	for _, p := range h.probes {
		p.online = true
	}
	h.rebuild()
	h.mu.Unlock()
	return h, rec
}

func res(id string, up bool) checks.Result {
	r := checks.Result{CheckID: id, Status: checks.Up}
	if !up {
		r.Status, r.Reason, r.Error = checks.Down, "timeout", "i/o timeout"
	}
	return r
}

func TestProbeWithoutInternetCausesNoFalseAlarm(t *testing.T) {
	h, rec := testHub(t)
	for range 2 { // retries = 2
		h.Ingest("ru-a", []checks.Result{res("_baseline/0", false), res("node", false)})
		h.Ingest("ru-b", []checks.Result{res("_baseline/0", true), res("node", true)})
		h.Ingest("de", []checks.Result{res("_baseline/0", true), res("node", true)})
	}
	if ev := rec.find(model.EventOpen, "node"); ev != nil {
		t.Fatalf("probe without internet produced an alert for the node: %+v", ev.Incident)
	}
	ev := rec.find(model.EventOpen, "probe-net:ru-a")
	if ev == nil || ev.Incident.Kind != model.KindProbeNoNet {
		t.Fatal("expected a 'probe has no internet' incident")
	}

	// Internet is back: the probe's failure now counts and the node is partially down.
	h.Ingest("ru-a", []checks.Result{res("_baseline/0", true), res("node", false)})
	if rec.find(model.EventResolve, "probe-net:ru-a") == nil {
		t.Fatal("probe-net incident should resolve")
	}
	if rec.find(model.EventOpen, "node") != nil {
		t.Fatal("a partial outage must settle before it is announced")
	}
	h.mu.Lock()
	h.incidents["node"].Started = time.Now().Add(-time.Hour)
	h.mu.Unlock()
	h.tick()
	ev = rec.find(model.EventOpen, "node")
	if ev == nil || ev.Incident.Kind != model.KindPartial {
		t.Fatalf("expected partial outage after settling, got %+v", ev)
	}
}

func TestRetriesAndEscalation(t *testing.T) {
	h, rec := testHub(t)
	for _, p := range []string{"ru-a", "ru-b"} {
		h.Ingest(p, []checks.Result{res("node", false)})
	}
	h.Ingest("de", []checks.Result{res("node", true)})
	if rec.find(model.EventOpen, "node") != nil {
		t.Fatal("a single failure must not alert (retries=2)")
	}
	for _, p := range []string{"ru-a", "ru-b"} {
		h.Ingest(p, []checks.Result{res("node", false)})
	}
	// ru-a crossed retries first (partial for a moment), ru-b followed: one
	// message, already "blocked".
	ev := rec.find(model.EventOpen, "node")
	if ev == nil || ev.Incident.Kind != model.KindBlocked || ev.Incident.Severity != model.Critical {
		t.Fatalf("expected critical 'blocked in ru', got %+v", ev)
	}
	if n := len(rec.events); n != 1 {
		t.Fatalf("expected exactly one notification so far, got %d", n)
	}
	// Germany fails too: blocked -> down is an escalation.
	h.Ingest("de", []checks.Result{res("node", false)})
	h.Ingest("de", []checks.Result{res("node", false)})
	up := rec.find(model.EventUpdate, "node")
	if up == nil || up.Incident.Kind != model.KindDown || !up.Escalated {
		t.Fatalf("expected escalation to down, got %+v", up)
	}
	// One success per probe clears it.
	for _, p := range []string{"ru-a", "ru-b", "de"} {
		h.Ingest(p, []checks.Result{res("node", true)})
	}
	r := rec.find(model.EventResolve, "node")
	if r == nil || r.Incident.Worst != model.KindDown || r.Incident.PeakFailing != 3 {
		t.Fatalf("expected resolve with worst=down peak=3, got %+v", r)
	}
}

func TestOfflineProbeAndMute(t *testing.T) {
	h, rec := testHub(t)
	h.mu.Lock()
	p := h.probes["de"]
	p.online, p.since = false, time.Now().Add(-time.Hour)
	h.mu.Unlock()
	h.tick()
	ev := rec.find(model.EventOpen, "probe:de")
	if ev == nil || ev.Incident.Severity != model.Warning {
		t.Fatal("expected a warning about the offline probe")
	}

	if err := h.Mute(ev.Incident.ID, time.Hour, "@leo"); err != nil {
		t.Fatal(err)
	}
	// With DE offline both RU probes failing is a plain "down"; the probe is muted, the check is not.
	for _, p := range []string{"ru-a", "ru-b"} {
		h.Ingest(p, []checks.Result{res("node", false), res("node", false)})
	}
	if ev := rec.find(model.EventOpen, "node"); ev == nil || ev.Incident.Kind != model.KindDown {
		t.Fatal("muting a probe incident must not mute checks")
	}
}

func TestSilentWitnessDoesNotResolve(t *testing.T) {
	h, rec := testHub(t)
	for range 2 {
		h.Ingest("ru-a", []checks.Result{res("node", false)})
		h.Ingest("ru-b", []checks.Result{res("node", true)})
		h.Ingest("de", []checks.Result{res("node", true)})
	}
	h.mu.Lock()
	h.incidents["node"].Started = time.Now().Add(-time.Hour)
	h.mu.Unlock()
	h.tick()
	if rec.find(model.EventOpen, "node") == nil {
		t.Fatal("expected a partial outage")
	}
	// The only failing probe disconnects: no evidence of recovery.
	h.mu.Lock()
	h.probes["ru-a"].online = false
	h.evaluateAll(time.Now())
	h.mu.Unlock()
	if rec.find(model.EventResolve, "node") != nil {
		t.Fatal("incident resolved because its only witness went offline")
	}
	// It comes back and reports success: now it is a real recovery.
	h.mu.Lock()
	h.probes["ru-a"].online = true
	h.mu.Unlock()
	h.Ingest("ru-a", []checks.Result{res("node", true)})
	if rec.find(model.EventResolve, "node") == nil {
		t.Fatal("expected resolve after the witness reported success")
	}
}

// A silent witness must survive a change of the failing set: a hiccup on
// another probe clearing up is not evidence that the witness recovered.
func TestSilentWitnessKeptWhenLinesChange(t *testing.T) {
	h, rec := testHub(t)
	for range 2 {
		h.Ingest("ru-a", []checks.Result{res("node", false)})
		h.Ingest("ru-b", []checks.Result{res("node", true)})
		h.Ingest("de", []checks.Result{res("node", true)})
	}
	h.mu.Lock()
	h.incidents["node"].Started = time.Now().Add(-time.Hour)
	h.mu.Unlock()
	h.tick()
	if rec.find(model.EventOpen, "node") == nil {
		t.Fatal("expected a partial outage")
	}
	// ru-a (home router) drops off the hub; the node stays broken for it.
	h.mu.Lock()
	h.probes["ru-a"].online = false
	h.evaluateAll(time.Now())
	h.mu.Unlock()
	// Another ISP has a short hiccup.
	h.Ingest("ru-b", []checks.Result{res("node", false)})
	h.Ingest("ru-b", []checks.Result{res("node", false)})
	h.Ingest("ru-b", []checks.Result{res("node", true)})
	if ev := rec.find(model.EventResolve, "node"); ev != nil {
		t.Fatalf("resolved while the witness ru-a never reported success: %+v", ev.Incident.Lines)
	}
	h.mu.Lock()
	kept := false
	for _, l := range h.incidents["node"].Lines {
		kept = kept || (l.Probe == "ru-a" && !l.OK)
	}
	h.mu.Unlock()
	if !kept {
		t.Fatal("the incident forgot its silent witness")
	}
	// The witness comes back and reports success: a real recovery.
	h.mu.Lock()
	h.probes["ru-a"].online = true
	h.mu.Unlock()
	h.Ingest("ru-a", []checks.Result{res("node", true)})
	if rec.find(model.EventResolve, "node") == nil {
		t.Fatal("expected resolve after the witness reported success")
	}
}

func restoredNoNet(h *Hub) {
	h.Restore([]model.Incident{{ID: "old1", Key: "probe-net:ru-a", Kind: model.KindProbeNoNet, Worst: model.KindProbeNoNet,
		Severity: model.Warning, Type: "probe", Name: "ru-a", Started: time.Now().Add(-time.Hour), Notified: true,
		Lines: []model.ProbeLine{{Probe: "ru-a", Reason: "baseline"}}}}, nil)
}

// A "probe has no internet" incident restored after a restart resolves on
// the next good baseline, and a later real loss is announced again.
func TestRestoredProbeNetResolves(t *testing.T) {
	h, rec := testHub(t)
	restoredNoNet(h)
	for range 3 {
		h.Ingest("ru-a", []checks.Result{res("_baseline/0", true), res("node", true)})
		h.tick()
	}
	if rec.find(model.EventResolve, "probe-net:ru-a") == nil {
		t.Fatal("restored 'probe has no internet' incident did not resolve although the baseline is up")
	}
	n := len(rec.events)
	for range 2 {
		h.Ingest("ru-a", []checks.Result{res("_baseline/0", false)})
	}
	h.tick()
	if len(rec.events) == n || rec.events[n].Type != model.EventOpen || rec.events[n].Incident.Key != "probe-net:ru-a" {
		t.Fatalf("a real loss of internet was not announced: %+v", rec.events[n:])
	}
}

// Right after a restart there are no baseline results yet: that is not
// evidence that the probe has internet again.
func TestRestoredProbeNetWaitsForBaseline(t *testing.T) {
	h, rec := testHub(t)
	restoredNoNet(h)
	h.tick()
	h.tick()
	if ev := rec.find(model.EventResolve, "probe-net:ru-a"); ev != nil {
		t.Fatal("resolved before any baseline result arrived")
	}
	// The probe is still excluded from verdicts meanwhile: its failure is not
	// counted against the node.
	for range 2 {
		h.Ingest("ru-a", []checks.Result{res("node", false)})
	}
	h.tick()
	if rec.find(model.EventOpen, "node") != nil {
		t.Fatal("a probe without internet produced an alert for the node")
	}
	// Baseline results that never arrive do not keep it excluded forever.
	h.mu.Lock()
	h.probes["ru-a"].since = time.Now().Add(-time.Hour)
	h.mu.Unlock()
	h.tick()
	if rec.find(model.EventResolve, "probe-net:ru-a") == nil {
		t.Fatal("a probe without baseline results stayed 'no internet' forever")
	}
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// tcpListener accepts and closes connections: a TCP service that is up.
func tcpListener(t *testing.T) string {
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
		time.Sleep(20 * time.Millisecond)
	}
}

// A connected agent probe turned into a virtual one (local/via) must count
// as online: no false "probe offline", and its failures raise alerts.
func TestAgentTurnedVirtualStaysOnline(t *testing.T) {
	up := tcpListener(t)
	svcLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := svcLn.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	svc := svcLn.Addr().String()
	const token = "x-token-0123456789abcdef"
	mk := func(local bool) *Config {
		p := ProbeConfig{ID: "x", Labels: map[string]string{"country": "nl"}}
		if local {
			p.Local = true
		} else {
			p.Token = token
			p.Probing = true
		}
		return &Config{
			DataDir:      t.TempDir(),
			OfflineGrace: checks.Duration(time.Second),
			Baseline:     []checks.Spec{{Type: "tcp", TCP: &checks.TCPParams{Address: up}}},
			Probes:       []ProbeConfig{p},
			Checks: []CheckConfig{{Spec: checks.Spec{ID: "c", Type: "tcp", Interval: checks.Duration(2 * time.Second),
				TCP: &checks.TCPParams{Address: svc}}, Retries: 1}},
		}
	}
	h, err := New(mk(false), quietLog())
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	h.AddNotifier(rec)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.Run(ctx)
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()
	go agent.New(ctx, agent.Config{HubURL: srv.URL, Token: token, MetricsInterval: -1}, quietLog()).Run(ctx)
	if !eventually(10*time.Second, func() bool { return h.Snapshot().Probes[0].Online }) {
		t.Fatal("agent never connected")
	}

	// The admin switches the probe to "run from the hub itself", and the
	// monitored service dies right after.
	if err := h.Apply(mk(true)); err != nil {
		t.Fatal(err)
	}
	converted := time.Now()
	svcLn.Close()
	if !eventually(20*time.Second, func() bool { return rec.find(model.EventOpen, "c") != nil }) {
		t.Fatalf("the service is down on the local probe but no alert was raised: %+v", h.Snapshot().Checks)
	}
	// Let at least one tick pass after the offline grace.
	if !eventually(20*time.Second, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.lastTick.After(converted.Add(h.cfg.OfflineGrace.D()))
	}) {
		t.Fatal("no tick")
	}
	if st := h.Snapshot(); !st.Probes[0].Online || st.Probes[0].Mode != "local" {
		t.Fatalf("the local probe is not online: %+v", st.Probes[0])
	}
	if ev := rec.find(model.EventOpen, "probe:x"); ev != nil {
		t.Fatalf("false 'probe offline' for a running local probe: %+v", ev.Incident)
	}
}

// An offline agent probe turned virtual resolves its "probe offline".
func TestOfflineAgentTurnedVirtualResolves(t *testing.T) {
	h, rec := testHub(t)
	h.mu.Lock()
	p := h.probes["de"]
	p.online, p.since = false, time.Now().Add(-time.Hour)
	h.mu.Unlock()
	h.tick()
	if rec.find(model.EventOpen, "probe:de") == nil {
		t.Fatal("expected a 'probe offline' incident")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.mu.Lock()
	h.ctx = ctx // as Run sets it
	h.mu.Unlock()
	cfg := h.Config().Clone()
	cfg.Probes[2].Token, cfg.Probes[2].TokenHash, cfg.Probes[2].Local = "", "", true
	if err := h.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	defer func() {
		h.mu.Lock()
		h.stopVirtual(h.probes["de"], time.Now())
		h.mu.Unlock()
	}()
	if rec.find(model.EventResolve, "probe:de") == nil {
		t.Fatal("the probe runs on the hub now, but 'probe offline' stayed open")
	}
	h.tick()
	if n := len(rec.events); rec.events[n-1].Type != model.EventResolve {
		t.Fatalf("unexpected events after the switch: %+v", rec.events)
	}
}

// A token rotated (or a probe removed) while an agent is still in the
// handshake must not attach that agent and hand it the check list.
func TestRotatedTokenRejectedDuringHandshake(t *testing.T) {
	mk := func(tok string) *Config {
		return &Config{DataDir: t.TempDir(), Baseline: []checks.Spec{},
			Probes: []ProbeConfig{{ID: "x", Token: tok}},
			Checks: []CheckConfig{{Spec: checks.Spec{ID: "secret-link", Type: "tcp", TCP: &checks.TCPParams{Address: "10.0.0.1:443"}}}}}
	}
	for _, tc := range []struct {
		name   string
		change *Config // applied between connect and hello; nil: no change
	}{
		{"unchanged", nil},
		{"rotated", mk("new-secret-token-0123456789")},
		{"removed", &Config{DataDir: t.TempDir(), Baseline: []checks.Spec{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, err := New(mk("old-leaked-token-0123456789"), quietLog())
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(h.Handler())
			defer srv.Close()
			ctx := context.Background()
			conn, _, err := websocket.Dial(ctx, "ws"+srv.URL[4:]+"/api/agent/ws", &websocket.DialOptions{
				HTTPHeader: http.Header{"Authorization": {"Bearer old-leaked-token-0123456789"}}})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			if tc.change != nil {
				if err := h.Apply(tc.change); err != nil {
					t.Fatal(err)
				}
			}
			hello, _ := proto.Encode(proto.TypeHello, proto.Hello{Proto: proto.Version, Version: "evil"})
			conn.Write(ctx, websocket.MessageText, hello)
			rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			_, data, err := conn.Read(rctx)
			if tc.change == nil {
				var env proto.Envelope
				if err != nil || json.Unmarshal(data, &env) != nil || env.Type != proto.TypeAssign {
					t.Fatalf("a valid agent must get its assignment: %v %s", err, data)
				}
				return
			}
			if err == nil {
				t.Fatalf("revoked token attached and received %s", data)
			}
			if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
				t.Fatalf("expected a policy-violation close, got %v", err)
			}
			for _, p := range h.Snapshot().Probes {
				if p.Online {
					t.Fatalf("probe %s is online", p.ID)
				}
			}
		})
	}
}

// memRecorder keeps what the hub persists, like the database does.
type memRecorder struct {
	mu        sync.Mutex
	incidents map[string]model.Incident
	mutes     map[string]time.Time
}

func (r *memRecorder) Results(string, []checks.Result) {}
func (r *memRecorder) System(string, proto.Metrics)    {}
func (r *memRecorder) Incident(inc model.Incident) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.incidents == nil {
		r.incidents = map[string]model.Incident{}
	}
	r.incidents[inc.ID] = inc
}
func (r *memRecorder) Mutes(m map[string]time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mutes = m
}

// restart builds a new hub from what was persisted, as the app does.
func (r *memRecorder) restart(t *testing.T) (*Hub, *recorder) {
	r.mu.Lock()
	var open []model.Incident
	for _, inc := range r.incidents {
		if inc.Resolved.IsZero() {
			inc.Started = inc.Started.Add(-time.Hour) // the restart took a while
			open = append(open, inc)
		}
	}
	mutes := r.mutes
	r.mu.Unlock()
	h, rec := testHub(t)
	h.Restore(open, mutes)
	return h, rec
}

// Mute and ack on an incident that was not announced yet (a partial outage
// still settling) must survive a restart: no alert for a muted incident, no
// reminders for an acknowledged one.
func TestMuteAndAckBeforeNotifySurviveRestart(t *testing.T) {
	h, _ := testHub(t)
	mem := &memRecorder{}
	h.SetRecorder(mem)
	for range 2 {
		h.Ingest("ru-a", []checks.Result{res("node", false)})
		h.Ingest("ru-b", []checks.Result{res("node", true)})
		h.Ingest("de", []checks.Result{res("node", true)})
	}
	inc := h.Snapshot().Incidents
	if len(inc) != 1 || inc[0].Notified {
		t.Fatalf("expected one settling incident: %+v", inc)
	}
	if err := h.Mute(inc[0].ID, time.Hour, "@leo"); err != nil {
		t.Fatal(err)
	}
	if err := h.Ack(inc[0].ID, "@ann"); err != nil {
		t.Fatal(err)
	}

	h2, rec2 := mem.restart(t)
	h2.tick()
	if ev := rec2.find(model.EventOpen, "node"); ev != nil {
		t.Fatalf("a muted incident was announced after a restart: %+v", ev.Incident)
	}
	got := h2.Snapshot().Incidents
	if len(got) != 1 || got[0].MutedBy != "@leo" || got[0].MutedUntil.IsZero() || got[0].AckBy != "@ann" {
		t.Fatalf("mute/ack lost across the restart: %+v", got)
	}
}

func subscriptionHub(t *testing.T, url string) *Hub {
	t.Helper()
	cfg := &Config{DataDir: t.TempDir(), Baseline: []checks.Spec{},
		Probes: []ProbeConfig{{ID: "hub", Local: true}},
		Checks: []CheckConfig{{Spec: checks.Spec{ID: "canary", Type: "xray", Xray: &checks.XrayParams{Subscription: url}}}}}
	h, err := New(cfg, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go h.Run(ctx)
	return h
}

// One failed subscription fetch after a restart (the panel restarting) used
// to leave all its configs unmonitored until the next 10 min refresh. The
// fetch is retried soon, and a single failure is no alert.
func TestSubscriptionRetriedAfterFailedFetch(t *testing.T) {
	var n atomic.Int32
	sub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			http.Error(w, "panel restarting", 502)
			return
		}
		io.WriteString(w, "vless://b831381d-6324-4d53-ad4f-8cda48b30811@127.0.0.1:1?type=tcp#node\n")
	}))
	defer sub.Close()
	h := subscriptionHub(t, sub.URL)
	if !eventually(20*time.Second, func() bool { return len(h.Snapshot().Checks) > 0 }) {
		t.Fatalf("no config of the subscription is monitored after %d fetches", n.Load())
	}
	for _, inc := range h.Snapshot().Incidents {
		if inc.Key == "sub:canary" {
			t.Fatalf("one failed fetch raised an alert: %+v", inc)
		}
	}
}

// A malformed line in a subscription used to panic in the watcher and take
// the hub down; now it costs only that line.
func TestSubscriptionWithBadLine(t *testing.T) {
	sub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "vless://b831381d-6324-4d53-ad4f-8cda48b30811@ok.example.com:443?type=tcp#ok\n"+
			"vless://b831381d-6324-4d53-ad4f-8cda48b30811@bad.example.com:?type=tcp#bad\n")
	}))
	defer sub.Close()
	h := subscriptionHub(t, sub.URL)
	if !eventually(20*time.Second, func() bool { return len(h.Snapshot().Checks) > 0 }) {
		t.Fatal("the subscription was never expanded")
	}
	if st := h.Snapshot(); len(st.Checks) != 1 || st.Checks[0].Name != "ok" {
		t.Fatalf("checks: %+v", st.Checks)
	}
}

// The UI reads incidents.length: nothing open must be [], not null.
func TestSnapshotEmptyListsAreArrays(t *testing.T) {
	h, err := New(&Config{DataDir: t.TempDir(), Baseline: []checks.Spec{}}, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(h.Snapshot())
	for _, k := range []string{`"probes":[]`, `"checks":[]`, `"incidents":[]`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("snapshot %s: want %s", b, k)
		}
	}
}

// An agent only reports metrics until probing is switched on: it gets no
// checks, counts in no verdict, and turning it off forgets what it reported.
// A check that names a node runs there with probing off, and only that check:
// the node's other checks and the baseline stay away.
func TestCheckNamesAgentWithoutProbing(t *testing.T) {
	cfg := &Config{DataDir: t.TempDir(), Baseline: []checks.Spec{{Type: "tcp", TCP: &checks.TCPParams{Address: "77.88.8.8:53"}}},
		Probes: []ProbeConfig{{ID: "hub", Local: true}, {ID: "node", Token: "token-node-0123456789", Labels: map[string]string{"country": "de"}}},
		Checks: []CheckConfig{
			{Spec: checks.Spec{ID: "named", Type: "tcp", TCP: &checks.TCPParams{Address: "10.0.0.1:443"}}, Probes: []string{"id=node"}},
			{Spec: checks.Spec{ID: "named-excluded", Type: "tcp", TCP: &checks.TCPParams{Address: "10.0.0.1:443"}}, Probes: []string{"id=node"}, Exclude: []string{"country=de"}},
			{Spec: checks.Spec{ID: "by-country", Type: "tcp", TCP: &checks.TCPParams{Address: "10.0.0.2:443"}}, Probes: []string{"country=de"}},
			{Spec: checks.Spec{ID: "everywhere", Type: "tcp", TCP: &checks.TCPParams{Address: "10.0.0.3:443"}}},
		}}
	h, err := New(cfg, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	a := h.assignmentFor(h.probes["node"])
	h.mu.Unlock()
	if len(a.Checks) != 1 || a.Checks[0].ID != "named" {
		t.Fatalf("the node must run only the check that names it, got %+v", a.Checks)
	}
	h.mu.Lock()
	h.probes["node"].online = true
	h.mu.Unlock()
	h.Ingest("node", []checks.Result{res("named", true)})
	for _, c := range h.Snapshot().Checks {
		if c.ID != "named" {
			continue
		}
		if len(c.Probes) != 1 || c.Probes[0].Probe != "node" || c.Probes[0].Last == nil {
			t.Fatalf("the named check must count this node's results: %+v", c.Probes)
		}
	}
}

// A passive check waits for something to reach the hub. On an agent it would
// report silence for ever, so even naming the agent must not send it there.
func TestPassiveChecksRunOnTheHubOnly(t *testing.T) {
	cfg := &Config{DataDir: t.TempDir(), Baseline: []checks.Spec{},
		Probes: []ProbeConfig{{ID: "hub", Local: true}, {ID: "node", Token: "token-node-0123456789", Probing: true}},
		Checks: []CheckConfig{
			{Spec: checks.Spec{ID: "cron", Type: "push", Params: map[string]string{"token": "tok-1234567890"}}, Probes: []string{"id=node"}},
			{Spec: checks.Spec{ID: "by-hand", Type: "manual", Params: map[string]string{"status": "up"}}},
		}}
	h, err := New(cfg, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if a := h.assignmentFor(h.probes["node"]); len(a.Checks) != 0 {
		t.Fatalf("an agent got passive checks: %+v", a.Checks)
	}
	if a := h.assignmentFor(h.probes["hub"]); len(a.Checks) != 2 {
		t.Fatalf("the hub must run both, got %+v", a.Checks)
	}
}

func TestAgentProbesOnlyWhenEnabled(t *testing.T) {
	mk := func(probing bool) *Config {
		return &Config{DataDir: t.TempDir(), Baseline: []checks.Spec{{Type: "tcp", TCP: &checks.TCPParams{Address: "77.88.8.8:53"}}},
			Probes: []ProbeConfig{{ID: "hub", Local: true}, {ID: "node", Token: "token-node-0123456789", Probing: probing}},
			Checks: []CheckConfig{{Spec: checks.Spec{ID: "c", Type: "tcp", TCP: &checks.TCPParams{Address: "10.0.0.1:443"}}}}}
	}
	assigned := func(h *Hub, probe string) int {
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.assignmentFor(h.probes[probe]).Checks)
	}
	h, err := New(mk(false), quietLog())
	if err != nil {
		t.Fatal(err)
	}
	if n := assigned(h, "node"); n != 0 {
		t.Fatalf("an agent without probing got %d checks", n)
	}
	if n := assigned(h, "hub"); n != 2 {
		t.Fatalf("the hub itself must run the check and the baseline, got %d", n)
	}
	rec := &recorder{}
	h.AddNotifier(rec)
	h.mu.Lock()
	h.probes["node"].online = true
	// Run is not called, so the hub's own probe never starts: make it one
	// long offline, not one still expected after a restart.
	h.probes["hub"].since = time.Now().Add(-time.Hour)
	h.mu.Unlock()
	h.Ingest("node", []checks.Result{res("c", false), res("c", false)})
	if len(h.Snapshot().Incidents) != 0 {
		t.Fatal("results of an agent without probing must not count")
	}

	if err := h.Apply(mk(true)); err != nil {
		t.Fatal(err)
	}
	if n := assigned(h, "node"); n != 2 {
		t.Fatalf("probing on: got %d checks", n)
	}
	h.Ingest("node", []checks.Result{res("c", false), res("c", false)})
	if len(h.Snapshot().Incidents) != 1 {
		t.Fatal("expected an incident from the probing agent")
	}
	if err := h.Apply(mk(false)); err != nil {
		t.Fatal(err)
	}
	if n := assigned(h, "node"); n != 0 {
		t.Fatalf("probing off: got %d checks", n)
	}
	// The hub still runs the check: its success is the recovery.
	h.mu.Lock()
	h.probes["hub"].online = true
	h.mu.Unlock()
	h.Ingest("hub", []checks.Result{res("c", true)})
	if len(h.Snapshot().Incidents) != 0 {
		t.Fatalf("switching probing off must not leave its incident open: %+v", h.Snapshot().Incidents)
	}

	// A check only that agent ran: nobody is left to confirm a recovery, so the
	// incident closes instead of hanging forever.
	only := func(probing bool) *Config {
		c := mk(probing)
		c.Probes[1].Labels = map[string]string{"kind": "edge"}
		c.Checks[0].Probes = []string{"kind=edge"}
		return c
	}
	if err := h.Apply(only(true)); err != nil {
		t.Fatal(err)
	}
	h.Ingest("node", []checks.Result{res("c", false), res("c", false)})
	if len(h.Snapshot().Incidents) != 1 {
		t.Fatal("expected an incident from the probing agent")
	}
	if err := h.Apply(only(false)); err != nil {
		t.Fatal(err)
	}
	if st := h.Snapshot(); len(st.Incidents) != 0 {
		t.Fatalf("incident of a check nobody runs stayed open: %+v", st.Incidents)
	}
}

// feedHost runs one snapshot of probe pid through the threshold rules at a given time.
func feedHost(h *Hub, pid string, at time.Time, m proto.Metrics) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.evalHost(h.probes[pid], m, at)
}

func cpuAt(v float64) proto.Metrics {
	return proto.Metrics{CPU: v, MemUsed: 1, MemTotal: 10, DiskUsed: 1, DiskTotal: 10}
}

func countEvents(rec *recorder, typ model.EventType, key string) (n int) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, e := range rec.events {
		if e.Type == typ && e.Incident.Key == key {
			n++
		}
	}
	return n
}

// A spike, or a run of spikes shorter than the window, is not an alert; a
// sustained run is one alert; hovering at the line neither resolves nor
// re-alerts; staying under the recovery line resolves it with the peak.
func TestHostThresholdNeedsSustainedRun(t *testing.T) {
	h, rec := testHub(t)
	const key = "host:ru-a:cpu"
	t0 := time.Now().Add(-time.Hour)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	for _, s := range []struct {
		min int
		cpu float64
	}{{0, 95}, {2, 96}, {4, 50}, {5, 97}, {8, 98}} { // runs of 4 and 3 minutes
		feedHost(h, "ru-a", at(s.min), cpuAt(s.cpu))
	}
	if rec.find(model.EventOpen, key) != nil {
		t.Fatal("runs shorter than 5 minutes must not alert")
	}
	for m := 10; m <= 16; m++ {
		feedHost(h, "ru-a", at(m), cpuAt(float64(91+m)))
	}
	ev := rec.find(model.EventOpen, key)
	if ev == nil || ev.Incident.Kind != model.KindHost || ev.Incident.Severity != model.Warning || ev.Incident.Host == nil || ev.Incident.Host.Above != 90 {
		t.Fatalf("a 5-minute run must alert once: %+v", ev)
	}
	feedHost(h, "ru-a", at(17), cpuAt(88)) // inside the hysteresis band
	feedHost(h, "ru-a", at(19), cpuAt(89))
	if rec.find(model.EventResolve, key) != nil {
		t.Fatal("a value between the recovery line and the threshold must not resolve")
	}
	feedHost(h, "ru-a", at(20), cpuAt(40))
	feedHost(h, "ru-a", at(21), cpuAt(40))
	if rec.find(model.EventResolve, key) != nil {
		t.Fatal("one minute under the line is not a recovery yet")
	}
	feedHost(h, "ru-a", at(22), cpuAt(40))
	res := rec.find(model.EventResolve, key)
	if res == nil || res.Incident.Host == nil || res.Incident.Host.Peak != 107 {
		t.Fatalf("two minutes under the line resolve, keeping the peak: %+v", res)
	}
	if n := countEvents(rec, model.EventOpen, key); n != 1 {
		t.Fatalf("one sustained run is one alert, got %d", n)
	}
}

// Old agents send no steal or iowait: no alert, and no false "recovered".
func TestHostOldAgentHasNoDetail(t *testing.T) {
	h, rec := testHub(t)
	t0 := time.Now().Add(-time.Hour)
	for m := 0; m < 30; m++ {
		feedHost(h, "ru-a", t0.Add(time.Duration(m)*time.Minute), cpuAt(10))
	}
	for _, metric := range []string{"steal", "iowait", "swap", "load"} {
		if ev := rec.find(model.EventOpen, "host:ru-a:"+metric); ev != nil {
			t.Fatalf("%s alerted without data", metric)
		}
	}
	m := cpuAt(10)
	m.Ext = &proto.HostExt{CPUSteal: 35}
	for i := 0; i <= 10; i++ {
		feedHost(h, "ru-a", t0.Add(time.Duration(40+i)*time.Minute), m)
	}
	if rec.find(model.EventOpen, "host:ru-a:steal") == nil {
		t.Fatal("10 minutes of 35% steal must alert")
	}
}

// Load is per core: 7 on 4 cores is over 1.5, 7 on 8 cores is not.
func TestHostLoadPerCore(t *testing.T) {
	h, rec := testHub(t)
	h.mu.Lock()
	h.probes["ru-a"].sysinfo = &proto.SysInfo{Cores: 4}
	h.probes["ru-b"].sysinfo = &proto.SysInfo{Cores: 8}
	h.mu.Unlock()
	t0 := time.Now().Add(-time.Hour)
	m := cpuAt(10)
	m.Load1 = 7
	for i := 0; i <= 10; i++ {
		feedHost(h, "ru-a", t0.Add(time.Duration(i)*time.Minute), m)
		feedHost(h, "ru-b", t0.Add(time.Duration(i)*time.Minute), m)
	}
	if rec.find(model.EventOpen, "host:ru-a:load") == nil || rec.find(model.EventOpen, "host:ru-b:load") != nil {
		t.Fatal("load must be judged per core")
	}
}

// Switching a rule off closes its open incidents; overrides replace defaults by metric.
func TestHostRuleOffResolves(t *testing.T) {
	h, rec := testHub(t)
	full := proto.Metrics{CPU: 1, MemUsed: 1, MemTotal: 10, DiskUsed: 99, DiskTotal: 100}
	t0 := time.Now().Add(-time.Hour)
	feedHost(h, "de", t0, full)
	feedHost(h, "de", t0.Add(time.Minute), full)
	if rec.find(model.EventOpen, "host:de:disk") == nil {
		t.Fatal("a full disk for a minute must alert")
	}
	h.mu.Lock()
	h.cfg.Alerts.Host = []HostRule{{Metric: "disk", Off: true}}
	h.rebuild()
	h.mu.Unlock()
	ev := rec.find(model.EventResolve, "host:de:disk")
	if ev == nil || ev.Incident.Note != "removed" {
		t.Fatalf("turning the rule off must close the incident: %+v", ev)
	}
	feedHost(h, "de", t0.Add(3*time.Minute), full)
	if countEvents(rec, model.EventOpen, "host:de:disk") != 1 {
		t.Fatal("an off rule must not alert again")
	}
	rules := (&Config{Alerts: Alerts{Host: []HostRule{{Metric: "cpu", Above: 50, For: checks.Duration(time.Minute)}}}}).HostRules()
	if len(rules) != len(HostMetrics) || rules[0].Above != 50 || rules[1].Above != 90 {
		t.Fatalf("overrides: %+v", rules)
	}
}

func TestHostRulesValidate(t *testing.T) {
	for _, bad := range [][]HostRule{
		{{Metric: "temp", Above: 80}},
		{{Metric: "cpu", Above: 0}},
		{{Metric: "cpu", Above: 120}},
		{{Metric: "cpu", Above: 80}, {Metric: "cpu", Above: 70}},
		{{Metric: "mem", Above: 80, For: checks.Duration(48 * time.Hour)}},
	} {
		if validateHostRules(bad) == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if err := validateHostRules([]HostRule{{Metric: "load", Above: 2}, {Metric: "disk", Off: true}}); err != nil {
		t.Fatal(err)
	}
}

// dropHub is a hub with the Remnawave drop alert on (defaults: 80%, 30 users, 1h).
func dropHub(t *testing.T) (*Hub, *recorder) {
	h, rec := testHub(t)
	h.mu.Lock()
	h.cfg.Remnawave = &Remnawave{URL: "https://panel.example", Token: "x"}
	h.mu.Unlock()
	return h, rec
}

// feedUsers polls every 30 seconds from t0; users(i) gives the nodes at poll i.
func feedUsers(h *Hub, t0 time.Time, from, to int, users func(i int) []NodeUsers) {
	for i := from; i < to; i++ {
		h.ObserveUsers(t0.Add(time.Duration(i)*30*time.Second), users(i))
	}
}

func node(id string, users int) NodeUsers {
	return NodeUsers{ID: id, Name: id, Country: "DE", Users: users, Connected: true}
}

// A node collapsing while the rest of the fleet stays is an alert once the
// cliff has held five minutes; half the users back for two polls resolves
// it, keeping the low.
func TestUsersDropAlertsAndRecovers(t *testing.T) {
	h, rec := dropHub(t)
	t0 := time.Now().Add(-3 * time.Hour)
	fleet := func(de int) []NodeUsers { return []NodeUsers{node("de", de), node("nl", 900), node("se", 700)} }
	feedUsers(h, t0, 0, 120, func(i int) []NodeUsers { return fleet(1000 + i%7) }) // an hour of normal
	feedUsers(h, t0, 120, 130, func(int) []NodeUsers { return fleet(2) })
	if rec.find(model.EventOpen, "rw:de") != nil {
		t.Fatal("a cliff of four and a half minutes is not enough")
	}
	feedUsers(h, t0, 130, 131, func(int) []NodeUsers { return fleet(1) })
	ev := rec.find(model.EventOpen, "rw:de")
	if ev == nil || ev.Incident.Kind != model.KindUsersDrop || ev.Incident.Severity != model.Critical || ev.Incident.Note != "" {
		t.Fatalf("a collapse must alert: %+v", ev)
	}
	if d := ev.Incident.Drop; d == nil || d.Before < 1000 || d.Now != 1 || !d.Connected {
		t.Fatalf("drop details: %+v", d)
	}
	feedUsers(h, t0, 131, 132, func(int) []NodeUsers { return fleet(600) })
	if rec.find(model.EventResolve, "rw:de") != nil {
		t.Fatal("one poll back is not a recovery")
	}
	feedUsers(h, t0, 132, 133, func(int) []NodeUsers { return fleet(640) })
	res := rec.find(model.EventResolve, "rw:de")
	if res == nil || res.Incident.Drop == nil || res.Incident.Drop.Low != 1 {
		t.Fatalf("recovery: %+v", res)
	}
}

// What happens on a real node: a ban does not empty a node in one poll; users
// drain over tens of minutes as clients reconnect elsewhere. The 45- and
// 90-minute slides from 800 to 39 while the fleet holds must alert; a
// 10-minute baseline missed them, as it missed a real ban.
func TestUsersDropCatchesASlowDrain(t *testing.T) {
	h, rec := dropHub(t)
	t0 := time.Now().Add(-3 * time.Hour)
	fleet := func(pl int) []NodeUsers { return []NodeUsers{node("pl", pl), node("swe", 880), node("de", 650)} }
	for _, mins := range []int{45, 90} {
		h, rec = dropHub(t)
		n := mins * 2
		feedUsers(h, t0, 0, 120, func(int) []NodeUsers { return fleet(800) })
		feedUsers(h, t0, 120, 120+n, func(i int) []NodeUsers { return fleet(800 - (i-120)*761/n) })
		feedUsers(h, t0, 120+n, 124+n, func(int) []NodeUsers { return fleet(39) })
		if rec.find(model.EventOpen, "rw:pl") == nil {
			t.Fatalf("a node drained over %d minutes while the fleet held must alert", mins)
		}
	}
}

// What must not alert: the evening (every node falls, one a bit more), a
// small node, a single bad poll, and a node without enough history.
func TestUsersDropIgnoresWhatIsNotACollapse(t *testing.T) {
	h, rec := dropHub(t)
	t0 := time.Now().Add(-5 * time.Hour)
	feedUsers(h, t0, 0, 120, func(int) []NodeUsers {
		return []NodeUsers{node("a", 1000), node("b", 1000), node("c", 1000), node("x", 1000), node("small", 25), node("blip", 800)}
	})
	feedUsers(h, t0, 120, 150, func(i int) []NodeUsers { // 10 minutes: everyone -70%, x -88%
		f := float64(min(i-120, 20)) / 20
		all := func(drop float64) int { return int(1000 * (1 - drop*f)) }
		blip := 800
		if i == 135 {
			blip = 0
		}
		return []NodeUsers{node("a", all(0.7)), node("b", all(0.7)), node("c", all(0.7)), node("x", all(0.88)), node("small", 0), node("blip", blip)}
	})
	h.mu.Lock() // x did cross the absolute line: only its share kept it quiet
	base, _ := median(h.users["x"].samples, t0.Add(149*30*time.Second), time.Hour)
	now := h.users["x"].samples[len(h.users["x"].samples)-1].users
	h.mu.Unlock()
	if float64(now) > float64(base)*0.2 {
		t.Fatalf("test setup: x %d vs usual %d does not cross the line", now, base)
	}
	for _, id := range []string{"a", "b", "c", "x", "small", "blip"} {
		if rec.find(model.EventOpen, "rw:"+id) != nil {
			t.Errorf("%s must not alert", id)
		}
	}
	h2, rec2 := dropHub(t) // a fresh hub with 20 minutes of history has no baseline yet
	feedUsers(h2, t0, 0, 44, func(i int) []NodeUsers {
		de := 1000
		if i >= 40 {
			de = 0
		}
		return []NodeUsers{node("de", de), node("nl", 900)}
	})
	if rec2.find(model.EventOpen, "rw:de") != nil {
		t.Fatal("no baseline, no alert")
	}
}

// After a restart the stored history is the baseline: a collapse right after
// boot is caught, not missed for half an hour.
func TestUsersDropSeededHistory(t *testing.T) {
	h, rec := dropHub(t)
	now := time.Now()
	var hist []UsersHistory
	for i := 120; i > 0; i-- {
		at := now.Add(-time.Duration(i) * 30 * time.Second)
		hist = append(hist, UsersHistory{"de", at, 1000}, UsersHistory{"nl", at, 900})
	}
	h.SeedUsers(hist)
	for i := range 11 { // the cliff holds five minutes
		h.ObserveUsers(now.Add(time.Duration(i)*30*time.Second), []NodeUsers{node("de", 3), node("nl", 900)})
	}
	if rec.find(model.EventOpen, "rw:de") == nil {
		t.Fatal("seeded history must make the first polls count")
	}
}

// A node that is most of the fleet cannot lose share; it is judged alone.
func TestUsersDropDominantNode(t *testing.T) {
	h, rec := dropHub(t)
	t0 := time.Now().Add(-3 * time.Hour)
	feedUsers(h, t0, 0, 120, func(int) []NodeUsers { return []NodeUsers{node("big", 1000), node("tiny", 40)} })
	feedUsers(h, t0, 120, 131, func(int) []NodeUsers { return []NodeUsers{node("big", 10), node("tiny", 40)} })
	if rec.find(model.EventOpen, "rw:big") == nil {
		t.Fatal("a dominant node collapsing must alert")
	}
}

// Most nodes collapsing together says "panel or network", not "this node is
// banned"; a node gone from the panel closes its incident; switching the
// alert off closes the rest.
func TestUsersDropMassAndRemoved(t *testing.T) {
	h, rec := dropHub(t)
	t0 := time.Now().Add(-3 * time.Hour)
	ids := []string{"a", "b", "c", "d"}
	all := func(n int) []NodeUsers {
		var out []NodeUsers
		for _, id := range ids {
			out = append(out, node(id, n))
		}
		return out
	}
	feedUsers(h, t0, 0, 120, func(int) []NodeUsers { return all(500) })
	feedUsers(h, t0, 120, 131, func(int) []NodeUsers { return all(3) })
	ev := rec.find(model.EventOpen, "rw:a")
	if ev == nil || ev.Incident.Note != "mass" {
		t.Fatalf("mass drop: %+v", ev)
	}
	feedUsers(h, t0, 131, 132, func(int) []NodeUsers { return all(3)[1:] }) // "a" deleted in the panel
	if res := rec.find(model.EventResolve, "rw:a"); res == nil || res.Incident.Note != "removed" {
		t.Fatalf("removed node: %+v", res)
	}
	h.mu.Lock()
	h.cfg.Remnawave.Drop = &UsersDrop{Off: true}
	h.rebuild()
	h.mu.Unlock()
	if rec.find(model.EventResolve, "rw:b") == nil {
		t.Fatal("switching the alert off closes its incidents")
	}
}

// lost is a node the panel cannot reach, as Remnawave reports it.
func lost(id string) NodeUsers {
	return NodeUsers{ID: id, Name: id, Country: "PL", Status: "Failed to get system stats"}
}

// A reboot: the panel shows zero users for a
// minute and a half while it still calls the node connected, then loses it,
// then the node is back and users return over a few minutes. That is not a
// users drop at any point. A short reboot is no alert at all; one that lasts
// is "node down", closed when the node is back.
func TestUsersDropNotForAReboot(t *testing.T) {
	t0 := time.Now().Add(-3 * time.Hour)
	run := func(downPolls int) *recorder {
		h, rec := dropHub(t)
		fleet := func(pl NodeUsers) []NodeUsers { return []NodeUsers{pl, node("swe", 880), node("de", 650)} }
		feedUsers(h, t0, 0, 120, func(int) []NodeUsers { return fleet(node("pl", 470)) })
		feedUsers(h, t0, 120, 123, func(int) []NodeUsers { return fleet(node("pl", 0)) })
		end := 123 + downPolls
		feedUsers(h, t0, 123, end, func(int) []NodeUsers { return fleet(lost("pl")) })
		feedUsers(h, t0, end, end+12, func(i int) []NodeUsers { // users come back over five minutes
			n := node("pl", min(470, (i-end)*45))
			n.Uptime = time.Duration(i-end) * 30 * time.Second
			return fleet(n)
		})
		feedUsers(h, t0, end+12, end+40, func(int) []NodeUsers { return fleet(node("pl", 470)) })
		for _, ev := range rec.events {
			if ev.Incident.Kind == model.KindUsersDrop || ev.Incident.Worst == model.KindUsersDrop {
				t.Fatalf("a reboot of %d polls raised a users drop: %+v", downPolls, ev.Incident)
			}
		}
		return rec
	}
	if rec := run(4); len(rec.events) != 0 {
		t.Fatalf("a two-minute reboot is no alert: %+v", rec.events)
	}
	rec := run(10)
	open := rec.find(model.EventOpen, "rw:pl")
	if open == nil || open.Incident.Kind != model.KindNodeDown || open.Incident.Severity != model.Critical {
		t.Fatalf("a node lost for five minutes is down: %+v", open)
	}
	if d := open.Incident.Drop; d == nil || d.Before != 470 {
		t.Fatalf("it says how many were on it: %+v", d)
	}
	if l := open.Incident.Lines; len(l) != 1 || l[0].Reason != "node_down" || l[0].Error != "Failed to get system stats" {
		t.Fatalf("with the panel's reason: %+v", l)
	}
	if rec.find(model.EventResolve, "rw:pl") == nil {
		t.Fatal("back on the panel closes it")
	}
}

// A node emptied in one poll and back within two minutes, while the panel
// still calls it connected, is the panel or xray hiccuping, not a ban. The
// same cliff that holds for five minutes is a ban.
func TestUsersDropCliffMustHold(t *testing.T) {
	t0 := time.Now().Add(-3 * time.Hour)
	fleet := func(pl int) []NodeUsers { return []NodeUsers{node("pl", pl), node("swe", 880), node("de", 650)} }
	h, rec := dropHub(t)
	feedUsers(h, t0, 0, 120, func(int) []NodeUsers { return fleet(450) })
	for blip := range 4 { // as seen on a real node: eight such falls in an evening
		at := 120 + blip*20
		feedUsers(h, t0, at, at+4, func(int) []NodeUsers { return fleet(0) })
		feedUsers(h, t0, at+4, at+20, func(int) []NodeUsers { return fleet(440) })
	}
	if len(rec.events) != 0 {
		t.Fatalf("two-minute falls to zero are not bans: %+v", rec.events[0].Incident)
	}
	feedUsers(h, t0, 200, 209, func(int) []NodeUsers { return fleet(0) })
	if rec.find(model.EventOpen, "rw:pl") != nil {
		t.Fatal("alerted before the cliff held five minutes")
	}
	feedUsers(h, t0, 209, 212, func(int) []NodeUsers { return fleet(0) })
	if ev := rec.find(model.EventOpen, "rw:pl"); ev == nil || ev.Incident.Kind != model.KindUsersDrop {
		t.Fatalf("a cliff that holds is a ban: %+v", ev)
	}
}

// Right after xray restarts, clients are still reconnecting: not judged.
func TestUsersDropQuietAfterXrayRestart(t *testing.T) {
	h, rec := dropHub(t)
	t0 := time.Now().Add(-3 * time.Hour)
	fleet := func(pl NodeUsers) []NodeUsers { return []NodeUsers{pl, node("swe", 880), node("de", 650)} }
	feedUsers(h, t0, 0, 120, func(int) []NodeUsers { return fleet(node("pl", 450)) })
	feedUsers(h, t0, 120, 128, func(i int) []NodeUsers {
		n := node("pl", 20)
		n.Uptime = time.Duration(i-119) * 30 * time.Second
		return fleet(n)
	})
	if len(rec.events) != 0 {
		t.Fatalf("a fresh xray is not a ban: %+v", rec.events[0].Incident)
	}
}

// A server with an agent of the same name says it is down itself; the panel
// losing the node then adds nothing. One server down, one alert.
func TestNodeDownDefersToTheAgent(t *testing.T) {
	h, rec := dropHub(t)
	t0 := time.Now().Add(-3 * time.Hour)
	h.mu.Lock()
	h.probes["de"].online = false // the agent "de" is on the panel's node "DE (main)"
	h.mu.Unlock()
	nd := func(n NodeUsers) NodeUsers { n.Name = "DE (main)"; return n }
	fleet := func(de NodeUsers) []NodeUsers { return []NodeUsers{de, node("swe", 880), node("pl", 650)} }
	feedUsers(h, t0, 0, 120, func(int) []NodeUsers { return fleet(nd(node("de", 470))) })
	feedUsers(h, t0, 120, 140, func(int) []NodeUsers { return fleet(nd(lost("de"))) })
	if rec.find(model.EventOpen, "rw:de") != nil {
		t.Fatal("the agent's alert already says this server is down")
	}
	h.mu.Lock()
	h.probes["de"].online = true // the server is up, only the panel lost the node
	h.mu.Unlock()
	feedUsers(h, t0, 140, 141, func(int) []NodeUsers { return fleet(nd(lost("de"))) })
	if ev := rec.find(model.EventOpen, "rw:de"); ev == nil || ev.Incident.Kind != model.KindNodeDown {
		t.Fatalf("with the agent up, the node down is news: %+v", ev)
	}
}

// A ban in progress, then the node is lost: the open drop becomes "node
// down" in place rather than a second alert; most nodes lost at once say so.
func TestNodeDownTakesOverAndMass(t *testing.T) {
	h, rec := dropHub(t)
	t0 := time.Now().Add(-3 * time.Hour)
	fleet := func(pl NodeUsers) []NodeUsers { return []NodeUsers{pl, node("swe", 880), node("de", 650)} }
	feedUsers(h, t0, 0, 120, func(int) []NodeUsers { return fleet(node("pl", 800)) })
	feedUsers(h, t0, 120, 240, func(i int) []NodeUsers { return fleet(node("pl", 800-(i-120)*6)) })
	feedUsers(h, t0, 240, 244, func(int) []NodeUsers { return fleet(node("pl", 40)) })
	open := rec.find(model.EventOpen, "rw:pl")
	if open == nil || open.Incident.Drop == nil {
		t.Fatal("the drain must alert")
	}
	feedUsers(h, t0, 244, 245, func(int) []NodeUsers { return fleet(lost("pl")) })
	up := rec.find(model.EventUpdate, "rw:pl")
	if up == nil || up.Incident.Kind != model.KindNodeDown || up.Incident.Drop.Before != open.Incident.Drop.Before {
		t.Fatalf("the drop turns into node down, keeping the baseline: %+v", up)
	}

	h, rec = dropHub(t)
	feedUsers(h, t0, 0, 120, func(int) []NodeUsers { return fleet(node("pl", 800)) })
	feedUsers(h, t0, 120, 130, func(int) []NodeUsers { return []NodeUsers{lost("pl"), lost("swe"), lost("de")} })
	for _, id := range []string{"pl", "swe", "de"} {
		if ev := rec.find(model.EventOpen, "rw:"+id); ev == nil || ev.Incident.Note != "mass" {
			t.Fatalf("%s: the panel losing every node is marked mass: %+v", id, ev)
		}
	}
}

func TestUsersDropRule(t *testing.T) {
	if r := (&Remnawave{}).DropRule(); r.Percent != 80 || r.MinUsers != 30 || r.Window.D() != time.Hour {
		t.Fatalf("defaults: %+v", r)
	}
	if r := (&Remnawave{Drop: &UsersDrop{Percent: 60}}).DropRule(); r.Percent != 60 || r.MinUsers != 30 {
		t.Fatalf("override: %+v", r)
	}
	for _, bad := range []*UsersDrop{{Percent: 5}, {Percent: 101}, {MinUsers: -1}, {Window: checks.Duration(5 * time.Minute)}, {Window: checks.Duration(7 * time.Hour)}} {
		if validateDrop(bad) == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

// After a hub restart every agent reconnects at once with backoff: two
// minutes without one is not an outage yet, four minutes is. And an agent
// with stored history is not "never connected" just because the hub restarted.
func TestRestartGivesAgentsTimeAndRemembersThem(t *testing.T) {
	h, rec := testHub(t)
	h.MarkSeen([]string{"de"})
	setBoot := func(ago time.Duration) {
		h.mu.Lock()
		h.started = time.Now().Add(-ago)
		for _, id := range []string{"de", "ru-a"} {
			p := h.probes[id]
			p.online, p.since, p.hello = false, h.started, proto.Hello{}
		}
		h.mu.Unlock()
	}
	setBoot(2 * time.Minute)
	h.tick()
	if rec.find(model.EventOpen, "probe:de") != nil || rec.find(model.EventOpen, "probe:ru-a") != nil {
		t.Fatal("two minutes after a restart is not an outage")
	}
	setBoot(4 * time.Minute)
	h.tick()
	de, ru := rec.find(model.EventOpen, "probe:de"), rec.find(model.EventOpen, "probe:ru-a")
	if de == nil || ru == nil {
		t.Fatal("four minutes after a restart is an outage")
	}
	if de.Incident.Note == "never" {
		t.Fatal("an agent with stored history must not be called never connected")
	}
	if ru.Incident.Note != "never" {
		t.Fatalf("an agent never seen is never connected: %q", ru.Incident.Note)
	}
}

// "Resolved" by hand closes the incident at once; while the check keeps
// failing nothing reopens; once it has recovered, the next failure is a new
// incident. The mark survives a restart with the mutes.
func TestResolveByHandStaysQuietUntilRecovered(t *testing.T) {
	h, rec := testHub(t)
	mem := &memRecorder{}
	h.SetRecorder(mem)
	fail := func() {
		for _, p := range []string{"ru-a", "ru-b", "de"} {
			h.Ingest(p, []checks.Result{res("node", false), res("node", false)})
		}
	}
	fail()
	ev := rec.find(model.EventOpen, "node")
	if ev == nil {
		t.Fatal("setup: no incident")
	}
	if err := h.Resolve(ev.Incident.ID, "@leo"); err != nil {
		t.Fatal(err)
	}
	closed := rec.find(model.EventResolve, "node")
	if closed == nil || closed.Incident.ResolvedBy != "@leo" || closed.Incident.Note != "manual" {
		t.Fatalf("manual resolve: %+v", closed)
	}
	if err := h.Resolve(ev.Incident.ID, "@leo"); err == nil {
		t.Fatal("a closed incident cannot be closed again")
	}
	fail()
	h.tick()
	if countEvents(rec, model.EventOpen, "node") != 1 {
		t.Fatal("still failing after a manual close must stay quiet")
	}

	// A restarted hub keeps the mark.
	h2, rec2 := testHub(t)
	mem.mu.Lock()
	saved := mem.mutes
	mem.mu.Unlock()
	h2.Restore(nil, saved)
	for _, p := range []string{"ru-a", "ru-b", "de"} {
		h2.Ingest(p, []checks.Result{res("node", false), res("node", false)})
	}
	if rec2.find(model.EventOpen, "node") != nil {
		t.Fatal("the manual close must survive a restart")
	}

	// Recovery lifts it: the next failure is news again.
	for _, p := range []string{"ru-a", "ru-b", "de"} {
		h.Ingest(p, []checks.Result{res("node", true)})
	}
	fail()
	if countEvents(rec, model.EventOpen, "node") != 2 {
		t.Fatal("after recovery a new failure must alert")
	}
}

// A check's tags travel with its incident, sorted, for routing and templates.
func TestTagsReachIncident(t *testing.T) {
	h, rec := testHub(t)
	h.mu.Lock()
	h.cfg.Checks[0].Tags = []string{"de", "critical"}
	if err := h.cfg.normalize(); err != nil {
		t.Fatal(err)
	}
	h.rebuild()
	h.mu.Unlock()
	for range 2 {
		for _, p := range []string{"ru-a", "ru-b", "de"} {
			h.Ingest(p, []checks.Result{res("_baseline/0", true), res("node", false)})
		}
	}
	ev := rec.find(model.EventOpen, "node")
	if ev == nil || !slices.Equal(ev.Incident.Tags, []string{"critical", "de"}) {
		t.Fatalf("incident tags: %+v", ev)
	}
}

// The configs of a subscription carry its tags, as they carry its group.
func TestSubscriptionConfigsInheritTags(t *testing.T) {
	sub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "vless://b831381d-6324-4d53-ad4f-8cda48b30811@ok.example.com:443?type=tcp#ok\n")
	}))
	defer sub.Close()
	cfg := &Config{DataDir: t.TempDir(), Baseline: []checks.Spec{},
		Probes: []ProbeConfig{{ID: "hub", Local: true}},
		Checks: []CheckConfig{{Spec: checks.Spec{ID: "canary", Type: "xray", Xray: &checks.XrayParams{Subscription: sub.URL}}, Tags: []string{"vless", "de"}}}}
	h, err := New(cfg, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.Run(ctx)
	if !eventually(20*time.Second, func() bool { return len(h.Snapshot().Checks) > 0 }) {
		t.Fatal("the subscription was never expanded")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, c := range h.checks {
		if c.cfg.parent == "canary" && !slices.Equal(c.cfg.Tags, []string{"de", "vless"}) {
			t.Fatalf("%s tags: %v", id, c.cfg.Tags)
		}
	}
}

// depsHub has a node "de-node-1" (panel uuid-de, de1.example.com) with a TCP
// check on its SSH port and an HTTPS service on it, plus an unrelated check.
func depsHub(t *testing.T, mod func(*Config)) *Hub {
	t.Helper()
	tcp := func(id, addr string) CheckConfig {
		return CheckConfig{Spec: checks.Spec{ID: id, Type: "tcp", TCP: &checks.TCPParams{Address: addr}}}
	}
	cfg := &Config{DataDir: t.TempDir(), Baseline: []checks.Spec{},
		Probes: []ProbeConfig{{ID: "hub", Local: true}},
		Checks: []CheckConfig{
			tcp("de-tcp", "de1.example.com:443"),
			tcp("de-ssh", "de1.example.com:22"),
			{Spec: checks.Spec{ID: "de-web", Type: "http", HTTP: &checks.HTTPParams{URL: "https://de1.example.com/"}}},
			tcp("other", "nl.example.com:443"),
		},
		Remnawave: &Remnawave{URL: "https://panel.example", Token: "x"}}
	if mod != nil {
		mod(cfg)
	}
	h, err := New(cfg, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	for _, p := range h.probes {
		p.online = true
	}
	h.rebuild()
	h.mu.Unlock()
	h.ObserveUsers(time.Now(), []NodeUsers{{ID: "uuid-de", Name: "de-node-1", Address: "de1.example.com", Connected: true, Users: 100}})
	return h
}

func parentsOf(h *Hub, id string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.checks[id].parents
}

// A service on a node depends on the node's reachability check and on the
// panel's view of the node, found by address.
func TestParentsAuto(t *testing.T) {
	h := depsHub(t, nil)
	if got := parentsOf(h, "de-web"); !slices.Equal(got, []string{"de-tcp", "rw:uuid-de"}) {
		t.Fatalf("de-web parents: %v", got)
	}
	if got := parentsOf(h, "other"); len(got) != 0 {
		t.Fatalf("other: %v", got)
	}
	// A reachability check is no parent of another on the same host.
	if got := parentsOf(h, "de-tcp"); slices.Contains(got, "de-web") || slices.Contains(got, "de-tcp") {
		t.Fatalf("de-tcp parents: %v", got)
	}
}

// A hand-set dependency replaces the automatic ones; "none" clears them.
func TestParentsManualWinsAndNone(t *testing.T) {
	h := depsHub(t, func(c *Config) {
		for i := range c.Checks {
			switch c.Checks[i].ID {
			case "de-web":
				c.Checks[i].DependsOn = []string{"node:De-Node-1"}
			case "other":
				c.Checks[i].DependsOn = []string{"none"}
			}
		}
	})
	if got := parentsOf(h, "de-web"); !slices.Equal(got, []string{"rw:uuid-de"}) {
		t.Fatalf("manual: %v", got)
	}
	if got := parentsOf(h, "other"); len(got) != 0 {
		t.Fatalf("none: %v", got)
	}
}

func TestDependsOnValidation(t *testing.T) {
	tcp := func(id string, deps ...string) CheckConfig {
		return CheckConfig{Spec: checks.Spec{ID: id, Type: "tcp", TCP: &checks.TCPParams{Address: "x:1"}}, DependsOn: deps}
	}
	for name, cs := range map[string][]CheckConfig{
		"dependency cycle a → b → a": {tcp("a", "b"), tcp("b", "a")},
		`unknown check "zz"`:         {tcp("a", "zz")},
		`"none" must be alone`:       {tcp("a", "none", "probe:x")},
	} {
		c := &Config{DataDir: t.TempDir(), Probes: []ProbeConfig{{ID: "hub", Local: true}}, Checks: cs}
		if err := c.normalize(); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// downHub is depsHub with five more services on the node, all on hub.
func downHub(t *testing.T, mod func(*Config)) (*Hub, *recorder) {
	t.Helper()
	h := depsHub(t, func(c *Config) {
		for i := 2; i <= 6; i++ {
			c.Checks = append(c.Checks, CheckConfig{Spec: checks.Spec{ID: fmt.Sprintf("de-web%d", i), Type: "http",
				HTTP: &checks.HTTPParams{URL: fmt.Sprintf("https://de1.example.com/%d", i)}}})
		}
		if mod != nil {
			mod(c)
		}
	})
	rec := &recorder{}
	h.AddNotifier(rec)
	return h, rec
}

var services = []string{"de-web", "de-web2", "de-web3", "de-web4", "de-web5", "de-web6"}

func fail(h *Hub, ids ...string) {
	for range 2 { // retries = 2
		var rs []checks.Result
		for _, id := range ids {
			rs = append(rs, res(id, false))
		}
		h.Ingest("hub", rs)
	}
}

func pass(h *Hub, ids ...string) {
	var rs []checks.Result
	for _, id := range ids {
		rs = append(rs, res(id, true))
	}
	h.Ingest("hub", rs)
}

// expire ends the wait of key's held-back incident and runs a tick.
func expire(h *Hub, key string) {
	h.mu.Lock()
	h.holdUntil[key] = time.Now().Add(-time.Second)
	h.mu.Unlock()
	h.tick()
}

// The node's reachability check is down: its six services fail too, and
// that is one alert, the node's, saying six were affected.
func TestNodeDownSilencesItsConfigs(t *testing.T) {
	h, rec := downHub(t, nil)
	fail(h, "de-tcp")
	if rec.find(model.EventOpen, "de-tcp") == nil {
		t.Fatal("the node's check must alert")
	}
	fail(h, services...)
	for _, id := range services {
		expire(h, id)
		if rec.find(model.EventOpen, id) != nil {
			t.Fatalf("%s alerted while its node is down", id)
		}
	}
	var last *model.Event
	for i := range rec.events {
		if ev := rec.events[i]; ev.Incident.Key == "de-tcp" && ev.Type == model.EventRefresh {
			last = &rec.events[i]
		}
	}
	if last == nil || len(last.Incident.Affected) != 6 || !last.Silent {
		t.Fatalf("the node's card must quietly list six affected: %+v", last)
	}
}

// A service that fails before its node is confirmed down waits, and stays
// quiet once the node is down.
func TestConfigFailingBeforeNodeWaits(t *testing.T) {
	h, rec := downHub(t, nil)
	fail(h, "de-web")
	h.tick()
	if rec.find(model.EventOpen, "de-web") != nil {
		t.Fatal("a service with a parent waits before alerting")
	}
	fail(h, "de-tcp")
	expire(h, "de-web")
	if rec.find(model.EventOpen, "de-web") != nil {
		t.Fatal("the node went down within the wait: the service stays quiet")
	}
}

// The node is fine and the service fails: it alerts once the wait is over.
func TestConfigFailingAloneAlertsAfterWait(t *testing.T) {
	h, rec := downHub(t, nil)
	fail(h, "de-web")
	pass(h, "de-tcp")
	h.tick()
	if rec.find(model.EventOpen, "de-web") != nil {
		t.Fatal("alerted before the wait")
	}
	h.mu.Lock()
	wait := time.Until(h.holdUntil["de-web"])
	h.mu.Unlock()
	if wait <= 0 || wait > 3*time.Minute {
		t.Fatalf("wait %v, want up to 3 min", wait)
	}
	expire(h, "de-web")
	if ev := rec.find(model.EventOpen, "de-web"); ev == nil || ev.Incident.CauseKey != "" {
		t.Fatalf("a service failing on a live node must alert: %+v", ev)
	}
}

// The node is back but the service is still down: now it is its own problem.
func TestNodeBackConfigStillDownAlerts(t *testing.T) {
	h, rec := downHub(t, nil)
	fail(h, "de-tcp")
	fail(h, "de-web")
	expire(h, "de-web")
	pass(h, "de-tcp")
	if rec.find(model.EventResolve, "de-tcp") == nil {
		t.Fatal("the node recovered")
	}
	h.tick()
	if rec.find(model.EventOpen, "de-web") != nil {
		t.Fatal("the service waits again after its node recovers")
	}
	expire(h, "de-web")
	if ev := rec.find(model.EventOpen, "de-web"); ev == nil || ev.Incident.CauseKey != "" {
		t.Fatalf("the service is still down on a live node: %+v", ev)
	}
}

// Two panel nodes behind one address: one of them down does not explain a
// service failing there.
func TestSharedAddressTwoParents(t *testing.T) {
	h, rec := downHub(t, func(c *Config) {
		c.Checks = slices.DeleteFunc(c.Checks, func(x CheckConfig) bool { return x.ID == "de-tcp" })
	})
	h.ObserveUsers(time.Now(), []NodeUsers{
		{ID: "a", Name: "de-a", Address: "de1.example.com", Connected: true, Users: 100},
		{ID: "b", Name: "de-b", Address: "de1.example.com", Connected: true, Users: 100},
	})
	if got := parentsOf(h, "de-web"); !slices.Equal(got, []string{"rw:a", "rw:b"}) {
		t.Fatalf("parents: %v", got)
	}
	h.mu.Lock()
	h.transition("rw:a", model.KindNodeDown, "", nil, meta{name: "de-a", typ: "users", severity: "critical"}, time.Now())
	h.mu.Unlock()
	fail(h, "de-web")
	expire(h, "de-web")
	if rec.find(model.EventOpen, "de-web") == nil {
		t.Fatal("one of two nodes down must not silence the service")
	}
}

// A service that recovers under its down node never said anything, so it
// does not announce a recovery either.
func TestSuppressedChildResolvesQuietly(t *testing.T) {
	h, rec := downHub(t, nil)
	fail(h, "de-tcp")
	fail(h, "de-web")
	expire(h, "de-web")
	pass(h, "de-web")
	if rec.find(model.EventResolve, "de-web") != nil || rec.find(model.EventOpen, "de-web") != nil {
		t.Fatal("a silenced service must stay silent")
	}
}

// A reachability check of another port says nothing about this one: SSH
// closed on a node must not silence its web service on 443.
func TestParentsOtherPortIsNoParent(t *testing.T) {
	h := depsHub(t, nil)
	if got := parentsOf(h, "de-web"); slices.Contains(got, "de-ssh") || !slices.Contains(got, "de-tcp") {
		t.Fatalf("de-web parents: %v", got)
	}
	h2, rec := downHub(t, nil)
	fail(h2, "de-ssh")
	fail(h2, "de-web")
	expire(h2, "de-web")
	if rec.find(model.EventOpen, "de-web") == nil {
		t.Fatal("SSH down must not silence the web service")
	}
}

// A panel node and its agent match by name, ignoring case and a "(…)" note.
func TestNodeKey(t *testing.T) {
	if NodeKey("NL-1 (Amsterdam)") != "nl-1" || NodeKey(" de-node ") != "de-node" || NodeKey("a") == NodeKey("b") {
		t.Fatal(NodeKey("NL-1 (Amsterdam)"))
	}
}

// A parent only blocked from some networks does not explain a service that
// is down from everywhere: Reality broken under a DPI-blocked port alerts.
func TestBlockedParentDoesNotExplainDownChild(t *testing.T) {
	h, rec := downHub(t, nil)
	h.mu.Lock()
	h.transition("de-tcp", model.KindBlocked, "ru", nil, meta{name: "de-tcp", typ: "tcp", severity: "critical"}, time.Now())
	h.mu.Unlock()
	fail(h, "de-web")
	expire(h, "de-web")
	if rec.find(model.EventOpen, "de-web") == nil {
		t.Fatal("a blocked parent must not silence a service that is down")
	}
}

// A parent open for hours does not explain a service that starts failing
// now: that is a new problem, not the old outage spreading.
func TestLongOpenParentDoesNotExplainNewFailure(t *testing.T) {
	h, rec := downHub(t, nil)
	h.mu.Lock()
	h.transition("de-tcp", model.KindDown, "", nil, meta{name: "de-tcp", typ: "tcp", severity: "critical"}, time.Now().Add(-6*time.Hour))
	h.mu.Unlock()
	fail(h, "de-web")
	expire(h, "de-web")
	if rec.find(model.EventOpen, "de-web") == nil {
		t.Fatal("a failure hours after the parent opened must alert")
	}
}

// A service folded into a blocked parent that then goes down from
// everywhere is no longer explained: it alerts and leaves the parent's list.
func TestFoldedChildEscalatesAlerts(t *testing.T) {
	h, rec := downHub(t, nil)
	now := time.Now()
	h.mu.Lock()
	h.transition("de-tcp", model.KindBlocked, "ru", nil, meta{name: "de-tcp", typ: "tcp", severity: "critical"}, now)
	h.transition("de-web", model.KindBlocked, "ru", nil, meta{name: "de-web", typ: "http", severity: "critical"}, now)
	cause := h.incidents["de-web"].CauseKey
	h.mu.Unlock()
	if cause != "de-tcp" {
		t.Fatalf("blocked under blocked folds: %q", cause)
	}
	h.mu.Lock()
	h.transition("de-web", model.KindDown, "", nil, meta{name: "de-web", typ: "http", severity: "critical"}, now.Add(time.Minute))
	affected := slices.Clone(h.incidents["de-tcp"].Affected)
	h.mu.Unlock()
	if slices.Contains(affected, "de-web") {
		t.Fatalf("an escalated service leaves its parent's list: %v", affected)
	}
	expire(h, "de-web")
	if rec.find(model.EventOpen, "de-web") == nil {
		t.Fatal("the escalation must alert")
	}
}

// The panel has lost the node but not yet for the 3 minutes node_down
// needs: the node's services keep waiting for it, then fold into it.
func TestServicesWaitForPendingNodeDown(t *testing.T) {
	h, rec := downHub(t, func(c *Config) {
		c.Checks = slices.DeleteFunc(c.Checks, func(x CheckConfig) bool { return x.ID == "de-tcp" })
	})
	start := time.Now()
	h.ObserveUsers(start, []NodeUsers{{ID: "uuid-de", Name: "de-node-1", Address: "de1.example.com", Connected: false}})
	fail(h, "de-web")
	expire(h, "de-web")
	if rec.find(model.EventOpen, "de-web") != nil {
		t.Fatal("the panel lost the node: its services wait for node_down")
	}
	h.ObserveUsers(start.Add(nodeDownAfter+time.Second), []NodeUsers{{ID: "uuid-de", Name: "de-node-1", Address: "de1.example.com", Connected: false}})
	expire(h, "de-web")
	if rec.find(model.EventOpen, "de-web") != nil || rec.find(model.EventOpen, "rw:uuid-de") == nil {
		t.Fatal("one alert, the node's")
	}
}

// After a restart the panel's nodes are not known until its first poll:
// services that could depend on a node wait for it.
func TestRestartWaitsForPanelNodes(t *testing.T) {
	cfg := &Config{DataDir: t.TempDir(), Baseline: []checks.Spec{}, Probes: []ProbeConfig{{ID: "hub", Local: true}},
		Checks:    []CheckConfig{{Spec: checks.Spec{ID: "de-web", Type: "http", HTTP: &checks.HTTPParams{URL: "https://de1.example.com/"}}}},
		Remnawave: &Remnawave{URL: "https://panel.example", Token: "x"}}
	h, err := New(cfg, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	for _, p := range h.probes {
		p.online = true
	}
	h.rebuild()
	h.mu.Unlock()
	rec := &recorder{}
	h.AddNotifier(rec)
	fail(h, "de-web")
	expire(h, "de-web")
	if rec.find(model.EventOpen, "de-web") != nil {
		t.Fatal("before the panel's first poll the service waits")
	}
	h.ObserveUsers(time.Now(), []NodeUsers{{ID: "uuid-de", Name: "de-node-1", Address: "de1.example.com", Connected: true, Users: 10}})
	expire(h, "de-web")
	if rec.find(model.EventOpen, "de-web") == nil {
		t.Fatal("the node is up: the service alerts")
	}
}

// After a hub restart agents reconnect one by one. The first one back with
// a failure of its own (DPI at one Russian ISP) is not "down from
// everywhere": the probes that have not reported yet are still expected,
// and the verdict waits for them. Once they report it is the partial it is;
// a probe that never comes back stops being waited for after the grace.
func TestRestartFirstProbeBackIsNotDown(t *testing.T) {
	h, rec := testHub(t)
	h.mu.Lock()
	for _, id := range []string{"ru-b", "de"} {
		h.probes[id].online = false // not reconnected yet
	}
	h.mu.Unlock()
	h.Ingest("ru-a", []checks.Result{res("node", false)})
	h.Ingest("ru-a", []checks.Result{res("node", false)})
	h.tick()
	for _, ev := range rec.events {
		if ev.Incident.Key == "node" {
			t.Fatalf("one probe back after a restart decided the verdict: %s %s", ev.Type, ev.Incident.Kind)
		}
	}
	h.mu.Lock()
	h.probes["ru-b"].online, h.probes["de"].online = true, true
	h.mu.Unlock()
	h.Ingest("ru-b", []checks.Result{res("node", true)})
	h.Ingest("de", []checks.Result{res("node", true)})
	h.mu.Lock()
	kind := model.KindOK
	if inc := h.incidents["node"]; inc != nil {
		kind = inc.Kind
	}
	h.mu.Unlock()
	if kind != model.KindPartial {
		t.Fatalf("with every probe back it is a partial outage, got %s", kind)
	}
	if ev := rec.find(model.EventOpen, "node"); ev != nil && ev.Incident.Kind != model.KindPartial {
		t.Fatalf("announced as %s", ev.Incident.Kind)
	}

	// A probe that does not come back is not waited for forever.
	h2, rec2 := testHub(t)
	h2.mu.Lock()
	for _, id := range []string{"ru-b", "de"} {
		h2.probes[id].online = false
	}
	h2.mu.Unlock()
	h2.Ingest("ru-a", []checks.Result{res("node", false), res("node", false)})
	h2.mu.Lock()
	h2.started = time.Now().Add(-10 * time.Minute)
	for _, id := range []string{"ru-a", "ru-b", "de"} {
		h2.probes[id].since = h2.started
	}
	h2.mu.Unlock()
	h2.Ingest("ru-a", []checks.Result{res("node", false)})
	h2.tick()
	if rec2.find(model.EventOpen, "node") == nil {
		t.Fatal("after the grace the probes that never came back are offline, and the failure is reported")
	}
}

// A node_down closed by hand on a node too small for the drop rule (or one
// without history yet) is lifted when the node is back on the panel, so its
// next outage is news.
func TestNodeDownClosedByHandLiftedWhenBack(t *testing.T) {
	h, rec := dropHub(t)
	t0 := time.Now().Add(-3 * time.Hour)
	fleet := func(pl NodeUsers) []NodeUsers { return []NodeUsers{pl, node("swe", 880), node("de", 650)} }
	feedUsers(h, t0, 0, 120, func(int) []NodeUsers { return fleet(node("pl", 10)) })
	feedUsers(h, t0, 120, 130, func(int) []NodeUsers { return fleet(lost("pl")) })
	ev := rec.find(model.EventOpen, "rw:pl")
	if ev == nil || ev.Incident.Kind != model.KindNodeDown {
		t.Fatalf("setup: %+v", ev)
	}
	if err := h.Resolve(ev.Incident.ID, "@leo"); err != nil {
		t.Fatal(err)
	}
	feedUsers(h, t0, 130, 200, func(int) []NodeUsers { return fleet(node("pl", 10)) })
	feedUsers(h, t0, 200, 220, func(int) []NodeUsers { return fleet(lost("pl")) })
	if n := countEvents(rec, model.EventOpen, "rw:pl"); n != 2 {
		t.Fatalf("the second node_down after the node was back was swallowed: opens=%d", n)
	}
}

// A parent partial for hours (one ISP) that now goes down from everywhere
// takes its services down with it: what counts is when it became down, not
// when its incident opened.
func TestParentDownAfterLongPartialFoldsChildren(t *testing.T) {
	h, rec := downHub(t, nil)
	h.mu.Lock()
	h.transition("de-tcp", model.KindPartial, "", nil, meta{name: "de-tcp", typ: "tcp"}, time.Now().Add(-3*time.Hour))
	h.incidents["de-tcp"].Notified = true
	h.transition("de-tcp", model.KindDown, "", []model.ProbeLine{{Probe: "hub"}}, meta{name: "de-tcp", typ: "tcp"}, time.Now())
	h.mu.Unlock()
	if up := rec.find(model.EventUpdate, "de-tcp"); up == nil || up.Incident.Kind != model.KindDown {
		t.Fatalf("setup: the parent escalates to down: %+v", up)
	}
	fail(h, services...)
	for _, id := range services {
		expire(h, id)
		if rec.find(model.EventOpen, id) != nil {
			t.Fatalf("%s alerted while its node is down", id)
		}
	}
	// And the other way: a parent down for hours still does not explain a
	// service that fails only now.
	h2, rec2 := downHub(t, nil)
	h2.mu.Lock()
	h2.transition("de-tcp", model.KindDown, "", nil, meta{name: "de-tcp", typ: "tcp"}, time.Now().Add(-3*time.Hour))
	h2.transition("de-tcp", model.KindBlocked, "ru", nil, meta{name: "de-tcp", typ: "tcp"}, time.Now().Add(-2*time.Hour))
	h2.transition("de-tcp", model.KindDown, "", nil, meta{name: "de-tcp", typ: "tcp"}, time.Now().Add(-time.Hour))
	h2.mu.Unlock()
	fail(h2, "de-web")
	expire(h2, "de-web")
	if rec2.find(model.EventOpen, "de-web") == nil {
		t.Fatal("a parent down for an hour does not explain a failure that starts now")
	}
}

// Closing the node's incident by hand ("I know, it's being reinstalled")
// does not release its services one by one while the node is still down.
// Once the node is back, they are judged on their own again.
func TestParentClosedByHandKeepsChildrenFolded(t *testing.T) {
	setup := func() (*Hub, *recorder) {
		h, rec := downHub(t, nil)
		fail(h, "de-tcp")
		fail(h, services...)
		for _, id := range services {
			expire(h, id)
		}
		p := rec.find(model.EventOpen, "de-tcp")
		if p == nil {
			t.Fatal("setup: the node alerts")
		}
		if err := h.Resolve(p.Incident.ID, "@leo"); err != nil {
			t.Fatal(err)
		}
		fail(h, "de-tcp")
		fail(h, services...)
		h.tick()
		for _, id := range services {
			expire(h, id)
			if rec.find(model.EventOpen, id) != nil {
				t.Fatalf("%s alerted: its node, closed by hand, is still down", id)
			}
		}
		return h, rec
	}

	// The node is back, one service is not: that one is news.
	h, rec := setup()
	pass(h, "de-tcp")
	pass(h, services[1:]...)
	fail(h, "de-web")
	h.tick() // no longer explained: it waits again, as any service would
	if rec.find(model.EventOpen, "de-web") != nil {
		t.Fatal("the service waits for its parent like any other")
	}
	expire(h, "de-web")
	if ev := rec.find(model.EventOpen, "de-web"); ev == nil || ev.Incident.CauseKey != "" {
		t.Fatalf("a service down on a live node must alert: %+v", ev)
	}
	for _, id := range services[1:] {
		if rec.find(model.EventOpen, id) != nil || rec.find(model.EventResolve, id) != nil {
			t.Fatalf("%s never alerted, so it does not announce a recovery either", id)
		}
	}

	// Everything recovers, then the node fails again: business as usual.
	h, rec = setup()
	pass(h, "de-tcp")
	pass(h, services...)
	fail(h, "de-tcp")
	if countEvents(rec, model.EventOpen, "de-tcp") != 2 {
		t.Fatal("the node recovered: its next outage alerts")
	}
	fail(h, services...)
	for _, id := range services {
		expire(h, id)
		if rec.find(model.EventOpen, id) != nil {
			t.Fatalf("%s alerted under its down node", id)
		}
	}
}

// The same for a node the panel lost: its node_down closed by hand keeps
// its configs quiet while the node stays lost.
func TestNodeDownClosedByHandKeepsConfigsFolded(t *testing.T) {
	h, rec := downHub(t, func(c *Config) {
		c.Checks = slices.DeleteFunc(c.Checks, func(x CheckConfig) bool { return x.ID == "de-tcp" })
	})
	lostDE := []NodeUsers{{ID: "uuid-de", Name: "de-node-1", Address: "de1.example.com", Connected: false}}
	// Lost 4.5 minutes ago: node_down is 1.5 minutes old, and the services'
	// wait for a pending node_down (lost + 4 min) is over.
	start := time.Now().Add(-nodeDownAfter - 90*time.Second)
	h.ObserveUsers(start, lostDE)
	h.ObserveUsers(start.Add(nodeDownAfter+time.Second), lostDE)
	nd := rec.find(model.EventOpen, "rw:uuid-de")
	if nd == nil {
		t.Fatal("setup: node_down")
	}
	fail(h, services...)
	if err := h.Resolve(nd.Incident.ID, "@leo"); err != nil {
		t.Fatal(err)
	}
	h.ObserveUsers(start.Add(nodeDownAfter+time.Minute), lostDE)
	for _, id := range services {
		expire(h, id)
		if rec.find(model.EventOpen, id) != nil {
			t.Fatalf("%s alerted: its node, closed by hand, is still lost", id)
		}
	}
	h.ObserveUsers(start.Add(nodeDownAfter+2*time.Minute), []NodeUsers{{ID: "uuid-de", Name: "de-node-1", Address: "de1.example.com", Connected: true, Users: 100}})
	h.tick()
	expire(h, "de-web")
	if rec.find(model.EventOpen, "de-web") == nil {
		t.Fatal("the node is back and the service is still down: news")
	}
}

// node_down yields to the agent's alert only when there is one that says
// the same: open, announced, not muted, and about this outage. An agent
// closed by hand, muted or offline since long before is no such alert.
func TestNodeDownYieldsOnlyToALiveAgentAlert(t *testing.T) {
	cases := map[string]func(h *Hub, inc *model.Incident){
		"closed by hand": func(h *Hub, inc *model.Incident) {
			if err := h.Resolve(inc.ID, "@leo"); err != nil {
				t.Fatal(err)
			}
		},
		"muted": func(h *Hub, inc *model.Incident) {
			if err := h.Mute(inc.ID, 24*time.Hour, "@leo"); err != nil {
				t.Fatal(err)
			}
		},
		"offline long before": func(h *Hub, inc *model.Incident) {
			h.mu.Lock()
			h.incidents[inc.Key].Started = time.Now().Add(-48 * time.Hour)
			h.mu.Unlock()
		},
	}
	for name, prepare := range cases {
		t.Run(name, func(t *testing.T) {
			h, rec := dropHub(t)
			h.mu.Lock()
			p := h.probes["de"]
			p.online, p.since = false, time.Now().Add(-48*time.Hour)
			h.mu.Unlock()
			h.tick()
			ev := rec.find(model.EventOpen, "probe:de")
			if ev == nil {
				t.Fatal("setup")
			}
			prepare(h, &ev.Incident)
			t0 := time.Now().Add(-3 * time.Hour)
			fleet := func(de NodeUsers) []NodeUsers { return []NodeUsers{de, node("swe", 880), node("nl", 650)} }
			feedUsers(h, t0, 0, 120, func(int) []NodeUsers { return fleet(node("de", 400)) })
			feedUsers(h, t0, 120, 160, func(int) []NodeUsers { return fleet(lost("de")) })
			if rec.find(model.EventOpen, "rw:de") == nil {
				t.Fatal("node_down swallowed: no live agent alert says the server is down")
			}
		})
	}
	t.Run("live agent alert", func(t *testing.T) {
		h, rec := dropHub(t)
		t0 := time.Now().Add(-90 * time.Minute)
		fleet := func(de NodeUsers) []NodeUsers { return []NodeUsers{de, node("swe", 880), node("nl", 650)} }
		feedUsers(h, t0, 0, 160, func(int) []NodeUsers { return fleet(node("de", 400)) })
		h.mu.Lock()
		p := h.probes["de"]
		p.online, p.since = false, time.Now().Add(-10*time.Minute)
		h.mu.Unlock()
		h.tick()
		if rec.find(model.EventOpen, "probe:de") == nil {
			t.Fatal("setup")
		}
		feedUsers(h, t0, 160, 180, func(int) []NodeUsers { return fleet(lost("de")) })
		if rec.find(model.EventOpen, "rw:de") != nil {
			t.Fatal("the agent's alert already says this server is down")
		}
	})
}

// The German probe that saw the node fine goes offline in the middle of a
// "blocked in ru": that is no evidence the node is now down from
// everywhere. The incident stays blocked; only DE failing makes it down.
func TestSilentOKWitnessDoesNotEscalate(t *testing.T) {
	h, rec := testHub(t)
	for range 2 {
		h.Ingest("ru-a", []checks.Result{res("node", false)})
		h.Ingest("ru-b", []checks.Result{res("node", false)})
		h.Ingest("de", []checks.Result{res("node", true)})
	}
	if ev := rec.find(model.EventOpen, "node"); ev == nil || ev.Incident.Kind != model.KindBlocked {
		t.Fatalf("setup %+v", ev)
	}
	h.mu.Lock()
	h.probes["de"].online = false
	h.evaluateAll(time.Now())
	h.mu.Unlock()
	h.mu.Lock()
	h.checks["node"].state["de"].at = time.Now().Add(-10 * time.Minute) // long silent
	h.mu.Unlock()
	h.Ingest("ru-a", []checks.Result{res("node", false)})
	if up := rec.find(model.EventUpdate, "node"); up != nil {
		t.Fatalf("an OK probe going silent changed the incident: %s escalated=%v", up.Incident.Kind, up.Escalated)
	}
	// DE back and failing: now it is down.
	h.mu.Lock()
	h.probes["de"].online = true
	h.mu.Unlock()
	h.Ingest("de", []checks.Result{res("node", false), res("node", false)})
	h.Ingest("ru-a", []checks.Result{res("node", false)})
	h.Ingest("ru-b", []checks.Result{res("node", false)})
	if up := rec.find(model.EventUpdate, "node"); up == nil || up.Incident.Kind != model.KindDown || !up.Escalated {
		t.Fatalf("DE failing escalates to down: %+v", up)
	}
}

// Configs of one subscription that share an endpoint (one CDN front, many
// SNIs) keep their ids when the panel reorders them, and never share one.
func TestSubscriptionSharedEndpointIDs(t *testing.T) {
	h, _ := testHub(t)
	parent := CheckConfig{Spec: checks.Spec{ID: "sub", Type: "xray", Xray: &checks.XrayParams{Subscription: "https://x/y"}}}
	e := func(name, sni string) xray.Entry {
		return xray.Entry{Name: name, Endpoint: "cdn.example:443", Outbound: map[string]any{"protocol": "vless", "sni": sni}}
	}
	ids := func(cs []CheckConfig) map[string]string {
		out := map[string]string{}
		for _, c := range cs {
			if prev, dup := out[c.ID]; dup {
				t.Fatalf("%s and %s share id %s", prev, c.Name, c.ID)
			}
			out[c.ID] = c.Name
		}
		return out
	}
	h.mu.Lock()
	ab := ids(h.expand(parent, []xray.Entry{e("A", "a"), e("B", "b")}, nil))
	ba := ids(h.expand(parent, []xray.Entry{e("B", "b"), e("A", "a")}, nil))
	same := h.expand(parent, []xray.Entry{e("DE", "a"), e("DE", "b"), e("DE", "c"), e("DE", "c")}, nil)
	h.mu.Unlock()
	if !maps.Equal(ab, ba) {
		t.Fatalf("ids depend on the order: %v vs %v", ab, ba)
	}
	ids(same)
	if len(same) != 4 {
		t.Fatalf("configs lost: %d", len(same))
	}
}

// A check id with ':' reads as a system key (probe:, rw:, host:): its
// incident would never count as orphaned, and "probe:x" would clash with a
// probe's. Config refuses it.
func TestCheckIDWithColonRejected(t *testing.T) {
	for _, id := range []string{"de1:443", "probe:de", "rw:x"} {
		c := &Config{DataDir: t.TempDir(), Probes: []ProbeConfig{{ID: "hub", Local: true}},
			Checks: []CheckConfig{{Spec: checks.Spec{ID: id, Type: "tcp", TCP: &checks.TCPParams{Address: "x:1"}}}}}
		if err := c.normalize(); err == nil || !strings.Contains(err.Error(), "':'") {
			t.Errorf("%s: %v", id, err)
		}
	}
}

// readerNotifier reads each event's incident in a goroutine, as the
// Telegram and webhook notifiers do.
type readerNotifier struct{ wg sync.WaitGroup }

func (r *readerNotifier) Notify(ev model.Event) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		for range 200 {
			json.Marshal(ev.Incident)
		}
	}()
}

// What the hub hands out (events, the recorder, snapshots) is a copy: a
// child leaving its parent's list must not rewrite a card already sent, a
// row being written or a snapshot being encoded (a data race under -race).
func TestIncidentCopiesDoNotShareSlices(t *testing.T) {
	h, rec := downHub(t, nil)
	mem := &memRecorder{}
	h.SetRecorder(mem)
	rd := &readerNotifier{}
	h.AddNotifier(rd)
	fail(h, "de-tcp")
	fail(h, services...)
	for _, id := range services {
		expire(h, id)
	}
	var sent *model.Event
	for i := range rec.events {
		if ev := rec.events[i]; ev.Incident.Key == "de-tcp" && ev.Type == model.EventRefresh {
			sent = &rec.events[i]
		}
	}
	if sent == nil || len(sent.Incident.Affected) != 6 {
		t.Fatalf("setup: %+v", sent)
	}
	before := slices.Clone(sent.Incident.Affected)
	h.mu.Lock()
	parentID := h.incidents["de-tcp"].ID
	h.mu.Unlock()
	mem.mu.Lock()
	stored := slices.Clone(mem.incidents[parentID].Affected)
	storedRef := mem.incidents[parentID].Affected
	mem.mu.Unlock()
	snap := h.Snapshot()
	var snapAffected []string
	for _, inc := range snap.Incidents {
		if inc.Key == "de-tcp" {
			snapAffected = inc.Affected
		}
	}
	snapBefore := slices.Clone(snapAffected)

	h.mu.Lock()
	for _, id := range services[:3] {
		h.detachCause(h.incidents[id])
	}
	h.mu.Unlock()
	rd.wg.Wait()
	if !slices.Equal(sent.Incident.Affected, before) {
		t.Fatalf("a sent event changed under the notifier: %v, was %v", sent.Incident.Affected, before)
	}
	if !slices.Equal(storedRef, stored) {
		t.Fatalf("a persisted copy changed: %v, was %v", storedRef, stored)
	}
	if !slices.Equal(snapAffected, snapBefore) {
		t.Fatalf("a snapshot changed: %v, was %v", snapAffected, snapBefore)
	}
}

// A probe without internet that was offline for a while reconnects: its
// baseline results from before are no news either way. "No internet" stays
// until a fresh baseline says otherwise, rather than resolving on the next
// tick and reopening with the first fresh failure.
func TestProbeNetDoesNotFlapOnReconnect(t *testing.T) {
	h, rec := testHub(t)
	for range 2 {
		h.Ingest("ru-a", []checks.Result{res("_baseline/0", false)})
	}
	if rec.find(model.EventOpen, "probe-net:ru-a") == nil {
		t.Fatal("setup")
	}
	h.mu.Lock()
	for _, ch := range h.checks {
		if st := ch.state["ru-a"]; st != nil {
			st.at = st.at.Add(-10 * time.Minute)
		}
	}
	h.probes["ru-a"].since = time.Now() // reconnected after 10 minutes away
	h.mu.Unlock()
	h.tick()
	if rec.find(model.EventResolve, "probe-net:ru-a") != nil {
		t.Fatal("probe-net resolved without a single fresh baseline result")
	}
	for range 2 {
		h.Ingest("ru-a", []checks.Result{res("_baseline/0", false)})
	}
	if n := countEvents(rec, model.EventOpen, "probe-net:ru-a"); n != 1 {
		t.Fatalf("probe-net flapped: %d opens", n)
	}
	h.Ingest("ru-a", []checks.Result{res("_baseline/0", true)})
	if rec.find(model.EventResolve, "probe-net:ru-a") == nil {
		t.Fatal("a fresh good baseline resolves it")
	}
}

// Severity changed in the config applies to the open incident: critical
// to warning is a quiet update, warning to critical an escalation.
func TestSeverityChangeAppliesToOpenIncident(t *testing.T) {
	h, rec := testHub(t)
	failAll := func() {
		for _, p := range []string{"ru-a", "ru-b", "de"} {
			h.Ingest(p, []checks.Result{res("node", false), res("node", false)})
		}
	}
	setSeverity := func(s string) {
		h.mu.Lock()
		h.cfg.Checks[0].Severity = s
		h.rebuild()
		h.mu.Unlock()
	}
	failAll()
	if ev := rec.find(model.EventOpen, "node"); ev == nil || ev.Incident.Severity != model.Critical {
		t.Fatalf("setup: %+v", ev)
	}
	setSeverity("warning")
	failAll()
	up := rec.find(model.EventUpdate, "node")
	if up == nil || up.Incident.Severity != model.Warning || up.Escalated {
		t.Fatalf("critical to warning: %+v", up)
	}
	setSeverity("critical")
	failAll()
	var last model.Event
	for _, e := range rec.events {
		if e.Type == model.EventUpdate && e.Incident.Key == "node" {
			last = e
		}
	}
	if last.Incident.Severity != model.Critical || !last.Escalated {
		t.Fatalf("warning to critical is an escalation: %+v", last)
	}
}

// What an open incident learns without changing its verdict (a new error,
// the peak of a metric, the lowest users count) reaches the database too.
func TestUnchangedIncidentDetailIsPersisted(t *testing.T) {
	h, _ := testHub(t)
	mem := &memRecorder{}
	h.SetRecorder(mem)
	stored := func(key string) model.Incident {
		h.mu.Lock()
		id := h.incidents[key].ID
		h.mu.Unlock()
		mem.mu.Lock()
		defer mem.mu.Unlock()
		return mem.incidents[id]
	}
	for _, p := range []string{"ru-a", "ru-b", "de"} {
		h.Ingest(p, []checks.Result{res("node", false), res("node", false)})
	}
	r := res("node", false)
	r.Reason, r.Error = "refused", "connection refused"
	h.Ingest("de", []checks.Result{r})
	for _, l := range stored("node").Lines {
		if l.Probe == "de" && l.Error != "connection refused" {
			t.Fatalf("the new error was not persisted: %+v", l)
		}
	}

	now := time.Now()
	h.mu.Lock()
	m := meta{name: "de", typ: "probe", severity: "warning"}
	m.host = &model.HostValue{Metric: "cpu", Value: 95, Above: 90}
	h.transition("host:de:cpu", model.KindHost, "", nil, m, now)
	m.host = &model.HostValue{Metric: "cpu", Value: 99, Above: 90}
	h.transition("host:de:cpu", model.KindHost, "", nil, m, now.Add(time.Minute))
	d := meta{name: "pl", typ: "users", severity: "critical"}
	d.drop = &model.UsersDrop{Before: 800, Now: 60}
	h.transition("rw:pl", model.KindUsersDrop, "", nil, d, now)
	d.drop = &model.UsersDrop{Before: 800, Now: 20}
	h.transition("rw:pl", model.KindUsersDrop, "", nil, d, now.Add(time.Minute))
	h.mu.Unlock()
	if hv := stored("host:de:cpu").Host; hv == nil || hv.Peak != 99 {
		t.Fatalf("host peak not persisted: %+v", hv)
	}
	if dv := stored("rw:pl").Drop; dv == nil || dv.Low != 20 {
		t.Fatalf("drop low not persisted: %+v", dv)
	}
}

// A manual close of a check, probe or node that no longer exists goes with
// it, instead of staying in the mutes forever (and silencing a new check
// that reuses the id).
func TestClosedByHandMarksOfRemovedThingsAreCleaned(t *testing.T) {
	h, rec := dropHub(t)
	mem := &memRecorder{}
	h.SetRecorder(mem)
	for _, p := range []string{"ru-a", "ru-b", "de"} {
		h.Ingest(p, []checks.Result{res("node", false), res("node", false)})
	}
	h.mu.Lock()
	p := h.probes["de"]
	p.online, p.since = false, time.Now().Add(-time.Hour)
	h.mu.Unlock()
	h.tick()
	t0 := time.Now().Add(-3 * time.Hour)
	fleet := func(pl NodeUsers) []NodeUsers { return []NodeUsers{pl, node("swe", 880), node("nl", 650)} }
	feedUsers(h, t0, 0, 120, func(int) []NodeUsers { return fleet(node("pl", 10)) })
	feedUsers(h, t0, 120, 130, func(int) []NodeUsers { return fleet(lost("pl")) })
	for _, key := range []string{"node", "probe:de", "rw:pl"} {
		ev := rec.find(model.EventOpen, key)
		if ev == nil {
			t.Fatalf("setup: %s", key)
		}
		if err := h.Resolve(ev.Incident.ID, "@leo"); err != nil {
			t.Fatal(err)
		}
	}
	// The node leaves the panel; the check and the probe leave the config.
	feedUsers(h, t0, 130, 131, func(int) []NodeUsers { return []NodeUsers{node("swe", 880), node("nl", 650)} })
	h.mu.Lock()
	cfg := *h.cfg
	h.mu.Unlock()
	cfg.Checks = nil
	cfg.Probes = slices.DeleteFunc(slices.Clone(cfg.Probes), func(p ProbeConfig) bool { return p.ID == "de" })
	if err := h.Apply(&cfg); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, key := range []string{"node", "probe:de", "rw:pl"} {
		if h.closedByHand(key) {
			t.Errorf("%s: the manual close outlived what it closed", key)
		}
	}
	mem.mu.Lock()
	defer mem.mu.Unlock()
	if len(mem.mutes) != 0 {
		t.Errorf("stored mutes still hold them: %v", mem.mutes)
	}
}

// A parent blocked in ru explains a service failing in ru, not one blocked
// in another region or failing on networks elsewhere.
func TestBlockedParentExplainsOnlyItsRegion(t *testing.T) {
	line := func(probe, region string, ok bool) model.ProbeLine {
		return model.ProbeLine{Probe: probe, Region: region, OK: ok}
	}
	cases := []struct {
		name   string
		kind   model.Kind
		region string
		lines  []model.ProbeLine
		folded bool
	}{
		{"blocked in ru", model.KindBlocked, "ru", []model.ProbeLine{line("a", "ru", false), line("b", "de", true)}, true},
		{"blocked in de", model.KindBlocked, "de", []model.ProbeLine{line("a", "ru", true), line("b", "de", false)}, false},
		{"partial in ru", model.KindPartial, "", []model.ProbeLine{line("a", "ru", false), line("c", "ru", true), line("b", "de", true)}, true},
		{"partial in de", model.KindPartial, "", []model.ProbeLine{line("a", "ru", true), line("b", "de", false), line("c", "de", true)}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, _ := downHub(t, nil)
			now := time.Now()
			h.mu.Lock()
			defer h.mu.Unlock()
			h.transition("de-tcp", model.KindBlocked, "ru", nil, meta{name: "de-tcp", typ: "tcp", severity: "critical"}, now)
			h.transition("de-web", c.kind, c.region, c.lines, meta{name: "de-web", typ: "http"}, now)
			if got := h.incidents["de-web"].CauseKey == "de-tcp"; got != c.folded {
				t.Fatalf("folded=%v, want %v", got, c.folded)
			}
		})
	}
}

// With the users-drop rule off no node_down ever opens, so the services of
// a node the panel lost do not wait for one.
func TestNoNodePendingWhenDropRuleOff(t *testing.T) {
	h, rec := downHub(t, func(c *Config) {
		c.Checks = slices.DeleteFunc(c.Checks, func(x CheckConfig) bool { return x.ID == "de-tcp" })
		c.Remnawave.Drop = &UsersDrop{Off: true}
	})
	h.ObserveUsers(time.Now(), []NodeUsers{{ID: "uuid-de", Name: "de-node-1", Address: "de1.example.com", Connected: false}})
	fail(h, "de-web")
	expire(h, "de-web")
	if rec.find(model.EventOpen, "de-web") == nil {
		t.Fatal("nothing will ever explain it: the service alerts after its own wait")
	}
}

// countRecorder counts the results the hub records.
type countRecorder struct {
	memRecorder
	results []checks.Result
}

func (r *countRecorder) Results(_ string, rs []checks.Result) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results = append(r.results, rs...)
}

// An agent's message is not trusted to be sane: a negative age (a result
// from the future) must not lock out honest results, an absurd one is
// dropped, strings and maps are capped, and so is the batch.
func TestIngestRejectsInsaneResults(t *testing.T) {
	h, _ := testHub(t)
	cr := &countRecorder{}
	h.SetRecorder(cr)
	last := func() (checks.Result, time.Time) {
		h.mu.Lock()
		defer h.mu.Unlock()
		st := h.checks["node"].state["ru-a"]
		return st.last, st.at
	}

	future := res("node", false)
	future.Age = -86400
	h.Ingest("ru-a", []checks.Result{future})
	if _, at := last(); at.After(time.Now()) {
		t.Fatalf("a result was placed in the future: %v", at)
	}
	h.Ingest("ru-a", []checks.Result{res("node", true)})
	if r, _ := last(); r.Status != checks.Up {
		t.Fatal("an honest result after a negative age was refused")
	}

	for _, age := range []float64{1e300, 7200, math.NaN(), math.Inf(1)} {
		old := res("node", false)
		old.Age = age
		h.Ingest("ru-a", []checks.Result{old})
		if r, at := last(); r.Status != checks.Up || at.After(time.Now()) {
			t.Fatalf("age %v: taken as %s at %v", age, r.Status, at)
		}
	}

	big := res("node", false)
	big.Error, big.Reason = strings.Repeat("e", 100_000), strings.Repeat("r", 1000)
	big.Info, big.Phases = map[string]string{}, map[string]float64{}
	for i := range 1000 {
		big.Info[fmt.Sprint("k", i)] = strings.Repeat("v", 10_000)
		big.Phases[fmt.Sprint("p", i)] = 1
	}
	h.Ingest("ru-a", []checks.Result{big})
	r, _ := last()
	if len(r.Error) > 2048 || len(r.Reason) > 128 || len(r.Info) > 32 || len(r.Phases) > 32 {
		t.Fatalf("not capped: error %d, reason %d, info %d, phases %d", len(r.Error), len(r.Reason), len(r.Info), len(r.Phases))
	}
	for k, v := range r.Info {
		if len(k) > 128 || len(v) > 1024 {
			t.Fatalf("info entry not capped: %d/%d", len(k), len(v))
		}
	}

	cr.mu.Lock()
	cr.results = nil
	cr.mu.Unlock()
	flood := make([]checks.Result, maxResultsPerBatch+1000)
	for i := range flood {
		flood[i] = res("node", true)
	}
	flood[len(flood)-1] = res("node", false) // the newest
	h.Ingest("ru-a", flood)
	cr.mu.Lock()
	n := len(cr.results)
	cr.mu.Unlock()
	if n > maxResultsPerBatch {
		t.Fatalf("%d results recorded from one batch", n)
	}
	if r, _ := last(); r.Status == checks.Up {
		t.Fatal("a capped batch keeps the newest results")
	}

	// Host metrics carry an age too.
	m := cpuAt(99)
	m.Age = -86400
	h.IngestMetrics("ru-a", m)
	h.IngestMetrics("ru-a", cpuAt(5))
	h.mu.Lock()
	cpu, at := h.probes["ru-a"].metrics.CPU, h.probes["ru-a"].metricsAt
	h.mu.Unlock()
	if cpu != 5 || at.After(time.Now()) {
		t.Fatalf("metrics from the future locked out honest ones: cpu %v at %v", cpu, at)
	}
}

// The largest batch an honest agent sends (its whole buffer after a long
// disconnect) fits under the agent read limit. Errors are not clipped on the
// agent, so this assumes typical ones; see agentReadLimit.
func TestHonestAgentBatchFitsReadLimit(t *testing.T) {
	const agentBuffer = 5000 // internal/agent bufferLimit
	rs := make([]checks.Result, agentBuffer)
	for i := range rs {
		rs[i] = checks.Result{CheckID: fmt.Sprintf("subscription-%d/cdn-example-com-443-a1b2c3", i), Status: checks.Down,
			Reason: "timeout", Error: strings.Repeat("proxy/vless/outbound: failed to dial: i/o timeout ", 10)[:500], LatencyMS: 25000.123,
			Phases: map[string]float64{"dns": 1.5, "connect": 20.25, "tls": 40.5, "ttfb": 120.75, "total": 25000.5},
			Info:   map[string]string{"egress_ip": "203.0.113.10", "egress_loc": "Frankfurt, DE", "status_code": "200"}, Age: 12345.678}
	}
	msg, err := proto.Encode(proto.TypeResults, proto.Results{Results: rs})
	if err != nil {
		t.Fatal(err)
	}
	if len(msg) >= agentReadLimit {
		t.Fatalf("a full honest batch is %d bytes, the limit %d", len(msg), agentReadLimit)
	}
	t.Logf("full batch: %.1f MB of %.0f MB", float64(len(msg))/(1<<20), float64(agentReadLimit)/(1<<20))
}

// A "blocked in ru" open before the restart must not turn into "down" just
// because the German probe has not reported yet.
func TestRestoredBlockedDoesNotEscalateOnPartialSet(t *testing.T) {
	h, rec := testHub(t)
	mem := &memRecorder{}
	h.SetRecorder(mem)
	for range 2 {
		h.Ingest("ru-a", []checks.Result{res("node", false)})
		h.Ingest("ru-b", []checks.Result{res("node", false)})
		h.Ingest("de", []checks.Result{res("node", true)})
	}
	if ev := rec.find(model.EventOpen, "node"); ev == nil || ev.Incident.Kind != model.KindBlocked {
		t.Fatalf("setup %+v", ev)
	}
	h2, rec2 := mem.restart(t)
	h2.Ingest("ru-a", []checks.Result{res("node", false)})
	h2.Ingest("ru-a", []checks.Result{res("node", false)})
	h2.tick()
	for _, e := range rec2.events {
		if e.Incident.Key == "node" && (e.Escalated || e.Incident.Kind != model.KindBlocked) {
			t.Fatalf("restored blocked changed on a partial set: %s kind=%s escalated=%v", e.Type, e.Incident.Kind, e.Escalated)
		}
	}
	h2.mu.Lock()
	k := h2.incidents["node"].Kind
	h2.mu.Unlock()
	if k != model.KindBlocked {
		t.Fatalf("kind %s", k)
	}
}

func TestCheckerRoleValidation(t *testing.T) {
	cases := []struct {
		name string
		p    ProbeConfig
		want string
	}{
		{"via checker", ProbeConfig{ID: "v", Via: "socks5://127.0.0.1:1080", Role: RoleChecker}, "role is only for agents"},
		{"local checker", ProbeConfig{ID: "l", Local: true, Role: RoleChecker}, "role is only for agents"},
		{"trusted server", ProbeConfig{ID: "s", Token: "token-s-0123456789", Trusted: true}, "trusted"},
		{"unknown role", ProbeConfig{ID: "u", Token: "token-u-0123456789", Role: "boss"}, "role"},
	}
	for _, tc := range cases {
		c := &Config{Probes: []ProbeConfig{tc.p}}
		if err := c.normalize(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want error with %q, got %v", tc.name, tc.want, err)
		}
	}
	c := &Config{Probes: []ProbeConfig{
		{ID: "chk", Token: "token-chk-0123456789", Role: RoleChecker, Trusted: true},
		// A stale label from when it was a checker must not keep it in "role=checker".
		{ID: "srv", Token: "token-srv-0123456789", Role: RoleServer, Labels: map[string]string{"role": RoleChecker}},
	}}
	if err := c.normalize(); err != nil {
		t.Fatal(err)
	}
	if p := c.Probes[0]; !p.Checker() || p.Labels["role"] != RoleChecker || !p.RunsChecks() {
		t.Fatalf("checker: role %q labels %v", p.Role, p.Labels)
	}
	if p := c.Probes[1]; p.Checker() || p.Labels["role"] != "" || p.RunsChecks() {
		t.Fatalf("server: role %q labels %v", p.Role, p.Labels)
	}
}

// checkerHub has a checker with isp=mts and a probing server agent; checks is
// what they are offered.
func checkerHub(t *testing.T, trusted bool, cs ...CheckConfig) *Hub {
	t.Helper()
	cfg := &Config{DataDir: t.TempDir(), Baseline: []checks.Spec{},
		Probes: []ProbeConfig{
			{ID: "chk", Token: "token-chk-0123456789", Role: RoleChecker, Trusted: trusted, Labels: map[string]string{"isp": "mts"}},
			{ID: "srv", Token: "token-srv-0123456789", Probing: true, Labels: map[string]string{"isp": "mts"}},
		},
		Checks: cs}
	h, err := New(cfg, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func assignedIDs(h *Hub, probe string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var ids []string
	for _, s := range h.assignmentFor(h.probes[probe]).Checks {
		ids = append(ids, s.ID)
	}
	return ids
}

func tcpCheck(id string, probes ...string) CheckConfig {
	return CheckConfig{Spec: checks.Spec{ID: id, Type: "tcp", TCP: &checks.TCPParams{Address: "10.0.0.1:443"}}, Probes: probes}
}

// A checker is someone else's machine: "everywhere" must not reach it, only a
// selector that picks it out.
func TestCheckerGetsOnlyExplicitChecks(t *testing.T) {
	h := checkerHub(t, false,
		tcpCheck("all"), tcpCheck("star", "*"),
		tcpCheck("role", "role=checker"), tcpCheck("isp", "isp=mts"), tcpCheck("id", "id=chk"),
		tcpCheck("other-isp", "isp=beeline"),
		CheckConfig{Spec: checks.Spec{ID: "excluded", Type: "tcp", TCP: &checks.TCPParams{Address: "10.0.0.1:443"}}, Probes: []string{"isp=mts"}, Exclude: []string{"role=checker"}},
	)
	if got, want := assignedIDs(h, "chk"), []string{"role", "isp", "id"}; !slices.Equal(got, want) {
		t.Fatalf("checker got %v, want %v", got, want)
	}
	if got, want := assignedIDs(h, "srv"), []string{"all", "star", "isp", "excluded"}; !slices.Equal(got, want) {
		t.Fatalf("server agent got %v, want %v", got, want)
	}
}

func TestUntrustedCheckerGetsNoSecrets(t *testing.T) {
	vpn := CheckConfig{Spec: checks.Spec{ID: "vpn", Type: "xray", Xray: &checks.XrayParams{
		Link: "vless://b831381d-6324-4d53-ad4f-8cda48b30811@ok.example.com:443?type=tcp#ok"}}, Probes: []string{"isp=mts"}}
	plain := tcpCheck("plain", "isp=mts")
	if got, want := assignedIDs(checkerHub(t, false, vpn, plain), "chk"), []string{"plain"}; !slices.Equal(got, want) {
		t.Fatalf("untrusted checker got %v, want %v", got, want)
	}
	if got, want := assignedIDs(checkerHub(t, true, vpn, plain), "chk"), []string{"vpn", "plain"}; !slices.Equal(got, want) {
		t.Fatalf("trusted checker got %v, want %v", got, want)
	}
	if got, want := assignedIDs(checkerHub(t, false, vpn, plain), "srv"), []string{"vpn", "plain"}; !slices.Equal(got, want) {
		t.Fatalf("server agent got %v, want %v", got, want)
	}
}

func TestCheckerAssignHasNoMetrics(t *testing.T) {
	h := checkerHub(t, false)
	h.mu.Lock()
	defer h.mu.Unlock()
	if m := h.assignmentFor(h.probes["chk"]).Metrics; m == nil || *m {
		t.Fatalf("checker assign metrics = %v, want false", m)
	}
	if m := h.assignmentFor(h.probes["srv"]).Metrics; m != nil {
		t.Fatalf("server assign metrics = %v, want nil", *m)
	}
}

// sysRecorder counts the host snapshots that reach storage.
type sysRecorder struct {
	memRecorder
	mu2 sync.Mutex
	n   int
}

func (r *sysRecorder) System(string, proto.Metrics) {
	r.mu2.Lock()
	r.n++
	r.mu2.Unlock()
}

func (r *sysRecorder) systems() int {
	r.mu2.Lock()
	defer r.mu2.Unlock()
	return r.n
}

// A checker is a volunteer's machine: it goes away when its owner turns it
// off, and that is no incident. The same silence of a server agent is.
func TestCheckerOfflineIsQuiet(t *testing.T) {
	h := checkerHub(t, false)
	rec := &recorder{}
	h.AddNotifier(rec)
	h.mu.Lock()
	for _, id := range []string{"chk", "srv"} {
		p := h.probes[id]
		p.hello.Version = "test"
		p.online = false
		p.since = time.Now().Add(-48 * time.Hour)
	}
	h.mu.Unlock()
	h.tick()
	if ev := rec.find(model.EventOpen, "probe:chk"); ev != nil {
		t.Fatalf("a checker going offline was announced: %+v", ev.Incident)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.incidents["probe:chk"] != nil {
		t.Fatalf("a checker going offline raised an incident: %+v", h.incidents["probe:chk"])
	}
	if h.incidents["probe:srv"] == nil {
		t.Fatal("a server agent going offline must still raise probe_offline")
	}
}

// A checker behind a dead home connection is no alert either, and its rows
// still drop out of the verdicts.
func TestCheckerWithoutInternetIsQuiet(t *testing.T) {
	probe := func(role string) ProbeConfig {
		return ProbeConfig{ID: "p-" + role, Token: "token-" + role + "-0123456789", Role: role, Probing: role != RoleChecker,
			Baseline: []checks.Spec{{Type: "tcp", TCP: &checks.TCPParams{Address: "10.0.0.2:443"}}}}
	}
	cfg := &Config{DataDir: t.TempDir(), Baseline: []checks.Spec{},
		Probes: []ProbeConfig{probe(RoleChecker), probe(RoleServer)},
		Checks: []CheckConfig{
			{Spec: checks.Spec{ID: "node", Type: "tcp", TCP: &checks.TCPParams{Address: "10.0.0.1:443"}}, Probes: []string{"id=p-checker", "id=p-server"}},
		}}
	h, err := New(cfg, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	h.AddNotifier(rec)
	h.mu.Lock()
	for _, p := range h.probes {
		p.online = true
		p.since = time.Now().Add(-time.Hour)
	}
	h.rebuild()
	h.mu.Unlock()

	for range 3 {
		for _, id := range []string{"p-checker", "p-server"} {
			h.Ingest(id, []checks.Result{res("_baseline/"+id+"/0", false), res("node", false)})
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.probes["p-server"].noNet || h.incidents["probe-net:p-server"] == nil {
		t.Fatal("a server agent without internet must still raise probe-net")
	}
	if c := h.probes["p-checker"]; !c.noNet {
		t.Fatal("the checker's lost internet must still keep its rows out of verdicts")
	}
	if h.incidents["probe-net:p-checker"] != nil {
		t.Fatalf("a checker without internet raised an incident: %+v", h.incidents["probe-net:p-checker"])
	}
}

func TestCheckerMetricsDropped(t *testing.T) {
	h := checkerHub(t, false)
	rec := &sysRecorder{}
	h.SetRecorder(rec)
	h.IngestMetrics("chk", cpuAt(99))
	h.mu.Lock()
	if h.probes["chk"].metrics != nil || len(h.probes["chk"].host) != 0 {
		t.Fatal("a checker's host metrics were kept or fed to host alerts")
	}
	h.mu.Unlock()
	if n := rec.systems(); n != 0 {
		t.Fatalf("recorder got %d points from a checker", n)
	}
	h.IngestMetrics("srv", cpuAt(5))
	if n := rec.systems(); n != 1 {
		t.Fatalf("a server agent's metrics must be stored, recorder got %d", n)
	}
}

// The global internet baseline is offered to every checker whatever its
// selectors say: it carries no secrets and it is what tells a dead home line
// from a dead node.
func TestCheckerGetsGlobalBaseline(t *testing.T) {
	cfg := &Config{DataDir: t.TempDir(),
		Baseline: []checks.Spec{{Type: "tcp", TCP: &checks.TCPParams{Address: "10.0.0.2:443"}}},
		Probes: []ProbeConfig{
			{ID: "chk", Token: "token-chk-0123456789", Role: RoleChecker, Labels: map[string]string{"isp": "mts"}},
			{ID: "srv", Token: "token-srv-0123456789", Probing: true, Labels: map[string]string{"isp": "mts"}},
		},
		Checks: []CheckConfig{tcpCheck("node", "isp=mts")}}
	h, err := New(cfg, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	h.AddNotifier(rec)
	if got, want := assignedIDs(h, "chk"), []string{"_baseline/0", "node"}; !slices.Equal(got, want) {
		t.Fatalf("checker got %v, want %v", got, want)
	}
	if got, want := assignedIDs(h, "srv"), []string{"_baseline/0", "node"}; !slices.Equal(got, want) {
		t.Fatalf("server agent got %v, want %v", got, want)
	}
	h.mu.Lock()
	for _, p := range h.probes {
		p.online = true
		p.since = time.Now().Add(-time.Hour)
	}
	h.mu.Unlock()
	for range 3 {
		h.Ingest("chk", []checks.Result{res("_baseline/0", false), res("node", false)})
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.probes["chk"].noNet {
		t.Fatal("a checker without internet must be noNet, so its failures stay out of verdicts")
	}
	if h.incidents["probe-net:chk"] != nil || h.incidents["node"] != nil {
		t.Fatalf("a checker without internet raised alerts: %v", h.incidents)
	}
}

// A probe that becomes a checker never alerts on its own health, so what it
// had open is closed without a word.
func TestProbeBecomingCheckerClosesItsIncidentsQuietly(t *testing.T) {
	h, rec := testHub(t)
	h.mu.Lock()
	now := time.Now()
	h.transition("probe:ru-a", model.KindProbeOffline, "", nil, meta{name: "ru-a", typ: "probe", severity: "warning"}, now)
	h.transition("probe-net:ru-a", model.KindProbeNoNet, "", nil, meta{name: "ru-a", typ: "probe", severity: "warning"}, now)
	h.mu.Unlock()
	if rec.find(model.EventOpen, "probe:ru-a") == nil || rec.find(model.EventOpen, "probe-net:ru-a") == nil {
		t.Fatal("setup: the incidents were not announced")
	}
	h.mu.Lock()
	cfg := h.cfg.Clone()
	h.mu.Unlock()
	for i := range cfg.Probes {
		if cfg.Probes[i].ID == "ru-a" {
			cfg.Probes[i].Role = RoleChecker
		}
	}
	if err := h.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.incidents["probe:ru-a"] != nil || h.incidents["probe-net:ru-a"] != nil {
		t.Fatal("a checker's own-health incidents must be closed")
	}
	if rec.find(model.EventResolve, "probe:ru-a") != nil || rec.find(model.EventResolve, "probe-net:ru-a") != nil {
		t.Fatal("closing them must not notify")
	}
}

func mtsNet() geoip.Network {
	return geoip.Network{IP: netip.MustParseAddr("203.0.113.7"), Country: "ru", ASN: "as8359", Org: "MTS PJSC"}
}

// A checker is labelled by the network it connects from, so a check can pick
// "the MTS subscribers"; a label the admin set by hand stays.
func TestSetNetworkLabelsChecker(t *testing.T) {
	cfg := &Config{DataDir: t.TempDir(), Baseline: []checks.Spec{},
		Probes: []ProbeConfig{
			{ID: "chk", Token: "token-chk-0123456789", Role: RoleChecker},
			{ID: "kz", Token: "token-kz-00123456789", Role: RoleChecker, Labels: map[string]string{"country": "kz"}, LabelsLocked: []string{"country"}},
		},
		Checks: []CheckConfig{tcpCheck("mts", "isp=mts")}}
	h, err := New(cfg, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	if got := assignedIDs(h, "chk"); len(got) != 0 {
		t.Fatalf("before its network is known the checker got %v", got)
	}
	before := h.Snapshot()
	h.SetNetwork("chk", mtsNet())
	h.SetNetwork("kz", mtsNet())

	h.mu.Lock()
	chk, kz := maps.Clone(h.probes["chk"].cfg.Labels), maps.Clone(h.probes["kz"].cfg.Labels)
	h.mu.Unlock()
	if chk["country"] != "ru" || chk["asn"] != "as8359" || chk["isp"] != "mts" {
		t.Fatalf("checker labels %v", chk)
	}
	if kz["country"] != "kz" || kz["isp"] != "mts" {
		t.Fatalf("a locked label was overwritten: %v", kz)
	}
	if got := assignedIDs(h, "chk"); !slices.Equal(got, []string{"mts"}) {
		t.Fatalf("checker on MTS got %v, want [mts]", got)
	}
	// What was handed out before is a copy: the API marshals it unlocked.
	for _, p := range before.Probes {
		if p.Labels["isp"] != "" {
			t.Fatalf("an earlier snapshot changed under its reader: %v", p.Labels)
		}
	}
	snap := h.Snapshot()
	for _, p := range snap.Probes {
		if n := p.Network; p.ID == "chk" && (n == nil || n.IP != "203.0.113.7" || n.Country != "ru" || n.ASN != "as8359" || n.Org != "MTS PJSC" || n.ISP != "mts") {
			t.Fatalf("network view %+v", n)
		}
	}
}

// A checker that moves to another operator stops running what was meant for
// the old one, and what it measured there is no evidence any more: its line
// leaves the check, and the incident it alone kept open closes as soon as the
// remaining probes say so, not earlier.
func TestCheckerNetworkChangeDropsOldState(t *testing.T) {
	h := checkerHub(t, false, tcpCheck("node", "isp=mts"))
	rec := &recorder{}
	h.AddNotifier(rec)
	h.SetNetwork("chk", mtsNet())
	h.mu.Lock()
	for _, p := range h.probes {
		p.online, p.since = true, time.Now().Add(-time.Hour)
	}
	h.mu.Unlock()
	for range 2 {
		h.Ingest("chk", []checks.Result{res("node", false)})
		h.Ingest("srv", []checks.Result{res("node", true)})
	}
	h.mu.Lock()
	h.incidents["node"].Started = time.Now().Add(-time.Hour)
	h.mu.Unlock()
	h.tick()
	if ev := rec.find(model.EventOpen, "node"); ev == nil || len(failingSet(ev.Incident.Lines)) != 1 {
		t.Fatalf("setup: expected an outage seen by the checker alone, got %+v", ev)
	}

	// The server agent goes quiet, then the checker moves to Beeline.
	h.mu.Lock()
	h.probes["srv"].online = false
	h.mu.Unlock()
	h.SetNetwork("chk", geoip.Network{IP: netip.MustParseAddr("198.51.100.4"), Country: "ru", ASN: "as3216", Org: "Beeline"})

	if got := assignedIDs(h, "chk"); slices.Contains(got, "node") {
		t.Fatalf("the checker still runs the MTS check after moving to Beeline: %v", got)
	}
	h.mu.Lock()
	_, kept := h.checks["node"].state["chk"]
	h.mu.Unlock()
	if kept {
		t.Fatal("the checker's MTS result is still part of the check")
	}
	h.tick()
	if ev := rec.find(model.EventResolve, "node"); ev != nil {
		t.Fatalf("resolved with nobody left to say the node works: %+v", ev.Incident.Lines)
	}

	// The server agent is back and the node works for it: a real recovery.
	h.mu.Lock()
	h.probes["srv"].online = true
	h.mu.Unlock()
	h.Ingest("srv", []checks.Result{res("node", true)})
	ev := rec.find(model.EventResolve, "node")
	if ev == nil {
		t.Fatal("the incident stayed open on the line of a probe that no longer runs the check")
	}
	for _, l := range ev.Incident.Lines {
		if !l.OK {
			t.Fatalf("resolved with a failing line: %+v", ev.Incident.Lines)
		}
	}
}

// Our own servers keep the labels the admin gave them; their network is only
// shown.
func TestServerAgentNetworkNoLabels(t *testing.T) {
	h := checkerHub(t, false, tcpCheck("node", "isp=mts"))
	h.SetNetwork("srv", geoip.Network{IP: netip.MustParseAddr("198.51.100.4"), Country: "de", ASN: "as24940", Org: "Hetzner Online GmbH"})
	h.mu.Lock()
	labels := maps.Clone(h.probes["srv"].cfg.Labels)
	h.mu.Unlock()
	if labels["isp"] != "mts" || labels["country"] != "" || labels["asn"] != "" {
		t.Fatalf("a server agent was relabelled: %v", labels)
	}
	if got := assignedIDs(h, "srv"); !slices.Equal(got, []string{"node"}) {
		t.Fatalf("server agent got %v", got)
	}
	for _, p := range h.Snapshot().Probes {
		if n := p.Network; p.ID == "srv" && (n == nil || n.IP != "198.51.100.4" || n.Country != "de" || n.ASN != "as24940" || n.ISP != "hetzner-online") {
			t.Fatalf("server agent network view %+v", n)
		}
	}
}

// The app may apply a config composed from a probe object saved before the
// network was known: the checker keeps its labels. A lookup for an address
// the agent has since left is ignored.
func TestNetworkLabelsKeptAcrossApply(t *testing.T) {
	h := checkerHub(t, false, tcpCheck("node", "isp=mts"))
	h.SetNetwork("chk", geoip.Network{IP: netip.MustParseAddr("198.51.100.4"), Country: "ru", ASN: "as3216", Org: "Beeline"})
	h.mu.Lock()
	cfg := h.cfg.Clone()
	h.mu.Unlock()
	cfg.Probes[0].Labels = map[string]string{"isp": "mts"} // the stale object
	if err := h.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	isp := h.probes["chk"].cfg.Labels["isp"]
	h.probes["chk"].addr = netip.MustParseAddr("192.0.2.1") // reconnected from elsewhere
	h.mu.Unlock()
	if isp != "beeline" {
		t.Fatalf("Apply dropped the network label: isp=%s", isp)
	}
	h.SetNetwork("chk", mtsNet()) // the lookup for the old address finishes late
	h.mu.Lock()
	defer h.mu.Unlock()
	if isp := h.probes["chk"].cfg.Labels["isp"]; isp != "beeline" {
		t.Fatalf("a stale lookup relabelled the checker: isp=%s", isp)
	}
}

// "Check this config from there" may pick any checker, but a config with a
// secret is not handed to one nobody vouched for: the answer says why, and
// the agent sees nothing. A trusted checker, or a check without secrets,
// still goes out.
func TestRunOnceRefusesSecretsOnUntrustedChecker(t *testing.T) {
	vpn := checks.Spec{Type: "xray", Xray: &checks.XrayParams{
		Link: "vless://b831381d-6324-4d53-ad4f-8cda48b30811@ok.example.com:443?type=tcp#ok"}}
	plain := checks.Spec{Type: "tcp", TCP: &checks.TCPParams{Address: "10.0.0.1:443"}}
	connect := func(h *Hub) chan []byte {
		out := make(chan []byte, 16)
		h.mu.Lock()
		h.probes["chk"].conn = &agentConn{assign: make(chan []byte, 1), out: out}
		h.mu.Unlock()
		return out
	}

	h := checkerHub(t, false)
	out := connect(h)
	got := h.RunOnce(context.Background(), vpn, []string{"chk"})
	if len(got) != 1 || got[0].Result == nil || got[0].Result.Status != checks.Down || got[0].Result.Reason != "unavailable" ||
		!strings.Contains(got[0].Result.Error, "нужен доверенный агент") {
		t.Fatalf("untrusted checker, xray: %+v (result %+v)", got, got[0].Result)
	}
	if n := len(out); n != 0 {
		t.Fatalf("%d messages went to the untrusted checker", n)
	}

	// The same checker still runs what carries no secret; a trusted one runs
	// both. A cancelled context returns once the request went out.
	done, cancel := context.WithCancel(context.Background())
	cancel()
	h.RunOnce(done, plain, []string{"chk"})
	if n := len(out); n != 1 {
		t.Fatalf("a plain check sent %d messages to the checker, want 1", n)
	}
	h = checkerHub(t, true)
	out = connect(h)
	h.RunOnce(done, vpn, []string{"chk"})
	if n := len(out); n != 1 {
		t.Fatalf("a trusted checker got %d messages for xray, want 1", n)
	}
}
