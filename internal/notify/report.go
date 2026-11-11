package notify

import (
	"fmt"
	"strings"
	"time"
)

// Report is a week of monitoring, as the weekly message tells it.
type Report struct {
	From, To           time.Time
	Uptime, UptimePrev float64 // mean uptime of the checks; -1 when unknown
	Groups             []GroupUptime
	Worst              []Downtime // checks, longest down first
	Nodes              []Downtime // nodes the panel lost and servers offline
	Bans               []Downtime // users-online collapses per node
	Opened, Resolved   int
	ByHand             int // closed with "Resolved", not by recovery
	MTTR               time.Duration
	UsersPeak          int
	UsersAvg           int
	UsersAvgPrev       int
	Traffic            uint64 // the users' traffic (TrafficUsers) or the servers'
	TrafficUsers       bool   // Traffic is what the VPN panel counted for users
}

type GroupUptime struct {
	Name   string
	Uptime float64
}

type Downtime struct {
	Name  string
	Down  time.Duration
	Count int
}

// maxReportRows caps each list of the report.
const maxReportRows = 5

// RichReport renders the weekly report through the user's template or the default.
func (f *Formatter) RichReport(r Report) string { return f.fill(KeyReport, "", f.reportVars(r)) }

func pctText(u float64) string { return fmt.Sprintf("%.2f%%", u*100) }

func bytesText(n uint64) string {
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	v, i := float64(n), 0
	for v >= 1024 && i < len(units)-1 {
		v, i = v/1024, i+1
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

// downtimeTable is one titled list of the report: name, how long, how often.
func (f *Formatter) downtimeTable(title string, rows []Downtime, count bool) string {
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<p><b>" + esc(title) + "</b></p><table bordered striped compact>")
	for _, r := range rows[:min(len(rows), maxReportRows)] {
		v := esc(f.Duration(r.Down))
		if count && r.Count > 1 {
			v += " · ×" + fmt.Sprint(r.Count)
		}
		b.WriteString("<tr><td>" + esc(r.Name) + "</td><td>" + v + "</td></tr>")
	}
	b.WriteString("</table>")
	return b.String()
}

// reportVars are the weekly report's placeholders.
func (f *Formatter) reportVars(r Report) Vars {
	loc := f.Loc
	if loc == nil {
		loc = time.UTC
	}
	v := Vars{
		"title":  Text(f.t("report_title")),
		"period": Text(r.From.In(loc).Format("02.01") + " – " + r.To.In(loc).Format("02.01.2006")),
	}
	if r.Uptime >= 0 {
		v["uptime"] = HTML(f.t("report_uptime", pctText(r.Uptime)))
		if r.UptimePrev >= 0 {
			d := (r.Uptime - r.UptimePrev) * 100
			switch {
			case d > 0.005:
				v["uptime_delta"] = Text(fmt.Sprintf("▲%.2f", d))
			case d < -0.005:
				v["uptime_delta"] = Text(fmt.Sprintf("▼%.2f", -d))
			}
		}
	}
	if len(r.Groups) > 1 {
		var b strings.Builder
		b.WriteString("<table bordered striped compact>")
		for _, g := range r.Groups {
			b.WriteString("<tr><td>" + esc(g.Name) + "</td><td>" + pctText(g.Uptime) + "</td></tr>")
		}
		b.WriteString("</table>")
		v["groups"] = HTML(b.String())
	}
	v["worst"] = HTML(f.downtimeTable(f.t("report_worst"), r.Worst, true))
	v["nodes"] = HTML(f.downtimeTable(f.t("report_nodes"), r.Nodes, true))
	v["bans"] = HTML(f.downtimeTable(f.t("report_bans"), r.Bans, true))
	v["incidents"] = HTML(f.t("report_incidents", r.Opened, r.Resolved, r.ByHand))
	if r.Resolved > 0 {
		v["mttr"] = Text(f.t("report_mttr", f.Duration(r.MTTR)))
	}
	if r.UsersPeak > 0 {
		s := f.t("report_online", groupDigits(r.UsersPeak), groupDigits(r.UsersAvg))
		if r.UsersAvgPrev > 0 && r.UsersAvg != r.UsersAvgPrev {
			arrow := "▲"
			if r.UsersAvg < r.UsersAvgPrev {
				arrow = "▼"
			}
			s += fmt.Sprintf(" (%s%s)", arrow, groupDigits(abs(r.UsersAvg-r.UsersAvgPrev)))
		}
		v["online"] = HTML(s)
	}
	if r.Traffic > 0 {
		key := "report_traffic_srv"
		if r.TrafficUsers {
			key = "report_traffic"
		}
		v["traffic"] = HTML(f.t(key, bytesText(r.Traffic)))
	}
	return v
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// SampleReport is a week for the template editor.
func SampleReport() Report {
	to := time.Now()
	return Report{From: to.AddDate(0, 0, -7), To: to, Uptime: 0.9987, UptimePrev: 0.9971,
		Groups: []GroupUptime{{"VPN-ноды", 0.9991}, {"Сервисы", 0.9998}, {"Telegram", 0.9952}},
		Worst:  []Downtime{{"MTProxy am-node-1", 47 * time.Minute, 3}, {"Личный кабинет", 12 * time.Minute, 1}},
		Nodes:  []Downtime{{"pl-node-2", 26 * time.Minute, 9}, {"ee-node-1", 4 * time.Minute, 1}},
		Bans:   []Downtime{{"pl-node-1", 45 * time.Minute, 1}},
		Opened: 31, Resolved: 30, ByHand: 2, MTTR: 6 * time.Minute, UsersPeak: 2843, UsersAvg: 2104, UsersAvgPrev: 1987, Traffic: 92 << 40, TrafficUsers: true}
}
