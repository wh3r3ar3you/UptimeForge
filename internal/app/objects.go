package app

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/wh3r3ar3you/uptimeforge/internal/checks"
	"github.com/wh3r3ar3you/uptimeforge/internal/hub"
	"github.com/wh3r3ar3you/uptimeforge/internal/notify"
	"github.com/wh3r3ar3you/uptimeforge/internal/store"
	"github.com/wh3r3ar3you/uptimeforge/internal/xray"
)

var (
	ErrManaged  = errors.New("this item is managed by the config file and can't be changed in the UI")
	ErrNotFound = store.ErrNotFound
	ErrExists   = errors.New("an item with this id already exists")
)

// ValidationError wraps config errors so the API can answer 400.
type ValidationError struct{ Err error }

func (e *ValidationError) Error() string { return e.Err.Error() }
func (e *ValidationError) Unwrap() error { return e.Err }

func invalid(err error) error {
	if err == nil {
		return nil
	}
	return &ValidationError{err}
}

// ProbeItem is a probe as the UI edits it.
type ProbeItem struct {
	hub.ProbeConfig
	Managed bool `json:"managed"`
}

// CheckItem is a check as the UI edits it.
type CheckItem struct {
	hub.CheckConfig
	Managed bool `json:"managed"`
}

func (a *App) Probes() []ProbeItem {
	cfg := a.Hub.Config()
	out := make([]ProbeItem, 0, len(cfg.Probes))
	for _, p := range cfg.Probes {
		p.Token = ""
		out = append(out, ProbeItem{ProbeConfig: p, Managed: a.managed.probes[p.ID]})
	}
	return out
}

func (a *App) Checks() []CheckItem {
	cfg := a.Hub.Config()
	out := make([]CheckItem, 0, len(cfg.Checks))
	for _, c := range cfg.Checks {
		out = append(out, CheckItem{CheckConfig: c, Managed: a.managed.checks[c.ID]})
	}
	return out
}

func newToken() string {
	b := make([]byte, 24)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// SaveProbe creates or updates a probe. For agent probes a token is created
// on first save (and returned once); afterwards only its hash is kept.
func (a *App) SaveProbe(ctx context.Context, p hub.ProbeConfig, create bool) (token string, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.managed.probes[p.ID] {
		return "", ErrManaged
	}
	if !idRe.MatchString(p.ID) {
		return "", invalid(fmt.Errorf("id must be lowercase letters, digits, '-' or '_'"))
	}
	var old hub.ProbeConfig
	err = a.Store.GetObject(ctx, kindProbe, p.ID, &old)
	switch {
	case create && err == nil:
		return "", ErrExists
	case !create && err != nil:
		return "", err
	}
	p.Token = ""
	if !p.Virtual() {
		p.TokenHash = old.TokenHash
		if p.TokenHash == "" {
			token = newToken()
			p.TokenHash = hub.HashToken(token)
		}
	} else {
		p.TokenHash = ""
	}
	if err := a.validate(ctx, &override{probe: &p}); err != nil {
		return "", invalid(err)
	}
	if err := a.Store.PutObject(ctx, kindProbe, p.ID, p); err != nil {
		return "", err
	}
	return token, a.reload(ctx)
}

// RotateToken issues a new agent token; the connected agent is dropped.
func (a *App) RotateToken(ctx context.Context, id string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.managed.probes[id] {
		return "", ErrManaged
	}
	var p hub.ProbeConfig
	if err := a.Store.GetObject(ctx, kindProbe, id, &p); err != nil {
		return "", err
	}
	if p.Virtual() {
		return "", invalid(errors.New("only agent probes have tokens"))
	}
	token := newToken()
	p.TokenHash = hub.HashToken(token)
	if err := a.Store.PutObject(ctx, kindProbe, id, p); err != nil {
		return "", err
	}
	return token, a.reload(ctx)
}

func (a *App) DeleteProbe(ctx context.Context, id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.managed.probes[id] {
		return ErrManaged
	}
	if err := a.validate(ctx, &override{delete: "probe:" + id}); err != nil {
		return invalid(err)
	}
	if err := a.Store.DeleteObject(ctx, kindProbe, id); err != nil {
		return err
	}
	return a.reload(ctx)
}

func (a *App) SaveCheck(ctx context.Context, c hub.CheckConfig, create bool) (err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Validation makes a push token known so beats are taken before the
	// first run; if the check is not saved after all, take it back.
	if tok := c.Params["token"]; c.Type == "push" && tok != "" && !pushTokenInUse(a.Hub.Config(), tok) {
		defer func() {
			if err != nil {
				checks.ForgetPush(tok)
			}
		}()
	}
	if a.managed.checks[c.ID] {
		return ErrManaged
	}
	if !idRe.MatchString(c.ID) {
		return invalid(fmt.Errorf("id must be lowercase letters, digits, '-' or '_'"))
	}
	var old hub.CheckConfig
	err = a.Store.GetObject(ctx, kindCheck, c.ID, &old)
	switch {
	case create && err == nil:
		return ErrExists
	case !create && err != nil:
		return err
	}
	if c.Xray != nil && c.Xray.Link != "" {
		if _, err := xray.ParseLink(c.Xray.Link); err != nil {
			return invalid(fmt.Errorf("config link: %w", err))
		}
	}
	if err := a.validate(ctx, &override{check: &c}); err != nil {
		return invalid(err)
	}
	if err := a.Store.PutObject(ctx, kindCheck, c.ID, c); err != nil {
		return err
	}
	if t := old.Params["token"]; old.Type == "push" && (c.Type != "push" || c.Params["token"] != t) {
		checks.ForgetPush(t) // the old URL stops being accepted
	}
	return a.reload(ctx)
}

// pushTokenInUse says whether a running push check already has tok.
func pushTokenInUse(cfg *hub.Config, tok string) bool {
	return slices.ContainsFunc(cfg.Checks, func(c hub.CheckConfig) bool { return c.Type == "push" && c.Params["token"] == tok })
}

func (a *App) DeleteCheck(ctx context.Context, id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.managed.checks[id] {
		return ErrManaged
	}
	var old hub.CheckConfig
	a.Store.GetObject(ctx, kindCheck, id, &old)
	if err := a.Store.DeleteObject(ctx, kindCheck, id); err != nil {
		return err
	}
	if old.Type == "push" {
		checks.ForgetPush(old.Params["token"])
	}
	return a.reload(ctx)
}

func (a *App) SetPaused(ctx context.Context, id string, paused bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.managed.checks[id] {
		return ErrManaged
	}
	var c hub.CheckConfig
	if err := a.Store.GetObject(ctx, kindCheck, id, &c); err != nil {
		return err
	}
	c.Paused = paused
	if err := a.Store.PutObject(ctx, kindCheck, id, c); err != nil {
		return err
	}
	return a.reload(ctx)
}

// Settings is the editable configuration plus which sections the config
// file pins. Secrets are masked; saving a masked value keeps the old one.
type Settings struct {
	Config  hub.Config      `json:"config"`
	Managed map[string]bool `json:"managed"`
}

const masked = "••••••••"

func (a *App) Settings() Settings {
	cfg := *a.Hub.Config()
	if cfg.Telegram != nil {
		t := *cfg.Telegram
		t.Token = mask(t.Token)
		t.Proxy = maskLogin(t.Proxy)
		cfg.Telegram = &t
	}
	if cfg.Remnawave != nil {
		r := *cfg.Remnawave
		r.Token = mask(r.Token)
		r.Headers = maskAll(r.Headers)
		cfg.Remnawave = &r
	}
	if cfg.Webhook != nil {
		w := *cfg.Webhook
		w.URL = maskURLPath(w.URL)
		w.Headers = maskHeaders(w.Headers)
		w.Secret = mask(w.Secret)
		cfg.Webhook = &w
	}
	cfg.Heartbeat.URL = maskURLPath(cfg.Heartbeat.URL)
	return Settings{Config: cfg, Managed: a.managed.sections}
}

func mask(s string) string {
	if s == "" {
		return ""
	}
	return masked
}

func maskAll(h map[string]string) map[string]string {
	if h == nil {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		out[k] = mask(v)
	}
	return out
}

// splitURL cuts a URL into scheme, the host part without a login, and
// whether it had a login and anything after the host.
func splitURL(s string) (scheme, host string, login bool, tail string, ok bool) {
	scheme, rest, ok := strings.Cut(strings.TrimSpace(s), "://")
	if !ok {
		return "", "", false, "", false
	}
	host = rest
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		host, tail = rest[:i], rest[i:]
	}
	if i := strings.LastIndex(host, "@"); i >= 0 {
		host, login = host[i+1:], true
	}
	return scheme, host, login, tail, true
}

// maskLogin hides the login of a proxy URL and keeps where it points:
// socks5://user:pass@host:1080 becomes socks5://••••••••@host:1080.
func maskLogin(s string) string {
	scheme, host, login, tail, ok := splitURL(s)
	if !ok || !login {
		return s
	}
	return scheme + "://" + masked + "@" + host + tail
}

// maskURLPath keeps scheme and host of a URL whose path or query is the
// credential (Slack-style webhooks, hc-ping UUIDs).
func maskURLPath(s string) string {
	scheme, host, login, tail, ok := splitURL(s)
	if !ok {
		return s
	}
	out := scheme + "://"
	if login {
		out += masked + "@"
	}
	out += host
	if tail != "" && tail != "/" {
		out += "/" + masked
	}
	return out
}

// unmask turns a value the form sent back as shown into the saved one.
func unmask(in, old string, maskFn func(string) string) string {
	if old != "" && in != old && in == maskFn(old) {
		return old
	}
	return in
}

const telegramAPI = "https://api.telegram.org"

// sameURL compares where two URLs send requests.
func sameURL(a, b string) bool {
	return strings.TrimRight(strings.TrimSpace(a), "/") == strings.TrimRight(strings.TrimSpace(b), "/")
}

// keepSecret returns the saved secret for a masked (or, for tests of a saved
// form, empty) one, but only while the place it is sent to stays the same:
// a masked token sent along with a new URL would hand the saved secret to
// whoever runs that URL.
func keepSecret(what, in, old string, sameDest bool) (string, error) {
	if in != masked {
		return in, nil
	}
	if old == "" {
		return "", invalid(fmt.Errorf("%s is not set: enter it again", what))
	}
	if !sameDest {
		return "", invalid(fmt.Errorf("%s is hidden and the address it would be sent to changed: enter it again", what))
	}
	return old, nil
}

// keepHeaders is keepSecret for every header value.
func keepHeaders(what string, in, old map[string]string, sameDest bool) error {
	for k, v := range in {
		got, err := keepSecret(what+" header "+k, v, old[k], sameDest)
		if err != nil {
			return err
		}
		in[k] = got
	}
	return nil
}

// keepSettingsSecrets restores what the settings form sent back masked.
func keepSettingsSecrets(in *hub.Config, old hub.Config) error {
	in.Heartbeat.URL = unmask(in.Heartbeat.URL, old.Heartbeat.URL, maskURLPath)
	if t := in.Telegram; t != nil {
		var o hub.Telegram
		if old.Telegram != nil {
			o = *old.Telegram
		}
		t.Proxy = unmask(t.Proxy, o.Proxy, maskLogin)
		same := sameURL(cmp.Or(t.APIURL, telegramAPI), cmp.Or(o.APIURL, telegramAPI)) && t.Proxy == o.Proxy
		var err error
		if t.Token, err = keepSecret("telegram bot token", t.Token, o.Token, same); err != nil {
			return err
		}
	}
	if r := in.Remnawave; r != nil {
		var o hub.Remnawave
		if old.Remnawave != nil {
			o = *old.Remnawave
		}
		same := sameURL(r.URL, o.URL)
		var err error
		if r.Token, err = keepSecret("remnawave API token", r.Token, o.Token, same); err != nil {
			return err
		}
		if err := keepHeaders("remnawave", r.Headers, o.Headers, same); err != nil {
			return err
		}
	}
	if w := in.Webhook; w != nil {
		var o hub.Webhook
		if old.Webhook != nil {
			o = *old.Webhook
		}
		w.URL = unmask(w.URL, o.URL, maskURLPath)
		if err := keepHeaders("webhook", w.Headers, o.Headers, sameURL(w.URL, o.URL)); err != nil {
			return err
		}
		var err error
		if w.Secret, err = keepSecret("webhook secret", w.Secret, o.Secret, sameURL(w.URL, o.URL)); err != nil {
			return err
		}
	}
	return nil
}

func maskHeaders(h map[string]string) map[string]string {
	if h == nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range h {
		if strings.EqualFold(k, "authorization") || strings.Contains(strings.ToLower(k), "token") || strings.Contains(strings.ToLower(k), "secret") {
			v = mask(v)
		}
		out[k] = v
	}
	return out
}

// SaveSettings stores the UI-editable settings. Sections pinned by the
// config file are ignored.
func (a *App) SaveSettings(ctx context.Context, in hub.Config) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	old := a.settings
	// Sections pinned by the config file are not the UI's to change: keep the
	// stored copy so masked secrets never end up in the database.
	if a.managed.sections[SectionTelegram] {
		in.Telegram = old.Telegram
	}
	if a.managed.sections[SectionWebhook] {
		in.Webhook = old.Webhook
	}
	if a.managed.sections[SectionRemnawave] {
		in.Remnawave = old.Remnawave
	}
	// Masked secrets are restored against what is running now (for pinned
	// sections that is the config file's copy).
	if err := keepSettingsSecrets(&in, *a.Hub.Config()); err != nil {
		return err
	}
	in.Probes, in.Checks = nil, nil
	for lang, ts := range in.Templates {
		for key, text := range ts {
			if err := notify.ValidateTemplate(lang, notify.TemplateKey(key), text); err != nil {
				return invalid(fmt.Errorf("template %s/%s: %w", lang, key, err))
			}
		}
	}
	if err := a.validate(ctx, &override{settings: &in}); err != nil {
		return invalid(err)
	}
	if err := a.Store.PutSetting(ctx, "settings", in); err != nil {
		return err
	}
	a.settings = in
	return a.reload(ctx)
}

// BulkAction is one change applied to many checks from the checks page.
type BulkAction struct {
	IDs        []string `json:"ids"`
	Action     string   `json:"action"` // pause, resume, group, tags, delete
	Group      string   `json:"group,omitempty"`
	AddTags    []string `json:"add_tags,omitempty"`
	RemoveTags []string `json:"remove_tags,omitempty"`
}

// BulkChecks applies b to each check in turn; checks config.yaml owns are
// skipped and returned, any other error stops the run.
func (a *App) BulkChecks(ctx context.Context, b BulkAction) (skipped []string, err error) {
	switch b.Action {
	case "pause", "resume", "group", "tags", "delete":
	default:
		return nil, invalid(fmt.Errorf("unknown bulk action %q", b.Action))
	}
	remove, err := hub.NormalizeTags(b.RemoveTags)
	if err != nil {
		return nil, invalid(err)
	}
	for _, id := range b.IDs {
		switch b.Action {
		case "pause", "resume":
			err = a.SetPaused(ctx, id, b.Action == "pause")
		case "delete":
			err = a.DeleteCheck(ctx, id)
		default:
			var c hub.CheckConfig
			if a.managed.checks[id] {
				err = ErrManaged
				break
			}
			if err = a.Store.GetObject(ctx, kindCheck, id, &c); err != nil {
				break
			}
			if b.Action == "group" {
				c.Group = b.Group
			} else {
				tags := slices.DeleteFunc(append(slices.Clone(c.Tags), b.AddTags...), func(t string) bool {
					return slices.Contains(remove, strings.ToLower(strings.TrimSpace(t)))
				})
				if c.Tags, err = hub.NormalizeTags(tags); err != nil {
					return skipped, invalid(fmt.Errorf("check %s: %w", id, err))
				}
			}
			err = a.SaveCheck(ctx, c, false)
		}
		switch {
		case errors.Is(err, ErrManaged):
			skipped = append(skipped, id)
		case err != nil:
			return skipped, err
		}
	}
	return skipped, nil
}
