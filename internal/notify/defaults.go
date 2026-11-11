package notify

import (
	"fmt"
	"strings"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/model"
)

// TemplateKey names a kind of message a template can be set for. A card
// can also be set for one kind of incident: "card.down", "resolved.host".
type TemplateKey string

const (
	KeyCard      TemplateKey = "card"      // an open incident's card (rich message)
	KeyResolved  TemplateKey = "resolved"  // the card once it is over
	KeyEscalated TemplateKey = "escalated" // the reply when it got worse (HTML)
	KeyRemind    TemplateKey = "remind"    // the reminder reply (HTML)
	KeyReport    TemplateKey = "report"    // the weekly report (rich message)
)

// TemplateKeys are the base keys, in the editor's order.
var TemplateKeys = []TemplateKey{KeyCard, KeyResolved, KeyEscalated, KeyRemind, KeyReport}

// Templates are a user's own texts: language → key → template.
type Templates map[string]map[TemplateKey]string

// DefaultTemplates render exactly the cards from before templates existed;
// TestSnapshotCurrentFormatter holds them to it. The words come from the
// placeholders, so one template serves every language.
var DefaultTemplates = map[TemplateKey]string{
	KeyCard: `<h3>{emoji} {title} · {name}</h3>{?target}<p><code>{target}</code></p>{/target}{details}` +
		`{?affected}<p>🔗 {affected}</p>{/affected}{?why}<blockquote>💡 {why}</blockquote>{/why}` +
		`<footer>⏱ {since}{?users} · {users}{/users}{?link} · <a href="{link}">{open}</a>{/link}</footer>` +
		`{?ack}<p>{ack}</p>{/ack}{?muted}<p>{muted}</p>{/muted}`,
	KeyResolved:  `<h3>{emoji} {title} · {name}</h3>{lines}`,
	KeyEscalated: `{text}`,
	KeyRemind:    `{text}`,
	KeyReport: `<h3>📊 {title}</h3><p>{period}</p>{?uptime}<p>{uptime}{?uptime_delta} {uptime_delta}{/uptime_delta}</p>{/uptime}` +
		`{groups}{worst}{nodes}{bans}<p>{incidents}{?mttr} · {mttr}{/mttr}</p>{?online}<p>{online}</p>{/online}{?traffic}<p>{traffic}</p>{/traffic}`,
}

// richKey says a key's templates are rich messages (the rest are Telegram HTML).
func richKey(k TemplateKey) bool { return k == KeyCard || k == KeyResolved || k == KeyReport }

// template is the text for key, the kind's own when the user set one, and
// whether it is the user's.
func (f *Formatter) template(key TemplateKey, kind model.Kind) (string, bool) {
	if c := f.Custom[f.Lang]; c != nil {
		if kind != "" {
			if s := c[key+"."+TemplateKey(kind)]; s != "" {
				return s, true
			}
		}
		if s := c[key]; s != "" {
			return s, true
		}
	}
	return DefaultTemplates[key], false
}

// fill renders key's template; a user's template that fails, or whose
// markup Telegram would refuse, falls back to the default, so a broken
// template never costs an alert.
func (f *Formatter) fill(key TemplateKey, kind model.Kind, v Vars) string {
	tpl, custom := f.template(key, kind)
	out, err := Render(tpl, v)
	if err == nil && custom {
		err = checkMarkup(key, out)
	}
	if err != nil && custom {
		if f.OnError != nil {
			f.OnError(fmt.Errorf("template %s: %w", key, err))
		}
		out, _ = Render(DefaultTemplates[key], v)
	}
	return out
}

// checkMarkup says whether Telegram would take out, a rendered key: as rich
// markup and its HTML fallback, or as HTML.
func checkMarkup(key TemplateKey, out string) error {
	if !richKey(key) {
		return ValidateHTML(out)
	}
	if err := ValidateRich(out); err != nil {
		return err
	}
	return ValidateHTML(RichToHTML(out))
}

// cardDetails is the part of a card that depends on the kind of incident:
// the probe table, the users line, the metric, or the error.
func (f *Formatter) cardDetails(inc *model.Incident) string {
	switch {
	case inc.Kind == model.KindNodeDown:
		if len(inc.Lines) > 0 && inc.Lines[0].Error != "" {
			return "<pre>" + esc(short(inc.Lines[0].Error, 200)) + "</pre>"
		}
	case inc.Type == "users" && inc.Drop != nil:
		return "<p>" + f.t("users_line", groupDigits(inc.Drop.Before), groupDigits(inc.Drop.Now)) + "</p>"
	case inc.Type == "host" && inc.Host != nil:
		hv := inc.Host
		if hv.For > 0 {
			return "<p>" + f.t("host_now", f.hostValue(hv.Metric, hv.Value), f.hostValue(hv.Metric, hv.Above), esc(f.Duration(time.Duration(hv.For)*time.Second))) + "</p>"
		}
		return "<p>" + f.t("host_now0", f.hostValue(hv.Metric, hv.Value), f.hostValue(hv.Metric, hv.Above)) + "</p>"
	case inc.Type == "subscription" || inc.Kind == model.KindProbeNoNet:
		if len(inc.Lines) > 0 && inc.Lines[0].Error != "" {
			return "<pre>" + esc(short(inc.Lines[0].Error, 200)) + "</pre>"
		}
	case inc.Type != "probe":
		return f.probeTable(inc)
	}
	return ""
}

// commonVars are the placeholders every message about an incident has.
func (f *Formatter) commonVars(inc *model.Incident, now time.Time) Vars {
	v := Vars{
		"name": HTML(f.cardName(inc)), "group": Text(inc.Group), "tags": Text(strings.Join(inc.Tags, ", ")),
		"region": Text(strings.ToUpper(inc.Region)), "flag": Text(Flag(inc.Region)),
		"started": Text(f.clock(inc.Started)), "duration": Text(f.Duration(now.Sub(inc.Started))),
		"node": Text(inc.Node), "link": Text(f.Link(inc)), "open": HTML(f.t("open")),
		"failing": Text(fmt.Sprint(inc.Failing())), "total": Text(fmt.Sprint(len(inc.Lines))),
		"probes": HTML(f.probeTable(inc)), "affected": HTML(f.affected(inc)), "resolved_by": Text(inc.ResolvedBy),
	}
	if inc.Target != "" && inc.Target != inc.Name {
		v["target"] = Text(inc.Target)
	}
	if inc.Users > 0 {
		v["users_count"] = Text(fmt.Sprint(inc.Users))
	}
	if d := inc.Drop; d != nil {
		v["drop_before"], v["drop_now"] = Text(groupDigits(d.Before)), Text(groupDigits(d.Now))
	}
	if hv := inc.Host; hv != nil {
		v["host_metric"] = Text(hv.Metric)
		v["host_value"], v["host_limit"], v["peak"] = HTML(f.hostValue(hv.Metric, hv.Value)), HTML(f.hostValue(hv.Metric, hv.Above)), HTML(f.hostValue(hv.Metric, hv.Peak))
	}
	return v
}

// cardVars are an open incident's placeholders.
func (f *Formatter) cardVars(inc *model.Incident, now time.Time) Vars {
	v := f.commonVars(inc, now)
	v["emoji"], v["title"] = Text(kindEmoji(inc, inc.Kind)), Text(f.title(inc, inc.Kind))
	v["details"], v["why"] = HTML(f.cardDetails(inc)), Text(f.why(inc))
	v["since"] = HTML(f.t("since", f.clock(inc.Started), esc(f.Duration(now.Sub(inc.Started)))))
	if inc.Users > 0 && inc.Type != "users" {
		v["users"] = HTML(f.t("users_now", esc(inc.Node), inc.Users))
	}
	if inc.AckBy != "" {
		v["ack"] = HTML(f.t("ack", esc(inc.AckBy)))
	}
	if now.Before(inc.MutedUntil) {
		v["muted"] = HTML(f.t("muted", f.clock(inc.MutedUntil), esc(inc.MutedBy)))
	}
	return v
}

// resolvedVars are a finished incident's placeholders.
func (f *Formatter) resolvedVars(inc *model.Incident) Vars {
	v := f.commonVars(inc, inc.Resolved)
	emoji, title := f.resolvedTitle(inc)
	v["emoji"], v["title"] = Text(emoji), Text(title)
	var lines strings.Builder
	for _, l := range f.resolvedLines(inc) {
		lines.WriteString("<p>" + l + "</p>")
	}
	v["lines"] = HTML(lines.String())
	return v
}
