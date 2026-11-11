// Package app wires the hub to its storage, notifiers and integrations, and
// owns configuration: settings, probes and checks made in the UI live in the
// database; an optional config.yaml provisions items that the UI shows as
// read-only ("managed by config file").
package app

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/geoip"
	"github.com/wh3r3ar3you/uptimeforge/internal/hub"
	"github.com/wh3r3ar3you/uptimeforge/internal/model"
	"github.com/wh3r3ar3you/uptimeforge/internal/notify"
	"github.com/wh3r3ar3you/uptimeforge/internal/store"
)

type Options struct {
	ConfigPath  string // optional provisioning file
	DataDir     string
	DatabaseURL string // postgres://user:password@host:5432/db
	Listen      string
	PublicURL   string
	AdminToken  string // bearer token for scripts calling the API
}

// Section names for settings that a config file can pin.
const (
	SectionGeneral   = "general"
	SectionAlerts    = "alerts"
	SectionHeartbeat = "heartbeat"
	SectionTelegram  = "telegram"
	SectionWebhook   = "webhook"
	SectionRemnawave = "remnawave"
)

type App struct {
	Opt   Options
	Log   *slog.Logger
	Store *store.Store
	Hub   *hub.Hub

	file    *hub.Config
	managed struct {
		probes, checks, sections map[string]bool
	}

	mu        sync.Mutex
	ctx       context.Context
	settings  hub.Config // database part: everything except probes and checks
	notifyKey string
	stopNotif context.CancelFunc
	tg        *notify.Telegram
	wh        *notify.Webhook
	rec       *recorder
	rw        remnawaveState
	nodes     nodeSnapshot
	geo       countryDB
}

const (
	kindProbe  = "probe"
	kindCheck  = "check"
	kindStatus = "status_page"
)

func New(opt Options, log *slog.Logger) (*App, error) {
	if opt.DataDir == "" {
		opt.DataDir = "./data"
	}
	if err := os.MkdirAll(opt.DataDir, 0o700); err != nil {
		return nil, err
	}
	st, err := store.Open(opt.DatabaseURL)
	if err != nil {
		return nil, err
	}
	a := &App{Opt: opt, Log: log, Store: st, rec: newRecorder(), geo: &geoip.DB{Dir: filepath.Join(opt.DataDir, "geoip")}}
	a.managed.probes, a.managed.checks, a.managed.sections = map[string]bool{}, map[string]bool{}, map[string]bool{}

	if opt.ConfigPath != "" {
		if _, err := os.Stat(opt.ConfigPath); err == nil {
			f, err := hub.ReadConfigFile(opt.ConfigPath)
			if err != nil {
				return nil, err
			}
			a.file = f
			a.markManaged()
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}

	ctx := context.Background()
	if _, err := st.GetSetting(ctx, "settings", &a.settings); err != nil {
		return nil, fmt.Errorf("settings: %w", err)
	}
	if err := a.firstRun(ctx); err != nil {
		return nil, err
	}
	cfg, err := a.compose(ctx, nil)
	if err != nil {
		return nil, err
	}
	if a.Hub, err = hub.New(cfg, log); err != nil {
		return nil, err
	}
	open, err := st.Incidents(ctx, store.IncidentFilter{Open: true})
	if err != nil {
		return nil, err
	}
	mutes := map[string]time.Time{}
	st.GetSetting(ctx, "mutes", &mutes)
	a.Hub.Restore(open, mutes)
	a.Hub.SetRecorder(a.rec)
	a.Hub.SetEnricher(a.enrichIncident)
	if seen, err := a.Store.ProbesSeen(context.Background()); err == nil {
		a.Hub.MarkSeen(seen)
	} else {
		log.Warn("load probes seen", "err", err)
	}
	return a, nil
}

func (a *App) markManaged() {
	f := a.file
	for _, p := range f.Probes {
		a.managed.probes[p.ID] = true
	}
	for _, c := range f.Checks {
		a.managed.checks[c.ID] = true
	}
	set := map[string]bool{
		SectionGeneral: f.Timezone != "" || f.Lang != "" || f.RegionLabel != "" || f.OfflineGrace != 0 ||
			f.Defaults != (hub.Defaults{}) || f.Baseline != nil || f.PublicURL != "",
		SectionAlerts:    f.Alerts.QuietHours != "" || f.Alerts.RemindEvery != nil || f.Alerts.Host != nil,
		SectionHeartbeat: f.Heartbeat.URL != "",
		SectionTelegram:  f.Telegram != nil,
		SectionWebhook:   f.Webhook != nil,
		SectionRemnawave: f.Remnawave != nil,
	}
	for k, v := range set {
		if v {
			a.managed.sections[k] = true
		}
	}
}

// firstRun gives a fresh install one probe: the hub itself.
func (a *App) firstRun(ctx context.Context) error {
	var done bool
	if found, _ := a.Store.GetSetting(ctx, "initialized", &done); found {
		return nil
	}
	if a.file == nil || len(a.file.Probes) == 0 {
		objs, err := a.Store.Objects(ctx, kindProbe)
		if err != nil {
			return err
		}
		if len(objs) == 0 {
			if err := a.Store.PutObject(ctx, kindProbe, "hub", hub.ProbeConfig{ID: "hub", Name: "Hub", Local: true}); err != nil {
				return err
			}
		}
	}
	return a.Store.PutSetting(ctx, "initialized", true)
}

// override lets a caller validate a pending change: compose with it applied.
type override struct {
	settings *hub.Config
	probe    *hub.ProbeConfig
	check    *hub.CheckConfig
	delete   string // "probe:<id>" or "check:<id>"
}

// compose merges database items with the provisioning file into the config
// the hub runs. File items win on id collisions and are read-only.
func (a *App) compose(ctx context.Context, ov *override) (*hub.Config, error) {
	cfg := a.settings
	if ov != nil && ov.settings != nil {
		cfg = *ov.settings
	}
	// Deep copy through JSON: the hub normalizes (mutates) what it gets.
	b, _ := json.Marshal(cfg)
	var c hub.Config
	json.Unmarshal(b, &c)
	if c.Lang == "" {
		c.Lang = "ru"
	}
	c.Listen, c.DataDir, c.AdminToken = a.Opt.Listen, a.Opt.DataDir, a.Opt.AdminToken
	if a.Opt.PublicURL != "" {
		c.PublicURL = a.Opt.PublicURL
	}

	if a.file != nil {
		f := a.file.Clone() // the hub normalizes what it gets; a.file must stay pristine
		if a.managed.sections[SectionGeneral] {
			c.Timezone, c.Lang, c.RegionLabel, c.OfflineGrace = pick(f.Timezone, c.Timezone), pick(f.Lang, c.Lang),
				pick(f.RegionLabel, c.RegionLabel), f.OfflineGrace
			if f.Defaults != (hub.Defaults{}) {
				c.Defaults = f.Defaults
			}
			if f.Baseline != nil {
				c.Baseline = f.Baseline
			}
			if f.PublicURL != "" {
				c.PublicURL = f.PublicURL
			}
		}
		if a.managed.sections[SectionAlerts] {
			c.Alerts = f.Alerts
		}
		if a.managed.sections[SectionHeartbeat] {
			c.Heartbeat = f.Heartbeat
		}
		if f.Telegram != nil {
			c.Telegram = f.Telegram
		}
		if f.Webhook != nil {
			c.Webhook = f.Webhook
		}
		if f.Remnawave != nil {
			c.Remnawave = f.Remnawave
		}
		c.Probes = append(c.Probes, f.Probes...)
		c.Checks = append(c.Checks, f.Checks...)
	}

	probes, err := a.Store.Objects(ctx, kindProbe)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, o := range probes {
		if a.managed.probes[o.ID] || (ov != nil && ov.delete == "probe:"+o.ID) {
			continue
		}
		var p hub.ProbeConfig
		if err := json.Unmarshal(o.Data, &p); err != nil {
			return nil, fmt.Errorf("probe %s: %w", o.ID, err)
		}
		if ov != nil && ov.probe != nil && ov.probe.ID == p.ID {
			p, seen[p.ID] = *ov.probe, true
		}
		c.Probes = append(c.Probes, p)
	}
	if ov != nil && ov.probe != nil && !seen[ov.probe.ID] {
		c.Probes = append(c.Probes, *ov.probe)
	}

	checks, err := a.Store.Objects(ctx, kindCheck)
	if err != nil {
		return nil, err
	}
	seen = map[string]bool{}
	for _, o := range checks {
		if a.managed.checks[o.ID] || (ov != nil && ov.delete == "check:"+o.ID) {
			continue
		}
		var ch hub.CheckConfig
		if err := json.Unmarshal(o.Data, &ch); err != nil {
			return nil, fmt.Errorf("check %s: %w", o.ID, err)
		}
		if ov != nil && ov.check != nil && ov.check.ID == ch.ID {
			ch, seen[ch.ID] = *ov.check, true
		}
		c.Checks = append(c.Checks, ch)
	}
	if ov != nil && ov.check != nil && !seen[ov.check.ID] {
		c.Checks = append(c.Checks, *ov.check)
	}
	return &c, nil
}

func pick(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// validate composes with the change and runs the hub's validation on it.
func (a *App) validate(ctx context.Context, ov *override) error {
	cfg, err := a.compose(ctx, ov)
	if err != nil {
		return err
	}
	return cfg.Normalize()
}

// reload re-reads everything and swaps the hub config and notifiers.
// Callers hold a.mu.
func (a *App) reload(ctx context.Context) error {
	cfg, err := a.compose(ctx, nil)
	if err != nil {
		return err
	}
	if err := a.Hub.Apply(cfg); err != nil {
		return err
	}
	a.syncNotifiers(a.Hub.Config())
	return nil
}

// Run starts background work and blocks until ctx ends.
func (a *App) Run(ctx context.Context) error {
	a.mu.Lock()
	a.ctx = ctx
	a.syncNotifiers(a.Hub.Config())
	a.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); a.rec.run(ctx, a.Store, a.Log) }()
	go func() { defer wg.Done(); a.pollRemnawave(ctx) }()
	go func() { defer wg.Done(); a.reportLoop(ctx) }()
	err := a.Hub.Run(ctx)
	wg.Wait()
	a.Store.Close()
	return err
}

// syncNotifiers rebuilds notifiers when their settings changed.
func (a *App) syncNotifiers(cfg *hub.Config) {
	if a.ctx == nil {
		return
	}
	// Everything the notifiers and their formatter read: a change in any of
	// it must reach the next alert.
	key, _ := json.Marshal([]any{cfg.Telegram, cfg.Webhook, cfg.Lang, cfg.Timezone, cfg.PublicURL, cfg.Templates})
	if string(key) == a.notifyKey {
		return
	}
	a.notifyKey = string(key)
	stopOld, oldTG, oldWH := a.stopNotif, a.tg, a.wh
	ctx, cancel := context.WithCancel(a.ctx)
	a.stopNotif = cancel
	loc, _ := time.LoadLocation(cfg.Timezone)
	if loc == nil {
		loc = time.UTC
	}
	f := &notify.Formatter{Lang: cfg.Lang, Loc: loc, PublicURL: cfg.PublicURL, Custom: templates(cfg.Templates),
		OnError: func(err error) { a.Log.Warn("notification template failed, the default was sent", "err", err) }}
	var ns []model.Notifier
	a.tg, a.wh = nil, nil
	if t := cfg.Telegram; t != nil {
		routes := map[string]notify.Route{}
		for ev, r := range t.Routes {
			routes[ev] = notify.Route(r)
		}
		var rules []notify.Rule
		for _, r := range t.Rules {
			rules = append(rules, notify.Rule{Tags: r.Tags, Groups: r.Groups, Events: r.Events,
				MinSeverity: model.Severity(r.MinSeverity), Route: notify.Route(r.Route)})
		}
		tg, err := notify.NewTelegram(notify.TelegramConfig{Token: t.Token, ChatID: t.ChatID, Topics: t.Topics, Routes: routes, Rules: rules,
			APIURL: t.APIURL, Proxy: t.Proxy, Admins: t.Admins, DataDir: a.Opt.DataDir}, f, a.Log.With("notifier", "telegram"))
		if err != nil {
			a.Log.Error("telegram disabled", "err", err)
		} else {
			tg.SetActions(a.Hub)
			ns = append(ns, tg)
			a.tg = tg
		}
	}
	if w := cfg.Webhook; w != nil {
		a.wh = notify.NewWebhook(notify.WebhookConfig{URL: w.URL, Headers: w.Headers, Secret: w.Secret}, f, a.Log.With("notifier", "webhook"))
		ns = append(ns, a.wh)
	}
	// New events go to the new instances from here on. The old ones may still
	// hold alerts (queued, or mid-retry against a flaky API): they are handed
	// over and go out first, so nothing is lost and the order stays.
	a.Hub.SetNotifiers(ns)
	if stopOld != nil {
		stopOld()
	}
	if oldTG != nil {
		if a.tg != nil {
			a.tg.Handover(oldTG) // alerts, and the cards they edit, in memory
		} else if left := oldTG.Undelivered(); len(left) > 0 {
			a.Log.Warn("telegram removed, dropping undelivered alerts", "count", len(left))
		}
	}
	if oldWH != nil {
		left := oldWH.Undelivered()
		if a.wh != nil {
			a.wh.Adopt(left)
		} else if len(left) > 0 {
			a.Log.Warn("webhook removed, dropping undelivered alerts", "count", len(left))
		}
	}
	if a.tg != nil {
		go a.tg.Run(ctx)
	}
	if a.wh != nil {
		go a.wh.Run(ctx)
	}
}

// TestTelegram posts a test message with the current settings.
func (a *App) TestTelegram(ctx context.Context, event string) error {
	a.mu.Lock()
	tg := a.tg
	a.mu.Unlock()
	if tg == nil {
		return errors.New("telegram is not configured")
	}
	if event != "" {
		return tg.SendTest(ctx, event)
	}
	cfg := a.Hub.Config()
	loc, _ := time.LoadLocation(cfg.Timezone)
	f := &notify.Formatter{Lang: cfg.Lang, Loc: loc}
	return tg.Send(ctx, f.Hello(len(cfg.Probes), len(cfg.Checks)), true)
}

// FindTelegramChats lists chats visible to a bot. A masked token (or proxy)
// means the saved one, but only for the saved API server and proxy: the
// saved token never goes anywhere else.
func (a *App) FindTelegramChats(ctx context.Context, token, apiURL, proxy string) (string, []notify.Chat, error) {
	old := a.Hub.Config().Telegram
	var o hub.Telegram
	if old != nil {
		o = *old
	}
	proxy = unmask(proxy, o.Proxy, maskLogin)
	same := sameURL(cmp.Or(apiURL, telegramAPI), cmp.Or(o.APIURL, telegramAPI)) && proxy == o.Proxy
	token, err := keepSecret("telegram bot token", token, o.Token, same)
	if err != nil {
		return "", nil, err
	}
	if token == "" {
		return "", nil, errors.New("telegram: no bot token")
	}
	a.mu.Lock()
	tg := a.tg
	a.mu.Unlock()
	if tg != nil && old != nil && token == old.Token { // the running bot holds the only getUpdates
		bot, err := tg.BotName(ctx)
		return bot, tg.Chats(), err
	}
	return notify.FindChats(ctx, notify.TelegramConfig{Token: token, APIURL: cmp.Or(apiURL, "https://api.telegram.org"), Proxy: proxy, DataDir: a.Opt.DataDir})
}

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// templates converts the settings' templates for the formatter.
func templates(in map[string]map[string]string) notify.Templates {
	if len(in) == 0 {
		return nil
	}
	out := notify.Templates{}
	for lang, ts := range in {
		out[lang] = map[notify.TemplateKey]string{}
		for k, v := range ts {
			out[lang][notify.TemplateKey(k)] = v
		}
	}
	return out
}

// SendTelegramText posts text (Telegram HTML) to the main chat: the template
// editor's test.
func (a *App) SendTelegramText(ctx context.Context, text string) error {
	a.mu.Lock()
	tg := a.tg
	a.mu.Unlock()
	if tg == nil {
		return errors.New("telegram is not configured")
	}
	return tg.Send(ctx, text, true)
}
