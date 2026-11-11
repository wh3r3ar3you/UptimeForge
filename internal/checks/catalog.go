package checks

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Field describes one setting of a check type. The UI builds its form from
// these, Validate checks them and redaction masks those marked Secret.
type Field struct {
	Name     string   `json:"name"`
	Kind     string   `json:"kind"` // text, password, number, bool, select, textarea
	Required bool     `json:"required,omitempty"`
	Secret   bool     `json:"secret,omitempty"`
	Default  string   `json:"default,omitempty"`
	Options  []string `json:"options,omitempty"`
	Advanced bool     `json:"advanced,omitempty"`
	ShowIf   string   `json:"show_if,omitempty"` // "field=value"
	// URL marks a field holding URLs, one per line: viewers see them with
	// token-like path segments and secret query values masked.
	URL bool `json:"-"`
}

// TypeInfo describes a check type, or a preset of one. For http and dns the
// field names are the JSON keys of HTTPParams and DNSParams; for types with a
// target func they are keys of Spec.Params.
type TypeInfo struct {
	Type     string  `json:"type"`
	Preset   string  `json:"preset,omitempty"`
	Category string  `json:"category"` // web, network, database, messaging, system, game, passive
	Fields   []Field `json:"fields,omitempty"`
	Keyword  bool    `json:"keyword,omitempty"`
	HubOnly  bool    `json:"hub_only,omitempty"`
	Needs    string  `json:"needs,omitempty"` // docker, chromium, systemd, pm2, tailscale, icmp: what the probe must have

	target   func(map[string]string) string
	validate func(params) error
}

var (
	typeInfos = map[string]TypeInfo{}
	presets   []TypeInfo
)

var categoryOrder = []string{"web", "network", "database", "messaging", "system", "game", "passive"}

// registerType adds a params-driven type. target must be set: it is what marks
// the type as validated from its fields.
func registerType(t TypeInfo, c Checker) {
	register(t.Type, c)
	typeInfos[t.Type] = t
}

// Catalog lists what this binary can run, presets right after their type. A
// lite build has fewer entries.
func Catalog() []TypeInfo {
	out := make([]TypeInfo, 0, len(typeInfos)+len(presets))
	for t, info := range typeInfos {
		if _, ok := registry[t]; ok {
			out = append(out, info)
		}
	}
	for _, p := range presets {
		if _, ok := registry[p.Type]; ok {
			out = append(out, p)
		}
	}
	slices.SortStableFunc(out, func(a, b TypeInfo) int {
		if d := slices.Index(categoryOrder, a.Category) - slices.Index(categoryOrder, b.Category); d != 0 {
			return d
		}
		if c := strings.Compare(a.Type, b.Type); c != 0 {
			return c
		}
		return strings.Compare(a.Preset, b.Preset)
	})
	return out
}

// TypeOf returns the entry of a type, never of a preset.
func TypeOf(t string) (TypeInfo, bool) {
	info, ok := typeInfos[t]
	if _, reg := registry[t]; !reg {
		return TypeInfo{}, false
	}
	return info, ok
}

// MaskParams copies p with its Secret fields replaced by mask where non-empty.
// A type this binary does not know gets everything masked: nothing says which
// of its values are harmless.
func MaskParams(t string, p map[string]string, mask string) map[string]string {
	if p == nil {
		return nil
	}
	out := make(map[string]string, len(p))
	info, known := TypeOf(t)
	for k, v := range p {
		if v != "" && (!known || info.secret(k)) {
			v = mask
		}
		out[k] = v
	}
	return out
}

// KeepSecrets restores in place what a form sent back as the mask, so saving
// a form that only ever saw masked values does not overwrite the real ones.
func KeepSecrets(t string, in, old map[string]string, mask string) {
	info, known := TypeOf(t)
	for k, v := range in {
		if v != mask || (known && !info.secret(k)) {
			continue
		}
		if prev, ok := old[k]; ok {
			in[k] = prev
		} else {
			delete(in, k)
		}
	}
}

// URLParam says whether parameter name of type t holds URLs to redact for
// viewers (beyond MaskParams, which hides Secret fields whole).
func URLParam(t, name string) bool {
	info, ok := TypeOf(t)
	return ok && slices.ContainsFunc(info.Fields, func(f Field) bool { return f.Name == name && f.URL })
}

func (t TypeInfo) secret(name string) bool {
	return slices.ContainsFunc(t.Fields, func(f Field) bool { return f.Name == name && f.Secret })
}

// MaskSecrets copies p for someone who may see what is checked but not how to
// log in. The URL and headers stay with the caller: they have their own rules.
func (p *HTTPParams) MaskSecrets(mask string) *HTTPParams {
	c := *p
	for _, f := range []*string{&c.Body, &c.BasicPassword, &c.BearerToken, &c.TLSKey} {
		if *f != "" {
			*f = mask
		}
	}
	return &c
}

// KeepSecrets is the HTTP counterpart of the function of the same name.
func (p *HTTPParams) KeepSecrets(old *HTTPParams, mask string) {
	if old == nil {
		return
	}
	for _, f := range []struct{ in, old *string }{
		{&p.Body, &old.Body}, {&p.BasicPassword, &old.BasicPassword},
		{&p.BearerToken, &old.BearerToken}, {&p.TLSKey, &old.TLSKey},
	} {
		if *f.in == mask {
			*f.in = *f.old
		}
	}
}

// params is a Spec.Params with catalog defaults filled in.
type params map[string]string

func paramsOf(s *Spec) params {
	out := params{}
	if t, ok := TypeOf(s.Type); ok {
		for _, f := range t.Fields {
			if f.Default != "" {
				out[f.Name] = f.Default
			}
		}
	}
	for k, v := range s.Params {
		if v != "" {
			out[k] = v
		}
	}
	return out
}

func (p params) int(name string) int {
	n, _ := strconv.Atoi(p[name])
	return n
}

func (p params) float(name string) float64 {
	n, _ := strconv.ParseFloat(p[name], 64)
	return n
}

func (p params) bool(name string) bool { return p[name] == "true" }

// lines splits a textarea or comma-separated list.
func (p params) lines(name string) []string {
	var out []string
	for _, l := range strings.FieldsFunc(p[name], func(r rune) bool { return r == '\n' || r == '\r' || r == ',' }) {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (p params) addr() string { return joinHostPort(p["host"], p["port"]) }

func joinHostPort(host, port string) string {
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if strings.Contains(host, ":") {
		return "[" + host + "]:" + port
	}
	return host + ":" + port
}

func targetAddr(p map[string]string) string { return params(p).addr() }

func targetParam(name string) func(map[string]string) string {
	return func(p map[string]string) string { return p[name] }
}

func targetNone(map[string]string) string { return "" }

// targetURL keeps only the scheme and host of a URL: its path and query may
// carry a token, and targets reach alerts and viewers.
func targetURL(name string) func(map[string]string) string {
	return func(p map[string]string) string {
		u, err := url.Parse(strings.TrimSpace(p[name]))
		if err != nil || u.Host == "" {
			return ""
		}
		return u.Scheme + "://" + u.Host
	}
}

func validateParams(t TypeInfo, s *Spec) error {
	p := paramsOf(s)
	for k := range s.Params {
		if !slices.ContainsFunc(t.Fields, func(f Field) bool { return f.Name == k }) {
			return fmt.Errorf("unknown parameter %q for type %s", k, s.Type)
		}
	}
	for _, f := range t.Fields {
		v := p[f.Name]
		if v == "" {
			if f.Required && shown(f, p) {
				return fmt.Errorf("missing parameter %s", f.Name)
			}
			continue
		}
		// Messages name the field and never quote the value: it may be a secret.
		switch f.Kind {
		case "number":
			n, err := strconv.ParseFloat(v, 64)
			if err != nil || n < 0 {
				return fmt.Errorf("parameter %s must be a non-negative number", f.Name)
			}
		case "bool":
			if v != "true" && v != "false" {
				return fmt.Errorf("parameter %s must be true or false", f.Name)
			}
		case "select":
			if !slices.Contains(f.Options, v) {
				return fmt.Errorf("parameter %s must be one of %s", f.Name, strings.Join(f.Options, ", "))
			}
		}
		switch f.Name {
		case "port":
			if n, err := strconv.Atoi(v); err != nil || n < 1 || n > 65535 {
				return fmt.Errorf("parameter port must be 1-65535")
			}
		case "host":
			if strings.ContainsAny(v, "/@ \t\r\n") {
				return fmt.Errorf("parameter host must be a bare host name or address, credentials and schemes go elsewhere")
			}
		case "url", "nodes":
			if err := validateURLs(f.Name, p.lines(f.Name)); err != nil {
				return err
			}
		}
	}
	// A query is the only text database types have to match against.
	if s.Keyword != "" && p["query"] == "" && slices.ContainsFunc(t.Fields, func(f Field) bool { return f.Name == "query" }) {
		return fmt.Errorf("a keyword needs a query to match against")
	}
	if t.validate != nil {
		return t.validate(p)
	}
	return nil
}

func shown(f Field, p params) bool {
	k, v, ok := strings.Cut(f.ShowIf, "=")
	return !ok || p[k] == v
}

var secretQueryKey = regexp.MustCompile(`(?i)pass|secret|token|key|auth|sid`)

// validateURLs rejects URLs that carry credentials. Params masks whole fields,
// so a password inside a URL could not be hidden from viewers.
func validateURLs(name string, urls []string) error {
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("parameter %s is not a valid URL", name)
		}
		if u.User != nil {
			return fmt.Errorf("parameter %s must not contain a user or password, use the separate fields", name)
		}
		for k := range u.Query() {
			if secretQueryKey.MatchString(k) {
				return fmt.Errorf("parameter %s must not carry credentials in its query (%s), use the separate fields", name, k)
			}
		}
	}
	return nil
}

// Shared field descriptions. Names repeat across types on purpose, so the UI
// needs one label per name.
var (
	fHost     = Field{Name: "host", Kind: "text", Required: true}
	fUsername = Field{Name: "username", Kind: "text"}
	fPassword = Field{Name: "password", Kind: "password", Secret: true}
	fDatabase = Field{Name: "database", Kind: "text"}
	fQuery    = Field{Name: "query", Kind: "textarea"}
	fTLS      = Field{Name: "tls", Kind: "bool"}
	fInsecure = Field{Name: "insecure", Kind: "bool", Advanced: true}
	fTLSMode  = Field{Name: "tls_mode", Kind: "select", Options: []string{"disable", "require", "verify"}, Default: "disable"}
)

func fPort(def string) Field {
	return Field{Name: "port", Kind: "number", Required: true, Default: def}
}

func required(f Field) Field {
	f.Required = true
	return f
}

func init() {
	httpFields := []Field{
		{Name: "url", Kind: "text", Required: true},
		{Name: "method", Kind: "select", Default: "GET", Options: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}},
		{Name: "accepted_status", Kind: "text"},
		{Name: "keyword", Kind: "text"},
		{Name: "invert_keyword", Kind: "bool"},
		{Name: "json_path", Kind: "text", Advanced: true},
		{Name: "json_op", Kind: "select", Options: compareOps, Advanced: true},
		{Name: "json_expect", Kind: "text", Advanced: true},
		{Name: "body", Kind: "textarea", Secret: true, Advanced: true},
		{Name: "headers", Kind: "textarea", Secret: true, Advanced: true},
		{Name: "max_redirects", Kind: "number", Default: "10", Advanced: true},
		{Name: "basic_user", Kind: "text", Advanced: true},
		{Name: "basic_password", Kind: "password", Secret: true, Advanced: true},
		{Name: "bearer_token", Kind: "password", Secret: true, Advanced: true},
		{Name: "tls_cert", Kind: "textarea", Advanced: true},
		{Name: "tls_key", Kind: "textarea", Secret: true, Advanced: true},
		{Name: "insecure", Kind: "bool", Advanced: true},
	}
	for _, t := range []TypeInfo{
		{Type: "http", Category: "web", Keyword: true, Fields: httpFields},
		{Type: "tcp", Category: "network", Fields: []Field{{Name: "address", Kind: "text", Required: true}}},
		{Type: "tls", Category: "network", Fields: []Field{
			{Name: "address", Kind: "text", Required: true},
			{Name: "server_name", Kind: "text", Advanced: true},
			{Name: "min_days", Kind: "number", Default: "7"},
		}},
		{Type: "dns", Category: "network", Keyword: true, Fields: []Field{
			{Name: "name", Kind: "text", Required: true},
			{Name: "record_type", Kind: "select", Options: dnsRecordNames()},
			{Name: "server", Kind: "text"},
			{Name: "port", Kind: "number", Advanced: true},
			{Name: "expect", Kind: "text"},
		}},
		// Their settings are structured and the form has its own sections for them.
		{Type: "xray", Category: "network"},
		{Type: "mtproto", Category: "network"},
	} {
		typeInfos[t.Type] = t
	}
	presets = append(presets,
		TypeInfo{Type: "http", Preset: "keyword", Category: "web", Keyword: true, Fields: presetFields(httpFields, "keyword")},
		TypeInfo{Type: "http", Preset: "json-query", Category: "web", Keyword: true, Fields: presetFields(httpFields, "json_path", "json_op", "json_expect")},
	)
}

// presetFields brings the settings a preset is about to the front of the form.
func presetFields(fields []Field, need ...string) []Field {
	out := make([]Field, len(fields))
	copy(out, fields)
	for i := range out {
		if slices.Contains(need, out[i].Name) {
			out[i].Required = true
			out[i].Advanced = false
		}
	}
	return out
}
