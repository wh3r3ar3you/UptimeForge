package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/model"
	"github.com/wh3r3ar3you/uptimeforge/internal/xray"
)

type subWatch struct {
	key    string
	cancel context.CancelFunc
}

// syncSubscriptions (re)starts a watcher per subscription check whose
// settings changed and stops watchers of removed ones. Callers hold h.mu.
func (h *Hub) syncSubscriptions() {
	if h.ctx == nil {
		return
	}
	want := map[string]bool{}
	for _, c := range h.cfg.Checks {
		if c.Xray == nil || c.Xray.Subscription == "" || c.Paused {
			continue
		}
		want[c.ID] = true
		b, _ := json.Marshal(c)
		key := string(b)
		if w := h.subWatch[c.ID]; w != nil {
			if w.key == key {
				continue
			}
			w.cancel()
		}
		ctx, cancel := context.WithCancel(h.ctx)
		h.subWatch[c.ID] = &subWatch{key: key, cancel: cancel}
		go h.watchSubscription(ctx, c)
	}
	for id, w := range h.subWatch {
		if !want[id] {
			w.cancel()
			delete(h.subWatch, id)
			delete(h.subs, id)
		}
	}
}

func (h *Hub) watchSubscription(ctx context.Context, parent CheckConfig) {
	refresh := parent.Refresh.D()
	if refresh <= 0 {
		refresh = 10 * time.Minute
	}
	var filter *regexp.Regexp
	if parent.Xray.Filter != "" {
		filter = regexp.MustCompile(parent.Xray.Filter) // validated in normalize
	}
	failures := 0
	for {
		failures = h.refreshSubscription(ctx, parent, filter, failures)
		// A failed fetch is retried soon (5s, 10s, 20s…): a panel restart must
		// not leave the configs unmonitored for a whole refresh period.
		wait := refresh
		if failures > 0 {
			wait = min(refresh, 5*time.Second<<min(failures-1, 10))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// refreshSubscription fetches the subscription once and returns the updated
// count of consecutive failures. Until the first successful fetch the last
// good copy from disk is monitored, so a restart during a panel outage still
// checks every config.
func (h *Hub) refreshSubscription(ctx context.Context, parent CheckConfig, filter *regexp.Regexp, failures int) (n int) {
	defer func() {
		if r := recover(); r != nil {
			h.log.Error("subscription refresh crashed", "check", parent.ID, "panic", r)
			n = failures + 1
		}
	}()
	fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	entries, err := xray.FetchSubscription(fctx, h.httpc, parent.Xray.Subscription, parent.Xray.UserAgent)
	cancel()
	if ctx.Err() != nil {
		return failures
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if ctx.Err() != nil {
		return failures
	}
	cache := h.subCachePath(parent.ID)
	name := parent.Name
	if name == "" {
		name = parent.ID
	}
	now := time.Now()
	m := meta{name: name, typ: "subscription", target: redactURL(parent.Xray.Subscription), severity: "warning"}
	if err != nil {
		failures++
		h.log.Warn("subscription fetch failed", "check", parent.ID, "attempt", failures, "err", err)
		if _, ok := h.subs[parent.ID]; !ok {
			if cached := loadSubCache(cache, parent.Xray.Subscription); len(cached) > 0 {
				h.subs[parent.ID] = h.expand(parent, cached, filter)
				h.rebuild()
				h.log.Info("subscription restored from cache", "check", parent.ID, "configs", len(h.subs[parent.ID]))
			}
		}
		// One failed fetch is a blip; two in a row is worth a message.
		if failures >= 2 {
			h.transition("sub:"+parent.ID, model.KindDown, "",
				[]model.ProbeLine{{Probe: "hub", Name: "hub", Reason: "subscription", Error: err.Error()}}, m, now)
		}
		return failures
	}
	h.subs[parent.ID] = h.expand(parent, entries, filter)
	h.transition("sub:"+parent.ID, model.KindOK, "", nil, m, now)
	h.rebuild()
	h.log.Info("subscription refreshed", "check", parent.ID, "configs", len(h.subs[parent.ID]))
	if err := saveSubCache(cache, parent.Xray.Subscription, entries); err != nil {
		h.log.Warn("subscription cache not saved", "check", parent.ID, "err", err)
	}
	return 0
}

type subCache struct {
	URL     string       `json:"url"`
	Entries []xray.Entry `json:"entries"`
}

func (h *Hub) subCachePath(id string) string {
	if h.cfg.DataDir == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(h.cfg.DataDir, "subscriptions", hex.EncodeToString(sum[:8])+".json")
}

func loadSubCache(path, url string) []xray.Entry {
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var c subCache
	if json.Unmarshal(b, &c) != nil || c.URL != url {
		return nil
	}
	return c.Entries
}

func saveSubCache(path, url string, entries []xray.Entry) error {
	if path == "" {
		return nil
	}
	b, err := json.Marshal(subCache{URL: url, Entries: entries})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// expand turns subscription entries into one check per config. IDs derive
// from the endpoint so they survive reordering and renaming in the panel.
// Configs sharing an endpoint (one CDN front, many SNIs) each get a suffix
// from their whole outbound, so none of them depends on the order either;
// exact duplicates are numbered. IDs are unique.
func (h *Hub) expand(parent CheckConfig, entries []xray.Entry, filter *regexp.Regexp) []CheckConfig {
	group := parent.Group
	if group == "" {
		group = parent.Name
	}
	var kept []xray.Entry
	shared := map[string]int{}
	for _, e := range entries {
		if filter != nil && !filter.MatchString(e.Name) {
			continue
		}
		kept = append(kept, e)
		shared[slug(e.Endpoint)]++
	}
	seen := map[string]bool{}
	var out []CheckConfig
	for _, e := range kept {
		id := parent.ID + "/" + slug(e.Endpoint)
		if shared[slug(e.Endpoint)] > 1 {
			b, _ := json.Marshal(e.Outbound) // map keys are sorted: stable
			sum := sha256.Sum256(b)
			id += "-" + hex.EncodeToString(sum[:3])
		}
		for n, base := 2, id; seen[id]; n++ {
			id = fmt.Sprintf("%s-%d", base, n)
		}
		seen[id] = true
		c := parent
		x := *parent.Xray
		x.Subscription, x.Link, x.Outbound = "", "", e.Outbound
		c.Xray = &x
		c.ID, c.Name, c.Group, c.parent = id, e.Name, group, parent.ID
		c = h.cfg.withDefaults(c)
		proto, _ := e.Outbound["protocol"].(string)
		c.target = strings.ToUpper(proto) + " · " + e.Endpoint
		out = append(out, c)
	}
	return out
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	return strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

func redactURL(u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		rest := u[i+3:]
		if j := strings.Index(rest, "/"); j >= 0 {
			return u[:i+3] + rest[:j] + "/…"
		}
	}
	return u
}
