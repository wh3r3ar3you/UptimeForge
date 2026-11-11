// Package xray turns share links and subscriptions into Xray outbounds and
// runs connectivity probes through an embedded xray-core instance.
package xray

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Outbound is an Xray outbound object exactly as it appears in config JSON.
type Outbound map[string]any

// Link is a parsed share link.
type Link struct {
	Name     string
	Protocol string
	Host     string
	Port     int
	Outbound Outbound
}

// Endpoint returns host:port of the server the link points to.
func (l *Link) Endpoint() string { return net.JoinHostPort(l.Host, strconv.Itoa(l.Port)) }

// TCPEndpoint returns host:port of the server an outbound connects to over
// TCP, or "" when it is not known or the transport is not TCP (Hysteria,
// WireGuard, mKCP, QUIC). It reads the flat settings of share links as well as
// the vnext/servers lists of hand-written outbounds.
func (o Outbound) TCPEndpoint() string {
	switch o["protocol"] {
	case "hysteria", "hysteria2", "wireguard":
		return ""
	}
	if ss, _ := o["streamSettings"].(map[string]any); ss != nil {
		switch ss["network"] {
		case "kcp", "mkcp", "quic":
			return ""
		}
	}
	s, _ := o["settings"].(map[string]any)
	if s == nil {
		return ""
	}
	for _, list := range []string{"vnext", "servers"} {
		if l, _ := s[list].([]any); len(l) > 0 {
			s, _ = l[0].(map[string]any)
			break
		}
	}
	host, _ := s["address"].(string)
	var port int
	switch p := s["port"].(type) {
	case int:
		port = p
	case float64:
		port = int(p)
	case string:
		port, _ = strconv.Atoi(p)
	}
	if host == "" || port <= 0 {
		return ""
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// ParseLink parses vless://, vmess://, trojan://, ss://, hysteria2:// (hy2://),
// socks:// and http(s):// links.
func ParseLink(raw string) (l *Link, err error) {
	// Links come from subscriptions we don't control; a parser bug must cost
	// one line, not the process.
	defer func() {
		if r := recover(); r != nil {
			l, err = nil, fmt.Errorf("malformed link")
		}
	}()
	raw = strings.TrimSpace(raw)
	scheme, _, ok := strings.Cut(raw, "://")
	if !ok {
		return nil, fmt.Errorf("not a share link")
	}
	switch strings.ToLower(scheme) {
	case "vless":
		return parseVLESS(raw)
	case "trojan":
		return parseTrojan(raw)
	case "vmess":
		return parseVMess(raw)
	case "ss":
		return parseShadowsocks(raw)
	case "hysteria2", "hy2":
		return parseHysteria2(raw)
	case "socks", "socks5", "http", "https":
		return parseSimpleProxy(raw)
	}
	return nil, fmt.Errorf("unsupported scheme %q", scheme)
}

func splitHostPort(u *url.URL) (string, int, error) {
	host := u.Hostname()
	if host == "" {
		return "", 0, fmt.Errorf("missing host")
	}
	// hysteria2 port hopping: "443,20000-30000" — the first port is enough to probe.
	p := u.Port()
	if p == "" {
		if i := strings.LastIndex(u.Host, ":"); i >= 0 {
			if f := strings.FieldsFunc(u.Host[i+1:], func(r rune) bool { return r == ',' || r == '-' }); len(f) > 0 {
				p = f[0]
			}
		}
	}
	port, err := strconv.Atoi(p)
	if err != nil || port <= 0 || port > 65535 {
		return "", 0, fmt.Errorf("bad port %q", p)
	}
	return host, port, nil
}

// parseURL tolerates port lists in the authority that net/url rejects.
func parseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err == nil {
		return u, nil
	}
	scheme, rest, _ := strings.Cut(raw, "://")
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		end = len(rest)
	}
	auth := rest[:end]
	at := strings.LastIndex(auth, "@")
	hostport := auth[at+1:]
	i := strings.LastIndex(hostport, ":")
	if i < 0 {
		return nil, err
	}
	first := strings.FieldsFunc(hostport[i+1:], func(r rune) bool { return r == ',' || r == '-' })
	if len(first) == 0 {
		return nil, err
	}
	fixed := scheme + "://" + auth[:at+1] + hostport[:i+1] + first[0] + rest[end:]
	return url.Parse(fixed)
}

func linkName(u *url.URL) string {
	if u.Fragment != "" {
		return u.Fragment
	}
	return u.Host
}

func parseVLESS(raw string) (*Link, error) {
	u, err := parseURL(raw)
	if err != nil {
		return nil, err
	}
	host, port, err := splitHostPort(u)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	id := u.User.Username()
	if id == "" {
		return nil, fmt.Errorf("vless: missing uuid")
	}
	enc := q.Get("encryption")
	if enc == "" {
		enc = "none"
	}
	settings := map[string]any{"address": host, "port": port, "id": id, "encryption": enc}
	if f := q.Get("flow"); f != "" {
		settings["flow"] = f
	}
	stream, err := streamSettings(q, host, "none")
	if err != nil {
		return nil, err
	}
	return &Link{Name: linkName(u), Protocol: "vless", Host: host, Port: port,
		Outbound: Outbound{"protocol": "vless", "settings": settings, "streamSettings": stream}}, nil
}

func parseTrojan(raw string) (*Link, error) {
	u, err := parseURL(raw)
	if err != nil {
		return nil, err
	}
	host, port, err := splitHostPort(u)
	if err != nil {
		return nil, err
	}
	pass := u.User.Username()
	if pass == "" {
		return nil, fmt.Errorf("trojan: missing password")
	}
	stream, err := streamSettings(u.Query(), host, "tls")
	if err != nil {
		return nil, err
	}
	return &Link{Name: linkName(u), Protocol: "trojan", Host: host, Port: port,
		Outbound: Outbound{"protocol": "trojan",
			"settings":       map[string]any{"address": host, "port": port, "password": pass},
			"streamSettings": stream}}, nil
}

func decodeB64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, "=")
	for _, enc := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("invalid base64")
}

func parseVMess(raw string) (*Link, error) {
	body := raw[strings.Index(raw, "://")+3:]
	data, err := decodeB64(body)
	if err != nil {
		return nil, fmt.Errorf("vmess: %w", err)
	}
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("vmess: %w", err)
	}
	str := func(k string) string {
		switch x := v[k].(type) {
		case string:
			return x
		case float64:
			return strconv.FormatFloat(x, 'f', -1, 64)
		}
		return ""
	}
	host := str("add")
	port, _ := strconv.Atoi(str("port"))
	if host == "" || port == 0 {
		return nil, fmt.Errorf("vmess: missing address")
	}
	q := url.Values{}
	q.Set("type", str("net"))
	if str("tls") == "tls" {
		q.Set("security", "tls")
	}
	for src, dst := range map[string]string{"sni": "sni", "fp": "fp", "alpn": "alpn", "host": "host", "path": "path"} {
		if s := str(src); s != "" {
			q.Set(dst, s)
		}
	}
	switch str("net") {
	case "grpc":
		q.Set("serviceName", str("path"))
		if str("type") == "multi" {
			q.Set("mode", "multi")
		}
	case "tcp", "":
		q.Set("headerType", str("type"))
	}
	stream, err := streamSettings(q, host, "none")
	if err != nil {
		return nil, err
	}
	sec := str("scy")
	if sec == "" {
		sec = "auto"
	}
	return &Link{Name: str("ps"), Protocol: "vmess", Host: host, Port: port,
		Outbound: Outbound{"protocol": "vmess",
			"settings":       map[string]any{"address": host, "port": port, "id": str("id"), "security": sec},
			"streamSettings": stream}}, nil
}

// parseShadowsocks handles SIP002 (base64 or plain userinfo) and the legacy
// fully-base64 form.
func parseShadowsocks(raw string) (*Link, error) {
	body := raw[strings.Index(raw, "://")+3:]
	name := ""
	if i := strings.Index(body, "#"); i >= 0 {
		name, _ = url.PathUnescape(body[i+1:])
		body = body[:i]
	}
	if !strings.Contains(body, "@") {
		dec, err := decodeB64(strings.SplitN(body, "?", 2)[0])
		if err != nil {
			return nil, fmt.Errorf("ss: %w", err)
		}
		body = string(dec)
	}
	u, err := url.Parse("ss://" + body)
	if err != nil {
		return nil, fmt.Errorf("ss: %w", err)
	}
	if p := u.Query().Get("plugin"); p != "" {
		return nil, fmt.Errorf("ss: plugin %q is not supported", p)
	}
	host, port, err := splitHostPort(u)
	if err != nil {
		return nil, err
	}
	method, pass := u.User.Username(), ""
	if p, ok := u.User.Password(); ok {
		pass = p
	} else if dec, err := decodeB64(method); err == nil {
		method, pass, _ = strings.Cut(string(dec), ":")
	}
	if method == "" || pass == "" {
		return nil, fmt.Errorf("ss: missing method or password")
	}
	if name == "" {
		name = u.Host
	}
	return &Link{Name: name, Protocol: "shadowsocks", Host: host, Port: port,
		Outbound: Outbound{"protocol": "shadowsocks",
			"settings": map[string]any{"address": host, "port": port, "method": method, "password": pass}}}, nil
}

func parseHysteria2(raw string) (*Link, error) {
	u, err := parseURL(raw)
	if err != nil {
		return nil, err
	}
	host, port, err := splitHostPort(u)
	if err != nil {
		return nil, err
	}
	auth := u.User.Username()
	if p, ok := u.User.Password(); ok {
		auth += ":" + p
	}
	q := u.Query()
	tls := map[string]any{"serverName": firstNonEmpty(q.Get("sni"), host), "alpn": []string{"h3"}}
	if q.Get("insecure") == "1" || q.Get("allowInsecure") == "1" {
		tls["allowInsecure"] = true
	}
	if pin := q.Get("pinSHA256"); pin != "" {
		tls["pinnedPeerCertSha256"] = pin
	}
	stream := map[string]any{
		"network":          "hysteria",
		"security":         "tls",
		"tlsSettings":      tls,
		"hysteriaSettings": map[string]any{"version": 2, "auth": auth},
	}
	if q.Get("obfs") == "salamander" {
		stream["finalmask"] = map[string]any{"udp": []any{
			map[string]any{"type": "salamander", "settings": map[string]any{"password": q.Get("obfs-password")}},
		}}
	}
	return &Link{Name: linkName(u), Protocol: "hysteria2", Host: host, Port: port,
		Outbound: Outbound{"protocol": "hysteria",
			"settings":       map[string]any{"version": 2, "address": host, "port": port},
			"streamSettings": stream}}, nil
}

func parseSimpleProxy(raw string) (*Link, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	host, port, err := splitHostPort(u)
	if err != nil {
		return nil, err
	}
	settings := map[string]any{"address": host, "port": port}
	if user := u.User.Username(); user != "" {
		pass, _ := u.User.Password()
		settings["user"], settings["pass"] = user, pass
	}
	proto := "socks"
	ob := Outbound{"settings": settings}
	if s := u.Scheme; s == "http" || s == "https" {
		proto = "http"
		if s == "https" {
			ob["streamSettings"] = map[string]any{"security": "tls", "tlsSettings": map[string]any{"serverName": host}}
		}
	}
	ob["protocol"] = proto
	return &Link{Name: linkName(u), Protocol: proto, Host: host, Port: port, Outbound: ob}, nil
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// streamSettings maps the de-facto share-link query parameters (as produced by
// Xray/Remnawave/v2rayN) onto Xray streamSettings.
func streamSettings(q url.Values, host, defaultSecurity string) (map[string]any, error) {
	network := strings.ToLower(firstNonEmpty(q.Get("type"), "tcp"))
	switch network {
	case "raw":
		network = "tcp"
	case "splithttp":
		network = "xhttp"
	case "h2", "http", "quic":
		return nil, fmt.Errorf("transport %q was removed from xray-core, use xhttp", network)
	}
	s := map[string]any{"network": network}

	path, hostHeader := q.Get("path"), q.Get("host")
	switch network {
	case "tcp":
		if q.Get("headerType") == "http" {
			req := map[string]any{"path": []string{firstNonEmpty(path, "/")}}
			if hostHeader != "" {
				req["headers"] = map[string]any{"Host": strings.Split(hostHeader, ",")}
			}
			s["tcpSettings"] = map[string]any{"header": map[string]any{"type": "http", "request": req}}
		}
	case "ws":
		s["wsSettings"] = map[string]any{"path": firstNonEmpty(path, "/"), "host": hostHeader}
	case "httpupgrade":
		s["httpupgradeSettings"] = map[string]any{"path": firstNonEmpty(path, "/"), "host": hostHeader}
	case "grpc":
		g := map[string]any{"serviceName": q.Get("serviceName"), "multiMode": q.Get("mode") == "multi"}
		if a := q.Get("authority"); a != "" {
			g["authority"] = a
		}
		s["grpcSettings"] = g
	case "xhttp":
		x := map[string]any{"path": firstNonEmpty(path, "/"), "host": hostHeader, "mode": firstNonEmpty(q.Get("mode"), "auto")}
		if extra := q.Get("extra"); extra != "" {
			var e any
			if err := json.Unmarshal([]byte(extra), &e); err != nil {
				return nil, fmt.Errorf("xhttp extra: %w", err)
			}
			x["extra"] = e
		}
		s["xhttpSettings"] = x
	case "kcp":
	default:
		return nil, fmt.Errorf("unsupported transport %q", network)
	}

	security := strings.ToLower(firstNonEmpty(q.Get("security"), defaultSecurity))
	switch security {
	case "none", "":
	case "tls":
		t := map[string]any{"serverName": firstNonEmpty(q.Get("sni"), hostHeader, host)}
		if fp := q.Get("fp"); fp != "" {
			t["fingerprint"] = fp
		}
		if alpn := q.Get("alpn"); alpn != "" {
			t["alpn"] = strings.Split(alpn, ",")
		}
		if q.Get("allowInsecure") == "1" || q.Get("insecure") == "1" {
			t["allowInsecure"] = true
		}
		if ech := q.Get("ech"); ech != "" {
			t["echConfigList"] = ech
		}
		if pin := q.Get("pcs"); pin != "" {
			t["pinnedPeerCertSha256"] = pin
		}
		s["security"], s["tlsSettings"] = "tls", t
	case "reality":
		pbk := q.Get("pbk")
		if pbk == "" {
			return nil, fmt.Errorf("reality: missing pbk")
		}
		r := map[string]any{
			"serverName":  q.Get("sni"),
			"fingerprint": firstNonEmpty(q.Get("fp"), "chrome"),
			"publicKey":   pbk,
			"shortId":     q.Get("sid"),
			"spiderX":     q.Get("spx"),
		}
		if pqv := q.Get("pqv"); pqv != "" {
			r["mldsa65Verify"] = pqv
		}
		s["security"], s["realitySettings"] = "reality", r
	default:
		return nil, fmt.Errorf("unsupported security %q", security)
	}
	return s, nil
}
