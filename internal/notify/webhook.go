package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/model"
)

type WebhookConfig struct {
	URL     string
	Headers map[string]string
	Secret  string // signs each body: X-UptimeForge-Signature: sha256=<hex HMAC-SHA256>
}

var webhookBackoff = 2 * time.Second // first wait between attempts; a var for tests

// Webhook POSTs every incident event as JSON. The payload carries the
// structured incident and a ready-to-post text, so a Discord/Slack bridge
// can forward "text" as is.
type Webhook struct {
	cfg    WebhookConfig
	f      *Formatter
	log    *slog.Logger
	client *http.Client
	queue  chan model.Event

	done    chan struct{} // closed when Run returns
	backlog []model.Event // handed over by the previous instance, delivered first
	left    []model.Event // cut off by shutdown, for the next instance
}

type WebhookPayload struct {
	Event     model.EventType `json:"event"`
	Escalated bool            `json:"escalated,omitempty"`
	Silent    bool            `json:"silent,omitempty"`
	Text      string          `json:"text"`
	URL       string          `json:"url,omitempty"`
	Incident  model.Incident  `json:"incident"`
}

func NewWebhook(cfg WebhookConfig, f *Formatter, log *slog.Logger) *Webhook {
	return &Webhook{cfg: cfg, f: f, log: log, client: &http.Client{Timeout: 15 * time.Second}, queue: make(chan model.Event, 1000), done: make(chan struct{})}
}

func (w *Webhook) Notify(ev model.Event) {
	select {
	case w.queue <- ev:
	default:
		w.log.Error("webhook queue is full, dropping event", "incident", ev.Incident.ID)
	}
}

func (w *Webhook) Run(ctx context.Context) {
	defer close(w.done)
	for {
		var ev model.Event
		if len(w.backlog) > 0 {
			ev, w.backlog = w.backlog[0], w.backlog[1:]
		} else {
			select {
			case <-ctx.Done():
				return
			case ev = <-w.queue:
			}
		}
		if err := w.deliver(ctx, ev); err != nil && ctx.Err() != nil {
			w.left = append(w.left, ev)
		} else if err != nil {
			w.log.Error("webhook delivery failed", "incident", ev.Incident.ID, "err", err)
		}
	}
}

// Adopt queues events handed over by a previous instance ahead of anything
// new. Call it before Run.
func (w *Webhook) Adopt(evs []model.Event) { w.backlog = append(w.backlog, evs...) }

// Undelivered waits for Run to return (cancel its context first) and gives
// back what it did not deliver, oldest first.
func (w *Webhook) Undelivered() []model.Event {
	<-w.done
	evs := append(w.left, w.backlog...)
	for {
		select {
		case ev := <-w.queue:
			evs = append(evs, ev)
		default:
			return evs
		}
	}
}

var tagRe = regexp.MustCompile(`<a href="[^"]*">([^<]*)</a>`)

func (w *Webhook) text(ev model.Event) string {
	inc := &ev.Incident
	var html string
	switch ev.Type {
	case model.EventResolve:
		html = w.f.Resolved(inc)
	case model.EventRemind:
		html = w.f.RemindReply(inc, time.Now())
	default:
		html = w.f.Incident(inc, time.Now())
	}
	return w.f.Plain(tagRe.ReplaceAllString(html, "$1"))
}

func (w *Webhook) deliver(ctx context.Context, ev model.Event) error {
	body, err := json.Marshal(WebhookPayload{Event: ev.Type, Escalated: ev.Escalated, Silent: ev.Silent,
		Text: w.text(ev), URL: w.f.Link(&ev.Incident), Incident: ev.Incident})
	if err != nil {
		return err
	}
	backoff := webhookBackoff
	deadline := time.Now().Add(5 * time.Minute)
	for {
		err = w.post(ctx, body)
		var perm *permanentError
		if err == nil || errors.As(err, &perm) || time.Now().After(deadline) || ctx.Err() != nil {
			return err
		}
		wait := backoff
		var busy *busyError
		if errors.As(err, &busy) && busy.after > 0 { // the receiver said when; a minute at most
			wait = min(busy.after, time.Minute)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

// busyError is a 429 or 5xx: try again, after Retry-After if it says.
type busyError struct {
	status int
	after  time.Duration
}

func (e *busyError) Error() string { return fmt.Sprintf("webhook answered HTTP %d", e.status) }

// retryAfter reads Retry-After: seconds or an HTTP date.
func retryAfter(h string) time.Duration {
	if h == "" {
		return 0
	}
	if s, err := strconv.Atoi(strings.TrimSpace(h)); err == nil && s >= 0 {
		return time.Duration(s) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		return max(time.Until(t), 0)
	}
	return 0
}

// permanentError: retrying gets the same answer (a 4xx, or a URL that does
// not parse).
type permanentError struct {
	status int
	err    error
}

func (e *permanentError) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return fmt.Sprintf("webhook answered HTTP %d", e.status)
}

// withoutURL drops the URL Go puts into url.Error: URLs carry secrets
// (tokens, proxy passwords), and error text reaches logs and the UI.
func withoutURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func (w *Webhook) post(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return &permanentError{err: fmt.Errorf("webhook URL: %w", withoutURL(err))}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "UptimeForge")
	for k, v := range w.cfg.Headers {
		req.Header.Set(k, v)
	}
	if w.cfg.Secret != "" { // set last: a custom header must not override it
		mac := hmac.New(sha256.New, []byte(w.cfg.Secret))
		mac.Write(body)
		req.Header.Set("X-UptimeForge-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return withoutURL(err) // the URL may carry a secret
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode < 300:
		return nil
	case resp.StatusCode == 429 || resp.StatusCode >= 500:
		return &busyError{status: resp.StatusCode, after: retryAfter(resp.Header.Get("Retry-After"))}
	}
	return &permanentError{status: resp.StatusCode}
}
