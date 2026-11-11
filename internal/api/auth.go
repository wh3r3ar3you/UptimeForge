package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/store"
)

const (
	sessionCookie = "uf_session"
	sessionTTL    = 30 * 24 * time.Hour
)

type ctxKey struct{}

func userFrom(ctx context.Context) store.User {
	u, _ := ctx.Value(ctxKey{}).(store.User)
	return u
}

// minAdminToken is the shortest UF_ADMIN_TOKEN the hub starts with.
const minAdminToken = 32

// CheckAdminToken refuses an admin token short enough to guess.
func CheckAdminToken(tok string) error {
	if tok != "" && len(tok) < minAdminToken {
		return fmt.Errorf("UF_ADMIN_TOKEN is %d characters, too short to be safe: use at least %d (openssl rand -hex 32)", len(tok), minAdminToken)
	}
	return nil
}

var (
	errNotSignedIn = errors.New("not signed in")
	errBadBearer   = errors.New("wrong bearer token")
	errThrottled   = errors.New("too many wrong tokens, try again in a minute")
)

// authenticate resolves the admin bearer token (scripts) or the session
// cookie. A request carrying a bearer token is judged by it alone; wrong
// ones count per address, and past the limit even the right one waits.
func (s *Server) authenticate(r *http.Request) (store.User, error) {
	if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && tok != "" {
		key := limitKey(s.clientIP(r))
		if s.bearerLimit.blocked(key) {
			return store.User{}, errThrottled
		}
		if !tokenEqual(tok, s.app.Opt.AdminToken) {
			s.bearerLimit.hit(key)
			s.log.Warn("wrong bearer token", "ip", s.clientIP(r))
			return store.User{}, errBadBearer
		}
		return store.User{Username: "api", Role: store.RoleAdmin}, nil
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return store.User{}, errNotSignedIn
	}
	u, err := s.app.Store.SessionUser(r.Context(), c.Value)
	if err != nil {
		return store.User{}, errNotSignedIn
	}
	return u, nil
}

// require wraps a handler with authentication, a role check and CSRF
// protection for state-changing requests.
func (s *Server) require(role store.Role) func(http.HandlerFunc) http.Handler {
	return func(next http.HandlerFunc) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, err := s.authenticate(r)
			if errors.Is(err, errThrottled) {
				writeError(w, http.StatusTooManyRequests, err)
				return
			}
			if err != nil {
				writeError(w, http.StatusUnauthorized, err)
				return
			}
			if role == store.RoleAdmin && u.Role != store.RoleAdmin {
				writeError(w, http.StatusForbidden, errors.New("admin role required"))
				return
			}
			if r.Method != http.MethodGet && !s.sameOrigin(r) {
				writeError(w, http.StatusForbidden, errors.New("cross-site request rejected"))
				return
			}
			next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
		})
	}
}

// sameOrigin rejects cross-site form posts: browsers send Origin on POST/PUT/DELETE,
// and SameSite=Lax already keeps the cookie off most of them.
func (s *Server) sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return r.Header.Get("Sec-Fetch-Site") != "cross-site"
	}
	return strings.TrimPrefix(strings.TrimPrefix(o, "https://"), "http://") == s.trust.host(r)
}

func (s *Server) clientIP(r *http.Request) string { return s.trust.clientIP(r) }

func (s *Server) setCookie(w http.ResponseWriter, r *http.Request, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: s.trust.secure(r), SameSite: http.SameSiteLaxMode})
}

func (s *Server) authState(w http.ResponseWriter, r *http.Request) {
	n, err := s.app.Store.UserCount(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	out := map[string]any{"setup_required": n == 0}
	if n == 0 {
		out["setup_token_required"] = true
	}
	if u, err := s.authenticate(r); err == nil {
		out["user"] = u
	}
	writeJSON(w, http.StatusOK, out)
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// announceSetup makes up the setup token when UF_SETUP_TOKEN is not set and,
// while the hub has no users, says in the log how to create the first admin.
// The token is only good for that: once an admin exists setup is refused.
func (s *Server) announceSetup() {
	generated := s.setupToken == ""
	if generated {
		s.setupToken = randomToken()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if n, err := s.app.Store.UserCount(ctx); err != nil || n > 0 {
		return
	}
	if generated {
		s.log.Warn("no users yet: open the web UI and create the first admin with this setup token (it changes on restart; set UF_SETUP_TOKEN to pin it)",
			"setup_token", s.setupToken)
		return
	}
	s.log.Warn("no users yet: open the web UI and create the first admin with the token from UF_SETUP_TOKEN")
}

func randomToken() string {
	b := make([]byte, 24)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// errSetupToken is what a setup without the right token gets.
var errSetupToken = errors.New("wrong or missing setup token: the hub prints it to its log at startup, or set UF_SETUP_TOKEN")

// setup creates the first admin. It needs the setup token and only works
// while there are no users; racing requests make one admin.
func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	if !s.setupLimit.allow(limitKey(s.clientIP(r))) {
		writeError(w, http.StatusTooManyRequests, errors.New("too many attempts, try again in a minute"))
		return
	}
	var body struct {
		credentials
		SetupToken string `json:"setup_token"`
	}
	if err := decode(r, &body); err != nil {
		s.fail(w, err)
		return
	}
	n, err := s.app.Store.UserCount(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if n > 0 {
		writeError(w, http.StatusConflict, store.ErrSetUp)
		return
	}
	if !tokenEqual(body.SetupToken, s.setupToken) {
		s.log.Warn("setup refused: wrong setup token", "ip", s.clientIP(r))
		writeError(w, http.StatusForbidden, errSetupToken)
		return
	}
	u, err := s.app.Store.CreateFirstAdmin(r.Context(), body.Username, body.Password)
	switch {
	case errors.Is(err, store.ErrSetUp):
		writeError(w, http.StatusConflict, err)
		return
	case err != nil:
		writeError(w, http.StatusBadRequest, err)
		return
	}
	s.log.Info("first admin created", "user", u.Username, "ip", s.clientIP(r))
	s.startSession(w, r, u)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.limiter.allow(limitKey(ip)) {
		writeError(w, http.StatusTooManyRequests, errors.New("too many attempts, try again in a minute"))
		return
	}
	var c credentials
	if err := decode(r, &c); err != nil {
		s.fail(w, err)
		return
	}
	// Per account too: an attacker with many addresses still gets few
	// guesses at one password.
	if !s.userLimit.allow(strings.ToLower(strings.TrimSpace(c.Username))) {
		s.log.Warn("login throttled", "user", c.Username, "ip", ip)
		writeError(w, http.StatusTooManyRequests, errors.New("too many attempts for this user, try again in a few minutes"))
		return
	}
	u, err := s.app.Store.Authenticate(r.Context(), c.Username, c.Password)
	if errors.Is(err, store.ErrBadCredentials) {
		s.log.Warn("failed login", "user", c.Username, "ip", ip)
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.startSession(w, r, u)
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u store.User) {
	tok, err := s.app.Store.CreateSession(r.Context(), u.ID, sessionTTL)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.setCookie(w, r, tok, int(sessionTTL.Seconds()))
	writeJSON(w, http.StatusOK, map[string]any{"user": u})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.app.Store.DeleteSession(r.Context(), c.Value)
	}
	s.setCookie(w, r, "", -1)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// limiter is a fixed-window counter per key, enough to stop password guessing.
type limiter struct {
	mu     sync.Mutex
	n      int
	window time.Duration
	hits   map[string]*hitWindow
}

type hitWindow struct {
	start time.Time
	count int
}

func newLimiter(n int, window time.Duration) *limiter {
	return &limiter{n: n, window: window, hits: map[string]*hitWindow{}}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.hits) > 10000 {
		for k, h := range l.hits {
			if now.Sub(h.start) > l.window {
				delete(l.hits, k)
			}
		}
	}
	h := l.hits[key]
	if h == nil || now.Sub(h.start) > l.window {
		h = &hitWindow{start: now}
		l.hits[key] = h
	}
	h.count++
	return h.count <= l.n
}

// current is key's current window; l.mu must be held.
func (l *limiter) current(key string, now time.Time) *hitWindow {
	h := l.hits[key]
	if h == nil || now.Sub(h.start) > l.window {
		h = &hitWindow{start: now}
		l.hits[key] = h
	}
	return h
}

// blocked says whether key used up its window, without counting.
func (l *limiter) blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	h := l.hits[key]
	return h != nil && time.Since(h.start) <= l.window && h.count >= l.n
}

// hit counts a failure for key.
func (l *limiter) hit(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.current(key, time.Now()).count++
}
