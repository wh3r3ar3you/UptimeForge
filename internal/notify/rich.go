package notify

import (
	"cmp"
	"fmt"
	"strings"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/model"
)

// Rich renders cards as Telegram Rich Messages (Bot API 10.1): a heading, a
// table of probes, a quote for the "why" and a footer. The plain HTML cards
// (Incident, Resolved) stay as the fallback for API servers without them.

// maxRichRows caps the probe table; the page has the rest.
const maxRichRows = 15

// cardName is the incident's name as a card shows it; a VPN node gets its flag.
func (f *Formatter) cardName(inc *model.Incident) string {
	name := esc(inc.Name)
	if inc.Type == "users" && len(inc.Lines) > 0 && inc.Lines[0].Region != "" {
		name = Flag(inc.Lines[0].Region) + " " + name
	}
	return name
}

// probeTable lists probes, failures first, as a compact table. A long list
// folds into a details block whose summary carries the count.
func (f *Formatter) probeTable(inc *model.Incident) string {
	var bad, good []string
	failing := 0
	for _, l := range inc.Lines {
		name := Flag(l.Region) + " " + esc(short(l.Name, 32))
		if l.OK {
			state := "✅"
			if l.LatencyMS > 0 {
				state += fmt.Sprintf(" %.0f ms", l.LatencyMS)
			}
			good = append(good, "<tr><td>"+name+"</td><td>"+state+"</td></tr>")
		} else {
			failing++
			bad = append(bad, "<tr><td>"+name+"</td><td>❌ "+esc(f.reason(l))+"</td></tr>")
		}
	}
	rows := append(bad, good...)
	if len(rows) == 0 {
		return ""
	}
	hidden := 0
	if len(rows) > maxRichRows {
		hidden, rows = len(rows)-maxRichRows, rows[:maxRichRows]
	}
	var t strings.Builder
	t.WriteString("<table bordered striped compact><tr><th>" + f.t("rich_probe") + "</th><th>" + f.t("rich_state") + "</th></tr>")
	t.WriteString(strings.Join(rows, ""))
	if hidden > 0 {
		t.WriteString(`<tr><td colspan="2">` + f.t("more", hidden) + "</td></tr>")
	}
	t.WriteString("</table>")
	if len(inc.Lines) <= 8 {
		return t.String()
	}
	return "<details><summary>" + f.t("probes_failing", failing, len(inc.Lines)) + "</summary>" + t.String() + "</details>"
}

// RichIncident renders an open incident as a rich message, through the
// user's card template or the default one.
func (f *Formatter) RichIncident(inc *model.Incident, now time.Time) string {
	return f.fill(KeyCard, inc.Kind, f.cardVars(inc, now))
}

// resolvedTitle: a recovery is "recovered"; a manual close only says it was
// closed, since the thing may still be broken.
func (f *Formatter) resolvedTitle(inc *model.Incident) (emoji, title string) {
	if inc.Type == "update" && inc.Note == "update_stopped" {
		return "⏹", f.t("update_closed")
	}
	if inc.ResolvedBy != "" {
		return "☑️", f.t("closed")
	}
	return "🟢", f.t("resolved")
}

// resolvedLines are the facts under a resolved card's heading, HTML-escaped.
func (f *Formatter) resolvedLines(inc *model.Incident) []string {
	lines := []string{"⏱ " + f.t("span", f.clock(inc.Started), f.clock(inc.Resolved), esc(f.Duration(inc.Resolved.Sub(inc.Started))))}
	if inc.Users > 0 && inc.Type != "users" {
		lines[0] += " · " + f.t("users_was", esc(inc.Node), inc.Users)
	}
	if inc.Note == "removed" {
		switch inc.Type {
		case "update":
			lines = append(lines, f.t("update_removed"))
		case "users":
			lines = append(lines, f.t("users_removed"))
		case "host":
			lines = append(lines, f.t("host_removed"))
		default:
			lines = append(lines, f.t("removed"))
		}
	}
	worst := inc.Worst
	if worst == "" {
		worst = inc.Kind
	}
	was := kindEmoji(inc, worst) + " " + esc(f.title(inc, worst))
	switch {
	case inc.Type == "host" && inc.Host != nil:
		lines = append(lines, f.t("was", was, f.t("host_peak", f.hostValue(inc.Host.Metric, inc.Host.Peak))))
	case worst == model.KindNodeDown:
		lines = append(lines, f.t("was", was, esc(f.Duration(inc.Resolved.Sub(inc.Started)))))
	case inc.Type == "users" && inc.Drop != nil:
		lines = append(lines, f.t("was", was, f.t("users_low", groupDigits(inc.Drop.Low), groupDigits(inc.Drop.Before))))
	case inc.Type != "probe" && inc.Type != "subscription" && inc.Type != "host" && inc.Type != "users" && inc.Type != "update":
		lines = append(lines, f.t("was", was, f.t("probes_failing", inc.PeakFailing, inc.PeakTotal)))
	default:
		lines = append(lines, f.t("was", was, esc(f.Duration(inc.Resolved.Sub(inc.Started)))))
	}
	switch {
	case inc.Type == "update" && inc.Note == "resumed":
		lines = append(lines, f.t("update_resumed", esc(inc.ResolvedBy)))
	case inc.Type == "update" && inc.Note == "update_stopped":
		lines = append(lines, f.t("update_stopped"))
	case inc.ResolvedBy != "":
		lines = append(lines, f.t("closed_by", esc(inc.ResolvedBy)))
	}
	if inc.AckBy != "" {
		lines = append(lines, f.t("ack", esc(inc.AckBy)))
	}
	return lines
}

// RichResolved replaces the incident card once it is over.
func (f *Formatter) RichResolved(inc *model.Incident) string {
	return f.fill(KeyResolved, cmp.Or(inc.Worst, inc.Kind), f.resolvedVars(inc))
}

// RichTest is the test message in rich form.
func (f *Formatter) RichTest(event string) string {
	return "<p>" + f.Test(event) + "</p>"
}
