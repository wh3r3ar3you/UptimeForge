// Package checks defines check specs, results and the checkers that run on
// probes. Specs travel from hub to agents as JSON and are written by users in
// YAML, so both encodings are supported.
package checks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration written as "30s" / "5m" in YAML and JSON.
type Duration time.Duration

func (d Duration) D() time.Duration { return time.Duration(d) }

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	*d = Duration(v)
	return err
}

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*d = Duration(v)
	return nil
}

// Spec is everything a probe needs to run one check.
type Spec struct {
	ID       string   `json:"id" yaml:"id"`
	Type     string   `json:"type" yaml:"type"`
	Interval Duration `json:"interval,omitempty" yaml:"interval,omitempty"`
	Timeout  Duration `json:"timeout,omitempty" yaml:"timeout,omitempty"`

	HTTP    *HTTPParams    `json:"http,omitempty" yaml:"http,omitempty"`
	TCP     *TCPParams     `json:"tcp,omitempty" yaml:"tcp,omitempty"`
	TLS     *TLSParams     `json:"tls,omitempty" yaml:"tls,omitempty"`
	DNS     *DNSParams     `json:"dns,omitempty" yaml:"dns,omitempty"`
	Xray    *XrayParams    `json:"xray,omitempty" yaml:"xray,omitempty"`
	MTProto *MTProtoParams `json:"mtproto,omitempty" yaml:"mtproto,omitempty"`

	// Params carries the settings of every other type, described by the
	// catalog (catalog.go). Keeping them flat lets the UI, Validate and
	// redaction work from that one description.
	Params map[string]string `json:"params,omitempty" yaml:"params,omitempty"`

	// Keyword is matched against the text a type returns; see Result.Text.
	Keyword       string `json:"keyword,omitempty" yaml:"keyword,omitempty"`
	KeywordInvert bool   `json:"keyword_invert,omitempty" yaml:"keyword_invert,omitempty"`
}

type HTTPParams struct {
	URL          string            `json:"url" yaml:"url"`
	Method       string            `json:"method,omitempty" yaml:"method,omitempty"`
	Headers      map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
	ExpectStatus []int             `json:"expect_status,omitempty" yaml:"expect_status,omitempty"`
	Keyword      string            `json:"keyword,omitempty" yaml:"keyword,omitempty"`
	Insecure     bool              `json:"insecure,omitempty" yaml:"insecure,omitempty"`

	Body string `json:"body,omitempty" yaml:"body,omitempty"`
	// AcceptedStatus holds Kuma-style codes and ranges ("200-299", "404"). It
	// adds to ExpectStatus; when both are empty any 2xx/3xx is fine.
	AcceptedStatus []string `json:"accepted_status,omitempty" yaml:"accepted_status,omitempty"`
	// MaxRedirects: nil keeps the default of 10, 0 does not follow any.
	MaxRedirects  *int   `json:"max_redirects,omitempty" yaml:"max_redirects,omitempty"`
	BasicUser     string `json:"basic_user,omitempty" yaml:"basic_user,omitempty"`
	BasicPassword string `json:"basic_password,omitempty" yaml:"basic_password,omitempty"`
	BearerToken   string `json:"bearer_token,omitempty" yaml:"bearer_token,omitempty"`
	// TLSCert and TLSKey are a PEM client certificate for mTLS.
	TLSCert       string `json:"tls_cert,omitempty" yaml:"tls_cert,omitempty"`
	TLSKey        string `json:"tls_key,omitempty" yaml:"tls_key,omitempty"`
	InvertKeyword bool   `json:"invert_keyword,omitempty" yaml:"invert_keyword,omitempty"`
	// JSONPath selects values from a JSON response, JSONOp compares them with
	// JSONExpect. See compareValue for the operators.
	JSONPath   string `json:"json_path,omitempty" yaml:"json_path,omitempty"`
	JSONOp     string `json:"json_op,omitempty" yaml:"json_op,omitempty"`
	JSONExpect string `json:"json_expect,omitempty" yaml:"json_expect,omitempty"`
}

type TCPParams struct {
	Address string `json:"address" yaml:"address"`
}

type TLSParams struct {
	Address    string `json:"address" yaml:"address"`
	ServerName string `json:"server_name,omitempty" yaml:"server_name,omitempty"`
	// MinDays is the days a certificate must still have: nil means 7, 0 turns
	// the expiry alert off.
	MinDays *int `json:"min_days,omitempty" yaml:"min_days,omitempty"`
}

type DNSParams struct {
	Name   string `json:"name" yaml:"name"`
	Server string `json:"server,omitempty" yaml:"server,omitempty"`
	Expect string `json:"expect,omitempty" yaml:"expect,omitempty"`
	// RecordType asks that exact type of the server directly. Empty keeps the
	// system resolver path, which only knows addresses.
	RecordType string `json:"record_type,omitempty" yaml:"record_type,omitempty"`
	Port       int    `json:"port,omitempty" yaml:"port,omitempty"`
}

type XrayParams struct {
	// One of Link or Outbound. Subscriptions are expanded by the hub into one
	// spec per config, so agents only ever see single configs.
	Link     string         `json:"link,omitempty" yaml:"link,omitempty"`
	Outbound map[string]any `json:"outbound,omitempty" yaml:"outbound,omitempty"`

	Subscription string `json:"subscription,omitempty" yaml:"subscription,omitempty"`
	UserAgent    string `json:"user_agent,omitempty" yaml:"user_agent,omitempty"`
	Filter       string `json:"filter,omitempty" yaml:"filter,omitempty"`

	URL           string   `json:"url,omitempty" yaml:"url,omitempty"`
	DownloadURL   string   `json:"download_url,omitempty" yaml:"download_url,omitempty"`
	DownloadBytes int64    `json:"download_bytes,omitempty" yaml:"download_bytes,omitempty"`
	HeavyEvery    int      `json:"heavy_every,omitempty" yaml:"heavy_every,omitempty"`
	StallTimeout  Duration `json:"stall_timeout,omitempty" yaml:"stall_timeout,omitempty"`
	Egress        *bool    `json:"egress,omitempty" yaml:"egress,omitempty"`
	ExpectEgress  []string `json:"expect_egress,omitempty" yaml:"expect_egress,omitempty"`
}

type MTProtoParams struct {
	// Link (tg://proxy?… or https://t.me/proxy?…), or Address + Secret.
	// Secrets are hex or base64: 16 bytes, dd+16 bytes or ee+16 bytes+domain.
	Link    string `json:"link,omitempty" yaml:"link,omitempty"`
	Address string `json:"address,omitempty" yaml:"address,omitempty"`
	Secret  string `json:"secret,omitempty" yaml:"secret,omitempty"`
	// DC pins the Telegram datacenter asked through the proxy (default: all of them).
	DC int `json:"dc,omitempty" yaml:"dc,omitempty"`
}

// Target is a short human description of what is checked.
func (s *Spec) Target() string {
	if t, ok := TypeOf(s.Type); ok && t.target != nil {
		return t.target(paramsOf(s))
	}
	switch {
	case s.HTTP != nil:
		return s.HTTP.URL
	case s.TCP != nil:
		return s.TCP.Address
	case s.TLS != nil:
		return s.TLS.Address
	case s.DNS != nil:
		return s.DNS.Name
	case s.MTProto != nil:
		return s.MTProto.address()
	}
	return ""
}

type Status string

const (
	Up   Status = "up"
	Down Status = "down"
)

// Result of a single check run on a single probe.
type Result struct {
	CheckID    string             `json:"check"`
	Status     Status             `json:"status"`
	Reason     string             `json:"reason,omitempty"` // timeout, refused, tls, stalled, http_status, ...
	Error      string             `json:"error,omitempty"`
	LatencyMS  float64            `json:"latency_ms"`
	Phases     map[string]float64 `json:"phases,omitempty"` // ms
	Bytes      int64              `json:"bytes,omitempty"`
	Throughput float64            `json:"bps,omitempty"` // bytes per second
	Heavy      bool               `json:"heavy,omitempty"`
	Info       map[string]string  `json:"info,omitempty"`

	// Text is what Spec.Keyword is matched against. It stays on the probe: a
	// response body has no business in a result that is buffered and sent.
	Text string `json:"-"`

	// Age is seconds between measurement and sending, so the receiver can
	// place buffered results in time without trusting the agent's clock.
	Age float64   `json:"age,omitempty"`
	At  time.Time `json:"-"`
}

// Env carries per-probe and per-run context into a checker.
type Env struct {
	// Dial opens connections the way this probe reaches the world: directly,
	// or through the probe's upstream proxy.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// Via is the probe's upstream proxy as an Xray outbound (nil = direct).
	Via json.RawMessage
	// ViaErr is set when Via could not be started. Every check that reaches
	// the network then fails with reason "via": dialing around the proxy would
	// claim it works, and staying silent would hide the probe.
	ViaErr error
	// Seq counts runs of this check on this probe, starting at 0.
	Seq int
	// Escalated is set after a failed heavy run: keep running heavy until it passes.
	Escalated bool
}

// Checker runs one check. It fills Status/Reason/Error and measurements; Run
// fills the rest.
type Checker func(ctx context.Context, s *Spec, env *Env) Result

var registry = map[string]Checker{}

func register(typ string, c Checker) { registry[typ] = c }

// Types lists registered check types.
func Types() []string {
	out := make([]string, 0, len(registry))
	for t := range registry {
		out = append(out, t)
	}
	return out
}

const (
	DefaultInterval = 60 * time.Second
	DefaultTimeout  = 15 * time.Second
)

// Validate checks that the spec has the parameters its type needs.
func (s *Spec) Validate() error {
	if s.ID == "" {
		return fmt.Errorf("check without id")
	}
	if _, ok := registry[s.Type]; !ok {
		return fmt.Errorf("check %s: unknown type %q (have %s)", s.ID, s.Type, strings.Join(Types(), ", "))
	}
	if t, known := TypeOf(s.Type); known {
		if s.Keyword != "" && !t.Keyword {
			return fmt.Errorf("check %s: type %s returns no text to match a keyword against", s.ID, s.Type)
		}
		if t.target != nil {
			if err := validateParams(t, s); err != nil {
				return fmt.Errorf("check %s: %w", s.ID, err)
			}
			return nil
		}
	}
	var ok bool
	switch s.Type {
	case "http":
		ok = s.HTTP != nil && s.HTTP.URL != ""
		if ok {
			if err := s.HTTP.validate(); err != nil {
				return fmt.Errorf("check %s: %w", s.ID, err)
			}
		}
	case "tcp":
		ok = s.TCP != nil && s.TCP.Address != ""
	case "tls":
		ok = s.TLS != nil && s.TLS.Address != ""
	case "dns":
		ok = s.DNS != nil && s.DNS.Name != ""
		if ok {
			if err := s.DNS.validate(); err != nil {
				return fmt.Errorf("check %s: %w", s.ID, err)
			}
		}
	case "xray":
		ok = s.Xray != nil && (s.Xray.Link != "" || len(s.Xray.Outbound) > 0 || s.Xray.Subscription != "")
	case "mtproto":
		ok = s.MTProto != nil && (s.MTProto.Link != "" || s.MTProto.Address != "" && s.MTProto.Secret != "")
		if ok {
			if _, err := s.MTProto.parse(); err != nil {
				return fmt.Errorf("check %s: %w", s.ID, err)
			}
		}
	default:
		ok = true
	}
	if !ok {
		return fmt.Errorf("check %s: missing %s parameters", s.ID, s.Type)
	}
	return nil
}

// Run executes a check with its timeout, never panics and always returns a
// result for the given check.
func Run(ctx context.Context, s *Spec, env *Env) (r Result) {
	timeout := s.Timeout.D()
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if env.Dial == nil {
		env.Dial = (&net.Dialer{}).DialContext
	}
	start := time.Now()
	defer func() {
		if p := recover(); p != nil {
			r = Result{Status: Down, Reason: "panic", Error: fmt.Sprint(p)}
		}
		scrubResult(&r, s, env.Via)
		r.CheckID = s.ID
		r.At = time.Now()
		if r.LatencyMS == 0 {
			r.LatencyMS = ms(time.Since(start))
		}
		if r.Status == "" {
			r.Status = Up
		}
	}()
	c, ok := registry[s.Type]
	if !ok {
		return Result{Status: Down, Reason: "config", Error: "unknown check type " + s.Type}
	}
	if env.ViaErr != nil && !isLocal(s.Type) {
		return Result{Status: Down, Reason: "via", Error: env.ViaErr.Error()}
	}
	r = c(ctx, s, env)
	matchKeyword(s, &r)
	return r
}

// isLocal reports whether a type looks only at the probe's own host (docker,
// services) and so does not depend on its upstream proxy.
func isLocal(typ string) bool {
	t, ok := TypeOf(typ)
	return ok && t.Category == "system"
}

// matchKeyword applies Spec.Keyword to what the checker returned. Text is
// dropped afterwards either way, see Result.Text.
func matchKeyword(s *Spec, r *Result) {
	if s.Keyword != "" && (r.Status == "" || r.Status == Up) {
		found := strings.Contains(r.Text, s.Keyword)
		switch {
		case !found && !s.KeywordInvert:
			r.Status, r.Reason, r.Error = Down, "keyword", fmt.Sprintf("keyword %q not found", s.Keyword)
		case found && s.KeywordInvert:
			r.Status, r.Reason, r.Error = Down, "keyword", fmt.Sprintf("keyword %q found, expected it to be absent", s.Keyword)
		}
	}
	r.Text = ""
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func fail(reason string, err error) Result {
	return Result{Status: Down, Reason: reason, Error: err.Error()}
}

// reasonOf maps common network errors to stable reason codes. Windows words
// socket errors differently ("actively refused", "forcibly closed"), and
// agents run there too.
func reasonOf(ctx context.Context, err error) string {
	if ctx.Err() != nil {
		return "timeout"
	}
	// The transport's own TLS handshake timeout fires before the check's
	// (a DPI blackhole after the ClientHello) and says so only by type.
	if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
		return "timeout"
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "i/o timeout") || strings.Contains(s, "deadline exceeded") || strings.Contains(s, "handshake timeout"):
		return "timeout"
	case strings.Contains(s, "connection refused") || strings.Contains(s, "actively refused"):
		return "refused"
	case strings.Contains(s, "no such host") || strings.Contains(s, "server misbehaving"):
		return "dns"
	case strings.Contains(s, "x509") || strings.Contains(s, "tls:"):
		return "tls"
	case strings.Contains(s, "reset by peer") || strings.Contains(s, "forcibly closed") || strings.Contains(s, "EOF"):
		return "reset"
	case strings.Contains(s, "network is unreachable") || strings.Contains(s, "no route") || strings.Contains(s, "unreachable host") ||
		strings.Contains(s, "unreachable network"):
		return "unreachable"
	}
	return "error"
}

// IsMTProxyLink reports whether s is a tg://proxy or t.me/proxy link.
func IsMTProxyLink(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, p := range []string{"tg://proxy?", "https://t.me/proxy?", "http://t.me/proxy?", "https://telegram.me/proxy?"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
