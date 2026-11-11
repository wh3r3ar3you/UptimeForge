package notify

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/model"
)

// SampleNames are the samples' keys, in the editor's order.
var SampleNames = []string{"node_down", "users_drop", "blocked", "down", "partial", "host", "probe_offline", "subscription"}

// Samples are one incident of every kind the hub raises, for the template
// editor's preview and to check a template before it is saved.
func Samples(now time.Time) map[string]*model.Incident {
	start := now.Add(-47 * time.Minute)
	probes := []model.ProbeLine{
		{Probe: "msk", Name: "ДЦ · МСК", Region: "ru", Reason: "stalled", Bytes: 16 << 10},
		{Probe: "spb", Name: "Дом · Москва", Region: "ru", Reason: "timeout"},
		{Probe: "fra", Name: "Hetzner · DE", Region: "de", OK: true, LatencyMS: 41},
	}
	return map[string]*model.Incident{
		"node_down": {Key: "rw:sample", Name: "de-node-1", Type: "users", Kind: model.KindNodeDown, Worst: model.KindNodeDown, Severity: model.Critical,
			Lines: []model.ProbeLine{{Name: "de-node-1", Region: "de", Reason: "node_down", Error: "Failed to get system stats"}},
			Drop:  &model.UsersDrop{Before: 812}, Affected: []string{"VLESS Reality", "Hysteria2"}, Tags: []string{"de", "critical"}, Started: start},
		"users_drop": {Key: "rw:sample2", Name: "pl-node-1", Type: "users", Kind: model.KindUsersDrop, Worst: model.KindUsersDrop, Severity: model.Critical,
			Lines: []model.ProbeLine{{Name: "pl-node-1", Region: "pl", Reason: "users_drop"}}, Drop: &model.UsersDrop{Before: 812, Now: 39, Low: 21}, Started: start},
		"blocked": {Key: "vless", Name: "de-node-1 · Reality", Group: "VPN-ноды", Target: "VLESS · de1.example.com:443", Kind: model.KindBlocked,
			Worst: model.KindBlocked, Region: "ru", Severity: model.Critical, Lines: probes, Users: 412, Node: "de-node-1", Started: start},
		"down": {Key: "web", Name: "Личный кабинет", Group: "Сервисы", Target: "https://cab.example.com/", Kind: model.KindDown, Worst: model.KindDown,
			Severity: model.Critical, Lines: []model.ProbeLine{{Probe: "hub", Name: "Hub", Reason: "http_status", Error: "HTTP 502"}}, Started: start},
		"partial": {Key: "nl", Name: "nl-amsterdam", Kind: model.KindPartial, Worst: model.KindPartial, Severity: model.Warning, Lines: probes, Started: start},
		"host": {Key: "host:p:cpu", Name: "uk-node-1", Type: "host", Kind: model.KindHost, Worst: model.KindHost, Severity: model.Warning,
			Host: &model.HostValue{Metric: "cpu", Value: 96, Peak: 99, Above: 90, For: 300}, Started: start},
		"probe_offline": {Key: "probe:p", Name: "Дом · Москва", Type: "probe", Kind: model.KindProbeOffline, Worst: model.KindProbeOffline, Severity: model.Warning,
			Lines: []model.ProbeLine{{Probe: "p", Name: "Дом · Москва", Region: "ru", Reason: "offline"}}, Started: start, Note: start.Format(time.RFC3339)},
		"subscription": {Key: "sub:canary", Name: "Подписка", Type: "subscription", Kind: model.KindDown, Worst: model.KindDown, Severity: model.Critical,
			Lines: []model.ProbeLine{{Name: "hub", Error: "HTTP 502"}}, Started: start},
	}
}

// templateKinds are the kinds a card template can be set for on its own.
var templateKinds = []model.Kind{model.KindDown, model.KindBlocked, model.KindPartial, model.KindProbeOffline,
	model.KindProbeNoNet, model.KindHost, model.KindUsersDrop, model.KindNodeDown}

// SplitTemplateKey reads "card.down" as the base key and the kind.
func SplitTemplateKey(k TemplateKey) (TemplateKey, model.Kind, error) {
	base, kind, _ := strings.Cut(string(k), ".")
	if !slices.Contains(TemplateKeys, TemplateKey(base)) {
		return "", "", fmt.Errorf("unknown template %q", k)
	}
	if kind != "" && !slices.Contains(templateKinds, model.Kind(kind)) {
		return "", "", fmt.Errorf("unknown kind %q in template %q", kind, k)
	}
	return TemplateKey(base), model.Kind(kind), nil
}

// sampleVars are the placeholders a template of key gets for inc.
func (f *Formatter) sampleVars(key TemplateKey, inc *model.Incident, now time.Time) Vars {
	switch key {
	case KeyResolved:
		r := *inc
		r.Resolved = now
		return f.resolvedVars(&r)
	case KeyEscalated:
		v := f.cardVars(inc, now)
		v["text"] = HTML(f.escalatedText(inc))
		return v
	case KeyRemind:
		v := f.cardVars(inc, now)
		v["text"] = HTML(f.remindText(inc, now))
		return v
	case KeyReport:
		return f.reportVars(SampleReport())
	}
	return f.cardVars(inc, now)
}

// ValidateTemplate renders text on every sample and checks Telegram would
// accept the result, rich and, for rich cards, the HTML fallback too.
func ValidateTemplate(lang string, key TemplateKey, text string) error {
	base, _, err := SplitTemplateKey(key)
	if err != nil {
		return err
	}
	now := time.Now()
	// Every section both ways: as the samples are, and acknowledged, muted
	// and closed by hand on a hub without a public URL.
	for _, busy := range []bool{false, true} {
		f := &Formatter{Lang: lang, Loc: time.UTC, PublicURL: "https://hub.example.com"}
		if busy {
			f.PublicURL = ""
		}
		for _, name := range SampleNames {
			inc := *Samples(now)[name]
			if busy {
				inc.AckBy, inc.MutedBy, inc.ResolvedBy, inc.MutedUntil = "leo", "leo", "leo", now.Add(time.Hour)
			}
			out, err := Render(text, f.sampleVars(base, &inc, now))
			if err != nil {
				return err
			}
			if err := checkMarkup(base, out); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	return nil
}

// Preview renders text as key for the sample, as rich markup (cards) and as
// Telegram HTML.
func Preview(lang string, key TemplateKey, text, sample string, loc *time.Location, publicURL string) (rich, html string, err error) {
	if err := ValidateTemplate(lang, key, text); err != nil {
		return "", "", err
	}
	base, _, _ := SplitTemplateKey(key)
	inc := Samples(time.Now())[sample]
	if inc == nil {
		inc = Samples(time.Now())[SampleNames[0]]
	}
	f := &Formatter{Lang: lang, Loc: loc, PublicURL: publicURL, Custom: Templates{lang: {key: text}}}
	now := time.Now()
	switch base {
	case KeyCard:
		return f.RichIncident(inc, now), f.Incident(inc, now), nil
	case KeyResolved:
		r := *inc
		r.Resolved = now
		return f.RichResolved(&r), f.Resolved(&r), nil
	case KeyEscalated:
		return "", f.EscalatedReply(inc), nil
	case KeyRemind:
		return "", f.RemindReply(inc, now), nil
	}
	out := f.RichReport(SampleReport())
	return out, RichToHTML(out), nil
}
