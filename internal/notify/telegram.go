package notify

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/model"
)

type TelegramConfig struct {
	Token   string
	ChatID  int64
	Topics  map[string]int   // severity -> message_thread_id ("default" as fallback)
	Routes  map[string]Route // event -> where it goes; absent = the main chat
	Rules   []Rule           // checked in order before Routes; the first match wins
	APIURL  string
	Proxy   string
	Admins  []int64
	DataDir string
}

// Telegram posts one message per incident and keeps editing it as the
// incident evolves, so a long outage is one message, not fifty. Loud
// follow-ups (escalation, recovery, reminders) are replies to it.
// Events a route can be set for. The first eight name what an incident is about,
// the last two what happened to it.
const (
	EventDown         = "down"
	EventBlocked      = "blocked"
	EventPartial      = "partial"
	EventProbeOffline = "probe_offline"
	EventProbeNoNet   = "probe_no_net"
	EventSubscription = "subscription"
	EventHost         = "host"
	EventUsersDrop    = "users_drop"
	EventResolved     = "resolved"
	EventRemind       = "remind"
	EventReport       = "report" // the weekly report
)

// Events lists every routable event.
var Events = []string{EventDown, EventBlocked, EventPartial, EventProbeOffline, EventProbeNoNet, EventSubscription, EventHost, EventUsersDrop, EventResolved, EventRemind, EventReport}

// Route says where one kind of event goes. Zero values mean the main chat, no
// topic, on, with sound.
type Route struct {
	Off    bool
	ChatID int64
	Topic  int
	Silent bool
}

// msgRef is where an incident's card lives, which its route may no longer say.
type msgRef struct {
	ID    int   `json:"id"`
	Chat  int64 `json:"chat"`
	Topic int   `json:"topic,omitempty"`
	Rich  bool  `json:"rich,omitempty"` // sent as a rich message, so edited as one
}

type Telegram struct {
	cfg     TelegramConfig
	f       *Formatter
	log     *slog.Logger
	client  *http.Client
	queue   chan model.Event
	actions model.Actions

	done    chan struct{} // closed when Run returns
	backlog []model.Event // handed over by the previous instance, delivered first
	left    []model.Event // cut off by shutdown, for the next instance

	// richOff is set once the Bot API server turns out not to know rich
	// messages (an old mirror or self-hosted server): plain HTML from then on.
	richOff atomic.Bool

	// Events the API could not take within a delivery's deadline, one per
	// incident in its latest state, oldest first. Only Run touches them
	// (and Undelivered once Run is over).
	pending   map[string]model.Event
	order     []string
	retryWait time.Duration

	mu       sync.Mutex
	refs     map[string]msgRef // incident id -> its card
	seen     map[int64]*Chat   // chats and topics seen since start, offered in the settings
	migrated map[int64]int64   // groups that became supergroups: old chat id -> new
	status   DeliveryStatus
}

// DeliveryStatus is how alert delivery is going, for the API to show.
type DeliveryStatus struct {
	LastError   string    `json:"last_error,omitempty"` // never carries the token
	LastErrorAt time.Time `json:"last_error_at,omitzero"`
	LastOK      time.Time `json:"last_ok,omitzero"`
	Waiting     int       `json:"waiting"` // incidents whose alerts wait for the API to come back
}

// Status reports the last delivery error and success, and what waits.
func (t *Telegram) Status() DeliveryStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.status
}

func (t *Telegram) delivered(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err == nil {
		t.status.LastOK = time.Now()
		return
	}
	t.status.LastError, t.status.LastErrorAt = err.Error(), time.Now()
}

// Timing of delivery; vars for tests.
var (
	callDeadline   = 5 * time.Minute  // one call keeps retrying this long
	callBackoff    = 2 * time.Second  // its first wait between attempts
	parkRetry      = 30 * time.Second // first wait before retrying parked events; doubles up to parkRetryMax
	offlineAttempt = time.Minute      // a retry of a parked event gets this long
)

const parkRetryMax = 5 * time.Minute

// retryable: the API did not answer, or asked to come back later. Anything
// else is an answer, and resending the same request gets the same one.
func retryable(err error) bool {
	if err == nil {
		return false
	}
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Code == 429 || ae.Code >= 500
	}
	return true
}

func NewTelegram(cfg TelegramConfig, f *Formatter, log *slog.Logger) (*Telegram, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.Proxy != "" {
		u, err := url.Parse(cfg.Proxy)
		if err != nil {
			return nil, fmt.Errorf("telegram proxy: %w", withoutURL(err))
		}
		tr.Proxy = http.ProxyURL(u)
	}
	t := &Telegram{cfg: cfg, f: f, log: log, client: &http.Client{Transport: tr, Timeout: 70 * time.Second},
		queue: make(chan model.Event, 1000), done: make(chan struct{}), refs: map[string]msgRef{}, seen: map[int64]*Chat{}, migrated: map[int64]int64{},
		pending: map[string]model.Event{}}
	if b, err := os.ReadFile(t.refsPath()); err == nil {
		json.Unmarshal(b, &t.refs)
	}
	return t, nil
}

func (t *Telegram) SetActions(a model.Actions) { t.actions = a }

func (t *Telegram) refsPath() string { return filepath.Join(t.cfg.DataDir, "telegram.json") }

// FirstRun is true until the bot has posted anything from this data dir.
func (t *Telegram) FirstRun() bool {
	_, err := os.Stat(t.refsPath())
	return os.IsNotExist(err)
}

func (t *Telegram) saveRefs() {
	b, _ := json.Marshal(t.refs)
	tmp := t.refsPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err == nil {
		os.Rename(tmp, t.refsPath())
	}
}

// Notify enqueues without blocking the hub.
func (t *Telegram) Notify(ev model.Event) {
	select {
	case t.queue <- ev:
	default:
		t.log.Error("telegram queue is full, dropping event", "incident", ev.Incident.ID, "type", ev.Type)
	}
}

// Run delivers queued events and handles button presses until ctx ends.
func (t *Telegram) Run(ctx context.Context) {
	defer close(t.done)
	if t.actions != nil {
		go t.pollUpdates(ctx)
	}
	retry := time.NewTimer(time.Hour)
	retry.Stop()
	defer retry.Stop()
	for {
		if len(t.backlog) > 0 {
			var ev model.Event
			ev, t.backlog = t.backlog[0], t.backlog[1:]
			t.handle(ctx, ev, retry)
			continue
		}
		var retryC <-chan time.Time
		if len(t.order) > 0 {
			retryC = retry.C
		}
		select {
		case <-ctx.Done():
			return
		case ev := <-t.queue:
			t.handle(ctx, ev, retry)
		case <-retryC:
			t.retryParked(ctx, retry)
		}
	}
}

// handle delivers one event. While the API is unreachable it does not even
// try (each try would take callDeadline): the event waits with the others.
func (t *Telegram) handle(ctx context.Context, ev model.Event, retry *time.Timer) {
	if len(t.order) > 0 {
		t.park(ev)
		return
	}
	err := t.deliver(ctx, ev)
	switch {
	case err == nil:
	case ctx.Err() != nil:
		t.left = append(t.left, ev)
	case retryable(err):
		t.log.Error("telegram API unreachable; alerts wait and are retried, the latest state of each incident goes out",
			"incident", ev.Incident.ID, "err", err)
		t.park(ev)
		t.retryWait = parkRetry
		retry.Reset(t.retryWait)
	}
}

// park keeps an event the API could not take, merged with what already waits
// for its incident: only the latest state matters.
func (t *Telegram) park(ev model.Event) {
	id := ev.Incident.ID
	if old, ok := t.pending[id]; ok {
		ev = mergeEvents(old, ev)
	} else {
		t.order = append(t.order, id)
	}
	t.pending[id] = ev
	t.mu.Lock()
	t.status.Waiting = len(t.order)
	t.mu.Unlock()
}

// mergeEvents folds a newer event of an incident into one still waiting.
func mergeEvents(old, ev model.Event) model.Event {
	ev.Silent = ev.Silent && old.Silent // anything loud keeps it loud
	update := func(e model.Event) bool { return e.Type == model.EventUpdate || e.Type == model.EventRefresh }
	switch {
	case ev.Type == model.EventRemind && old.Type != model.EventRemind:
		old.Incident, old.Silent = ev.Incident, ev.Silent // the state to tell is old's, with today's numbers
		return old
	case old.Type == model.EventOpen && update(ev):
		ev.Type = model.EventOpen // never told yet
	case update(old) && update(ev):
		ev.Escalated = ev.Escalated || old.Escalated
	}
	return ev
}

// retryParked delivers what waits, oldest first, until the API fails again.
func (t *Telegram) retryParked(ctx context.Context, retry *time.Timer) {
	for len(t.order) > 0 {
		id := t.order[0]
		dctx, cancel := context.WithTimeout(ctx, offlineAttempt)
		err := t.deliver(dctx, t.pending[id])
		cancel()
		if ctx.Err() != nil {
			return // shutting down: what waits goes to the next instance
		}
		if retryable(err) || errors.Is(err, context.DeadlineExceeded) {
			t.retryWait = min(t.retryWait*2, parkRetryMax)
			retry.Reset(t.retryWait)
			return
		}
		delete(t.pending, id)
		t.order = t.order[1:]
		t.mu.Lock()
		t.status.Waiting = len(t.order)
		t.mu.Unlock()
	}
	t.log.Info("telegram API reachable again, waiting alerts delivered")
}

// Adopt queues events handed over by a previous instance ahead of anything
// new. Call it before Run.
func (t *Telegram) Adopt(evs []model.Event) { t.backlog = append(t.backlog, evs...) }

// Handover takes over from the instance this one replaces: its undelivered
// events, and its cards and the chats it saw, from memory. telegram.json was
// read when this instance was built, while old was still posting, so it
// may miss cards. Cancel old's context first; call it before Run.
func (t *Telegram) Handover(old *Telegram) {
	t.Adopt(old.Undelivered())
	old.mu.Lock()
	refs, migrated, seen := maps.Clone(old.refs), maps.Clone(old.migrated), make(map[int64]*Chat, len(old.seen))
	for id, c := range old.seen {
		cc := *c
		cc.Topics = slices.Clone(c.Topics)
		seen[id] = &cc
	}
	old.mu.Unlock()
	t.mu.Lock()
	t.refs = refs
	maps.Copy(t.migrated, migrated)
	for id, c := range seen {
		if _, ok := t.seen[id]; !ok {
			t.seen[id] = c
		}
	}
	t.mu.Unlock()
}

// Undelivered waits for Run to return (cancel its context first) and gives
// back what it did not deliver, oldest first: an alert cut off mid-retry and
// everything still queued. A settings change rebuilds the notifiers; this is
// how their alerts survive it.
func (t *Telegram) Undelivered() []model.Event {
	<-t.done
	evs := t.left
	for _, id := range t.order {
		evs = append(evs, t.pending[id])
	}
	evs = append(evs, t.backlog...)
	for {
		select {
		case ev := <-t.queue:
			evs = append(evs, ev)
		default:
			return evs
		}
	}
}

// Send posts a standalone message (e.g. the hello on first start).
func (t *Telegram) Send(ctx context.Context, text string, silent bool) error {
	_, err := t.send(ctx, t.route("", nil), text, silent, 0, nil)
	if err == nil {
		t.mu.Lock()
		t.saveRefs()
		t.mu.Unlock()
	}
	return err
}

// place is a resolved route: an actual chat and topic.
type place struct {
	chat   int64
	topic  int
	silent bool
	off    bool
}

// Rule sends incidents with given tags, groups, events or severity to their
// own place. Empty conditions match anything; all set ones must match.
type Rule struct {
	Tags, Groups, Events []string
	MinSeverity          model.Severity
	Route                Route
}

func (r Rule) matches(event string, inc *model.Incident) bool {
	if inc == nil {
		return false
	}
	if len(r.Tags) > 0 && !slices.ContainsFunc(inc.Tags, func(t string) bool { return slices.Contains(r.Tags, t) }) {
		return false
	}
	if len(r.Groups) > 0 && !slices.Contains(r.Groups, inc.Group) {
		return false
	}
	if len(r.Events) > 0 && !slices.Contains(r.Events, event) {
		return false
	}
	return r.MinSeverity != model.Critical || inc.Severity == model.Critical
}

// route is where an event about inc goes: the first matching rule, else the
// event's route, else the main chat (and the severity topic of old settings).
// inc is nil for messages about no incident (tests, the weekly report).
func (t *Telegram) route(event string, inc *model.Incident) place {
	p := t.routeCfg(event, inc)
	p.chat = t.chat(p.chat)
	return p
}

func (t *Telegram) routeCfg(event string, inc *model.Incident) place {
	p := place{chat: t.cfg.ChatID}
	for _, rule := range t.cfg.Rules {
		if rule.matches(event, inc) {
			return p.with(rule.Route)
		}
	}
	r, ok := t.cfg.Routes[event]
	if !ok {
		if inc != nil {
			p.topic = t.topic(inc.Severity) // settings from before routes existed
		}
		return p
	}
	return p.with(r)
}

// chat follows a group that became a supergroup to its new id.
func (t *Telegram) chat(id int64) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if to, ok := t.migrated[id]; ok {
		return to
	}
	return id
}

func (p place) with(r Route) place {
	if r.ChatID != 0 {
		p.chat = r.ChatID
	}
	p.topic, p.silent, p.off = r.Topic, r.Silent, r.Off
	return p
}

// eventOf is the route an incident's current state belongs to.
func eventOf(inc *model.Incident, k model.Kind) string {
	if inc.Type == "subscription" {
		return EventSubscription
	}
	if k == model.KindNodeDown { // a server gone, the same as an agent gone
		return EventProbeOffline
	}
	return string(k)
}

func (r msgRef) at(p place) bool { return r.ID != 0 && r.Chat == p.chat && r.Topic == p.topic }

func (t *Telegram) deliver(ctx context.Context, ev model.Event) error {
	inc := &ev.Incident
	now := time.Now()
	t.mu.Lock()
	ref := t.refs[inc.ID]
	t.mu.Unlock()

	here := t.route(eventOf(inc, inc.Kind), inc)
	post := func(p place, text string, replyTo int, kb any) (msgRef, error) {
		id, err := t.send(ctx, p, text, ev.Silent || p.silent, replyTo, kb)
		return msgRef{ID: id, Chat: p.chat, Topic: p.topic}, err
	}
	card := func() (msgRef, error) {
		return t.sendCard(ctx, here, t.f.RichIncident(inc, now), t.f.Incident(inc, now), ev.Silent || here.silent, t.keyboard(inc))
	}

	var err error
	switch ev.Type {
	case model.EventOpen:
		if !here.off {
			ref, err = card()
		}
	case model.EventUpdate, model.EventRefresh:
		switch {
		case ref.ID == 0:
			if !here.off {
				ref, err = card()
			}
		default:
			err = t.editCard(ctx, ref, t.f.RichIncident(inc, now), t.f.Incident(inc, now), t.keyboard(inc))
			if isGone(err) {
				ref, err = msgRef{}, nil
				if !here.off {
					ref, err = card()
				}
				break
			}
			if err != nil || !ev.Escalated || here.off {
				break
			}
			if ref.at(here) {
				_, err = post(here, t.f.EscalatedReply(inc), ref.ID, nil)
			} else { // it got worse and that has its own topic: the card moves there
				ref, err = card()
			}
		}
	case model.EventRemind:
		if p := t.route(EventRemind, inc); !p.off {
			replyTo := 0
			if ref.at(p) {
				replyTo = ref.ID
			}
			_, err = t.send(ctx, p, t.f.RemindReply(inc, now), p.silent, replyTo, nil)
		}
	case model.EventResolve:
		worst := inc.Worst
		if worst == "" {
			worst = inc.Kind
		}
		p := t.route(EventResolved, inc)
		wasSent := ref.ID != 0 || !t.route(eventOf(inc, worst), inc).off
		switch {
		case ref.ID == 0:
			if !p.off && wasSent { // the card is lost, but somebody was told it broke
				_, err = t.sendCard(ctx, p, t.f.RichResolved(inc), t.f.Resolved(inc), ev.Silent || p.silent, nil)
			}
		default:
			err = t.editCard(ctx, ref, t.f.RichResolved(inc), t.f.Resolved(inc), nil)
			gone := isGone(err)
			if err != nil && !gone {
				break
			}
			err = nil
			switch {
			case p.off:
			case gone || !ref.at(p):
				_, err = t.sendCard(ctx, p, t.f.RichResolved(inc), t.f.Resolved(inc), ev.Silent || p.silent, nil)
			default:
				_, err = post(p, t.f.ResolvedReply(inc), ref.ID, nil)
			}
		}
		ref = msgRef{}
	}
	if err != nil && ctx.Err() != nil {
		return err // shutting down or out of time: the event is retried as is
	}
	t.delivered(err)
	if retryable(err) {
		return err // the API did not answer: retried as is, the cards untouched
	}
	if err != nil {
		t.log.Error("telegram delivery failed", "incident", inc.ID, "event", ev.Type, "err", err)
	}

	t.mu.Lock()
	if ref.ID != 0 {
		t.refs[inc.ID] = ref
	} else {
		delete(t.refs, inc.ID)
	}
	t.saveRefs()
	t.mu.Unlock()
	return err
}

type button struct {
	Text  string `json:"text"`
	Data  string `json:"callback_data"`
	Style string `json:"style,omitempty"` // Bot API 9.4: success, danger, primary; older clients ignore it
}

func (t *Telegram) keyboard(inc *model.Incident) any {
	if t.actions == nil {
		return nil
	}
	var top []button
	if inc.AckBy == "" {
		top = append(top, button{Text: t.f.t("btn_ack"), Data: "ack:" + inc.ID, Style: "primary"})
	}
	top = append(top, button{Text: t.f.t("btn_resolve"), Data: "resolve:" + inc.ID, Style: "success"})
	mute := []button{{Text: t.f.t("btn_mute1"), Data: "mute:" + inc.ID + ":3600"}, {Text: t.f.t("btn_mute8"), Data: "mute:" + inc.ID + ":28800"}}
	return map[string]any{"inline_keyboard": [][]button{top, mute}}
}

type apiError struct {
	Code        int
	Description string
	RetryAfter  int
	MigrateTo   int64 // the supergroup a group became
}

// ChatMigrations are the groups that turned into supergroups since start:
// old chat id -> new. Alerts already go to the new ids; the settings still
// name the old ones.
func (t *Telegram) ChatMigrations() map[int64]int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return maps.Clone(t.migrated)
}

func (e *apiError) Error() string { return fmt.Sprintf("telegram: %d %s", e.Code, e.Description) }

// call performs a Bot API method, retrying on network errors and 429/5xx for
// up to ~5 minutes: an alert that arrives late beats one that never arrives.
// A group that became a supergroup is followed to its new id, and a topic
// that is gone falls back to the chat itself.
func (t *Telegram) call(ctx context.Context, method string, params map[string]any, out any) error {
	body, _ := json.Marshal(params)
	endpoint := t.endpoint(method)
	backoff := callBackoff
	deadline := time.Now().Add(callDeadline)
	for {
		err := t.callOnce(ctx, endpoint, body, out)
		var ae *apiError
		if errors.As(err, &ae) {
			if old, ok := params["chat_id"].(int64); ok && ae.MigrateTo != 0 && ae.MigrateTo != old {
				t.mu.Lock()
				t.migrated[old] = ae.MigrateTo
				t.mu.Unlock()
				t.log.Warn("telegram: the group became a supergroup, sending to its new id; update the chat id in the settings",
					"old", old, "new", ae.MigrateTo)
				params["chat_id"] = ae.MigrateTo
				body = mustJSON(params)
				continue
			}
			if _, ok := params["message_thread_id"]; ok && ae.Code == 400 && strings.Contains(ae.Description, "message thread not found") {
				t.log.Warn("telegram: the topic is gone, sending to the chat itself; fix the route in the settings",
					"chat", params["chat_id"], "topic", params["message_thread_id"])
				delete(params, "message_thread_id")
				body = mustJSON(params)
				continue
			}
		}
		retry := err != nil && (!errors.As(err, &ae) || ae.Code == 429 || ae.Code >= 500)
		if !retry || time.Now().After(deadline) || ctx.Err() != nil {
			return err
		}
		wait := backoff
		if ae != nil && ae.RetryAfter > 0 {
			wait = time.Duration(ae.RetryAfter) * time.Second
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

func (t *Telegram) callOnce(ctx context.Context, endpoint string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return &apiError{Description: "bad API URL: " + withoutURL(err).Error()} // the URL holds the token; resending will not help
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		// Never log the URL: it contains the bot token.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return err
	}
	defer resp.Body.Close()
	var r struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int   `json:"retry_after"`
			MigrateTo  int64 `json:"migrate_to_chat_id"`
		} `json:"parameters"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("telegram: HTTP %d: %w", resp.StatusCode, err)
	}
	if !r.OK {
		return &apiError{Code: r.ErrorCode, Description: r.Description, RetryAfter: r.Parameters.RetryAfter, MigrateTo: r.Parameters.MigrateTo}
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

// SendTest posts a sample to where an event goes, so a wrong chat or topic shows
// up now and not during an outage.
func (t *Telegram) SendTest(ctx context.Context, event string) error {
	if !slices.Contains(Events, event) {
		return fmt.Errorf("unknown event %q", event)
	}
	to := t.route(event, nil)
	if !t.richOff.Load() {
		p := map[string]any{"chat_id": to.chat, "rich_message": map[string]any{"html": t.f.RichTest(event)}}
		if to.topic != 0 {
			p["message_thread_id"] = to.topic
		}
		err := t.callOnce(ctx, t.endpoint("sendRichMessage"), mustJSON(p), nil)
		if !richRefused(err) {
			return err
		}
		t.richFallback(err)
	}
	_, err := t.sendOnce(ctx, to, t.f.Test(event))
	return err
}

// sendCard posts an incident card as a rich message, or as plain HTML when
// the Bot API server refuses rich ones. The ref remembers which, so edits go
// the same way.
func (t *Telegram) sendCard(ctx context.Context, to place, rich, html string, silent bool, kb any) (msgRef, error) {
	if !t.richOff.Load() {
		p := map[string]any{"chat_id": to.chat, "rich_message": map[string]any{"html": rich}, "disable_notification": silent}
		if to.topic != 0 {
			p["message_thread_id"] = to.topic
		}
		if kb != nil {
			p["reply_markup"] = kb
		}
		var msg struct {
			MessageID int `json:"message_id"`
		}
		err := t.call(ctx, "sendRichMessage", p, &msg)
		if err == nil {
			return msgRef{ID: msg.MessageID, Chat: to.chat, Topic: to.topic, Rich: true}, nil
		}
		if !richRefused(err) {
			return msgRef{}, err
		}
		t.richFallback(err)
	}
	id, err := t.send(ctx, to, html, silent, 0, kb)
	return msgRef{ID: id, Chat: to.chat, Topic: to.topic}, err
}

// editCard edits a card the way it was sent.
func (t *Telegram) editCard(ctx context.Context, ref msgRef, rich, html string, kb any) error {
	if !ref.Rich {
		return t.edit(ctx, ref, html, kb)
	}
	p := map[string]any{"chat_id": ref.Chat, "message_id": ref.ID, "rich_message": map[string]any{"html": rich}}
	if kb != nil {
		p["reply_markup"] = kb
	} else {
		p["reply_markup"] = map[string]any{"inline_keyboard": [][]button{}}
	}
	err := t.call(ctx, "editMessageText", p, nil)
	var ae *apiError
	if errors.As(err, &ae) && strings.Contains(ae.Description, "message is not modified") {
		return nil
	}
	return err
}

// richRefused: the server answered, and the answer was no to the rich form
// (an unknown method or content it would not take), so plain HTML is the way.
func richRefused(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && (ae.Code == 400 || ae.Code == 404)
}

// richFallback logs the refusal; an unknown method switches rich off for good.
func (t *Telegram) richFallback(err error) {
	var ae *apiError
	if errors.As(err, &ae) && (ae.Code == 404 || strings.Contains(strings.ToLower(ae.Description), "method")) {
		if !t.richOff.Swap(true) {
			t.log.Warn("telegram: this Bot API server has no rich messages, sending plain HTML", "err", err)
		}
		return
	}
	t.log.Warn("telegram: rich message refused, sent as plain HTML", "err", err)
}

func (t *Telegram) send(ctx context.Context, to place, text string, silent bool, replyTo int, kb any) (int, error) {
	p := map[string]any{
		"chat_id":              to.chat,
		"text":                 text,
		"parse_mode":           "HTML",
		"disable_notification": silent,
		"link_preview_options": map[string]any{"is_disabled": true},
	}
	if to.topic != 0 {
		p["message_thread_id"] = to.topic
	}
	if replyTo != 0 {
		p["reply_parameters"] = map[string]any{"message_id": replyTo, "allow_sending_without_reply": true}
	}
	if kb != nil {
		p["reply_markup"] = kb
	}
	var msg struct {
		MessageID int `json:"message_id"`
	}
	err := t.call(ctx, "sendMessage", p, &msg)
	return msg.MessageID, err
}

// sendOnce does not retry: a person is waiting for the answer.
func (t *Telegram) sendOnce(ctx context.Context, to place, text string) (int, error) {
	p := map[string]any{"chat_id": to.chat, "text": text, "parse_mode": "HTML"}
	if to.topic != 0 {
		p["message_thread_id"] = to.topic
	}
	var msg struct {
		MessageID int `json:"message_id"`
	}
	err := t.callOnce(ctx, t.endpoint("sendMessage"), mustJSON(p), &msg)
	return msg.MessageID, err
}

func (t *Telegram) endpoint(method string) string {
	return strings.TrimRight(t.cfg.APIURL, "/") + "/bot" + t.cfg.Token + "/" + method
}

func (t *Telegram) edit(ctx context.Context, ref msgRef, text string, kb any) error {
	p := map[string]any{
		"chat_id":              ref.Chat,
		"message_id":           ref.ID,
		"text":                 text,
		"parse_mode":           "HTML",
		"link_preview_options": map[string]any{"is_disabled": true},
	}
	if kb != nil {
		p["reply_markup"] = kb
	} else {
		p["reply_markup"] = map[string]any{"inline_keyboard": [][]button{}}
	}
	err := t.call(ctx, "editMessageText", p, nil)
	var ae *apiError
	if errors.As(err, &ae) && strings.Contains(ae.Description, "message is not modified") {
		return nil
	}
	return err
}

// isGone: the message was deleted by someone, so post a fresh one.
func isGone(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Code == 400 &&
		(strings.Contains(ae.Description, "message to edit not found") || strings.Contains(ae.Description, "MESSAGE_ID_INVALID"))
}

func (t *Telegram) topic(sev model.Severity) int {
	if id, ok := t.cfg.Topics[string(sev)]; ok {
		return id
	}
	return t.cfg.Topics["default"]
}

type tgChat struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	Type      string `json:"type"`
}

type tgMessage struct {
	Chat     *tgChat `json:"chat"`
	ThreadID int     `json:"message_thread_id"`
	IsTopic  bool    `json:"is_topic_message"`
	Created  *struct {
		Name string `json:"name"`
	} `json:"forum_topic_created"`
	ReplyTo *struct {
		Created *struct {
			Name string `json:"name"`
		} `json:"forum_topic_created"`
	} `json:"reply_to_message"`
}

type update struct {
	UpdateID int        `json:"update_id"`
	Message  *tgMessage `json:"message"`
	Channel  *tgMessage `json:"channel_post"`
	Member   *tgMessage `json:"my_chat_member"`
	Callback *struct {
		ID   string `json:"id"`
		Data string `json:"data"`
		From struct {
			ID        int64  `json:"id"`
			Username  string `json:"username"`
			FirstName string `json:"first_name"`
		} `json:"from"`
		Message *struct {
			Chat struct {
				ID int64 `json:"id"`
			} `json:"chat"`
		} `json:"message"`
	} `json:"callback_query"`
}

// pollUpdates long-polls for inline button presses.
// Topic is a forum topic the bot has seen a message in.
type Topic struct {
	ID   int    `json:"id"`
	Name string `json:"name,omitempty"`
}

// Chat is a chat the bot has seen, offered when connecting it.
type Chat struct {
	ID     int64   `json:"id"`
	Title  string  `json:"title"`
	Type   string  `json:"type"`
	Topics []Topic `json:"topics,omitempty"`
}

func collectChats(seen map[int64]*Chat, u update) {
	for _, m := range []*tgMessage{u.Message, u.Channel, u.Member} {
		if m == nil || m.Chat == nil {
			continue
		}
		c := seen[m.Chat.ID]
		if c == nil {
			c = &Chat{ID: m.Chat.ID, Title: cmp.Or(m.Chat.Title, m.Chat.FirstName, m.Chat.Username), Type: m.Chat.Type}
			seen[m.Chat.ID] = c
		}
		if !m.IsTopic || m.ThreadID == 0 {
			continue
		}
		name := ""
		if m.Created != nil {
			name = m.Created.Name
		} else if m.ReplyTo != nil && m.ReplyTo.Created != nil {
			name = m.ReplyTo.Created.Name
		}
		i := slices.IndexFunc(c.Topics, func(x Topic) bool { return x.ID == m.ThreadID })
		if i < 0 {
			c.Topics = append(c.Topics, Topic{m.ThreadID, name})
		} else if name != "" {
			c.Topics[i].Name = name
		}
	}
}

func sortedChats(seen map[int64]*Chat) []Chat {
	out := make([]Chat, 0, len(seen))
	for _, c := range seen {
		out = append(out, *c)
	}
	slices.SortFunc(out, func(a, b Chat) int { return cmp.Compare(a.Title, b.Title) })
	return out
}

// Chats is what the running bot has seen. Telegram allows one getUpdates at a
// time, so while it polls FindChats cannot ask.
func (t *Telegram) Chats() []Chat {
	t.mu.Lock()
	defer t.mu.Unlock()
	return sortedChats(t.seen)
}

// BotName asks Telegram who this token belongs to.
func (t *Telegram) BotName(ctx context.Context) (string, error) {
	var me struct {
		Username string `json:"username"`
	}
	err := t.callOnce(ctx, t.endpoint("getMe"), []byte("{}"), &me)
	return me.Username, err
}

// FindChats validates a token that is not in use yet and lists the chats in the
// bot's pending updates, without confirming them. It takes one attempt: a person
// is waiting.
func FindChats(ctx context.Context, cfg TelegramConfig) (bot string, chats []Chat, err error) {
	t, err := NewTelegram(TelegramConfig{Token: cfg.Token, APIURL: cfg.APIURL, Proxy: cfg.Proxy, DataDir: cfg.DataDir}, nil, nil)
	if err != nil {
		return "", nil, err
	}
	if bot, err = t.BotName(ctx); err != nil {
		return "", nil, err
	}
	var ups []update
	if err := t.callOnce(ctx, t.endpoint("getUpdates"), mustJSON(map[string]any{
		"limit": 100, "allowed_updates": []string{"message", "channel_post", "my_chat_member"},
	}), &ups); err != nil {
		return bot, nil, err
	}
	seen := map[int64]*Chat{}
	for _, u := range ups {
		collectChats(seen, u)
	}
	return bot, sortedChats(seen), nil
}

// pollRetry is the first wait after a failed getUpdates; it doubles up to
// pollRetryMax while the failures go on.
var pollRetry = 5 * time.Second

const pollRetryMax = 5 * time.Minute

func (t *Telegram) pollUpdates(ctx context.Context) {
	offset := 0
	wait, conflict := pollRetry, false
	for ctx.Err() == nil {
		var ups []update
		err := t.callOnce(ctx, strings.TrimRight(t.cfg.APIURL, "/")+"/bot"+t.cfg.Token+"/getUpdates", mustJSON(map[string]any{
			"offset": offset, "timeout": 50, "allowed_updates": []string{"callback_query", "message", "channel_post", "my_chat_member"},
		}), &ups)
		if err != nil {
			// 409: another app polls this token or set a webhook. It may be
			// for a moment (a redeploy), so keep trying, less and less often.
			var ae *apiError
			switch {
			case errors.As(err, &ae) && ae.Code == 409:
				if !conflict {
					t.log.Error("telegram buttons paused: this bot token is used by another app (webhook or polling); give UptimeForge its own bot. Retrying.")
				}
				conflict = true
			case ctx.Err() == nil:
				t.log.Warn("telegram getUpdates failed", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			wait = min(wait*2, pollRetryMax)
			continue
		}
		if conflict {
			t.log.Info("telegram buttons work again")
		}
		wait, conflict = pollRetry, false
		for _, u := range ups {
			offset = u.UpdateID + 1
			t.mu.Lock()
			collectChats(t.seen, u)
			t.mu.Unlock()
			if u.Callback != nil {
				t.handleCallback(ctx, u)
			}
		}
	}
}

func (t *Telegram) handleCallback(ctx context.Context, u update) {
	cb := u.Callback
	answer := func(text string) {
		t.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": cb.ID, "text": text}, nil)
	}
	if cb.Message == nil || !t.ownChat(cb.Message.Chat.ID) ||
		(len(t.cfg.Admins) > 0 && !slices.Contains(t.cfg.Admins, cb.From.ID)) {
		answer(t.f.t("cb_denied"))
		return
	}
	who := cb.From.FirstName
	if cb.From.Username != "" {
		who = "@" + cb.From.Username
	}
	parts := strings.Split(cb.Data, ":")
	// Closing and muting silence alerts. Without an admin list anyone in the
	// chat could do it, so then only Acknowledge works.
	if (parts[0] == "resolve" || parts[0] == "mute") && len(t.cfg.Admins) == 0 {
		answer(t.f.t("cb_need_admins"))
		return
	}
	var err error
	var reply string
	switch {
	case parts[0] == "ack" && len(parts) == 2:
		err, reply = t.actions.Ack(parts[1], who), t.f.t("cb_ack")
	case parts[0] == "resolve" && len(parts) == 2:
		err, reply = t.actions.Resolve(parts[1], who), t.f.t("cb_resolved")
	case parts[0] == "mute" && len(parts) == 3:
		// The data comes from the client, not necessarily from our keyboard.
		secs, perr := strconv.ParseInt(parts[2], 10, 64)
		if perr != nil || secs <= 0 {
			return
		}
		d := min(time.Duration(min(secs, int64(maxMute/time.Second)))*time.Second, maxMute)
		err, reply = t.actions.Mute(parts[1], d, who), t.f.t("cb_muted", t.f.Duration(d))
	default:
		return
	}
	if err != nil {
		reply = err.Error()
	}
	answer(reply)
}

// maxMute bounds a mute from a button.
const maxMute = 7 * 24 * time.Hour

// ownChat: only chats the bot posts to may press its buttons: the main
// chat, routed chats and the chats of tag rules.
func (t *Telegram) ownChat(id int64) bool {
	t.mu.Lock()
	for old, to := range t.migrated {
		if to == id {
			id = old // a supergroup speaks for the group it was
			break
		}
	}
	t.mu.Unlock()
	if id == t.cfg.ChatID {
		return true
	}
	for _, r := range t.cfg.Routes {
		if r.ChatID == id {
			return true
		}
	}
	for _, r := range t.cfg.Rules {
		if r.Route.ChatID == id {
			return true
		}
	}
	return false
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// SendReport posts the weekly report where the report route says, quietly.
func (t *Telegram) SendReport(ctx context.Context, rich, html string) error {
	p := t.route(EventReport, nil)
	if p.off {
		return nil
	}
	_, err := t.sendCard(ctx, p, rich, html, true, nil)
	return err
}
