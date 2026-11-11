package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/model"
)

// pollCalls collects calls until cond holds or 5 s pass.
func pollCalls(calls func() []tgCall, cond func([]tgCall) bool) []tgCall {
	var all []tgCall
	deadline := time.Now().Add(5 * time.Second)
	for {
		all = append(all, calls()...)
		if cond(all) || time.Now().After(deadline) {
			return all
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type recActions struct {
	acked, resolved []string
	muted           []time.Duration
}

func (f *recActions) Ack(id, by string) error     { f.acked = append(f.acked, id); return nil }
func (f *recActions) Resolve(id, by string) error { f.resolved = append(f.resolved, id); return nil }
func (f *recActions) Mute(id string, d time.Duration, by string) error {
	f.muted = append(f.muted, d)
	return nil
}

func press(tg *Telegram, chat, from int64, data string) {
	var u update
	json.Unmarshal(fmt.Appendf(nil, `{"callback_query":{"id":"1","data":%q,"from":{"id":%d,"username":"x"},"message":{"chat":{"id":%d}}}}`,
		data, from, chat), &u)
	tg.handleCallback(context.Background(), u)
}

// Without an admin list anyone in the chat could close or mute incidents
// from the bot: only Acknowledge is open to everyone then.
func TestButtonsWithoutAdmins(t *testing.T) {
	tg, _ := fakeBot(t, TelegramConfig{})
	act := &recActions{}
	tg.SetActions(act)
	press(tg, -100, 7, "resolve:i1")
	press(tg, -100, 7, "mute:i1:3600")
	press(tg, -100, 7, "ack:i1")
	if len(act.resolved) != 0 || len(act.muted) != 0 || len(act.acked) != 1 {
		t.Fatalf("no admins: resolved %v, muted %v, acked %v", act.resolved, act.muted, act.acked)
	}
	tg.cfg.Admins = []int64{7}
	press(tg, -100, 7, "resolve:i1")
	press(tg, -100, 8, "resolve:i1")
	if len(act.resolved) != 1 {
		t.Fatalf("admin 7 resolves, 8 does not: %v", act.resolved)
	}
}

// Buttons work in the chats tag rules post to, not only in routed ones.
func TestButtonsInRuleChats(t *testing.T) {
	tg, _ := fakeBot(t, TelegramConfig{Admins: []int64{7}, Rules: []Rule{{Tags: []string{"de"}, Route: Route{ChatID: -200}}}})
	act := &recActions{}
	tg.SetActions(act)
	press(tg, -200, 7, "ack:i1")
	press(tg, -300, 7, "ack:i2")
	if len(act.acked) != 1 || act.acked[0] != "i1" {
		t.Fatalf("rule chat -200 allowed, unknown -300 not: %v", act.acked)
	}
}

// A mute from callback data is bounded: the data comes from Telegram's
// client, not from the keyboard the bot sent.
func TestMuteFromButtonIsBounded(t *testing.T) {
	tg, _ := fakeBot(t, TelegramConfig{Admins: []int64{7}})
	act := &recActions{}
	tg.SetActions(act)
	press(tg, -100, 7, "mute:i1:999999999")
	press(tg, -100, 7, "mute:i1:-5")
	press(tg, -100, 7, "mute:i1:3600")
	if len(act.muted) != 2 || act.muted[0] != 7*24*time.Hour || act.muted[1] != time.Hour {
		t.Fatalf("mutes: %v", act.muted)
	}
}

// fakeAPI answers the Bot API through fn and records each request's method
// and parameters.
func fakeAPI(t *testing.T, cfg TelegramConfig, fn func(method string, p map[string]any) string) (*Telegram, func() []map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var reqs []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		json.NewDecoder(r.Body).Decode(&p)
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		p["_method"] = method
		mu.Lock()
		reqs = append(reqs, p)
		mu.Unlock()
		io.WriteString(w, fn(method, p))
	}))
	t.Cleanup(srv.Close)
	cfg.Token, cfg.APIURL, cfg.DataDir = "1:abc", srv.URL, t.TempDir()
	if cfg.ChatID == 0 {
		cfg.ChatID = -100
	}
	tg, err := NewTelegram(cfg, &Formatter{Lang: "en", Loc: time.UTC}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return tg, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		out := reqs
		reqs = nil
		return out
	}
}

const tgOK = `{"ok":true,"result":{"message_id":7}}`

// A group that became a supergroup answers with its new id: the message goes
// there, and so does everything after it.
func TestChatMigratedToSupergroup(t *testing.T) {
	tg, reqs := fakeAPI(t, TelegramConfig{}, func(method string, p map[string]any) string {
		if p["chat_id"] == float64(-100) {
			return `{"ok":false,"error_code":400,"description":"Bad Request: group chat was upgraded to a supergroup chat","parameters":{"migrate_to_chat_id":-1001234}}`
		}
		return tgOK
	})
	ctx := context.Background()
	if err := tg.deliver(ctx, event(model.EventOpen, model.KindDown)); err != nil {
		t.Fatal(err)
	}
	r := reqs()
	if len(r) != 2 || r[1]["chat_id"] != float64(-1001234) {
		t.Fatalf("the card must be resent to the supergroup: %v", r)
	}
	ev := event(model.EventOpen, model.KindDown)
	ev.Incident.ID = "i2"
	tg.deliver(ctx, ev)
	if r := reqs(); len(r) != 1 || r[0]["chat_id"] != float64(-1001234) {
		t.Fatalf("the next card goes straight to the supergroup: %v", r)
	}
	if got := tg.ChatMigrations(); got[-100] != -1001234 {
		t.Fatalf("migration not exposed: %v", got)
	}
}

// A topic deleted from the forum must not swallow alerts: they go to the
// chat itself.
func TestDeletedTopicFallsBackToChat(t *testing.T) {
	tg, reqs := fakeAPI(t, TelegramConfig{Routes: map[string]Route{EventDown: {Topic: 5}}}, func(method string, p map[string]any) string {
		if _, ok := p["message_thread_id"]; ok {
			return `{"ok":false,"error_code":400,"description":"Bad Request: message thread not found"}`
		}
		return tgOK
	})
	if err := tg.deliver(context.Background(), event(model.EventOpen, model.KindDown)); err != nil {
		t.Fatal(err)
	}
	r := reqs()
	if len(r) != 2 || r[1]["chat_id"] != float64(-100) || r[1]["message_thread_id"] != nil {
		t.Fatalf("the card must go to the chat without the topic: %v", r)
	}
}

// api.telegram.org unreachable for longer than one delivery's deadline must
// not cost alerts: they wait, one per incident with only its latest state,
// and go out once the API answers. The last error is visible meanwhile.
func TestUnreachableAPIKeepsLatestStatePerIncident(t *testing.T) {
	defer func(d, b, p, a time.Duration) { callDeadline, callBackoff, parkRetry, offlineAttempt = d, b, p, a }(
		callDeadline, callBackoff, parkRetry, offlineAttempt)
	callDeadline, callBackoff, parkRetry, offlineAttempt = 50*time.Millisecond, 10*time.Millisecond, 20*time.Millisecond, time.Second
	var down atomic.Bool
	down.Store(true)
	tg, reqs := fakeAPI(t, TelegramConfig{}, func(method string, p map[string]any) string {
		if down.Load() {
			return `{"ok":false,"error_code":502,"description":"Bad Gateway"}`
		}
		return tgOK
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tg.Run(ctx)
	other := event(model.EventOpen, model.KindDown)
	other.Incident.ID, other.Incident.Name = "j", "other"
	for _, ev := range []model.Event{event(model.EventOpen, model.KindDown), other, event(model.EventUpdate, model.KindDown), event(model.EventResolve, model.KindDown)} {
		tg.Notify(ev)
	}
	wait := func(cond func(DeliveryStatus) bool) DeliveryStatus {
		deadline := time.Now().Add(5 * time.Second)
		for {
			s := tg.Status()
			if cond(s) || time.Now().After(deadline) {
				return s
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if s := wait(func(s DeliveryStatus) bool { return s.Waiting == 2 }); s.Waiting != 2 || !strings.Contains(s.LastError, "Bad Gateway") {
		t.Fatalf("while the API is down: %+v", s)
	}
	reqs()
	down.Store(false)
	if s := wait(func(s DeliveryStatus) bool { return s.Waiting == 0 && !s.LastOK.IsZero() }); s.Waiting != 0 {
		t.Fatalf("after the API is back: %+v", s)
	}
	var sent []string
	for _, r := range reqs() {
		if r["_method"] == "sendRichMessage" || r["_method"] == "sendMessage" {
			html, _ := r["rich_message"].(map[string]any)["html"].(string)
			sent = append(sent, html)
		}
	}
	if len(sent) != 2 || !strings.Contains(sent[0], "Recovered") || !strings.Contains(sent[1], "other") {
		t.Fatalf("one message per incident, in its latest state: %q", sent)
	}
}

// URLs carry secrets (proxy passwords, webhook tokens) and Go puts the URL
// into its parse errors, which end up in logs and the settings page.
func TestBadURLErrorsHideSecrets(t *testing.T) {
	_, err := NewTelegram(TelegramConfig{Token: "1:x", Proxy: "http://user:s3cret@proxy.example.com:bad"}, nil, nil)
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("proxy error: %v", err)
	}
	tg, err := NewTelegram(TelegramConfig{Token: "1:s3cret", APIURL: "https://api.example.com:bad", ChatID: 1, DataDir: t.TempDir()},
		&Formatter{Lang: "en", Loc: time.UTC}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if err := tg.Send(context.Background(), "x", true); err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("bot API URL error: %v", err)
	}
	w := NewWebhook(WebhookConfig{URL: "https://hooks.example.com:bad/T0KEN-s3cret"}, &Formatter{Lang: "en", Loc: time.UTC}, slog.New(slog.DiscardHandler))
	err = w.post(context.Background(), []byte(`{}`))
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("webhook error: %v", err)
	}
}

// Plain text for webhooks has no markup left, whatever tags a card or a
// custom template produced, and entities are decoded.
func TestPlainStripsEveryTag(t *testing.T) {
	f := &Formatter{Lang: "en", Loc: time.UTC}
	in := `<b>a</b> <blockquote expandable>b</blockquote> <u>c</u> <s>d</s> <pre>e</pre> <tg-spoiler>f</tg-spoiler>` +
		` <pre><code class="language-go">g</code></pre> <a href="https://x">h</a> <ins>i</ins> &lt;tag&gt; &amp; &quot;q&quot;`
	want := `a b c d e f g h i <tag> & "q"`
	if got := f.Plain(in); got != want {
		t.Fatalf("Plain:\n got %q\nwant %q", got, want)
	}
}

// A 429 says when to come back; the webhook waits that long, not its own
// short backoff, and then delivers.
func TestWebhookHonoursRetryAfter(t *testing.T) {
	defer func(b time.Duration) { webhookBackoff = b }(webhookBackoff)
	webhookBackoff = time.Millisecond
	var first time.Time
	var n atomic.Int32
	var gap atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			first = time.Now()
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		gap.Store(int64(time.Since(first)))
	}))
	defer srv.Close()
	w := NewWebhook(WebhookConfig{URL: srv.URL}, &Formatter{Lang: "en", Loc: time.UTC}, slog.New(slog.DiscardHandler))
	if err := w.deliver(context.Background(), event(model.EventOpen, model.KindDown)); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 2 || time.Duration(gap.Load()) < 900*time.Millisecond {
		t.Fatalf("%d requests, retried after %v", n.Load(), time.Duration(gap.Load()))
	}
}

// With a secret the receiver can check the payload came from this hub:
// X-UptimeForge-Signature is sha256=<hex HMAC-SHA256 of the body>.
func TestWebhookSignature(t *testing.T) {
	var sig, body atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sig.Store(r.Header.Get("X-UptimeForge-Signature"))
		body.Store(b)
	}))
	defer srv.Close()
	f := &Formatter{Lang: "en", Loc: time.UTC}
	w := NewWebhook(WebhookConfig{URL: srv.URL, Secret: "k3y"}, f, slog.New(slog.DiscardHandler))
	if err := w.deliver(context.Background(), event(model.EventOpen, model.KindDown)); err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte("k3y"))
	mac.Write(body.Load().([]byte))
	if want := "sha256=" + hex.EncodeToString(mac.Sum(nil)); sig.Load() != want {
		t.Fatalf("signature %q, want %q", sig.Load(), want)
	}
	w = NewWebhook(WebhookConfig{URL: srv.URL}, f, slog.New(slog.DiscardHandler))
	w.deliver(context.Background(), event(model.EventOpen, model.KindDown))
	if sig.Load() != "" {
		t.Fatalf("signed without a secret: %q", sig.Load())
	}
}

// Every reason the checks emit is worded in Telegram as in the UI
// (web/src/reason.ts), not left as the raw error.
func TestReasonTexts(t *testing.T) {
	cases := []struct {
		reason, err, ru, en string
	}{
		{"http_status", "HTTP 503", "ответ 503", "answered 503"},
		{"dns_mismatch", "got 10.0.0.1", "подмена DNS", "DNS spoofing"},
		{"egress", "exit 1.2.3.4", "не тот IP выхода", "wrong exit IP"},
		{"cert_expiring", "in 3 days", "сертификат скоро истечёт", "certificate expires soon"},
		{"config", "bad link", "ошибка конфигурации: bad link", "config error: bad link"},
		{"error", "boom", "ошибка: boom", "error: boom"},
		{"via", "dial upstream", "сломан upstream-прокси пробы", "probe's upstream proxy is broken"},
		{"egress_unknown", "", "не удалось определить IP выхода", "exit IP could not be determined"},
		{"panic", "", "проверка аварийно завершилась", "the check crashed"},
		{"subscription", "", "подписка недоступна", "subscription unavailable"},
	}
	for _, c := range cases {
		l := model.ProbeLine{Reason: c.reason, Error: c.err}
		for lang, want := range map[string]string{"ru": c.ru, "en": c.en} {
			f := &Formatter{Lang: lang, Loc: time.UTC}
			if got := f.reason(l); got != want {
				t.Errorf("%s/%s: %q, want %q", lang, c.reason, got, want)
			}
		}
	}
}

// A 409 from getUpdates (another app polled the same token, often for a
// moment during a redeploy) must not switch the buttons off until restart.
func TestButtonsSurviveConflict(t *testing.T) {
	old := pollRetry
	pollRetry = 10 * time.Millisecond
	defer func() { pollRetry = old }()
	var conflicts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/getUpdates") {
			io.WriteString(w, `{"ok":true,"result":true}`)
			return
		}
		if conflicts.Add(1) <= 3 {
			io.WriteString(w, `{"ok":false,"error_code":409,"description":"Conflict: terminated by other getUpdates request"}`)
			return
		}
		var p struct{ Offset int }
		json.NewDecoder(r.Body).Decode(&p)
		if p.Offset > 1 {
			time.Sleep(50 * time.Millisecond)
			io.WriteString(w, `{"ok":true,"result":[]}`)
			return
		}
		io.WriteString(w, `{"ok":true,"result":[{"update_id":1,"callback_query":{"id":"1","data":"ack:i1","from":{"id":7},"message":{"chat":{"id":-100}}}}]}`)
	}))
	defer srv.Close()
	tg, err := NewTelegram(TelegramConfig{Token: "1:x", ChatID: -100, APIURL: srv.URL, DataDir: t.TempDir()},
		&Formatter{Lang: "en", Loc: time.UTC}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	act := &syncActions{}
	tg.SetActions(act)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tg.Run(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for act.acks() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if act.acks() != 1 {
		t.Fatal("buttons stayed off after a 409")
	}
}

type syncActions struct {
	mu sync.Mutex
	n  int
}

func (s *syncActions) Ack(string, string) error                 { s.mu.Lock(); s.n++; s.mu.Unlock(); return nil }
func (s *syncActions) Resolve(string, string) error             { return nil }
func (s *syncActions) Mute(string, time.Duration, string) error { return nil }
func (s *syncActions) acks() int                                { s.mu.Lock(); defer s.mu.Unlock(); return s.n }

// A settings change builds the new notifier while the old one still runs, so
// telegram.json read at construction can miss a card the old one posted
// since. The cards are handed over in memory: the next update edits the
// card instead of posting a second one.
func TestHandoverKeepsCards(t *testing.T) {
	old, calls := fakeBot(t, TelegramConfig{})
	next, err := NewTelegram(old.cfg, old.f, slog.New(slog.DiscardHandler)) // built while old still runs
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go old.Run(ctx)
	old.Notify(event(model.EventOpen, model.KindDown))
	if c := pollCalls(calls, func(c []tgCall) bool { return len(c) > 0 }); len(c) != 1 || c[0].method != "sendMessage" {
		t.Fatalf("the card: %+v", c)
	}
	cancel()
	next.Handover(old)
	next.deliver(context.Background(), event(model.EventUpdate, model.KindDown))
	if c := calls(); len(c) != 1 || c[0].method != "editMessageText" {
		t.Fatalf("the update after a rebuild must edit the old card: %+v", c)
	}
}
