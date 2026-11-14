package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/wh3r3ar3you/uptimeforge/internal/app"
	"github.com/wh3r3ar3you/uptimeforge/internal/checks"
	"github.com/wh3r3ar3you/uptimeforge/internal/hub"
	"github.com/wh3r3ar3you/uptimeforge/internal/model"
	"github.com/wh3r3ar3you/uptimeforge/internal/remnawave"
	"github.com/wh3r3ar3you/uptimeforge/internal/store"
)

// Viewers see what is checked and where, never how to log in: credentials in
// share links, subscription URLs, outbounds, proxy links and HTTP headers are
// replaced with a placeholder. Scheme, host and port stay, so the UI still
// says which node and protocol it is. Admins get everything as is.

const hidden = "••••••••"

func isViewer(r *http.Request) bool { return userFrom(r.Context()).Role != store.RoleAdmin }

// redactLink hides the user part and secret parameters of a share or proxy
// link: vless://uuid@host:443?…&sid=… becomes vless://••••••••@host:443?…&sid=••••••••.
func redactLink(s string) string {
	scheme, rest, ok := strings.Cut(strings.TrimSpace(s), "://")
	if !ok || scheme == "" {
		return s
	}
	switch strings.ToLower(scheme) {
	case "vmess":
		return redactVmess(scheme, rest)
	case "ss":
		rest = expandSS(rest)
	}
	rest, frag, hasFrag := strings.Cut(rest, "#")
	rest, query, hasQuery := strings.Cut(rest, "?")
	authority, path, hasPath := strings.Cut(rest, "/")
	if i := strings.LastIndex(authority, "@"); i >= 0 {
		authority = hidden + authority[i:]
	}
	out := scheme + "://" + authority
	if hasPath {
		out += "/" + redactPath(path)
	}
	if hasQuery {
		out += "?" + redactQuery(query)
	}
	if hasFrag {
		out += "#" + frag
	}
	return out
}

// redactPath hides path segments that look like tokens (checks.Tokenish):
// a subscription page at /p_q7Xk2LmN9vRt4Z hands out a user's VPN configs
// to whoever has it; /api/v1/health or /telegram-widget stay. What is hidden
// here is what checks.URLCarriesSecret keeps off untrusted checkers.
func redactPath(path string) string {
	segs := strings.Split(path, "/")
	for i, seg := range segs {
		if checks.Tokenish(seg) {
			segs[i] = hidden
		}
	}
	return strings.Join(segs, "/")
}

// redactQuery hides values of secret keys, and values that look like tokens
// whatever their key is called (?room=Ab12…, ?c=…).
func redactQuery(q string) string {
	parts := strings.Split(q, "&")
	for i, kv := range parts {
		if k, v, ok := strings.Cut(kv, "="); ok && (checks.SecretKey(k) || checks.Tokenish(v)) {
			parts[i] = k + "=" + hidden
		}
	}
	return strings.Join(parts, "&")
}

// expandSS turns the all-base64 form ss://base64(method:pass@host:port)#name
// into the plain one, so the password can be cut out like in any other link.
func expandSS(rest string) string {
	body, frag, hasFrag := strings.Cut(rest, "#")
	if strings.Contains(body, "@") {
		return rest
	}
	b, err := decodeB64(body)
	if err != nil || !strings.Contains(string(b), "@") {
		return rest
	}
	if hasFrag {
		return string(b) + "#" + frag
	}
	return string(b)
}

func redactVmess(scheme, rest string) string {
	b, err := decodeB64(rest)
	var v map[string]any
	if err != nil || json.Unmarshal(b, &v) != nil {
		return scheme + "://" + hidden
	}
	out, _ := json.Marshal(redactTree(v))
	return scheme + "://" + base64.StdEncoding.EncodeToString(out)
}

func decodeB64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return base64.StdEncoding.DecodeString(s)
}

// redactSubscription keeps scheme, host and port: the token of a
// subscription URL usually is its path or query.
func redactSubscription(s string) string {
	scheme, rest, ok := strings.Cut(strings.TrimSpace(s), "://")
	if !ok {
		return hidden
	}
	authority, tail := rest, ""
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		authority, tail = rest[:i], rest[i:]
	}
	if i := strings.LastIndex(authority, "@"); i >= 0 {
		authority = authority[i+1:]
	}
	if tail != "" && tail != "/" {
		tail = "/" + hidden
	}
	return scheme + "://" + authority + tail
}

// redactTree copies an outbound (or a vmess JSON) with credential values hidden.
func redactTree(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if checks.SecretKey(k) {
				out[k] = hidden
			} else {
				out[k] = redactTree(val)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = redactTree(val)
		}
		return out
	}
	return v
}

func redactHeaders(h map[string]string) map[string]string {
	if h == nil {
		return nil
	}
	out := make(map[string]string, len(h))
	for k := range h {
		out[k] = hidden
	}
	return out
}

func redactSpec(s checks.Spec) checks.Spec {
	if x := s.Xray; x != nil {
		c := *x
		if c.Link != "" {
			c.Link = redactLink(c.Link)
		}
		if c.Subscription != "" {
			c.Subscription = redactSubscription(c.Subscription)
		}
		if c.Outbound != nil {
			c.Outbound = redactTree(c.Outbound).(map[string]any)
		}
		s.Xray = &c
	}
	if m := s.MTProto; m != nil {
		c := *m
		if c.Link != "" {
			c.Link = redactLink(c.Link)
		}
		if c.Secret != "" {
			c.Secret = hidden
		}
		s.MTProto = &c
	}
	if h := s.HTTP; h != nil {
		c := h.MaskSecrets(hidden)
		c.URL, c.Headers = redactLink(c.URL), redactHeaders(c.Headers)
		s.HTTP = c
	}
	s.Params = checks.MaskParams(s.Type, s.Params, hidden)
	for k, v := range s.Params {
		if v != hidden && checks.URLParam(s.Type, k) {
			lines := strings.Split(v, "\n")
			for i, l := range lines {
				lines[i] = redactLink(l)
			}
			s.Params[k] = strings.Join(lines, "\n")
		}
	}
	return s
}

func redactCheck(c hub.CheckConfig) hub.CheckConfig {
	c.Spec = redactSpec(c.Spec)
	return c
}

func redactProbe(p hub.ProbeConfig) hub.ProbeConfig {
	if p.Via != "" {
		p.Via = redactLink(p.Via)
	}
	if p.Baseline != nil {
		b := make([]checks.Spec, len(p.Baseline))
		for i, s := range p.Baseline {
			b[i] = redactSpec(s)
		}
		p.Baseline = b
	}
	return p
}

// redactRows hides credentials in targets (an HTTP check's URL may carry
// them) and in the incidents attached to rows.
func redactRows(rows []CheckRow) {
	for i := range rows {
		rows[i].Target = redactLink(rows[i].Target)
		if inc := rows[i].Incident; inc != nil {
			c := *inc
			c.Target = redactLink(c.Target)
			rows[i].Incident = &c
		}
	}
}

func redactIncidents(incs []model.Incident) {
	for i := range incs {
		incs[i].Target = redactLink(incs[i].Target)
	}
}

// redactRemnawave is the panel's state for a viewer: nodes and users online,
// not how the panel reaches a node. The proxy URL (with its login), the
// provider's login page and every IP that is not a node's public inbound or
// outbound address are left out. The cached view is not touched.
func redactRemnawave(v app.RemnawaveView) app.RemnawaveView {
	if v.Nodes == nil {
		return v
	}
	nodes := make([]remnawave.Node, len(v.Nodes))
	for i, n := range v.Nodes {
		n.ProxyURL = nil
		var ips []remnawave.NodeIP
		for _, ip := range n.IPs {
			if ip.Status == "INBOUND" || ip.Status == "OUTBOUND" {
				ips = append(ips, ip)
			}
		}
		n.IPs = ips
		if n.Provider != nil {
			p := *n.Provider
			p.LoginURL = nil
			n.Provider = &p
		}
		nodes[i] = n
	}
	v.Nodes = nodes
	return v
}

// redactProbeStatus hides where agents connect from: a checker's address is
// its owner's home. The country and the operator stay.
func redactProbeStatus(ps []hub.ProbeStatus) {
	for i := range ps {
		if n := ps[i].Network; n != nil && n.IP != "" {
			c := *n
			c.IP = ""
			ps[i].Network = &c
		}
	}
}
