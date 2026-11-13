package notify

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/model"
)

func TestFormatIncident(t *testing.T) {
	f := &Formatter{Lang: "ru", Loc: time.UTC, PublicURL: "https://hub.example.com"}
	started := time.Now().Add(-12 * time.Minute)
	inc := &model.Incident{
		Key: "de-node-1", Name: "de-node-1 · Reality", Group: "VPN", Target: "VLESS · de1.example.com:443",
		Kind: model.KindBlocked, Region: "ru", Severity: model.Critical, Started: started, Node: "de-node-1", Users: 412,
		Lines: []model.ProbeLine{
			{Probe: "home", Name: "Дом · Москва", Region: "ru", Reason: "stalled", Bytes: 16 << 10},
			{Probe: "msk", Name: "ДЦ <МСК>", Region: "ru", Reason: "stalled", Bytes: 16 << 10},
			{Probe: "de", Name: "Hetzner", Region: "de", OK: true, LatencyMS: 34},
		},
	}
	msg := f.Incident(inc, time.Now())
	for _, want := range []string{
		"🟠 <b>Блок в RU</b>", "🇷🇺", "обрыв после 16.0 KB", "ДЦ &lt;МСК&gt;", "✅ 🇩🇪 Hetzner · 34 ms",
		"фильтрация ТСПУ", "👥 412 онлайн", "12 мин", `href="https://hub.example.com/checks/de-node-1"`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("incident message lacks %q:\n%s", want, msg)
		}
	}

	inc.Resolved, inc.Worst, inc.PeakFailing, inc.PeakTotal = started.Add(27*time.Minute), model.KindBlocked, 2, 3
	res := f.Resolved(inc)
	for _, want := range []string{"🟢 <b>Восстановлено</b>", "Было: 🟠 Блок в RU · 2 из 3 проб", "27 мин", "было онлайн до 412"} {
		if !strings.Contains(res, want) {
			t.Errorf("resolved message lacks %q:\n%s", want, res)
		}
	}
}

func TestFormatProbeOfflineAndEnglish(t *testing.T) {
	f := &Formatter{Lang: "en", Loc: time.UTC}
	inc := &model.Incident{Key: "probe:kz", Name: "PS · Almaty", Type: "probe", Kind: model.KindProbeOffline, Note: "never",
		Severity: model.Warning, Started: time.Now()}
	msg := f.Incident(inc, time.Now())
	if !strings.Contains(msg, "Server offline") || !strings.Contains(msg, "never connected") || strings.Contains(msg, "href") {
		t.Fatalf("probe offline message:\n%s", msg)
	}
}

// Flag used to panic on non-ASCII regions (strings.ToUpper changes the
// length of some of them).
func TestFlagNonASCIIRegion(t *testing.T) {
	f := &Formatter{Lang: "en", Loc: time.UTC}
	for _, region := range []string{"é", "ж", "ß", "ü", "ı", "ſ", "жж"} {
		if got := Flag(region); got != "📍" {
			t.Errorf("Flag(%q) = %q", region, got)
		}
		f.Incident(&model.Incident{Name: "n", Kind: model.KindDown, Lines: []model.ProbeLine{{Probe: "p", Region: region}}}, time.Now())
	}
	if got := Flag("de"); got != "🇩🇪" {
		t.Errorf("Flag(de) = %q", got)
	}
}

// A message stays readable with many probes down: the page has the rest.
func TestFormatStaysShort(t *testing.T) {
	f := &Formatter{Lang: "ru", Loc: time.UTC}
	inc := &model.Incident{Key: "c", Name: "c", Kind: model.KindPartial, Started: time.Now()}
	for i := range 30 {
		inc.Lines = append(inc.Lines, model.ProbeLine{Probe: "p", Name: "проба", Region: "de", OK: i%2 == 0, Reason: "timeout"})
	}
	msg := f.Incident(inc, time.Now())
	// Outside the expandable quote the card is a handful of lines; inside,
	// at most the bad and good caps plus the count of the rest.
	q0, q1 := strings.Index(msg, "<blockquote expandable>"), strings.Index(msg, "</blockquote>")
	if q0 < 0 || q1 < q0 {
		t.Fatalf("probe lines must sit in an expandable quote:\n%s", msg)
	}
	if n := strings.Count(msg[:q0]+msg[q1:], "\n"); n > 5 {
		t.Fatalf("%d visible lines:\n%s", n, msg)
	}
	if n := strings.Count(msg[q0:q1], "\n"); n > maxBadLines+maxGoodLines {
		t.Fatalf("%d quoted lines:\n%s", n, msg)
	}
	if !strings.Contains(msg, "… и ещё 15") {
		t.Fatalf("hidden probes are not counted:\n%s", msg)
	}
}

// A host threshold says what, how much, against which rule, and why it matters.
func TestFormatHostIncident(t *testing.T) {
	f := &Formatter{Lang: "ru", Loc: time.UTC, PublicURL: "https://hub.example.com"}
	started := time.Now().Add(-14 * time.Minute)
	inc := &model.Incident{
		Key: "host:de-host-1:steal", Name: "de-host-1", Type: "host", Kind: model.KindHost, Worst: model.KindHost, Severity: model.Warning,
		Started: started, Host: &model.HostValue{Metric: "steal", Value: 34.2, Peak: 41, Above: 20, For: 600},
	}
	msg := f.Incident(inc, time.Now())
	for _, want := range []string{"📈", "Соседи отнимают ЦП", "de-host-1", "<b>34%</b>", "порог 20%", "10 мин", "Гипервизор", "/probes/de-host-1"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "<code>") {
		t.Errorf("no target line for a host incident:\n%s", msg)
	}
	inc.Resolved = time.Now()
	done := f.Resolved(inc)
	if !strings.Contains(done, "Восстановлено") || !strings.Contains(done, "пик 41%") {
		t.Errorf("resolved:\n%s", done)
	}
	inc.Host = &model.HostValue{Metric: "load", Value: 1.83, Above: 1.5, For: 600}
	if msg := f.Incident(inc, time.Now()); !strings.Contains(msg, "1.83 на ядро") {
		t.Errorf("load:\n%s", msg)
	}
	if eventOf(inc, inc.Kind) != EventHost {
		t.Fatal("host incidents route to their own event")
	}
}

func TestHostValueFormat(t *testing.T) {
	f := &Formatter{Lang: "ru"}
	for v, want := range map[float64]string{1: "1%", 3.14: "3.1%", 20: "20%", 87.6: "88%"} {
		if got := f.hostValue("cpu", v); got != want {
			t.Errorf("%v: %q, want %q", v, got, want)
		}
	}
}

// A users drop says how many were there, how many are left, and what it
// most likely means; it is critical and routes to its own event.
func TestFormatUsersDrop(t *testing.T) {
	f := &Formatter{Lang: "ru", Loc: time.UTC, PublicURL: "https://hub.example.com"}
	inc := &model.Incident{
		Key: "rw:uuid-1", Name: "de-node-3", Type: "users", Kind: model.KindUsersDrop, Worst: model.KindUsersDrop,
		Severity: model.Critical, Started: time.Now().Add(-3 * time.Minute), Users: 1012, Node: "de-node-3",
		Lines: []model.ProbeLine{{Probe: "uuid-1", Name: "de-node-3", Region: "de", Reason: "users_drop"}},
		Drop:  &model.UsersDrop{Before: 1012, Now: 2, Low: 2, Connected: true},
	}
	msg := f.Incident(inc, time.Now())
	for _, want := range []string{"📉", "Ушли пользователи", "🇩🇪 de-node-3", "Было <b>1 012</b> → сейчас <b>2</b>", "ТСПУ", "/remnawave"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "онлайн") {
		t.Errorf("the drop line already gives the numbers:\n%s", msg)
	}
	inc.Note = "mass"
	if !strings.Contains(f.Incident(inc, time.Now()), "большинстве нод") {
		t.Error("a mass drop must say so")
	}
	inc.Note, inc.Drop.Connected = "", false
	if !strings.Contains(f.Incident(inc, time.Now()), "потеряла связь с панелью") {
		t.Error("a disconnected node is a different story")
	}
	inc.Resolved = time.Now()
	if done := f.Resolved(inc); !strings.Contains(done, "минимум 2 из 1 012") {
		t.Errorf("resolved:\n%s", done)
	}
	if eventOf(inc, inc.Kind) != EventUsersDrop {
		t.Fatal("drops route to their own event")
	}
}

// A node the panel lost is an outage, said as one: no "users gone", the
// panel's own message, how many were on it; it routes with servers offline.
func TestFormatNodeDown(t *testing.T) {
	f := &Formatter{Lang: "ru", Loc: time.UTC, PublicURL: "https://hub.example.com"}
	inc := &model.Incident{
		Key: "rw:uuid-1", Name: "pl-node-2", Type: "users", Kind: model.KindNodeDown, Worst: model.KindNodeDown,
		Severity: model.Critical, Started: time.Now().Add(-4 * time.Minute),
		Lines: []model.ProbeLine{{Probe: "uuid-1", Name: "pl-node-2", Region: "pl", Reason: "node_down", Error: "Failed to get system stats"}},
		Drop:  &model.UsersDrop{Before: 472},
	}
	for _, msg := range []string{f.Incident(inc, time.Now()), f.RichIncident(inc, time.Now())} {
		for _, want := range []string{"🔌", "Нода не на связи с панелью", "pl-node-2", "Failed to get system stats", "перезагружается", "было 472 онлайн"} {
			if !strings.Contains(msg, want) {
				t.Errorf("missing %q in:\n%s", want, msg)
			}
		}
		for _, bad := range []string{"Ушли пользователи", "ТСПУ", "сейчас"} {
			if strings.Contains(msg, bad) {
				t.Errorf("a node that is down is not a drop: %q in:\n%s", bad, msg)
			}
		}
	}
	inc.Note = "mass"
	if !strings.Contains(f.Incident(inc, time.Now()), "большинство нод") {
		t.Error("the panel losing most nodes must say so")
	}
	inc.Resolved = time.Now()
	if done := f.Resolved(inc); strings.Contains(done, "минимум") || !strings.Contains(done, "Нода не на связи") {
		t.Errorf("resolved:\n%s", done)
	}
	if eventOf(inc, inc.Kind) != EventProbeOffline {
		t.Fatal("a lost node routes with servers offline")
	}
}

// telegramHTML reports why s would be refused by Telegram's HTML parse mode
// ("can't parse entities"), or "" if it is fine: only the tags Telegram
// knows, balanced, and every & starting a valid entity.
func telegramHTML(s string) string {
	if err := ValidateHTML(s); err != nil {
		return err.Error()
	}
	return ""
}

// Every message we send must parse as Telegram HTML: one that does not is
// refused whole, and the alert is lost. A fresh incident is the classic
// trap ("<1 min").
func TestEveryMessageIsTelegramHTML(t *testing.T) {
	now := time.Now()
	nasty := "a<b & c>"
	lines := []model.ProbeLine{
		{Probe: "p1", Name: nasty, Region: "ru", Reason: "stalled", Bytes: 16 << 10, Error: "x < y & z"},
		{Probe: "p2", Name: "ok", Region: "de", OK: true, LatencyMS: 12},
	}
	var incs []*model.Incident
	for _, k := range []model.Kind{model.KindDown, model.KindBlocked, model.KindPartial, model.KindProbeOffline, model.KindProbeNoNet} {
		incs = append(incs, &model.Incident{Key: "k", Name: nasty, Target: "VLESS · " + nasty, Kind: k, Worst: k, Region: "ru", Lines: lines, Node: nasty, Users: 3})
	}
	incs = append(incs,
		&model.Incident{Key: "sub:x", Name: nasty, Type: "subscription", Kind: model.KindDown, Worst: model.KindDown, Lines: []model.ProbeLine{{Name: "hub", Error: "<html>"}}},
		&model.Incident{Key: "host:p:cpu", Name: nasty, Type: "host", Kind: model.KindHost, Worst: model.KindHost, Host: &model.HostValue{Metric: "cpu", Value: 95, Peak: 99, Above: 90, For: 300}},
		&model.Incident{Key: "rw:u", Name: nasty, Type: "users", Kind: model.KindUsersDrop, Worst: model.KindUsersDrop, Note: "mass",
			Lines: []model.ProbeLine{{Name: nasty, Region: "de"}}, Drop: &model.UsersDrop{Before: 1012, Now: 2, Low: 1}},
		&model.Incident{Key: "rw:d", Name: nasty, Type: "users", Kind: model.KindNodeDown, Worst: model.KindNodeDown,
			Lines: []model.ProbeLine{{Name: nasty, Region: "de", Reason: "node_down", Error: nasty}}, Drop: &model.UsersDrop{Before: 472}},
	)
	for _, lang := range []string{"ru", "en"} {
		f := &Formatter{Lang: lang, Loc: time.UTC, PublicURL: "https://hub.example.com/a?b=1&c=2"}
		var msgs []string
		for _, inc := range incs {
			for _, age := range []time.Duration{10 * time.Second, 3 * time.Hour} { // fresh and long-running
				inc.Started, inc.AckBy, inc.MutedBy, inc.MutedUntil = now.Add(-age), nasty, nasty, now.Add(time.Hour)
				msgs = append(msgs, f.Incident(inc, now), f.EscalatedReply(inc), f.RemindReply(inc, now))
				r := *inc
				r.Resolved = now
				msgs = append(msgs, f.Resolved(&r), f.ResolvedReply(&r))
			}
		}
		for _, ev := range Events {
			msgs = append(msgs, f.Test(ev))
		}
		msgs = append(msgs, f.Hello(3, 7))
		for _, m := range msgs {
			if why := telegramHTML(m); why != "" {
				t.Errorf("%s: %s in:\n%s", lang, why, m)
			}
		}
	}
}

// richHTML checks rich message markup: the rich tag set, balanced, entities ok.
func richHTML(s string) string {
	if err := ValidateRich(s); err != nil {
		return err.Error()
	}
	return ""
}

// Every rich card parses: Telegram refuses a malformed one whole.
func TestRichCardsAreValid(t *testing.T) {
	now := time.Now()
	nasty := "a<b & c>"
	var lines []model.ProbeLine
	for i := range 20 {
		lines = append(lines, model.ProbeLine{Probe: fmt.Sprint(i), Name: nasty, Region: "ru", OK: i%3 == 0, Reason: "timeout", LatencyMS: 12})
	}
	incs := []*model.Incident{
		{Key: "k", Name: nasty, Target: nasty, Kind: model.KindBlocked, Worst: model.KindBlocked, Region: "ru", Lines: lines[:4], Users: 9, Node: nasty},
		{Key: "k2", Name: nasty, Kind: model.KindPartial, Worst: model.KindPartial, Lines: lines},
		{Key: "host:p:cpu", Name: nasty, Type: "host", Kind: model.KindHost, Worst: model.KindHost, Host: &model.HostValue{Metric: "cpu", Value: 95, Peak: 99, Above: 90, For: 300}},
		{Key: "rw:u", Name: nasty, Type: "users", Kind: model.KindUsersDrop, Worst: model.KindUsersDrop, Lines: []model.ProbeLine{{Name: nasty, Region: "de"}}, Drop: &model.UsersDrop{Before: 1012, Now: 2, Low: 1}},
		{Key: "sub:x", Name: nasty, Type: "subscription", Kind: model.KindDown, Worst: model.KindDown, Lines: []model.ProbeLine{{Name: "hub", Error: "<html>"}}},
		{Key: "rw:d", Name: nasty, Type: "users", Kind: model.KindNodeDown, Worst: model.KindNodeDown, Note: "mass",
			Lines: []model.ProbeLine{{Name: nasty, Region: "de", Reason: "node_down", Error: nasty}}, Drop: &model.UsersDrop{Before: 472}},
	}
	for _, lang := range []string{"ru", "en"} {
		f := &Formatter{Lang: lang, Loc: time.UTC, PublicURL: "https://hub.example.com"}
		for _, inc := range incs {
			inc.Started, inc.AckBy = now.Add(-10*time.Second), nasty
			for _, m := range []string{f.RichIncident(inc, now)} {
				if why := richHTML(m); why != "" {
					t.Errorf("%s: %s in:\n%s", lang, why, m)
				}
			}
			r := *inc
			r.Resolved, r.ResolvedBy = now, nasty
			if why := richHTML(f.RichResolved(&r)); why != "" {
				t.Errorf("%s resolved: %s", lang, why)
			}
			if !strings.Contains(f.RichResolved(&r), "☑️") || strings.Contains(f.RichResolved(&r), "🟢") {
				t.Errorf("a manual close is not a recovery:\n%s", f.RichResolved(&r))
			}
		}
		if !strings.Contains(f.RichIncident(incs[1], now), "<details>") {
			t.Error("a long probe list folds into details")
		}
	}
}

// A node's card lists what it took down: ten names, then a count.
func TestFormatAffected(t *testing.T) {
	f := &Formatter{Lang: "ru", Loc: time.UTC}
	inc := &model.Incident{Key: "de-tcp", Name: "de-node-1", Kind: model.KindDown, Worst: model.KindDown, Started: time.Now()}
	for i := range 12 {
		inc.Affected = append(inc.Affected, fmt.Sprintf("cfg<%d>", i))
	}
	for _, msg := range []string{f.Incident(inc, time.Now()), f.RichIncident(inc, time.Now())} {
		if !strings.Contains(msg, "Затронуто проверок: 12") || !strings.Contains(msg, "cfg&lt;9&gt;") || strings.Contains(msg, "cfg&lt;10&gt;") || !strings.Contains(msg, "и ещё 2") {
			t.Errorf("affected line:\n%s", msg)
		}
	}
	inc.Affected = nil
	if strings.Contains(f.Incident(inc, time.Now()), "Затронуто") {
		t.Error("no affected line without affected checks")
	}
}

// agentUpdateIncident is what the hub raises when agents it updated do not
// come back (internal/hub/rollout.go).
func agentUpdateIncident(now time.Time) *model.Incident {
	return &model.Incident{Key: "update:agents", Name: "uptimeforge-agent 0.2.0", Type: "update", Kind: model.KindAgentUpdate, Worst: model.KindAgentUpdate,
		Severity: model.Warning, Started: now.Add(-12 * time.Minute), PeakFailing: 2, PeakTotal: 2, Lines: []model.ProbeLine{
			{Probe: "a01", Name: "msk-1", Region: "ru", Reason: "update_missing", Error: "not back on 0.2.0 within 10m0s"},
			{Probe: "a02", Name: "a<b>", Region: "de", Reason: "update_rolled_back", Error: "back on 0.1.0: the update to 0.2.0 was rolled back"},
		}}
}

// A rollout halt reads as a warning about the agents, in the user's
// language, and its close says why it closed.
func TestFormatAgentUpdate(t *testing.T) {
	now := time.Now()
	f := &Formatter{Lang: "ru", Loc: time.UTC, PublicURL: "https://hub.example.com"}
	inc := agentUpdateIncident(now)
	for name, msg := range map[string]string{"card": f.Incident(inc, now), "rich": f.RichIncident(inc, now)} {
		for _, want := range []string{"⚠️", "Агенты не вернулись после обновления", "uptimeforge-agent 0.2.0", "не вернулся на новую версию",
			"обновление откатилось", "Раскатка остановлена", "a&lt;b&gt;"} {
			if !strings.Contains(msg, want) {
				t.Errorf("%s lacks %q:\n%s", name, want, msg)
			}
		}
		if strings.Contains(msg, "🟢") || strings.Contains(msg, "<b></b>") || strings.Contains(msg, "within 10m0s") {
			t.Errorf("%s reads wrong:\n%s", name, msg)
		}
	}
	if why := telegramHTML(f.Incident(inc, now)); why != "" {
		t.Errorf("card: %s", why)
	}
	if why := richHTML(f.RichIncident(inc, now)); why != "" {
		t.Errorf("rich card: %s", why)
	}

	en := &Formatter{Lang: "en", Loc: time.UTC}
	if msg := en.Incident(inc, now); !strings.Contains(msg, "Agents did not come back after the update") || !strings.Contains(msg, "update rolled back") {
		t.Errorf("en card:\n%s", msg)
	}

	// Back on the new version: recovered.
	r := *inc
	r.Resolved = now
	if msg := f.Resolved(&r); !strings.Contains(msg, "🟢") || !strings.Contains(msg, "Было: ⚠️ Агенты не вернулись после обновления") {
		t.Errorf("recovered:\n%s", msg)
	}
	// Resumed by hand: closed by someone, the agents did not come back.
	resumed := r
	resumed.ResolvedBy, resumed.Note = "leo", "resumed"
	for _, msg := range []string{f.Resolved(&resumed), f.RichResolved(&resumed)} {
		if strings.Contains(msg, "🟢") || !strings.Contains(msg, "Раскатку продолжил leo") || strings.Contains(msg, "пока не восстановится") {
			t.Errorf("resumed:\n%s", msg)
		}
	}
	// Updates switched off: not a recovery either.
	stopped := r
	stopped.Note = "update_stopped"
	for _, msg := range []string{f.Resolved(&stopped), f.RichResolved(&stopped), f.ResolvedReply(&stopped)} {
		if strings.Contains(msg, "🟢") || !strings.Contains(msg, "Раскатка прекращена") {
			t.Errorf("stopped:\n%s", msg)
		}
	}
	removed := r
	removed.Note = "removed"
	if msg := f.Resolved(&removed); !strings.Contains(msg, "Агент удалён") || strings.Contains(msg, "Проверка удалена") {
		t.Errorf("removed:\n%s", msg)
	}
}
