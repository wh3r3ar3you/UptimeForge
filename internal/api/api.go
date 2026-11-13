// Package api is the HTTP API behind the web UI, plus the public pieces:
// status pages, the agent installer and agent binaries.
package api

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/app"
	"github.com/wh3r3ar3you/uptimeforge/internal/store"
)

type Server struct {
	app      *app.App
	log      *slog.Logger
	ui       fs.FS // built web UI; may be empty
	agentDir string
	limiter  *limiter

	setupToken   string
	setupLimit   *limiter
	userLimit    *limiter // login attempts per username
	bearerLimit  *limiter // wrong admin bearer tokens per address
	joinLimit    *limiter // invite exchanges per address
	trust        trust    // which proxies' X-Forwarded-* count
	metricsToken string
	agentSums    agentSums // sha256 of the agent binaries, for checksums.txt

	trafficMu sync.Mutex
	traffic   map[string]store.Traffic // last 24h per probe, cached for a minute
	bars      barsCache
	public    publicCache
	trafficAt time.Time
}

type Options struct {
	UI       fs.FS
	AgentDir string // directory with uptimeforge-agent_<os>_<arch> binaries served to installers
	// SetupToken (UF_SETUP_TOKEN) creates the first admin. Empty: the hub
	// makes one up and prints it to its log while there are no users.
	SetupToken string
	// TrustedProxies (UF_TRUSTED_PROXIES, see ParseTrustedProxies) may set
	// X-Forwarded-For/-Proto/-Host. nil: loopback and private networks.
	TrustedProxies []netip.Prefix
	// MetricsToken (UF_METRICS_TOKEN) is the bearer token /metrics wants.
	// Empty: /metrics answers only loopback and private addresses, and
	// nothing that came through a reverse proxy.
	MetricsToken string
}

func New(a *app.App, opt Options) *Server {
	s := &Server{app: a, log: a.Log, ui: opt.UI, agentDir: opt.AgentDir, limiter: newLimiter(10, time.Minute),
		setupToken: opt.SetupToken, setupLimit: newLimiter(10, time.Minute), userLimit: newLimiter(10, 5*time.Minute), bearerLimit: newLimiter(10, time.Minute),
		joinLimit: newLimiter(10, time.Minute), trust: newTrust(opt.TrustedProxies), metricsToken: opt.MetricsToken}
	s.announceSetup()
	return s
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	hub := s.app.Hub.Handler()
	mux.Handle("GET /api/agent/ws", s.agentAddr(hub))
	mux.Handle("GET /metrics", s.metricsGuard(hub))
	mux.Handle("GET /healthz", hub)

	mux.HandleFunc("GET /api/v1/auth/state", s.authState)
	mux.HandleFunc("POST /api/v1/auth/setup", s.setup)
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("POST /api/v1/auth/logout", s.logout)

	view, admin := s.require(store.RoleViewer), s.require(store.RoleAdmin)
	mux.Handle("GET /api/v1/meta", view(s.meta))
	mux.Handle("GET /api/v1/me/prefs", view(s.getPrefs))
	mux.Handle("PUT /api/v1/me/prefs", view(s.putPrefs))
	mux.Handle("GET /api/v1/overview", view(s.overview))

	mux.Handle("GET /api/v1/checks", view(s.listChecks))
	mux.Handle("POST /api/v1/checks", admin(s.createCheck))
	mux.Handle("GET /api/v1/checks/bars", view(s.checkBars))
	mux.Handle("POST /api/v1/checks/bulk", admin(s.bulkChecks))
	mux.Handle("GET /api/v1/checks/{id}", view(s.getCheck))
	mux.Handle("PUT /api/v1/checks/{id}", admin(s.updateCheck))
	mux.Handle("DELETE /api/v1/checks/{id}", admin(s.deleteCheck))
	mux.Handle("POST /api/v1/checks/{id}/pause", admin(s.pauseCheck(true)))
	mux.Handle("POST /api/v1/checks/{id}/resume", admin(s.pauseCheck(false)))
	mux.Handle("GET /api/v1/checks/{id}/series", view(s.checkSeries))
	mux.Handle("GET /api/v1/checks/{id}/uptime", view(s.checkUptime))

	mux.Handle("GET /api/v1/probes", view(s.listProbes))
	mux.Handle("POST /api/v1/probes", admin(s.createProbe))
	mux.Handle("PUT /api/v1/probes/{id}", admin(s.updateProbe))
	mux.Handle("DELETE /api/v1/probes/{id}", admin(s.deleteProbe))
	mux.Handle("POST /api/v1/probes/{id}/token", admin(s.rotateToken))
	mux.Handle("GET /api/v1/probes/{id}/metrics", view(s.probeMetrics))
	mux.Handle("GET /api/v1/probes/{id}/live", view(s.liveStream))
	mux.Handle("GET /api/v1/sparklines", view(s.sparklines))

	mux.Handle("POST /api/v1/checkers/invites", admin(s.createInvite))
	mux.Handle("GET /api/v1/checkers/invites", admin(s.listInvites))
	mux.Handle("DELETE /api/v1/checkers/invites/{id}", admin(s.revokeInvite))
	mux.HandleFunc("POST /api/agent/join", s.join)

	mux.Handle("GET /api/v1/incidents", view(s.listIncidents))
	mux.Handle("POST /api/v1/incidents/{id}/ack", admin(s.ackIncident))
	mux.Handle("POST /api/v1/incidents/{id}/resolve", admin(s.resolveIncident))
	mux.Handle("POST /api/v1/incidents/{id}/mute", admin(s.muteIncident))

	mux.Handle("POST /api/v1/tools/run", admin(s.runTool))

	mux.Handle("GET /api/v1/settings", admin(s.getSettings))
	mux.Handle("PUT /api/v1/settings", admin(s.putSettings))
	mux.Handle("POST /api/v1/settings/telegram/test", admin(s.testTelegram))
	mux.Handle("POST /api/v1/settings/telegram/chats", admin(s.telegramChats))
	mux.Handle("POST /api/v1/settings/remnawave/test", admin(s.testRemnawave))
	mux.Handle("POST /api/v1/geoip/proxy", admin(s.locateProxy))
	mux.Handle("GET /api/v1/templates", admin(s.templatesInfo))
	mux.Handle("GET /api/v1/report/preview", admin(s.reportPreview))
	mux.Handle("POST /api/v1/report/send", admin(s.reportSend))
	mux.Handle("POST /api/v1/templates/preview", admin(s.previewTemplate))
	mux.Handle("POST /api/v1/templates/test", admin(s.testTemplate))
	mux.Handle("GET /api/v1/remnawave", view(s.remnawave))
	mux.Handle("GET /api/v1/remnawave/users", view(s.remnawaveUsers))

	mux.Handle("GET /api/v1/users", admin(s.listUsers))
	mux.Handle("POST /api/v1/users", admin(s.createUser))
	mux.Handle("DELETE /api/v1/users/{id}", admin(s.deleteUser))
	mux.Handle("PUT /api/v1/users/{id}/password", view(s.setPassword))

	mux.Handle("GET /api/v1/status-pages", view(s.listStatusPages))
	mux.Handle("PUT /api/v1/status-pages/{slug}", admin(s.putStatusPage))
	mux.Handle("DELETE /api/v1/status-pages/{slug}", admin(s.deleteStatusPage))
	mux.HandleFunc("GET /api/public/status/{slug}", s.publicStatus)
	mux.HandleFunc("GET /api/push/{token}", s.push)
	mux.HandleFunc("POST /api/push/{token}", s.push)

	mux.HandleFunc("GET /install.sh", s.installScript)
	mux.HandleFunc("GET /join.sh", s.joinScript)
	mux.HandleFunc("GET /download/{file}", s.download)

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, errors.New("no such endpoint"))
	})
	mux.HandleFunc("/", s.serveUI)
	return s.securityHeaders(mux)
}

// csp lets the UI load only its own scripts: markup that slips into a page
// (a template preview, say) cannot run. Styles may be inline (Mantine and
// ECharts set them), fonts come bundled (small ones inlined as data:),
// images may be data: or https (provider icons), and the UI only talks to
// the hub itself.
const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: https:; " +
	"font-src 'self' data:; connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", csp)
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		if s.trust.secure(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// fail maps app errors onto HTTP statuses.
func (s *Server) fail(w http.ResponseWriter, err error) {
	var ve *app.ValidationError
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, app.ErrNotFound):
		writeError(w, http.StatusNotFound, errors.New("not found"))
	case errors.Is(err, app.ErrManaged):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, app.ErrExists):
		writeError(w, http.StatusConflict, err)
	default:
		s.log.Error("api", "err", err)
		writeError(w, http.StatusInternalServerError, errors.New("internal error"))
	}
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		return &app.ValidationError{Err: errors.New("bad JSON: " + err.Error())}
	}
	return nil
}

// serveUI serves the SPA: real files when they exist, index.html otherwise.
// A missing file under /assets is a 404: a tab left open across an upgrade
// asks for chunks that are gone and must see that, not index.html.
func (s *Server) serveUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.ui == nil {
		http.Error(w, "web UI is not built", http.StatusNotFound)
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/")
	if p != "" {
		if f, err := s.ui.Open(p); err == nil {
			st, _ := f.Stat()
			f.Close()
			if !st.IsDir() {
				if strings.HasPrefix(p, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				http.ServeFileFS(w, r, s.ui, p)
				return
			}
		}
		if p == "assets" || strings.HasPrefix(p, "assets/") {
			http.NotFound(w, r)
			return
		}
	}
	idx, err := fs.ReadFile(s.ui, "index.html")
	if err != nil {
		http.Error(w, "web UI is not built", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(idx)
}

func decodeJSON(b []byte, v any) error { return json.Unmarshal(b, v) }
