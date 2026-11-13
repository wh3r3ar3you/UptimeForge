// Package notify renders incidents into messages and delivers them.
package notify

import (
	"cmp"
	"fmt"
	"html"
	"math"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/model"
	"github.com/wh3r3ar3you/uptimeforge/internal/xray"
)

// Formatter renders incidents as Telegram HTML.
type Formatter struct {
	Lang      string // "ru" or "en"
	Loc       *time.Location
	PublicURL string // hub address for "open" links; empty = no link
	// Custom are the user's templates; OnError hears about one that failed
	// to render (its default went out instead).
	Custom  Templates
	OnError func(error)
	// Now is the formatter's clock (nil = time.Now); tests pin it.
	Now func() time.Time
}

type dict map[string]string

var texts = map[string]dict{
	"ru": {
		"down": "Не работает", "blocked": "Блок в %s", "partial": "Частично",
		"probe_offline": "Сервер офлайн", "probe_no_net": "Нет интернета", "subscription": "Подписка недоступна",
		"resolved": "Восстановлено", "host": "Порог метрики", "closed": "Закрыто вручную",
		"closed_by": "☑️ Закрыл %s · пока не восстановится, тревог по нему не будет", "reply_closed": "☑️ Закрыто вручную: <b>%s</b> · %s",
		"rich_probe": "Проба", "rich_state": "Состояние",
		"btn_resolve": "☑️ Решено", "cb_resolved": "Закрыто. Пока не восстановится, тревог не будет", "users_drop": "Ушли пользователи", "node_down": "Нода не на связи с панелью",
		"users_line": "Было <b>%s</b> → сейчас <b>%s</b>", "users_low": "минимум %s из %s",
		"why_users_drop":  "Нода на связи с панелью, а клиенты до неё не доходят: похоже на блокировку ТСПУ.",
		"why_users_gone":  "Нода потеряла связь с панелью, клиенты отвалились вместе с ней.",
		"why_node_down":   "Панель не достаёт до ноды: сервер лежит, перезагружается или потерял сеть.",
		"why_node_down_n": "Панель не достаёт до ноды: сервер лежит, перезагружается или потерял сеть. До этого на ней было %s онлайн.",
		"why_node_mass":   "Панель потеряла большинство нод сразу: похоже на сбой самой панели или её сети.",
		"agent_update":    "Агенты не вернулись после обновления",
		"why_agent_update": "Раскатка остановлена: другим агентам новая версия не уйдёт, пока её не продолжат вручную. " +
			"Проверьте эти серверы — агент не запустился на новой версии или запускатель откатил обновление.",
		"r_update_missing": "не вернулся на новую версию", "r_update_rolled_back": "обновление откатилось",
		"update_resumed": "▶️ Раскатку продолжил %s: эти агенты новую версию больше не получат",
		"update_stopped": "⏹ Раскатка прекращена: обновление агентов выключено или у хаба другая версия",
		"update_closed":  "Раскатка прекращена", "update_removed": "🗑 Агент удалён",
		"reply_update_stopped": "⏹ Раскатка прекращена: <b>%s</b>",
		"update_done":          "Агентов больше не ждём", "reply_update_done": "☑️ Агентов больше не ждём: <b>%s</b>",
		"update_left":    "↪️ Агент перешёл на другую версию (dev или новее)",
		"update_refused": "✋ Агент отказался ставить обновление",
		"update_settled": "☑️ Каждый отставший агент вернулся, удалён, перешёл на другую версию или отказался от обновления",
		"affected":       "Затронуто проверок: %d — %s", "affected_more": "и ещё %d",
		"report_title": "Неделя мониторинга", "report_uptime": "Аптайм проверок: <b>%s</b>", "report_worst": "Дольше всех лежали",
		"report_nodes": "Ноды и серверы без связи", "report_bans": "Блокировки (ушли пользователи)",
		"report_incidents": "Инцидентов: открыто <b>%d</b>, закрыто <b>%d</b>, вручную %d", "report_mttr": "в среднем до восстановления %s",
		"report_online": "👥 Онлайн: пик <b>%s</b>, в среднем <b>%s</b>", "report_traffic": "📶 Трафик пользователей: <b>%s</b>",
		"report_traffic_srv": "📶 Трафик серверов: <b>%s</b>",
		"why_users_mass":     "Онлайн упал сразу на большинстве нод: похоже на сбой панели или сети, а не на блок одной ноды.",
		"users_removed":      "🗑 Нода удалена или выключена в панели",

		"host_cpu": "Высокая загрузка ЦП", "host_mem": "Мало свободной памяти", "host_disk": "Заканчивается диск",
		"host_swap": "Заполнен swap", "host_steal": "Соседи отнимают ЦП", "host_iowait": "Диск не успевает", "host_load": "Высокий load average",
		"host_now": "Сейчас <b>%s</b> · порог %s дольше %s", "host_now0": "Сейчас <b>%s</b> · порог %s", "host_peak": "пик %s",
		"host_per_core": "%.2f на ядро", "host_removed": "🗑 Правило выключено или сервер удалён",
		"why_host_cpu":    "Процессор занят почти полностью: проверьте, что его грузит.",
		"why_host_mem":    "Когда память кончится, ядро начнёт убивать процессы.",
		"why_host_disk":   "На полном диске перестают писаться логи и базы.",
		"why_host_swap":   "Сервер активно использует swap, всё замедляется.",
		"why_host_steal":  "Гипервизор отдаёт процессор соседям: сервер тормозит не из-за своей нагрузки.",
		"why_host_iowait": "Процессор ждёт диск: узкое место в хранилище.",
		"why_host_load":   "Задач в очереди больше, чем ядер.",

		"why_down_single":  "Проверяет только одна проба, подтвердить нечем.",
		"why_blocked":      "Из %s не работает, из других стран работает. Похоже на блокировку.",
		"why_partial":      "Сбой только у части проб. Похоже на блокировку или провайдера.",
		"why_stalled":      "Обрыв после ~%s: так выглядит фильтрация ТСПУ.",
		"why_handshake":    "Рвётся на рукопожатии: похоже на блокировку по SNI.",
		"why_timeout":      "Соединение не устанавливается: похоже на блокировку по IP.",
		"why_offline":      "Агент не на связи с хабом.",
		"why_never":        "Агент ещё ни разу не подключался.",
		"why_no_net":       "У пробы пропал интернет, её проверки не учитываются.",
		"why_subscription": "Хаб не может скачать подписку.",

		"since": "с %s · %s", "was": "Было: %s · %s", "span": "%s → %s · %s", "test": "🔔 Тест: так придёт «%s»",
		"ack": "✅ Принял: %s", "muted": "🔕 Заглушено до %s · %s", "removed": "🗑 Проверка удалена из конфигурации",
		"more": "… и ещё %d", "probes_failing": "%d из %d проб",

		"reply_resolved":  "🟢 Восстановилось: <b>%s</b> · %s",
		"reply_escalated": "⬆️ Стало хуже: %s",
		"reply_remind":    "⏰ Всё ещё %s · уже %s",

		"btn_ack": "✅ Принял", "btn_mute1": "🔕 1 ч", "btn_mute8": "🔕 8 ч",
		"cb_ack": "Принято", "cb_muted": "Заглушено на %s", "cb_denied": "Нет прав",
		"cb_need_admins": "Закрыть или заглушить могут только админы бота: добавьте их в настройках Telegram",

		"r_timeout": "таймаут", "r_refused": "соединение отклонено", "r_reset": "сброс соединения",
		"r_tls": "ошибка TLS", "r_handshake": "обрыв на рукопожатии", "r_stalled": "обрыв после %s",
		"r_dns": "ошибка DNS", "r_unreachable": "сеть недоступна", "r_keyword": "нет ключевого слова",
		"r_offline": "нет связи", "r_baseline": "нет интернета",
		"r_http_status": "ответ %s", "r_dns_mismatch": "подмена DNS", "r_egress": "не тот IP выхода",
		"r_cert_expiring": "сертификат скоро истечёт", "r_config": "ошибка конфигурации", "r_error": "ошибка",
		"r_faketls":     "неверный секрет, не MTProxy или сбиты часы пробы",
		"r_unavailable": "недоступно на этой пробе", "r_auth": "неверные учётные данные", "r_query": "ошибка запроса", "r_bad_response": "неожиданный ответ", "r_not_found": "не найдено", "r_redirects": "слишком много редиректов", "r_value_mismatch": "значение не совпало", "r_grpc_status": "ошибка gRPC", "r_sip_status": "ответ SIP не 2xx", "r_ntp_offset": "часы расходятся", "r_unhealthy": "сервис сообщает о проблеме", "r_not_running": "не запущено", "r_no_message": "нет сообщений в топике", "r_host_key": "ключ сервера не совпал", "r_rate_limited": "исчерпан лимит API", "r_remote_failed": "ошибка на удалённой пробе", "r_push_down": "задача сообщила об ошибке", "r_no_heartbeat": "нет сигнала", "r_manual_down": "отключено вручную",
		"r_via": "сломан upstream-прокси пробы", "r_egress_unknown": "не удалось определить IP выхода", "r_panic": "проверка аварийно завершилась", "r_subscription": "подписка недоступна",

		"d": "д", "h": "ч", "m": "мин", "s": "с", "under_min": "меньше минуты",
		"hello":     "✅ <b>UptimeForge</b> на связи\nПроб: %d · проверок: %d",
		"open":      "Открыть",
		"users_now": "👥 %[2]d онлайн", "users_was": "👥 было онлайн до %[2]d",
	},
	"en": {
		"down": "Down", "blocked": "Blocked in %s", "partial": "Partial",
		"probe_offline": "Server offline", "probe_no_net": "No internet", "subscription": "Subscription unavailable",
		"resolved": "Recovered", "host": "Metric threshold", "closed": "Closed by hand",
		"closed_by": "☑️ Closed by %s · no alerts for it until it recovers", "reply_closed": "☑️ Closed by hand: <b>%s</b> · %s",
		"rich_probe": "Probe", "rich_state": "State",
		"btn_resolve": "☑️ Resolved", "cb_resolved": "Closed. No alerts for it until it recovers", "users_drop": "Users gone", "node_down": "Node lost by the panel",
		"users_line": "Was <b>%s</b> → now <b>%s</b>", "users_low": "low of %s from %s",
		"why_users_drop":  "The node is connected to the panel, but clients no longer reach it: looks like a DPI ban.",
		"why_users_gone":  "The node lost the panel, and its clients went with it.",
		"why_node_down":   "The panel cannot reach the node: the server is down, rebooting or off the network.",
		"why_node_down_n": "The panel cannot reach the node: the server is down, rebooting or off the network. It had %s users online.",
		"why_node_mass":   "The panel lost most nodes at once: looks like a failure of the panel itself or its network.",
		"agent_update":    "Agents did not come back after the update",
		"why_agent_update": "The rollout is halted: no other agent gets the new version until someone resumes it. " +
			"Look at these servers: the agent did not start on the new version, or the launcher rolled the update back.",
		"r_update_missing": "not back on the new version", "r_update_rolled_back": "update rolled back",
		"update_resumed": "▶️ Rollout resumed by %s: these agents will not get the new version again",
		"update_stopped": "⏹ Rollout stopped: agent updates were switched off or the hub runs another version",
		"update_closed":  "Rollout stopped", "update_removed": "🗑 Agent deleted",
		"reply_update_stopped": "⏹ Rollout stopped: <b>%s</b>",
		"update_done":          "No longer waiting for the agents", "reply_update_done": "☑️ No longer waiting for the agents: <b>%s</b>",
		"update_left":    "↪️ The agent moved to another version (dev or newer)",
		"update_refused": "✋ The agent refused the update",
		"update_settled": "☑️ Each straggler came back, was deleted, moved to another version or refused the update",
		"affected":       "Checks affected: %d — %s", "affected_more": "and %d more",
		"report_title": "Monitoring week", "report_uptime": "Checks uptime: <b>%s</b>", "report_worst": "Down the longest",
		"report_nodes": "Nodes and servers lost", "report_bans": "Bans (users gone)",
		"report_incidents": "Incidents: <b>%d</b> opened, <b>%d</b> closed, %d by hand", "report_mttr": "%s to recover on average",
		"report_online": "👥 Online: peak <b>%s</b>, average <b>%s</b>", "report_traffic": "📶 Users' traffic: <b>%s</b>",
		"report_traffic_srv": "📶 Servers' traffic: <b>%s</b>",
		"why_users_mass":     "Users dropped on most nodes at once: looks like a panel or network failure, not one banned node.",
		"users_removed":      "🗑 Node deleted or disabled in the panel",

		"host_cpu": "High CPU", "host_mem": "Low free memory", "host_disk": "Disk almost full",
		"host_swap": "Swap filling up", "host_steal": "Neighbours take the CPU", "host_iowait": "Disk can't keep up", "host_load": "High load average",
		"host_now": "Now <b>%s</b> · threshold %s for %s", "host_now0": "Now <b>%s</b> · threshold %s", "host_peak": "peak %s",
		"host_per_core": "%.2f per core", "host_removed": "🗑 Rule switched off or server removed",
		"why_host_cpu":    "The CPU is nearly saturated: see what loads it.",
		"why_host_mem":    "When memory runs out, the kernel starts killing processes.",
		"why_host_disk":   "On a full disk logs and databases stop writing.",
		"why_host_swap":   "The server leans on swap and everything slows down.",
		"why_host_steal":  "The hypervisor gives the CPU to neighbours: the slowdown is not this server's load.",
		"why_host_iowait": "The CPU waits on the disk: storage is the bottleneck.",
		"why_host_load":   "More tasks are queued than there are cores.",

		"why_down_single":  "Only one probe checks this, nothing to confirm it with.",
		"why_blocked":      "Fails from %s, works from other countries. Looks like blocking.",
		"why_partial":      "Fails from some probes only. Looks like blocking or an ISP.",
		"why_stalled":      "Freezes after ~%s: the signature of volume-based DPI.",
		"why_handshake":    "Dies during the handshake: looks like SNI blocking.",
		"why_timeout":      "Connections never establish: looks like IP blocking.",
		"why_offline":      "The agent lost its connection to the hub.",
		"why_never":        "The agent has never connected.",
		"why_no_net":       "The probe lost internet, its checks do not count.",
		"why_subscription": "The hub can't download the subscription.",

		"since": "since %s · %s", "was": "Was: %s · %s", "span": "%s → %s · %s", "test": "🔔 Test: this is how \"%s\" arrives",
		"ack": "✅ Acknowledged by %s", "muted": "🔕 Muted until %s · %s", "removed": "🗑 Check removed from config",
		"more": "… and %d more", "probes_failing": "%d of %d probes",

		"reply_resolved":  "🟢 Recovered: <b>%s</b> · %s",
		"reply_escalated": "⬆️ Got worse: %s",
		"reply_remind":    "⏰ Still %s · for %s",

		"btn_ack": "✅ Ack", "btn_mute1": "🔕 1h", "btn_mute8": "🔕 8h",
		"cb_ack": "Acknowledged", "cb_muted": "Muted for %s", "cb_denied": "Not allowed",
		"cb_need_admins": "Only bot admins can close or mute: add them in the Telegram settings",

		"r_timeout": "timeout", "r_refused": "connection refused", "r_reset": "connection reset",
		"r_tls": "TLS error", "r_handshake": "handshake failed", "r_stalled": "stalled after %s",
		"r_dns": "DNS error", "r_unreachable": "network unreachable", "r_keyword": "keyword missing",
		"r_offline": "no connection", "r_baseline": "no internet",
		"r_http_status": "answered %s", "r_dns_mismatch": "DNS spoofing", "r_egress": "wrong exit IP",
		"r_cert_expiring": "certificate expires soon", "r_config": "config error", "r_error": "error",
		"r_faketls":     "wrong secret, not an MTProxy, or the probe's clock is off",
		"r_unavailable": "not available on this probe", "r_auth": "wrong credentials", "r_query": "query failed", "r_bad_response": "unexpected response", "r_not_found": "not found", "r_redirects": "too many redirects", "r_value_mismatch": "value mismatch", "r_grpc_status": "gRPC error", "r_sip_status": "SIP answer is not 2xx", "r_ntp_offset": "clock offset too large", "r_unhealthy": "service reports a problem", "r_not_running": "not running", "r_no_message": "no message on the topic", "r_host_key": "server key mismatch", "r_rate_limited": "API limit reached", "r_remote_failed": "failed on the remote probe", "r_push_down": "the job reported a failure", "r_no_heartbeat": "no heartbeat", "r_manual_down": "set to down by hand",
		"r_via": "probe's upstream proxy is broken", "r_egress_unknown": "exit IP could not be determined", "r_panic": "the check crashed", "r_subscription": "subscription unavailable",

		"d": "d", "h": "h", "m": "m", "s": "s", "under_min": "under a minute",
		"hello":     "✅ <b>UptimeForge</b> is on\nProbes: %d · checks: %d",
		"open":      "Open",
		"users_now": "👥 %[2]d online", "users_was": "👥 up to %[2]d were online",
	},
}

func (f *Formatter) t(key string, args ...any) string {
	d, ok := texts[f.Lang]
	if !ok {
		d = texts["en"]
	}
	s, ok := d[key]
	if !ok {
		s = texts["en"][key]
	}
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}

var esc = html.EscapeString

func kindEmoji(inc *model.Incident, k model.Kind) string {
	if inc.Type == "subscription" {
		return "📡"
	}
	if k == model.KindHost {
		return "📈"
	}
	if k == model.KindUsersDrop {
		return "📉"
	}
	if k == model.KindNodeDown {
		return "🔌"
	}
	if k == model.KindAgentUpdate {
		return "⚠️"
	}
	switch k {
	case model.KindDown:
		return "🔴"
	case model.KindBlocked:
		return "🟠"
	case model.KindPartial:
		return "🟡"
	case model.KindProbeOffline:
		return "🔌"
	case model.KindProbeNoNet:
		return "🛰"
	}
	return "🟢"
}

func (f *Formatter) title(inc *model.Incident, k model.Kind) string {
	if inc.Type == "subscription" {
		return f.t("subscription")
	}
	if k == model.KindHost && inc.Host != nil {
		return f.t("host_" + inc.Host.Metric)
	}
	if k == model.KindBlocked {
		return f.t("blocked", strings.ToUpper(inc.Region))
	}
	return f.t(string(k))
}

// Flag turns an ISO country code into its flag emoji.
func Flag(region string) string {
	r := []rune(strings.ToUpper(region))
	if len(r) != 2 || r[0] < 'A' || r[0] > 'Z' || r[1] < 'A' || r[1] > 'Z' {
		return "📍"
	}
	return string([]rune{0x1F1E6 + r[0] - 'A', 0x1F1E6 + r[1] - 'A'})
}

func (f *Formatter) Duration(d time.Duration) string {
	d = d.Round(time.Minute)
	if d < time.Minute {
		return f.t("under_min")
	}
	days, hours, mins := int(d.Hours())/24, int(d.Hours())%24, int(d.Minutes())%60
	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", days, f.t("d")))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", hours, f.t("h")))
	}
	if mins > 0 && days == 0 {
		parts = append(parts, fmt.Sprintf("%d %s", mins, f.t("m")))
	}
	return strings.Join(parts, " ")
}

func (f *Formatter) clock(t time.Time) string {
	t = t.In(f.Loc)
	now := time.Now
	if f.Now != nil {
		now = f.Now
	}
	if now().Sub(t) > 20*time.Hour {
		return t.Format("02.01 15:04")
	}
	return t.Format("15:04")
}

func (f *Formatter) reason(l model.ProbeLine) string {
	switch l.Reason {
	case "stalled":
		return f.t("r_stalled", xray.HumanBytes(l.Bytes))
	case "http_status": // the error is "HTTP 503"
		return f.t("r_http_status", short(strings.TrimPrefix(l.Error, "HTTP "), 40))
	}
	if _, known := texts["en"]["r_"+l.Reason]; known {
		r := f.t("r_" + l.Reason)
		switch l.Reason { // too generic alone: the detail says what to fix
		case "baseline", "config", "error":
			if l.Error != "" {
				r += ": " + short(l.Error, 60)
			}
		}
		return r
	}
	if l.Error != "" {
		return short(l.Error, 90)
	}
	return l.Reason
}

func short(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// The probe list sits in an expandable quote: collapsed, Telegram shows its
// first lines (failures come first), and a tap shows the rest, so the card
// stays short while the detail is one tap away.
const (
	maxBadLines  = 10
	maxGoodLines = 5
)

// quote wraps lines in a blockquote, expandable once there are more than
// Telegram shows collapsed anyway.
func quote(lines []string) string {
	tag := "<blockquote>"
	if len(lines) > 3 {
		tag = "<blockquote expandable>"
	}
	return tag + strings.Join(lines, "\n") + "</blockquote>"
}

func (f *Formatter) probeLines(inc *model.Incident) []string {
	var bad, good []string
	for _, l := range inc.Lines {
		name := Flag(l.Region) + " " + esc(short(l.Name, 32))
		if l.OK {
			s := "✅ " + name
			if l.LatencyMS > 0 {
				s += fmt.Sprintf(" · %.0f ms", l.LatencyMS)
			}
			good = append(good, s)
		} else {
			bad = append(bad, "❌ "+name+" · "+esc(f.reason(l)))
		}
	}
	shown := append(bad[:min(len(bad), maxBadLines)], good[:min(len(good), maxGoodLines)]...)
	if hidden := len(bad) + len(good) - len(shown); hidden > 0 {
		shown = append(shown, f.t("more", hidden))
	}
	return shown
}

// why explains the verdict in one or two sentences.
func (f *Formatter) why(inc *model.Incident) string {
	switch {
	case inc.Type == "subscription":
		return f.t("why_subscription")
	case inc.Kind == model.KindProbeOffline:
		if inc.Note == "never" {
			return f.t("why_never")
		}
		return f.t("why_offline")
	case inc.Kind == model.KindProbeNoNet:
		return f.t("why_no_net")
	case inc.Kind == model.KindAgentUpdate:
		return f.t("why_agent_update")
	case inc.Kind == model.KindNodeDown:
		switch {
		case inc.Note == "mass":
			return f.t("why_node_mass")
		case inc.Drop != nil && inc.Drop.Before > 0:
			return f.t("why_node_down_n", groupDigits(inc.Drop.Before))
		}
		return f.t("why_node_down")
	case inc.Type == "users":
		switch {
		case inc.Note == "mass":
			return f.t("why_users_mass")
		case inc.Drop != nil && !inc.Drop.Connected:
			return f.t("why_users_gone")
		}
		return f.t("why_users_drop")
	case inc.Type == "host":
		if inc.Host == nil {
			return ""
		}
		return f.t("why_host_" + inc.Host.Metric)
	}

	reasons := map[string]int{}
	var stalledAt int64
	failing := 0
	for _, l := range inc.Lines {
		if !l.OK {
			failing++
			reasons[l.Reason]++
			if l.Reason == "stalled" && l.Bytes > stalledAt {
				stalledAt = l.Bytes
			}
		}
	}
	all := func(rs ...string) bool {
		n := 0
		for _, r := range rs {
			n += reasons[r]
		}
		return failing > 0 && n == failing
	}

	var s string
	switch inc.Kind {
	case model.KindDown:
		if len(inc.Lines) == 1 {
			s = f.t("why_down_single")
		}
	case model.KindBlocked:
		s = f.t("why_blocked", strings.ToUpper(inc.Region))
	case model.KindPartial:
		s = f.t("why_partial")
	}
	if inc.Kind == model.KindBlocked || inc.Kind == model.KindPartial {
		switch {
		case all("stalled"):
			s = f.t("why_stalled", xray.HumanBytes(stalledAt))
		case all("handshake", "reset", "tls"):
			s = f.t("why_handshake")
		case all("timeout"):
			s = f.t("why_timeout")
		}
	}
	return s
}

func (f *Formatter) header(inc *model.Incident, emoji, title string) string {
	var b strings.Builder
	name := esc(inc.Name)
	if inc.Type == "users" && len(inc.Lines) > 0 && inc.Lines[0].Region != "" {
		name = Flag(inc.Lines[0].Region) + " " + name
	}
	fmt.Fprintf(&b, "%s <b>%s</b> · <b>%s</b>", emoji, esc(title), name)
	if inc.Target != "" && inc.Target != inc.Name {
		b.WriteString("\n<code>" + esc(inc.Target) + "</code>")
	}
	return b.String()
}

// Incident renders an open incident as Telegram HTML: the user's card
// template turned into HTML, or the classic card.
func (f *Formatter) Incident(inc *model.Incident, now time.Time) string {
	if _, custom := f.template(KeyCard, inc.Kind); custom {
		return RichToHTML(f.RichIncident(inc, now))
	}
	var b strings.Builder
	b.WriteString(f.header(inc, kindEmoji(inc, inc.Kind), f.title(inc, inc.Kind)))
	if inc.Kind == model.KindNodeDown {
		if len(inc.Lines) > 0 && inc.Lines[0].Error != "" {
			b.WriteString("\n\n<code>" + esc(short(inc.Lines[0].Error, 120)) + "</code>")
		}
	} else if inc.Type == "users" && inc.Drop != nil {
		b.WriteString("\n\n" + f.t("users_line", groupDigits(inc.Drop.Before), groupDigits(inc.Drop.Now)))
	} else if inc.Type == "host" && inc.Host != nil {
		hv := inc.Host
		if hv.For > 0 {
			b.WriteString("\n\n" + f.t("host_now", f.hostValue(hv.Metric, hv.Value), f.hostValue(hv.Metric, hv.Above), esc(f.Duration(time.Duration(hv.For)*time.Second))))
		} else {
			b.WriteString("\n\n" + f.t("host_now0", f.hostValue(hv.Metric, hv.Value), f.hostValue(hv.Metric, hv.Above)))
		}
	} else if lines := f.probeLines(inc); len(lines) > 0 && inc.Type != "probe" {
		b.WriteString("\n" + quote(lines))
	} else if (inc.Type == "subscription" || inc.Kind == model.KindProbeNoNet) && len(inc.Lines) > 0 && inc.Lines[0].Error != "" {
		b.WriteString("\n\n<code>" + esc(short(inc.Lines[0].Error, 120)) + "</code>")
	}
	if a := f.affected(inc); a != "" {
		b.WriteString("\n🔗 " + a)
	}
	if why := f.why(inc); why != "" {
		b.WriteString("\n💡 " + esc(why))
	}
	b.WriteString("\n⏱ " + f.t("since", f.clock(inc.Started), esc(f.Duration(now.Sub(inc.Started)))))
	if inc.Users > 0 && inc.Type != "users" { // a drop states its own numbers
		b.WriteString(" · " + f.t("users_now", esc(inc.Node), inc.Users))
	}
	if link := f.Link(inc); link != "" {
		b.WriteString(" · <a href=\"" + esc(link) + "\">" + f.t("open") + "</a>")
	}
	if inc.AckBy != "" {
		b.WriteString("\n" + f.t("ack", esc(inc.AckBy)))
	}
	if now.Before(inc.MutedUntil) {
		b.WriteString("\n" + f.t("muted", f.clock(inc.MutedUntil), esc(inc.MutedBy)))
	}
	return b.String()
}

// Resolved replaces the incident message once it is over.
func (f *Formatter) Resolved(inc *model.Incident) string {
	if _, custom := f.template(KeyResolved, cmp.Or(inc.Worst, inc.Kind)); custom {
		return RichToHTML(f.RichResolved(inc))
	}
	emoji, title := f.resolvedTitle(inc)
	return f.header(inc, emoji, title) + "\n" + strings.Join(f.resolvedLines(inc), "\n")
}

func (f *Formatter) ResolvedReply(inc *model.Incident) string {
	if inc.Type == "update" && inc.Note == "update_stopped" {
		return f.t("reply_update_stopped", esc(inc.Name))
	}
	if updateClosedWithoutReturn(inc) {
		return f.t("reply_update_done", esc(inc.Name))
	}
	if inc.ResolvedBy != "" {
		return f.t("reply_closed", esc(inc.Name), esc(inc.ResolvedBy))
	}
	return f.t("reply_resolved", esc(inc.Name), esc(f.Duration(inc.Resolved.Sub(inc.Started))))
}

func (f *Formatter) EscalatedReply(inc *model.Incident) string {
	if _, custom := f.template(KeyEscalated, inc.Kind); custom {
		v := f.cardVars(inc, time.Now())
		v["text"] = HTML(f.escalatedText(inc))
		return f.fill(KeyEscalated, inc.Kind, v)
	}
	return f.escalatedText(inc)
}

func (f *Formatter) escalatedText(inc *model.Incident) string {
	return f.t("reply_escalated", kindEmoji(inc, inc.Kind)+" <b>"+esc(f.title(inc, inc.Kind))+"</b> · "+esc(inc.Name))
}

func (f *Formatter) RemindReply(inc *model.Incident, now time.Time) string {
	if _, custom := f.template(KeyRemind, inc.Kind); custom {
		v := f.cardVars(inc, now)
		v["text"] = HTML(f.remindText(inc, now))
		return f.fill(KeyRemind, inc.Kind, v)
	}
	return f.remindText(inc, now)
}

func (f *Formatter) remindText(inc *model.Incident, now time.Time) string {
	return f.t("reply_remind", kindEmoji(inc, inc.Kind)+" <b>"+esc(f.title(inc, inc.Kind))+"</b> · "+esc(inc.Name), esc(f.Duration(now.Sub(inc.Started))))
}

func (f *Formatter) Hello(probes, checks int) string { return f.t("hello", probes, checks) }

// Test is the message an event's "send test" button posts to its route.
// groupDigits writes 1012 as "1 012", the way people read user counts.
func groupDigits(n int) string {
	s := fmt.Sprint(n)
	if n < 0 || len(s) <= 3 {
		return s
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteRune(' ')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// hostValue prints a metric the way the UI does: percent, or load per core.
func (f *Formatter) hostValue(metric string, v float64) string {
	if metric == "load" {
		return f.t("host_per_core", v)
	}
	if v < 10 && v != math.Trunc(v) {
		return fmt.Sprintf("%.1f%%", v)
	}
	return fmt.Sprintf("%.0f%%", v)
}

func (f *Formatter) Test(event string) string {
	return f.t("test", f.EventTitle(event))
}

// EventTitle names a notification event as its messages do.
func (f *Formatter) EventTitle(event string) string {
	switch event {
	case EventBlocked:
		return f.t("blocked", "RU")
	case EventRemind:
		return "⏰"
	}
	return f.t(event)
}

// Link points to the incident's page in the web UI.
func (f *Formatter) Link(inc *model.Incident) string {
	if f.PublicURL == "" {
		return ""
	}
	base := strings.TrimRight(f.PublicURL, "/")
	switch {
	case strings.HasPrefix(inc.Key, "probe:"), strings.HasPrefix(inc.Key, "probe-net:"), strings.HasPrefix(inc.Key, "host:"):
		_, id, _ := strings.Cut(inc.Key, ":")
		id, _, _ = strings.Cut(id, ":")
		return base + "/probes/" + url.PathEscape(id)
	case strings.HasPrefix(inc.Key, "sub:"):
		return base + "/checks"
	case strings.HasPrefix(inc.Key, "rw:"):
		return base + "/remnawave"
	case strings.HasPrefix(inc.Key, "update:"):
		return base + "/settings/general" // the rollout status and its Resume
	}
	return base + "/checks/" + url.PathEscape(inc.Key)
}

// Plain renders the incident message without markup (webhooks, logs): every
// tag goes, whatever a card or a custom template used, then entities are
// decoded. Text inside is escaped, so a "<" left there was never a tag.
func (f *Formatter) Plain(s string) string {
	return html.UnescapeString(anyTag.ReplaceAllString(s, ""))
}

var anyTag = regexp.MustCompile(`</?[a-zA-Z][a-zA-Z0-9-]*(?:\s[^>]*)?/?>`)

// maxAffected is how many names of affected checks a card lists.
const maxAffected = 10

// affected is the line naming the checks an incident took down with it,
// HTML-escaped; "" when there are none.
func (f *Formatter) affected(inc *model.Incident) string {
	n := len(inc.Affected)
	if n == 0 {
		return ""
	}
	names := make([]string, 0, maxAffected+1)
	for _, a := range inc.Affected[:min(n, maxAffected)] {
		names = append(names, esc(a))
	}
	if n > maxAffected {
		names = append(names, f.t("affected_more", n-maxAffected))
	}
	return f.t("affected", n, strings.Join(names, ", "))
}
