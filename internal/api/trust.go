package api

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strings"

	"github.com/wh3r3ar3you/uptimeforge/internal/hub"
)

// defaultProxies are trusted to set X-Forwarded-* when UF_TRUSTED_PROXIES is
// not set: a reverse proxy on the same host or on the Docker network (which
// is how a host proxy reaches a published container port, too). The internet
// never comes from these, so nobody out there can pick their own address.
var defaultProxies = []netip.Prefix{
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("fc00::/7"),
}

// ParseTrustedProxies reads UF_TRUSTED_PROXIES: comma-separated addresses or
// CIDRs of reverse proxies whose X-Forwarded-For/-Proto/-Host count. "" is
// the default (loopback and private networks); "none" trusts nobody.
func ParseTrustedProxies(s string) ([]netip.Prefix, error) {
	s = strings.TrimSpace(s)
	switch s {
	case "":
		return nil, nil
	case "none":
		return []netip.Prefix{}, nil
	}
	var out []netip.Prefix
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if p, err := netip.ParsePrefix(f); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(f)
		if err != nil {
			return nil, fmt.Errorf("UF_TRUSTED_PROXIES: %q is neither an address nor a CIDR", f)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// trust decides which forwarding headers to believe.
type trust struct{ proxies []netip.Prefix }

// newTrust: nil means the default proxies, an empty list nobody.
func newTrust(p []netip.Prefix) trust {
	if p == nil {
		p = defaultProxies
	}
	return trust{p}
}

func (t trust) trusted(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range t.proxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func remoteAddr(r *http.Request) (netip.Addr, string) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	a, _ := netip.ParseAddr(host)
	return a.Unmap(), host
}

// fromProxy says whether the request came straight from a trusted proxy.
func (t trust) fromProxy(r *http.Request) bool {
	a, _ := remoteAddr(r)
	return a.IsValid() && t.trusted(a)
}

// clientIP walks X-Forwarded-For from the right, past the trusted proxies:
// the first hop no trusted proxy vouches for is the client. Entries left of
// it are whatever the client chose to send.
func (t trust) clientIP(r *http.Request) string {
	if ip := t.clientAddr(r); ip.IsValid() {
		return ip.String()
	}
	_, host := remoteAddr(r)
	return host
}

// clientAddr is clientIP as an address; invalid when RemoteAddr is not one.
func (t trust) clientAddr(r *http.Request) netip.Addr {
	ip, _ := remoteAddr(r)
	if !ip.IsValid() || !t.trusted(ip) {
		return ip
	}
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(v, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		a, ok := parseHop(hops[i])
		if !ok {
			break
		}
		ip = a
		if !t.trusted(a) {
			break
		}
	}
	return ip
}

// agentAddr hands the agent endpoint the agent's address, as seen past the
// trusted proxies: its network is looked up by it.
func (s *Server) agentAddr(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ip := s.trust.clientAddr(r); ip.IsValid() {
			r = r.WithContext(hub.WithClientIP(r.Context(), ip))
		}
		next.ServeHTTP(w, r)
	})
}

func parseHop(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if a, err := netip.ParseAddr(strings.Trim(s, "[]")); err == nil {
		return a.Unmap(), true
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap(), true
	}
	return netip.Addr{}, false
}

// lastValue is the value the proxy nearest to us wrote: with a list, the
// right-most entry.
func lastValue(r *http.Request, name string) string {
	vs := r.Header.Values(name)
	if len(vs) == 0 {
		return ""
	}
	parts := strings.Split(vs[len(vs)-1], ",")
	return strings.TrimSpace(parts[len(parts)-1])
}

// secure says whether the client talks https to us or to our proxy.
func (t trust) secure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return t.fromProxy(r) && strings.EqualFold(lastValue(r, "X-Forwarded-Proto"), "https")
}

// host is the host the client asked for.
func (t trust) host(r *http.Request) string {
	if t.fromProxy(r) {
		if h := lastValue(r, "X-Forwarded-Host"); h != "" {
			return h
		}
	}
	return r.Host
}

// proxied says whether a proxy stands between the client and us.
func proxied(r *http.Request) bool {
	for _, h := range []string{"X-Forwarded-For", "Forwarded", "X-Real-Ip", "X-Forwarded-Host", "X-Forwarded-Proto"} {
		if r.Header.Get(h) != "" {
			return true
		}
	}
	return false
}

// metricsGuard keeps /metrics (every check, probe and target by name) from
// the public. With UF_METRICS_TOKEN it wants that token (or UF_ADMIN_TOKEN)
// as a bearer token. Without it only a scraper on this host or a private
// network gets an answer, and only directly: a request a reverse proxy
// forwarded may come from anywhere, so it gets a 404 like everyone else.
func (s *Server) metricsGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.metricsToken != "" {
			tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || !(tokenEqual(tok, s.metricsToken) || tokenEqual(tok, s.app.Opt.AdminToken)) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
				http.Error(w, "metrics token required", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		a, _ := remoteAddr(r)
		if !a.IsValid() || !(a.IsLoopback() || a.IsPrivate()) || proxied(r) {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// tokenEqual compares a presented token with a configured one in constant
// time; an unset token matches nothing.
func tokenEqual(got, want string) bool {
	return want != "" && got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// limitKey is what a rate limit counts: an address, or for IPv6 its /64,
// since one host usually owns the whole block.
func limitKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil || !a.Unmap().Is6() {
		return ip
	}
	p, _ := a.Prefix(64)
	return p.String()
}

// hostRe is what a host may look like in links and the install script: a
// name or address with an optional port, nothing a shell would act on.
var hostRe = regexp.MustCompile(`^(\[[0-9A-Fa-f:.]+\]|[A-Za-z0-9.-]+)(:[0-9]{1,5})?$`)

func validHost(h string) bool { return h != "" && len(h) <= 255 && hostRe.MatchString(h) }

// safeShell are the bytes that need no quoting in sh.
var safeShell = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellQuote makes s one sh word that expands to nothing.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellWord quotes s only when sh would otherwise act on it, so commands
// shown in the UI stay readable.
func shellWord(s string) string {
	if safeShell.MatchString(s) {
		return s
	}
	return shellQuote(s)
}
