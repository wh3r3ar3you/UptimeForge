package hub

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/wh3r3ar3you/uptimeforge/internal/checks"
	"github.com/wh3r3ar3you/uptimeforge/internal/xray"
)

type Config struct {
	// Server settings come from flags/env (UF_LISTEN, UF_DATA_DIR, UF_ADMIN_TOKEN).
	Listen       string          `yaml:"-" json:"-"`
	DataDir      string          `yaml:"-" json:"-"`
	PublicURL    string          `yaml:"public_url" json:"public_url,omitempty"`
	AdminToken   string          `yaml:"-" json:"-"`
	Timezone     string          `yaml:"timezone" json:"timezone,omitempty"`
	Lang         string          `yaml:"lang" json:"lang,omitempty"`
	RegionLabel  string          `yaml:"region_label" json:"region_label,omitempty"`
	OfflineGrace checks.Duration `yaml:"offline_grace" json:"offline_grace,omitempty"`
	Heartbeat    Heartbeat       `yaml:"heartbeat" json:"heartbeat"`
	Defaults     Defaults        `yaml:"defaults" json:"defaults"`
	Baseline     []checks.Spec   `yaml:"baseline" json:"baseline"`
	Alerts       Alerts          `yaml:"alerts" json:"alerts"`
	// Templates are the user's own notification texts: language → key → text
	// (keys: card, card.<kind>, resolved, escalated, remind, report).
	Templates map[string]map[string]string `yaml:"templates" json:"templates,omitempty"`
	Probes    []ProbeConfig                `yaml:"probes" json:"-"`
	Checks    []CheckConfig                `yaml:"checks" json:"-"`
	Telegram  *Telegram                    `yaml:"telegram" json:"telegram,omitempty"`
	Webhook   *Webhook                     `yaml:"webhook" json:"webhook,omitempty"`
	Remnawave *Remnawave                   `yaml:"remnawave" json:"remnawave,omitempty"`
}

// Heartbeat is the dead man's switch: the hub pings URL while it is healthy,
// and the external service alerts when the pings stop.
type Heartbeat struct {
	URL      string          `yaml:"url" json:"url,omitempty"`
	Interval checks.Duration `yaml:"interval" json:"interval,omitempty"`
}

type Defaults struct {
	Interval checks.Duration `yaml:"interval" json:"interval,omitempty"`
	Timeout  checks.Duration `yaml:"timeout" json:"timeout,omitempty"`
	Retries  int             `yaml:"retries" json:"retries,omitempty"`
}

// Alerts is the notification policy shared by every notifier.
type Alerts struct {
	QuietHours  string           `yaml:"quiet_hours" json:"quiet_hours,omitempty"` // warnings arrive silently
	RemindEvery *checks.Duration `yaml:"remind_every" json:"remind_every,omitempty"`
	// Host overrides the default server thresholds, rule by rule (see HostRules).
	Host []HostRule `yaml:"host" json:"host,omitempty"`
	// Report is the weekly report; nil means on, Sundays at 20:00.
	Report *Report `yaml:"report" json:"report,omitempty"`
}

// Report schedules the weekly report in the hub's time zone.
type Report struct {
	Off bool   `yaml:"off" json:"off,omitempty"`
	Day string `yaml:"day" json:"day,omitempty"` // sun, mon, …; default sun
	At  string `yaml:"at" json:"at,omitempty"`   // HH:MM; default 20:00
}

var weekdays = map[string]time.Weekday{"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday}

// Schedule is the report's day and time with the defaults filled in.
func (r *Report) Schedule() (off bool, day time.Weekday, hh, mm int) {
	day, hh = time.Sunday, 20
	if r == nil {
		return false, day, hh, mm
	}
	if d, ok := weekdays[r.Day]; ok {
		day = d
	}
	if t, err := time.Parse("15:04", r.At); err == nil {
		hh, mm = t.Hour(), t.Minute()
	}
	return r.Off, day, hh, mm
}

type ProbeConfig struct {
	ID          string            `yaml:"id" json:"id"`
	Name        string            `yaml:"name" json:"name"`
	Labels      map[string]string `yaml:"labels" json:"labels,omitempty"`
	Concurrency int               `yaml:"concurrency" json:"concurrency,omitempty"`
	Baseline    []checks.Spec     `yaml:"baseline" json:"baseline,omitempty"`

	// Exactly one of: an agent connects with Token (UI-created probes keep
	// only TokenHash); the hub runs the checks itself through the Via proxy
	// link (socks5://, http://, vless://, ...); or Local, the hub runs them
	// from its own network.
	Token     string `yaml:"token" json:"-"`
	TokenHash string `yaml:"-" json:"token_hash,omitempty"`
	Via       string `yaml:"via" json:"via,omitempty"`
	Local     bool   `yaml:"local" json:"local,omitempty"`

	// Probing makes an agent run checks too. Off by default: an agent only
	// reports its server's metrics until someone asks it to probe.
	Probing bool `yaml:"probing" json:"probing,omitempty"`

	// Role is RoleServer (or empty) for our own infrastructure and RoleChecker
	// for a volunteer probe point on someone else's machine. Only agents have
	// a role.
	Role string `yaml:"role" json:"role,omitempty"`
	// Trusted lets a checker run checks that carry secrets (VPN links,
	// passwords). Only for checkers.
	Trusted bool `yaml:"trusted" json:"trusted,omitempty"`
	// LabelsLocked names the labels an admin set by hand.
	LabelsLocked []string `yaml:"labels_locked" json:"labels_locked,omitempty"`
}

const (
	RoleServer  = "server"
	RoleChecker = "checker"
)

func (p *ProbeConfig) Virtual() bool { return p.Via != "" || p.Local }

// Checker is true for volunteer probe points: they run checks that select
// them explicitly and report no host metrics.
func (p *ProbeConfig) Checker() bool { return p.Role == RoleChecker }

// RunsChecks is true for probes that get checks assigned: virtual ones exist
// for nothing else, agents only when probing is switched on.
// Checkers exist to run checks, but only the ones that select them.
func (p *ProbeConfig) RunsChecks() bool { return p.Probing || p.Virtual() || p.Checker() }

// HashToken is how agent tokens are stored and compared.
func HashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

type CheckConfig struct {
	checks.Spec `yaml:",inline"`
	Name        string `yaml:"name" json:"name,omitempty"`
	Group       string `yaml:"group" json:"group,omitempty"`
	// Tags are free labels (de, vless, critical) for filters and alert rules.
	Tags []string `yaml:"tags" json:"tags,omitempty"`
	// DependsOn names what this check's failure is a consequence of: check
	// ids, node:<panel node name>, probe:<id>; ["none"] turns it off. Empty
	// means found automatically by address.
	DependsOn  []string        `yaml:"depends_on" json:"depends_on,omitempty"`
	Probes     []string        `yaml:"probes" json:"probes,omitempty"`
	Exclude    []string        `yaml:"exclude" json:"exclude,omitempty"`
	Retries    int             `yaml:"retries" json:"retries,omitempty"`
	MinFailing int             `yaml:"min_failing" json:"min_failing,omitempty"`
	Severity   string          `yaml:"severity" json:"severity,omitempty"`
	Refresh    checks.Duration `yaml:"refresh" json:"refresh,omitempty"` // subscription re-fetch period
	Paused     bool            `yaml:"paused" json:"paused,omitempty"`

	target   string // derived: protocol and endpoint shown in alerts
	baseline bool
	parent   string // subscription the check was expanded from
	// carriesSecrets keeps the check off untrusted checkers; set by rebuild.
	carriesSecrets bool
}

type Telegram struct {
	Token  string         `yaml:"token" json:"token"`
	ChatID int64          `yaml:"chat_id" json:"chat_id"`
	Topics map[string]int `yaml:"topics" json:"topics,omitempty"` // severity -> message_thread_id
	// Routes sends each kind of event to its own chat and topic (notify.Events).
	Routes map[string]TelegramRoute `yaml:"routes" json:"routes,omitempty"`
	// Rules send incidents with given tags, groups, events or severity to their
	// own place; checked in order before Routes, the first match wins.
	Rules  []TelegramRule `yaml:"rules" json:"rules,omitempty"`
	APIURL string         `yaml:"api_url" json:"api_url,omitempty"`
	Proxy  string         `yaml:"proxy" json:"proxy,omitempty"`
	Admins []int64        `yaml:"admins" json:"admins,omitempty"`
}

// TelegramRule matches incidents; empty conditions match anything.
type TelegramRule struct {
	Tags        []string      `yaml:"tags" json:"tags,omitempty"`
	Groups      []string      `yaml:"groups" json:"groups,omitempty"`
	Events      []string      `yaml:"events" json:"events,omitempty"`
	MinSeverity string        `yaml:"min_severity" json:"min_severity,omitempty"`
	Route       TelegramRoute `yaml:"route" json:"route"`
}

// TelegramRoute is where one kind of event goes. Empty means the main chat.
type TelegramRoute struct {
	Off    bool  `yaml:"off" json:"off,omitempty"`
	ChatID int64 `yaml:"chat_id" json:"chat_id,omitempty"`
	Topic  int   `yaml:"topic" json:"topic,omitempty"`
	Silent bool  `yaml:"silent" json:"silent,omitempty"`
}

// Webhook posts every incident event as JSON (Discord/Slack bridges, n8n, your own bot).
type Webhook struct {
	URL     string            `yaml:"url" json:"url"`
	Headers map[string]string `yaml:"headers" json:"headers,omitempty"`
	// Secret signs every body: X-UptimeForge-Signature: sha256=<hex HMAC>.
	Secret string `yaml:"secret" json:"secret,omitempty"`
}

// Remnawave enriches the UI and alerts with node state and online users.
type Remnawave struct {
	URL     string            `yaml:"url" json:"url"`
	Token   string            `yaml:"token" json:"token"`
	Headers map[string]string `yaml:"headers" json:"headers,omitempty"`
	// Drop is the users-online collapse alert; nil means on with defaults.
	Drop *UsersDrop `yaml:"drop" json:"drop,omitempty"`
	// Prune pauses the checks of a node deleted from the panel (tagged
	// node-deleted) and resumes them if the node comes back; servers and
	// agent tokens are never touched.
	Prune bool `yaml:"prune" json:"prune,omitempty"`
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// LoadConfig reads YAML, substituting ${VAR} from the environment in values
// (never in comments). Substitution happens on parsed nodes, so secrets may
// contain any characters without breaking the YAML.
func LoadConfig(path string) (*Config, error) {
	c, err := ReadConfigFile(path)
	if err != nil {
		return nil, err
	}
	return c, c.normalize()
}

// ReadConfigFile parses the file without filling defaults, so callers can
// tell what the file actually sets.
func ReadConfigFile(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var missing []string
	expandEnv(&doc, &missing)
	if len(missing) > 0 {
		return nil, fmt.Errorf("config references unset environment variables: %s", strings.Join(missing, ", "))
	}
	expanded, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, err
	}
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(expanded))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && err != io.EOF {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

var number = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)

func expandEnv(n *yaml.Node, missing *[]string) {
	if n.Kind == yaml.ScalarNode && strings.Contains(n.Value, "${") {
		n.Value = envRef.ReplaceAllStringFunc(n.Value, func(m string) string {
			name := envRef.FindStringSubmatch(m)[1]
			v, ok := os.LookupEnv(name)
			if !ok {
				*missing = append(*missing, name)
			}
			return v
		})
		// chat_id: ${UF_TG_CHAT} must become a number; everything else stays a string.
		if n.Style == 0 && number.MatchString(n.Value) {
			n.Tag = ""
		} else {
			n.Tag = "!!str"
		}
	}
	for _, c := range n.Content {
		expandEnv(c, missing)
	}
}

// Normalize fills defaults and validates. The hub only runs normalized configs.
func (c *Config) Normalize() error { return c.normalize() }

func (c *Config) normalize() error {
	if c.Listen == "" {
		c.Listen = ":8080"
	}
	if c.DataDir == "" {
		c.DataDir = "./data"
	}
	if c.Lang == "" {
		c.Lang = "ru"
	}
	if c.Timezone == "" {
		c.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return fmt.Errorf("timezone: %w", err)
	}
	if c.RegionLabel == "" {
		c.RegionLabel = "country"
	}
	if c.OfflineGrace == 0 {
		c.OfflineGrace = checks.Duration(90 * time.Second)
	}
	if c.Heartbeat.Interval == 0 {
		c.Heartbeat.Interval = checks.Duration(time.Minute)
	}
	if c.Heartbeat.URL = strings.TrimSpace(c.Heartbeat.URL); c.Heartbeat.URL != "" {
		if err := httpURL(c.Heartbeat.URL); err != nil {
			return fmt.Errorf("heartbeat.url: %w", err)
		}
	}
	if c.Defaults.Interval == 0 {
		c.Defaults.Interval = checks.Duration(checks.DefaultInterval)
	}
	if c.Defaults.Timeout == 0 {
		c.Defaults.Timeout = checks.Duration(checks.DefaultTimeout)
	}
	if c.Defaults.Retries == 0 {
		c.Defaults.Retries = 2
	}
	if c.Baseline == nil {
		// Probe "has internet" if any of these answers. Mixed RU/foreign
		// targets so whitelist-mode networks still count as online.
		c.Baseline = []checks.Spec{
			{Type: "tcp", TCP: &checks.TCPParams{Address: "77.88.8.8:53"}},
			{Type: "tcp", TCP: &checks.TCPParams{Address: "1.1.1.1:443"}},
			{Type: "tcp", TCP: &checks.TCPParams{Address: "8.8.8.8:443"}},
		}
	}

	ids := map[string]bool{}
	tokens := map[string]string{}
	for i := range c.Probes {
		p := &c.Probes[i]
		if p.ID == "" || ids[p.ID] {
			return fmt.Errorf("probe #%d: missing or duplicate id %q", i+1, p.ID)
		}
		ids[p.ID] = true
		if p.Name == "" {
			p.Name = p.ID
		}
		if p.Token != "" {
			if len(p.Token) < 16 {
				return fmt.Errorf("probe %s: token must be at least 16 characters", p.ID)
			}
			p.TokenHash = HashToken(p.Token)
		}
		modes := 0
		for _, set := range []bool{p.TokenHash != "", p.Via != "", p.Local} {
			if set {
				modes++
			}
		}
		if modes != 1 {
			return fmt.Errorf("probe %s: set exactly one of token, via, local", p.ID)
		}
		switch p.Role {
		case "", RoleServer, RoleChecker:
		default:
			return fmt.Errorf("probe %s: role must be %s or %s", p.ID, RoleServer, RoleChecker)
		}
		if p.Role != "" && p.Virtual() {
			return fmt.Errorf("probe %s: role is only for agents", p.ID)
		}
		if p.Trusted && !p.Checker() {
			return fmt.Errorf("probe %s: trusted is only for role %s", p.ID, RoleChecker)
		}
		if p.TokenHash != "" {
			if other, dup := tokens[p.TokenHash]; dup {
				return fmt.Errorf("probes %s and %s share a token", other, p.ID)
			}
			tokens[p.TokenHash] = p.ID
		}
		if p.Via != "" {
			if _, err := xray.ParseLink(p.Via); err != nil {
				return fmt.Errorf("probe %s: via: %w", p.ID, err)
			}
		}
		if p.Labels == nil {
			p.Labels = map[string]string{}
		}
		p.Labels["id"] = p.ID
		// The label follows the role, so a check can pick every checker and
		// only checkers.
		if p.Checker() {
			p.Labels["role"] = RoleChecker
		} else if p.Labels["role"] == RoleChecker {
			delete(p.Labels, "role")
		}
	}

	cids := map[string]bool{}
	for i := range c.Checks {
		ch := &c.Checks[i]
		if ch.ID == "" {
			return fmt.Errorf("check #%d: missing id", i+1)
		}
		// '/' marks subscription configs and ':' system keys (probe:, rw:,
		// host:), which incidents are told apart by.
		if strings.ContainsAny(ch.ID, "/: ") || strings.HasPrefix(ch.ID, "_") {
			return fmt.Errorf("check %s: id must not contain '/', ':', spaces or start with '_'", ch.ID)
		}
		if cids[ch.ID] {
			return fmt.Errorf("duplicate check id %s", ch.ID)
		}
		cids[ch.ID] = true
		if err := ch.Spec.Validate(); err != nil {
			return err
		}
		if ch.Xray != nil && ch.Xray.Filter != "" {
			if _, err := regexp.Compile(ch.Xray.Filter); err != nil {
				return fmt.Errorf("check %s: filter: %w", ch.ID, err)
			}
		}
		tags, err := normalizeTags(ch.Tags)
		if err != nil {
			return fmt.Errorf("check %s: %w", ch.ID, err)
		}
		ch.Tags = tags
		switch ch.Severity {
		case "", "critical", "warning":
		default:
			return fmt.Errorf("check %s: severity must be critical or warning", ch.ID)
		}
		for _, sel := range append(append([]string{}, ch.Probes...), ch.Exclude...) {
			if sel != "*" && !strings.Contains(sel, "=") {
				return fmt.Errorf("check %s: probe selector %q must be \"*\" or label=value", ch.ID, sel)
			}
		}
	}

	if err := validateDeps(c.Checks); err != nil {
		return err
	}
	if err := validateHostRules(c.Alerts.Host); err != nil {
		return err
	}
	if c.Remnawave != nil {
		if err := validateDrop(c.Remnawave.Drop); err != nil {
			return err
		}
	}
	if c.Telegram != nil {
		for i := range c.Telegram.Rules {
			r := &c.Telegram.Rules[i]
			if r.MinSeverity != "" && r.MinSeverity != "critical" && r.MinSeverity != "warning" {
				return fmt.Errorf("telegram.rules[%d]: min_severity must be critical or warning", i)
			}
			tags, err := normalizeTags(r.Tags)
			if err != nil {
				return fmt.Errorf("telegram.rules[%d]: %w", i, err)
			}
			r.Tags = tags
		}
	}
	if r := c.Alerts.Report; r != nil {
		if _, ok := weekdays[r.Day]; r.Day != "" && !ok {
			return fmt.Errorf("alerts.report.day must be sun, mon, tue, wed, thu, fri or sat")
		}
		if _, err := time.Parse("15:04", r.At); r.At != "" && err != nil {
			return fmt.Errorf("alerts.report.at must be HH:MM")
		}
	}
	if c.Alerts.QuietHours != "" {
		if _, _, err := parseQuietHours(c.Alerts.QuietHours); err != nil {
			return fmt.Errorf("alerts.quiet_hours: %w", err)
		}
	}
	if c.Alerts.RemindEvery == nil {
		d := checks.Duration(30 * time.Minute)
		c.Alerts.RemindEvery = &d
	}
	if t := c.Telegram; t != nil {
		if t.Token == "" || t.ChatID == 0 {
			return fmt.Errorf("telegram: token and chat_id are required")
		}
		if t.APIURL == "" {
			t.APIURL = "https://api.telegram.org"
		}
	}
	if w := c.Webhook; w != nil && httpURL(w.URL) != nil {
		return fmt.Errorf("webhook: url must be http(s)")
	}
	if r := c.Remnawave; r != nil && (r.URL == "" || r.Token == "") {
		return fmt.Errorf("remnawave: url and token are required")
	}
	return nil
}

// withDefaults fills interval/timeout/retries and the human-readable target.
func (c *Config) withDefaults(ch CheckConfig) CheckConfig {
	if ch.Interval == 0 {
		ch.Interval = c.Defaults.Interval
	}
	if ch.Timeout == 0 {
		ch.Timeout = c.Defaults.Timeout
		if ch.Type == "xray" {
			ch.Timeout = max(ch.Timeout, checks.Duration(25*time.Second))
		}
	}
	if ch.Retries == 0 {
		ch.Retries = c.Defaults.Retries
	}
	if ch.MinFailing == 0 {
		ch.MinFailing = 1
	}
	if ch.Name == "" {
		ch.Name = ch.ID
	}
	ch.target = ch.Spec.Target()
	if ch.Xray != nil && ch.Xray.Link != "" {
		if l, err := xray.ParseLink(ch.Xray.Link); err == nil {
			ch.target = strings.ToUpper(l.Protocol) + " · " + l.Endpoint()
		}
	}
	return ch
}

// names reports whether the check lists this probe by id. That is a choice made
// for this one check, so it works on an agent with probing off.
func (ch *CheckConfig) names(p *ProbeConfig) bool {
	return slices.Contains(ch.Probes, "id="+p.ID)
}

// runsOn decides whether a probe gets this check. Passive types wait for
// something to reach the hub, so only the hub itself can judge them.
func (ch *CheckConfig) runsOn(p *ProbeConfig) bool {
	if info, ok := checks.TypeOf(ch.Type); ok && info.HubOnly {
		return p.Local
	}
	if p.Checker() && ch.carriesSecrets && !p.Trusted {
		return false // a stranger's machine must not see VPN links or passwords
	}
	return (p.RunsChecks() || ch.names(p)) && ch.selects(p)
}

// selects reports whether the probe matches the check's selectors. A checker
// is never part of "everyone" (no selectors or "*"): a selector has to pick it
// out by a label.
func (ch *CheckConfig) selects(p *ProbeConfig) bool {
	match := func(sel string) bool {
		if sel == "*" {
			return true
		}
		k, v, _ := strings.Cut(sel, "=")
		return p.Labels[k] == v
	}
	explicit := p.Checker()
	in := len(ch.Probes) == 0 && !explicit
	for _, s := range ch.Probes {
		in = in || (match(s) && !(explicit && s == "*"))
	}
	for _, s := range ch.Exclude {
		if match(s) {
			return false
		}
	}
	return in
}

func parseQuietHours(s string) (from, to time.Duration, err error) {
	a, b, ok := strings.Cut(s, "-")
	if !ok {
		return 0, 0, fmt.Errorf("want HH:MM-HH:MM")
	}
	parse := func(x string) (time.Duration, error) {
		t, err := time.Parse("15:04", strings.TrimSpace(x))
		if err != nil {
			return 0, err
		}
		return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute, nil
	}
	if from, err = parse(a); err != nil {
		return
	}
	to, err = parse(b)
	return
}

// inQuietHours handles ranges that wrap midnight (23:00-08:00).
func inQuietHours(spec string, now time.Time) bool {
	if spec == "" {
		return false
	}
	from, to, err := parseQuietHours(spec)
	if err != nil {
		return false
	}
	h, m, _ := now.Clock()
	cur := time.Duration(h)*time.Hour + time.Duration(m)*time.Minute
	if from <= to {
		return cur >= from && cur < to
	}
	return cur >= from || cur < to
}

// httpURL accepts absolute http(s) URLs with a host.
func httpURL(s string) error {
	u, err := url.Parse(s)
	if err != nil {
		return err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("must be an http(s) URL")
	}
	return nil
}

// Clone deep-copies the config: Normalize writes into probes' labels and
// sections, so a config that is (or may become) live must not share them.
func (c *Config) Clone() *Config {
	b, _ := json.Marshal(c)
	out := &Config{}
	json.Unmarshal(b, out)
	out.Listen, out.DataDir, out.AdminToken = c.Listen, c.DataDir, c.AdminToken
	out.Probes = make([]ProbeConfig, len(c.Probes))
	for i, p := range c.Probes {
		b, _ := json.Marshal(p)
		json.Unmarshal(b, &out.Probes[i])
		out.Probes[i].Token = p.Token
	}
	out.Checks = make([]CheckConfig, len(c.Checks))
	for i, ch := range c.Checks {
		b, _ := json.Marshal(ch)
		json.Unmarshal(b, &out.Checks[i])
		out.Checks[i].parent, out.Checks[i].target, out.Checks[i].baseline = ch.parent, ch.target, ch.baseline
	}
	return out
}

const (
	maxTags   = 16
	maxTagLen = 32
)

// normalizeTags lower-cases, trims, de-duplicates and sorts tags; a tag is
// letters (Latin or Cyrillic), digits and _-.: only, up to 32 characters.
func normalizeTags(in []string) ([]string, error) {
	var out []string
	for _, t := range in {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || slices.Contains(out, t) {
			continue
		}
		if utf8.RuneCountInString(t) > maxTagLen {
			return nil, fmt.Errorf("tag %q: at most %d characters", t, maxTagLen)
		}
		for _, r := range t {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r >= 'а' && r <= 'я' || r == 'ё' || strings.ContainsRune("_-.:", r)) {
				return nil, fmt.Errorf("tag %q: only letters, digits and _-.: are allowed", t)
			}
		}
		out = append(out, t)
	}
	if len(out) > maxTags {
		return nil, fmt.Errorf("at most %d tags", maxTags)
	}
	slices.Sort(out)
	return out, nil
}

// NormalizeTags is normalizeTags for callers outside the hub (bulk edits).
func NormalizeTags(in []string) ([]string, error) { return normalizeTags(in) }
