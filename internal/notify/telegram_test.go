package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/model"
)

func TestFindChats(t *testing.T) {
	const token = "123:SECRET"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bot" + token + "/getMe":
			w.Write([]byte(`{"ok":true,"result":{"username":"uf_bot"}}`))
		case "/bot" + token + "/getUpdates":
			w.Write([]byte(`{"ok":true,"result":[
				{"message":{"chat":{"id":-1001,"title":"Ops","type":"supergroup"}}},
				{"message":{"chat":{"id":-1001,"title":"Ops","type":"supergroup"}}},
				{"my_chat_member":{"chat":{"id":-1002,"title":"Alerts","type":"channel"}}},
				{"message":{"chat":{"id":42,"first_name":"Leo","type":"private"}}},
				{"callback_query":{"id":"1"}}
			]}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"ok":false,"error_code":401,"description":"Unauthorized"}`))
		}
	}))
	defer srv.Close()

	bot, chats, err := FindChats(context.Background(), TelegramConfig{Token: token, APIURL: srv.URL, DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if bot != "uf_bot" || len(chats) != 3 || chats[0].Title != "Alerts" || chats[1].Title != "Leo" || chats[2].ID != -1001 {
		t.Fatalf("bot %q, chats %+v", bot, chats)
	}

	_, _, err = FindChats(context.Background(), TelegramConfig{Token: "bad", APIURL: srv.URL, DataDir: t.TempDir()})
	if err == nil || strings.Contains(err.Error(), "bad") {
		t.Fatalf("a bad token must fail without echoing it: %v", err)
	}

	// A dead API must not leak the token through the error of the HTTP client.
	srv.Close()
	_, _, err = FindChats(context.Background(), TelegramConfig{Token: token, APIURL: srv.URL, DataDir: t.TempDir()})
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("token leaked or no error: %v", err)
	}
}

type tgCall struct {
	method string
	chat   int64
	topic  int
	reply  bool
	silent bool
	text   string
	rich   bool // sent or edited as a rich message
}

func fakeBot(t *testing.T, cfg TelegramConfig) (*Telegram, func() []tgCall) {
	return fakeBotRich(t, cfg, false)
}

// fakeBotRich is a fake Bot API; refuseRich makes it an older server that has
// no sendRichMessage.
func fakeBotRich(t *testing.T, cfg TelegramConfig, refuseRich bool) (*Telegram, func() []tgCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []tgCall
	id := 100
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Chat   int64           `json:"chat_id"`
			Topic  int             `json:"message_thread_id"`
			Reply  json.RawMessage `json:"reply_parameters"`
			Silent bool            `json:"disable_notification"`
			Text   string          `json:"text"`
			Rich   *struct {
				HTML string `json:"html"`
			} `json:"rich_message"`
		}
		json.NewDecoder(r.Body).Decode(&p)
		mu.Lock()
		defer mu.Unlock()
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if refuseRich && method == "sendRichMessage" {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"ok":false,"error_code":404,"description":"Not Found"}`))
			return
		}
		c := tgCall{method, p.Chat, p.Topic, p.Reply != nil, p.Silent, p.Text, p.Rich != nil}
		if c.method == "sendRichMessage" { // a card either way: tests read the text
			c.method = "sendMessage"
		}
		if p.Rich != nil {
			c.text = p.Rich.HTML
		}
		calls = append(calls, c)
		id++
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d}}`, id)
	}))
	t.Cleanup(srv.Close)
	cfg.Token, cfg.APIURL, cfg.DataDir = "1:abc", srv.URL, t.TempDir()
	if cfg.ChatID == 0 {
		cfg.ChatID = -100
	}
	tg, err := NewTelegram(cfg, &Formatter{Lang: "ru", Loc: time.UTC}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return tg, func() []tgCall {
		mu.Lock()
		defer mu.Unlock()
		out := calls
		calls = nil
		return out
	}
}

func event(typ model.EventType, kind model.Kind) model.Event {
	inc := model.Incident{ID: "i", Key: "c", Name: "c", Kind: kind, Worst: kind, Severity: model.Critical, Started: time.Now()}
	if typ == model.EventResolve {
		inc.Resolved = time.Now()
	}
	return model.Event{Type: typ, Incident: inc}
}

func TestRoutesSendEachEventToItsTopic(t *testing.T) {
	tg, calls := fakeBot(t, TelegramConfig{Routes: map[string]Route{
		EventPartial:  {Topic: 5},
		EventDown:     {Topic: 7},
		EventResolved: {Topic: 9, Silent: true},
		EventRemind:   {ChatID: -200, Topic: 3},
	}})
	ctx := context.Background()

	tg.deliver(ctx, event(model.EventOpen, model.KindPartial))
	if c := calls(); len(c) != 1 || c[0].topic != 5 || c[0].chat != -100 {
		t.Fatalf("open: %+v", c)
	}

	// It got worse, and down has its own topic: the old card is updated and a new one appears there.
	up := event(model.EventUpdate, model.KindDown)
	up.Escalated = true
	tg.deliver(ctx, up)
	c := calls()
	if len(c) != 2 || c[0].method != "editMessageText" || c[1].method != "sendMessage" || c[1].topic != 7 || c[1].reply {
		t.Fatalf("escalate: %+v", c)
	}

	tg.deliver(ctx, event(model.EventRemind, model.KindDown))
	if c := calls(); len(c) != 1 || c[0].chat != -200 || c[0].topic != 3 || c[0].reply {
		t.Fatalf("remind goes to its own chat, a reply cannot cross chats: %+v", c)
	}

	tg.deliver(ctx, event(model.EventResolve, model.KindDown))
	c = calls()
	if len(c) != 2 || c[0].method != "editMessageText" || c[1].topic != 9 || c[1].reply || !c[1].silent || !strings.Contains(c[1].text, "Восстановлено") {
		t.Fatalf("resolve: %+v", c)
	}
}

func TestRouteOffStaysQuiet(t *testing.T) {
	tg, calls := fakeBot(t, TelegramConfig{Routes: map[string]Route{EventPartial: {Off: true}, EventRemind: {Off: true}}})
	ctx := context.Background()
	tg.deliver(ctx, event(model.EventOpen, model.KindPartial))
	tg.deliver(ctx, event(model.EventRemind, model.KindPartial))
	tg.deliver(ctx, event(model.EventResolve, model.KindPartial))
	if c := calls(); len(c) != 0 {
		t.Fatalf("nobody was told it broke, so nobody hears it recovered: %+v", c)
	}

	// Partial is off but it became down: that is not lost, and its recovery is told.
	up := event(model.EventUpdate, model.KindDown)
	up.Escalated = true
	tg.deliver(ctx, up)
	if c := calls(); len(c) != 1 || c[0].method != "sendMessage" {
		t.Fatalf("escalated out of a muted kind: %+v", c)
	}
	tg.deliver(ctx, event(model.EventResolve, model.KindDown))
	if c := calls(); len(c) != 2 {
		t.Fatalf("resolve: %+v", c)
	}
}

func TestSameTopicRepliesToTheCard(t *testing.T) {
	tg, calls := fakeBot(t, TelegramConfig{Routes: map[string]Route{EventDown: {Topic: 7}, EventResolved: {Topic: 7}, EventRemind: {Topic: 7}}})
	ctx := context.Background()
	tg.deliver(ctx, event(model.EventOpen, model.KindDown))
	calls()
	tg.deliver(ctx, event(model.EventRemind, model.KindDown))
	if c := calls(); len(c) != 1 || !c[0].reply {
		t.Fatalf("remind: %+v", c)
	}
	tg.deliver(ctx, event(model.EventResolve, model.KindDown))
	if c := calls(); len(c) != 2 || !c[1].reply {
		t.Fatalf("resolve: %+v", c)
	}
}

func TestChatsAndTopicsAreCollected(t *testing.T) {
	seen := map[int64]*Chat{}
	var ups []update
	if err := json.Unmarshal([]byte(`[
		{"message":{"chat":{"id":-1,"title":"Ops","type":"supergroup"},"message_thread_id":5,"is_topic_message":true,"forum_topic_created":{"name":"Сбои"}}},
		{"message":{"chat":{"id":-1,"title":"Ops","type":"supergroup"},"message_thread_id":5,"is_topic_message":true}},
		{"message":{"chat":{"id":-1,"title":"Ops","type":"supergroup"},"message_thread_id":9,"is_topic_message":true,"reply_to_message":{"forum_topic_created":{"name":"Восстановления"}}}},
		{"message":{"chat":{"id":-1,"title":"Ops","type":"supergroup"},"message_thread_id":77}}
	]`), &ups); err != nil {
		t.Fatal(err)
	}
	for _, u := range ups {
		collectChats(seen, u)
	}
	c := seen[-1]
	if c == nil || len(c.Topics) != 2 || c.Topics[0] != (Topic{5, "Сбои"}) || c.Topics[1] != (Topic{9, "Восстановления"}) {
		t.Fatalf("%+v", c)
	}
}

func TestSendTestGoesToTheRoute(t *testing.T) {
	tg, calls := fakeBot(t, TelegramConfig{Routes: map[string]Route{EventDown: {Topic: 7}}})
	if err := tg.SendTest(context.Background(), EventDown); err != nil {
		t.Fatal(err)
	}
	c := calls()
	if len(c) != 1 || c[0].topic != 7 || !strings.Contains(c[0].text, "Не работает") {
		t.Fatalf("%+v", c)
	}
	if err := tg.SendTest(context.Background(), "nope"); err == nil {
		t.Fatal("unknown event accepted")
	}
}

// Cards go out as rich messages and are edited as rich messages.
func TestCardsAreRich(t *testing.T) {
	tg, calls := fakeBot(t, TelegramConfig{})
	ctx := context.Background()
	tg.deliver(ctx, event(model.EventOpen, model.KindDown))
	tg.deliver(ctx, event(model.EventRefresh, model.KindDown))
	tg.deliver(ctx, event(model.EventResolve, model.KindDown))
	c := calls()
	if len(c) < 3 || !c[0].rich || c[0].method != "sendMessage" || !strings.Contains(c[0].text, "<h3>") {
		t.Fatalf("open must be a rich card: %+v", c)
	}
	if c[1].method != "editMessageText" || !c[1].rich || c[2].method != "editMessageText" || !c[2].rich {
		t.Fatalf("a rich card is edited as rich: %+v", c)
	}
}

// A Bot API server without rich messages gets plain HTML, and after the first
// refusal it is not asked again.
func TestRichFallsBackToHTML(t *testing.T) {
	tg, calls := fakeBotRich(t, TelegramConfig{}, true)
	ctx := context.Background()
	tg.deliver(ctx, event(model.EventOpen, model.KindDown))
	c := calls()
	if len(c) != 1 || c[0].rich || c[0].method != "sendMessage" || !strings.Contains(c[0].text, "<b>") {
		t.Fatalf("fallback card: %+v", c)
	}
	if !tg.richOff.Load() {
		t.Fatal("an unknown method switches rich off")
	}
	tg.deliver(ctx, event(model.EventRefresh, model.KindDown))
	if c := calls(); len(c) != 1 || c[0].rich {
		t.Fatalf("a plain card is edited as plain: %+v", c)
	}
}

type fakeActions struct{ resolved, acked []string }

func (f *fakeActions) Ack(id, by string) error                  { f.acked = append(f.acked, id+" "+by); return nil }
func (f *fakeActions) Mute(string, time.Duration, string) error { return nil }
func (f *fakeActions) Resolve(id, by string) error {
	f.resolved = append(f.resolved, id+" "+by)
	return nil
}

// The card carries Acknowledge and Resolved; pressing Resolved closes the incident.
func TestResolveButton(t *testing.T) {
	tg, _ := fakeBot(t, TelegramConfig{Admins: []int64{7}}) // closing from the bot needs an admin list
	act := &fakeActions{}
	tg.SetActions(act)
	kb := tg.keyboard(&model.Incident{ID: "i42"})
	rows := kb.(map[string]any)["inline_keyboard"].([][]button)
	if len(rows) != 2 || rows[0][0].Data != "ack:i42" || rows[0][1].Data != "resolve:i42" || rows[0][1].Style != "success" {
		t.Fatalf("keyboard: %+v", rows)
	}
	var u update
	json.Unmarshal([]byte(`{"callback_query":{"id":"1","data":"resolve:i42","from":{"id":7,"username":"leo"},"message":{"chat":{"id":-100}}}}`), &u)
	tg.handleCallback(context.Background(), u)
	if len(act.resolved) != 1 || act.resolved[0] != "i42 @leo" {
		t.Fatalf("resolve not called: %+v", act.resolved)
	}
}

func tagged(typ model.EventType, kind model.Kind, sev model.Severity, tags ...string) model.Event {
	ev := event(typ, kind)
	ev.Incident.Severity, ev.Incident.Tags = sev, tags
	return ev
}

// A rule by tag beats the event's route; without the tag the route stands.
func TestRuleByTagBeatsEventRoute(t *testing.T) {
	tg, calls := fakeBot(t, TelegramConfig{
		Routes: map[string]Route{EventDown: {Topic: 10}},
		Rules:  []Rule{{Tags: []string{"critical"}, Route: Route{Topic: 77}}},
	})
	ctx := context.Background()
	tg.deliver(ctx, tagged(model.EventOpen, model.KindDown, model.Critical, "critical", "de"))
	if c := calls(); len(c) != 1 || c[0].topic != 77 {
		t.Fatalf("tagged: %+v", c)
	}
	ev := tagged(model.EventOpen, model.KindDown, model.Critical, "de")
	ev.Incident.ID = "other"
	tg.deliver(ctx, ev)
	if c := calls(); len(c) != 1 || c[0].topic != 10 {
		t.Fatalf("untagged: %+v", c)
	}
}

// Rules are checked in order and the first that matches wins.
func TestRulesInOrderFirstWins(t *testing.T) {
	tg, calls := fakeBot(t, TelegramConfig{Rules: []Rule{
		{Groups: []string{"VPN"}, Route: Route{Topic: 1}},
		{Tags: []string{"de"}, Route: Route{Topic: 2}},
	}})
	ev := tagged(model.EventOpen, model.KindDown, model.Critical, "de")
	ev.Incident.Group = "VPN"
	tg.deliver(context.Background(), ev)
	if c := calls(); len(c) != 1 || c[0].topic != 1 {
		t.Fatalf("first rule: %+v", c)
	}
}

// A rule for critical incidents does not catch warnings; events narrow it too.
func TestRuleMinSeverityAndEvents(t *testing.T) {
	tg, calls := fakeBot(t, TelegramConfig{Rules: []Rule{
		{MinSeverity: model.Critical, Route: Route{Topic: 5}},
		{Events: []string{EventPartial}, Route: Route{Topic: 6}},
	}})
	ctx := context.Background()
	tg.deliver(ctx, tagged(model.EventOpen, model.KindPartial, model.Warning))
	if c := calls(); len(c) != 1 || c[0].topic != 6 {
		t.Fatalf("warning partial goes past the critical rule to the partial one: %+v", c)
	}
	ev := tagged(model.EventOpen, model.KindHost, model.Warning)
	ev.Incident.ID = "h"
	tg.deliver(ctx, ev)
	if c := calls(); len(c) != 1 || c[0].topic != 0 {
		t.Fatalf("no rule matches a warning host alert: %+v", c)
	}
}
