package notify

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/model"
)

// snapshotIncidents are the cards the snapshot covers: every kind of
// incident the hub raises, with names that need escaping, acked and muted.
func snapshotIncidents(now time.Time) map[string]*model.Incident {
	nasty := "a<b & c>"
	var lines []model.ProbeLine
	for i := range 20 {
		lines = append(lines, model.ProbeLine{Probe: fmt.Sprint(i), Name: fmt.Sprintf("probe-%d", i), Region: []string{"ru", "de", "nl"}[i%3],
			OK: i%3 == 0, Reason: "timeout", Error: "i/o timeout", LatencyMS: 12})
	}
	start := now.Add(-3 * time.Hour)
	return map[string]*model.Incident{
		"down": {Key: "web", Name: "Сайт " + nasty, Target: "https://example.com/", Kind: model.KindDown, Worst: model.KindDown, Severity: model.Critical,
			Lines: lines[:4], Started: start, Users: 120, Node: "de-node-1", AckBy: nasty, Affected: []string{"cfg-1", nasty}},
		"blocked": {Key: "vless", Name: "de-node-1 · Reality", Target: "VLESS · de1.example.com:443", Kind: model.KindBlocked, Worst: model.KindBlocked,
			Region: "ru", Severity: model.Critical, Lines: lines[:6], Started: start, MutedUntil: now.Add(time.Hour), MutedBy: "leo"},
		"partial": {Key: "big", Name: "big", Kind: model.KindPartial, Worst: model.KindPartial, Severity: model.Warning, Lines: lines, Started: start},
		"offline": {Key: "probe:p", Name: "Дом", Type: "probe", Kind: model.KindProbeOffline, Worst: model.KindProbeOffline, Severity: model.Warning,
			Lines: []model.ProbeLine{{Probe: "p", Name: "Дом", Region: "ru", Reason: "offline"}}, Started: start, Note: start.Format(time.RFC3339)},
		"nonet": {Key: "probe-net:p", Name: "Дом", Type: "probe", Kind: model.KindProbeNoNet, Worst: model.KindProbeNoNet, Severity: model.Warning,
			Lines: []model.ProbeLine{{Probe: "p", Name: "Дом", Error: "dial tcp: i/o timeout"}}, Started: start},
		"host": {Key: "host:p:cpu", Name: "Сервер", Type: "host", Kind: model.KindHost, Worst: model.KindHost, Severity: model.Warning,
			Host: &model.HostValue{Metric: "cpu", Value: 95, Peak: 99, Above: 90, For: 300}, Started: start},
		"users": {Key: "rw:u", Name: "pl-node-1", Type: "users", Kind: model.KindUsersDrop, Worst: model.KindUsersDrop, Severity: model.Critical,
			Lines: []model.ProbeLine{{Name: "pl-node-1", Region: "pl"}}, Drop: &model.UsersDrop{Before: 812, Now: 39, Low: 21}, Started: start},
		"nodedown": {Key: "rw:d", Name: "ee-node-1", Type: "users", Kind: model.KindNodeDown, Worst: model.KindNodeDown, Severity: model.Critical,
			Lines: []model.ProbeLine{{Name: "ee-node-1", Region: "ee", Reason: "node_down", Error: "Failed to get system stats"}},
			Drop:  &model.UsersDrop{Before: 472}, Started: start},
		"sub": {Key: "sub:canary", Name: "Подписка", Type: "subscription", Kind: model.KindDown, Worst: model.KindDown, Severity: model.Critical,
			Lines: []model.ProbeLine{{Name: "hub", Error: "HTTP 502"}}, Started: start},
	}
}

// TestSnapshotCurrentFormatter pins every card to its text. The snapshot was
// taken from the formatter before templates existed: the default templates
// must render exactly the same. UF_UPDATE_SNAPSHOT=1 rewrites it.
func TestSnapshotCurrentFormatter(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	for _, lang := range []string{"ru", "en"} {
		f := &Formatter{Lang: lang, Loc: time.UTC, PublicURL: "https://hub.example.com"}
		for name, inc := range snapshotIncidents(now) {
			r := *inc
			r.Resolved = now
			r2 := r
			r2.ResolvedBy = "leo"
			got := strings.Join([]string{
				"== Incident\n" + f.Incident(inc, now),
				"== RichIncident\n" + f.RichIncident(inc, now),
				"== EscalatedReply\n" + f.EscalatedReply(inc),
				"== RemindReply\n" + f.RemindReply(inc, now),
				"== Resolved\n" + f.Resolved(&r),
				"== RichResolved\n" + f.RichResolved(&r),
				"== RichResolved by hand\n" + f.RichResolved(&r2),
			}, "\n") + "\n"
			path := filepath.Join("testdata", "snapshot", lang+"-"+name+".txt")
			if os.Getenv("UF_UPDATE_SNAPSHOT") != "" {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				continue
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%s: %v (UF_UPDATE_SNAPSHOT=1 to create)", path, err)
			}
			if got != string(want) {
				t.Errorf("%s changed:\n--- want\n%s\n--- got\n%s", path, want, got)
			}
		}
	}
}
