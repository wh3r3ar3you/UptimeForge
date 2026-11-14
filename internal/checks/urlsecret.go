package checks

import (
	"net/url"
	"regexp"
	"strings"
)

// SecretKey reports whether a query parameter or JSON key carries a credential.
// A percent-encoded key is decoded first; one that cannot be is assumed to.
func SecretKey(k string) bool {
	if strings.Contains(k, "%") {
		d, err := url.QueryUnescape(k)
		if err != nil {
			return true
		}
		k = d
	}
	k = strings.ToLower(k)
	switch k {
	case "id", "uuid", "auth", "psk", "key", "seed", "sid", "shortid", "shortids", "privatekey", "presharedkey":
		return true
	}
	return strings.Contains(k, "pass") || strings.Contains(k, "secret") || strings.Contains(k, "token")
}

// tokenRes are tokens with a shape of their own: a JSON web token (base64url
// header, payload and signature) and a Telegram bot token (bot id, a colon,
// 30+ base64url characters; in a path after "bot").
var tokenRes = []*regexp.Regexp{
	regexp.MustCompile(`^eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*$`),
	regexp.MustCompile(`^(bot)?[0-9]{6,}:[A-Za-z0-9_-]{30,}$`),
}

// Tokenish reports whether a path segment or query value looks like a token:
// 12+ characters of letters, digits, - or _ with both letters and digits
// (base64 "=" padding aside), a JSON web token or a Telegram bot token. A subscription page at
// /p_q7Xk2LmN9vRt4Z hands out a user's VPN configs to whoever has it, a
// Telegram bot at /bot123456:AAH… is its token; /api/v1/health,
// /telegram-widget or jquery-3.7.1.min.js do not count. It is decoded first:
// what cannot be decoded is assumed to hide a token.
func Tokenish(s string) bool {
	if strings.Contains(s, "%") {
		d, err := url.PathUnescape(s)
		if err != nil {
			return true
		}
		s = d
	}
	for _, re := range tokenRes {
		if re.MatchString(s) {
			return true
		}
	}
	s = strings.TrimRight(s, "=")
	if len(s) < 12 {
		return false
	}
	letters, digits := false, false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			letters = true
		case r >= '0' && r <= '9':
			digits = true
		case r == '-', r == '_':
		default:
			return false
		}
	}
	return letters && digits
}

// URLCarriesSecret reports whether a URL holds a credential: a user part
// (user:password@, a share link's uuid@), a token-like path segment, or a
// query parameter with a secret name or a token-like value. These are the
// parts viewers see redacted; a URL with any of them is a secret as a whole
// to an untrusted checker. The fragment never leaves the client and is not
// looked at.
func URLCarriesSecret(raw string) bool {
	_, rest, ok := strings.Cut(strings.TrimSpace(raw), "://")
	if !ok {
		return false
	}
	rest, _, _ = strings.Cut(rest, "#")
	rest, query, _ := strings.Cut(rest, "?")
	authority, path, _ := strings.Cut(rest, "/")
	if strings.Contains(authority, "@") {
		return true
	}
	for _, seg := range strings.Split(path, "/") {
		if Tokenish(seg) {
			return true
		}
	}
	for _, kv := range strings.Split(query, "&") {
		if k, v, ok := strings.Cut(kv, "="); ok && (SecretKey(k) || Tokenish(v)) {
			return true
		}
	}
	return false
}
