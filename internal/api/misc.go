package api

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"github.com/wh3r3ar3you/uptimeforge/internal/app"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/checks"
	"github.com/wh3r3ar3you/uptimeforge/internal/hub"
	"github.com/wh3r3ar3you/uptimeforge/internal/model"
	"github.com/wh3r3ar3you/uptimeforge/internal/notify"
	"github.com/wh3r3ar3you/uptimeforge/internal/store"
	"github.com/wh3r3ar3you/uptimeforge/internal/version"
)

// mtprotoSpec recognises MTProxy links (tg://proxy, https://t.me/proxy).
func mtprotoSpec(target string) *checks.Spec {
	if !checks.IsMTProxyLink(target) {
		return nil
	}
	return &checks.Spec{ID: "_run", Type: "mtproto", Timeout: checks.Duration(20 * time.Second),
		MTProto: &checks.MTProtoParams{Link: strings.TrimSpace(target)}}
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.app.Settings())
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var cfg hub.Config
	if err := decode(r, &cfg); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.app.SaveSettings(r.Context(), cfg); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.app.Settings())
}

func (s *Server) testTelegram(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := s.app.TestTelegram(ctx, r.URL.Query().Get("event")); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) telegramChats(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token  string `json:"token"`
		APIURL string `json:"api_url"`
		Proxy  string `json:"proxy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	bot, chats, err := s.app.FindTelegramChats(ctx, body.Token, body.APIURL, body.Proxy)
	if err != nil {
		s.upstreamFail(w, err)
		return
	}
	if chats == nil {
		chats = []notify.Chat{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"bot": bot, "chats": chats})
}

// upstreamFail answers 400 for what the form got wrong (a hidden secret
// with a new address) and 502 for what the remote end said.
func (s *Server) upstreamFail(w http.ResponseWriter, err error) {
	var ve *app.ValidationError
	if errors.As(err, &ve) {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeError(w, http.StatusBadGateway, err)
}

// testRemnawave checks a panel URL and API token before they are saved.
func (s *Server) testRemnawave(w http.ResponseWriter, r *http.Request) {
	var body hub.Remnawave
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res, err := s.app.TestRemnawave(ctx, body)
	if err != nil {
		s.upstreamFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// locateProxy says which country a proxy server is in, for the "add server" form.
func (s *Server) locateProxy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Link string `json:"link"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute) // the first call downloads the database
	defer cancel()
	loc, err := s.app.LocateProxy(ctx, body.Link)
	var ve *app.ValidationError
	switch {
	case errors.As(err, &ve):
		s.fail(w, err)
		return
	case err != nil: // the database could not be fetched: say why
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, loc)
}

// push takes a heartbeat from an external job, as Uptime Kuma does:
// /api/push/<token>?status=up&msg=OK&ping=12. The token is the only credential.
func (s *Server) push(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ping, _ := strconv.ParseFloat(q.Get("ping"), 64)
	if !checks.Beat(r.PathValue("token"), q.Get("status") != "down", q.Get("msg"), ping) {
		writeError(w, http.StatusNotFound, errors.New("no push check has this token"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// remnawaveUsers is users online per node over a window, from stored polls.
func (s *Server) remnawaveUsers(w http.ResponseWriter, r *http.Request) {
	from, to, err := parseRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	series, err := s.app.Store.NodeUsersSeries(r.Context(), from, to, 240)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from.Unix(), "to": to.Unix(), "nodes": series})
}

func (s *Server) remnawave(w http.ResponseWriter, r *http.Request) {
	v := s.app.Remnawave()
	if isViewer(r) {
		v = redactRemnawave(v)
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	us, err := s.app.Store.Users(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, us)
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		credentials
		Role store.Role `json:"role"`
	}
	if err := decode(r, &body); err != nil {
		s.fail(w, err)
		return
	}
	if body.Role != store.RoleAdmin && body.Role != store.RoleViewer {
		writeError(w, http.StatusBadRequest, errors.New("role must be admin or viewer"))
		return
	}
	u, err := s.app.Store.CreateUser(r.Context(), body.Username, body.Password, body.Role)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if id == userFrom(r.Context()).ID {
		writeError(w, http.StatusBadRequest, errors.New("you can't delete yourself"))
		return
	}
	if err := s.app.Store.DeleteUser(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) setPassword(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	u := userFrom(r.Context())
	if u.ID != id && u.Role != store.RoleAdmin {
		writeError(w, http.StatusForbidden, errors.New("admin role required"))
		return
	}
	var body struct {
		Password string `json:"password"`
		// Current is required to change your own password: a stolen
		// session alone must not be enough to take the account over.
		// The UI sends "current"; "current_password" is taken too.
		Current    string `json:"current"`
		CurrentAlt string `json:"current_password"`
	}
	if err := decode(r, &body); err != nil {
		s.fail(w, err)
		return
	}
	if body.Current == "" {
		body.Current = body.CurrentAlt
	}
	if u.ID == id {
		if !s.userLimit.allow(strings.ToLower(u.Username)) {
			writeError(w, http.StatusTooManyRequests, errors.New("too many attempts for this user, try again in a few minutes"))
			return
		}
		if _, err := s.app.Store.Authenticate(r.Context(), u.Username, body.Current); err != nil {
			if errors.Is(err, store.ErrBadCredentials) {
				writeError(w, http.StatusForbidden, errors.New("current password is wrong"))
				return
			}
			s.fail(w, err)
			return
		}
	}
	if err := s.app.Store.SetPassword(r.Context(), id, body.Password); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// StatusPage is a public page listing selected checks for customers.
type StatusPage struct {
	Slug        string        `json:"slug"`
	Title       string        `json:"title"`
	Description string        `json:"description,omitempty"`
	Groups      []StatusGroup `json:"groups"`
	// Kind is "groups" (checks, the default) or "locations" (the panel's nodes).
	Kind      string     `json:"kind,omitempty"`
	Locations *Locations `json:"locations,omitempty"`
}

type StatusGroup struct {
	Name   string   `json:"name"`
	Checks []string `json:"checks"` // check ids; "sub/" includes every config of a subscription
}

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func (s *Server) listStatusPages(w http.ResponseWriter, r *http.Request) {
	objs, err := s.app.Store.Objects(r.Context(), "status_page")
	if err != nil {
		s.fail(w, err)
		return
	}
	pages := []StatusPage{}
	for _, o := range objs {
		var p StatusPage
		if decodeJSON(o.Data, &p) == nil {
			if p.Groups == nil {
				p.Groups = []StatusGroup{} // a page of locations has none; the UI wants a list
			}
			pages = append(pages, p)
		}
	}
	writeJSON(w, http.StatusOK, pages)
}

func (s *Server) putStatusPage(w http.ResponseWriter, r *http.Request) {
	var p StatusPage
	if err := decode(r, &p); err != nil {
		s.fail(w, err)
		return
	}
	p.Slug = r.PathValue("slug")
	if !slugRe.MatchString(p.Slug) {
		writeError(w, http.StatusBadRequest, errors.New("slug must be lowercase letters, digits and dashes"))
		return
	}
	if strings.TrimSpace(p.Title) == "" {
		writeError(w, http.StatusBadRequest, errors.New("title is required"))
		return
	}
	if p.Kind != "" && p.Kind != "groups" && p.Kind != "locations" {
		writeError(w, http.StatusBadRequest, errors.New("kind must be groups or locations"))
		return
	}
	if p.Locations != nil && !locationModes[p.Locations.Mode] {
		writeError(w, http.StatusBadRequest, errors.New("locations.mode must be country, node or country_expand"))
		return
	}
	if err := s.app.Store.PutObject(r.Context(), "status_page", p.Slug, p); err != nil {
		s.fail(w, err)
		return
	}
	s.public.drop(p.Slug)
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) deleteStatusPage(w http.ResponseWriter, r *http.Request) {
	if err := s.app.Store.DeleteObject(r.Context(), "status_page", r.PathValue("slug")); err != nil {
		s.fail(w, err)
		return
	}
	s.public.drop(r.PathValue("slug"))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type publicItem struct {
	Name   string      `json:"name"`
	Status string      `json:"status"` // operational, degraded, outage, unknown
	Uptime *float64    `json:"uptime_90d,omitempty"`
	Days   []UptimeDay `json:"days"`
}

// publicStatus shows names and status only: no targets, probes or errors.
// The answer is cached for 30 seconds: anyone can ask, and it costs
// queries per item.
func (s *Server) publicStatus(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if body, ok := s.public.get(slug); ok {
		writeJSON(w, http.StatusOK, body)
		return
	}
	var p StatusPage
	if err := s.app.Store.GetObject(r.Context(), "status_page", slug, &p); err != nil {
		writeError(w, http.StatusNotFound, errors.New("no such status page"))
		return
	}
	build := s.publicGroups
	if p.Kind == "locations" {
		build = s.publicLocations
	}
	body, err := build(r.Context(), p)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.public.put(slug, body)
	writeJSON(w, http.StatusOK, body)
}

// publicGroups is the public answer of a page of checks.
func (s *Server) publicGroups(ctx context.Context, p StatusPage) (map[string]any, error) {
	snap := s.app.Hub.Snapshot()
	byID := map[string]hub.CheckStatus{}
	for _, c := range snap.Checks {
		byID[c.ID] = c
	}
	statusOf := func(k model.Kind) string {
		switch k {
		case model.KindOK:
			return "operational"
		case model.KindPartial:
			return "degraded"
		case model.KindDown, model.KindBlocked:
			return "outage"
		}
		return "unknown"
	}
	type group struct {
		Name  string       `json:"name"`
		Items []publicItem `json:"items"`
	}
	overall := "operational"
	var groups []group
	for _, g := range p.Groups {
		out := group{Name: g.Name}
		var ids []string
		for _, id := range g.Checks {
			if strings.HasSuffix(id, "/") {
				for _, c := range snap.Checks {
					if strings.HasPrefix(c.ID, id) {
						ids = append(ids, c.ID)
					}
				}
				continue
			}
			ids = append(ids, id)
		}
		for _, id := range ids {
			c, ok := byID[id]
			if !ok {
				continue
			}
			days, err := s.uptimeDays(ctx, id, 90)
			if err != nil {
				return nil, err
			}
			it := publicItem{Name: c.Name, Status: statusOf(c.Verdict), Days: days}
			var sum, n float64
			for _, d := range days {
				if d.Up != nil {
					sum, n = sum+*d.Up, n+1
				}
			}
			if n > 0 {
				u := sum / n
				it.Uptime = &u
			}
			switch {
			case it.Status == "outage":
				overall = "outage"
			case it.Status == "degraded" && overall == "operational":
				overall = "degraded"
			}
			out.Items = append(out.Items, it)
		}
		groups = append(groups, out)
	}
	return map[string]any{
		"title": p.Title, "description": p.Description, "status": overall, "groups": groups, "updated": time.Now(),
	}, nil
}

// publicCache holds public status answers for publicTTL.
type publicCache struct {
	mu sync.Mutex
	m  map[string]publicEntry
}

type publicEntry struct {
	body map[string]any
	at   time.Time
}

const publicTTL = 30 * time.Second

func (c *publicCache) get(slug string) (map[string]any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[slug]
	return e.body, ok && time.Since(e.at) < publicTTL
}

func (c *publicCache) put(slug string, body map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]publicEntry{}
	}
	for k, e := range c.m { // pages nobody asks for any more
		if time.Since(e.at) >= publicTTL {
			delete(c.m, k)
		}
	}
	c.m[slug] = publicEntry{body, time.Now()}
}

func (c *publicCache) drop(slug string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, slug)
}

//go:embed install-agent.sh
var installScript string

// installScript serves the installer with this hub's address as its
// default, quoted for sh: the address comes from the request when
// UF_PUBLIC_URL is not set.
func (s *Server) installScript(w http.ResponseWriter, r *http.Request) {
	hub := s.hubURL(r)
	if hub == "" {
		http.Error(w, "bad Host header: set UF_PUBLIC_URL", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Write([]byte(strings.NewReplacer("__HUB_URL__", shellQuote(hub), "__VERSION__", shellQuote(version.Version),
		"__RELEASES_URL__", shellQuote(ReleasesURL)).Replace(installScript)))
}

var agentFile = regexp.MustCompile(`^uptimeforge-agent_(linux|windows|darwin)_(amd64|arm64|armv7|mips|mipsle)(\.exe)?$`)

// download serves agent binaries bundled with the hub, so installs work
// even where GitHub is slow or unreachable.
func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if name == "checksums.txt" && s.agentDir != "" {
		s.checksums(w, r)
		return
	}
	if s.agentDir == "" || !agentFile.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.agentDir, name)
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeFile(w, r, path)
}
