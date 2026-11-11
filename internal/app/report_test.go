package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/hub"
	"github.com/wh3r3ar3you/uptimeforge/internal/model"
	"github.com/wh3r3ar3you/uptimeforge/internal/store/pgtest"
)

func TestNextReport(t *testing.T) {
	tb, _ := time.LoadLocation("Asia/Tbilisi")
	fri := time.Date(2026, 11, 13, 12, 0, 0, 0, tb)
	if got := nextReport(fri, time.Sunday, 20, 0, tb); !got.Equal(time.Date(2026, 11, 15, 20, 0, 0, 0, tb)) {
		t.Fatalf("from Friday: %v", got)
	}
	sun := time.Date(2026, 11, 15, 20, 0, 1, 0, tb)
	if got := nextReport(sun, time.Sunday, 20, 0, tb); !got.Equal(time.Date(2026, 11, 22, 20, 0, 0, 0, tb)) {
		t.Fatalf("just after: %v", got)
	}
	// The same instant seen from UTC lands on the hub's Sunday evening.
	if got := nextReport(fri.UTC(), time.Sunday, 20, 0, tb); !got.Equal(time.Date(2026, 11, 15, 20, 0, 0, 0, tb)) {
		t.Fatalf("from UTC: %v", got)
	}
}

// A hub restarted at 20:05 on Sunday sends that week's report once; a
// restart at 20:06 does not send it again; a day late it is skipped.
func TestReportDueCatchUp(t *testing.T) {
	a := newApp(t, Options{DatabaseURL: pgtest.URL(t), DataDir: t.TempDir()})
	defer a.Store.Close()
	ctx := context.Background()
	loc := a.Hub.Location()
	sun := time.Date(2026, 11, 15, 20, 5, 0, 0, loc)
	if due, _, _ := a.reportDue(ctx, sun); due {
		t.Fatal("a new hub does not send a report on its first day")
	}
	a.markReport(ctx, "2026-W45") // as if last week's went out
	due, week, err := a.reportDue(ctx, sun)
	if err != nil || !due || week != "2026-W46" {
		t.Fatalf("20:05: %v %q %v", due, week, err)
	}
	if err := a.markReport(ctx, week); err != nil {
		t.Fatal(err)
	}
	if due, _, _ := a.reportDue(ctx, sun.Add(time.Minute)); due {
		t.Fatal("sent twice")
	}
	if due, _, _ := a.reportDue(ctx, time.Date(2026, 11, 23, 21, 0, 0, 0, loc)); due {
		t.Fatal("a report more than a day late is skipped")
	}
}

// The report counts what happened in the week: node outages, bans, the
// worst checks and the incidents.
func TestBuildReport(t *testing.T) {
	a := newApp(t, Options{DatabaseURL: pgtest.URL(t), DataDir: t.TempDir()})
	defer a.Store.Close()
	ctx := context.Background()
	to := time.Now().Truncate(time.Hour)
	at := to.Add(-48 * time.Hour)
	save := func(id, key, name string, kind model.Kind, start time.Time, d time.Duration) {
		inc := model.Incident{ID: id, Key: key, Name: name, Kind: kind, Worst: kind, Severity: model.Critical, Started: start, Resolved: start.Add(d), Notified: true}
		if err := a.Store.SaveIncident(ctx, inc); err != nil {
			t.Fatal(err)
		}
	}
	save("1", "rw:de", "de-node", model.KindNodeDown, at, 30*time.Minute)
	save("2", "rw:de", "de-node", model.KindNodeDown, at.Add(5*time.Hour), 30*time.Minute)
	save("3", "rw:pl", "pl", model.KindUsersDrop, at, 45*time.Minute)
	for i := range 3 {
		save(string(rune('a'+i)), "cab", "cab", model.KindDown, at.Add(time.Duration(i)*time.Hour), 10*time.Minute)
	}
	r, err := a.BuildReport(ctx, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Nodes) == 0 || r.Nodes[0].Name != "de-node" || r.Nodes[0].Down != time.Hour {
		t.Fatalf("nodes: %+v", r.Nodes)
	}
	if len(r.Bans) != 1 || r.Bans[0].Name != "pl" || r.Bans[0].Count != 1 {
		t.Fatalf("bans: %+v", r.Bans)
	}
	if len(r.Worst) == 0 || r.Worst[0].Name != "cab" || r.Worst[0].Down != 30*time.Minute {
		t.Fatalf("worst: %+v", r.Worst)
	}
	if r.Opened != 6 || r.Resolved != 6 {
		t.Fatalf("incidents: opened %d resolved %d", r.Opened, r.Resolved)
	}
}

// With a Remnawave panel the report's traffic is the users' traffic the
// panel counted, not the servers' rx+tx: a relay counts every byte twice
// and a chain of relays more.
func TestReportTrafficFromPanel(t *testing.T) {
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/bandwidth-stats/nodes/" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"response":{"categories":["a","b","c"],"sparklineData":[1000000000000,1000000000000,1000000000000],"topNodes":[],"series":[]}}`))
	}))
	defer panel.Close()
	a := newApp(t, Options{DatabaseURL: pgtest.URL(t), DataDir: t.TempDir()})
	defer a.Store.Close()
	ctx := context.Background()
	if err := a.SaveSettings(ctx, hub.Config{Remnawave: &hub.Remnawave{URL: panel.URL, Token: "t"}}); err != nil {
		t.Fatal(err)
	}
	r, err := a.BuildReport(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if r.Traffic != 3_000_000_000_000 || !r.TrafficUsers {
		t.Fatalf("traffic %d, users %v", r.Traffic, r.TrafficUsers)
	}
}

// The report counts what people heard about: an incident that was held and
// never announced is not "opened", and closing one by hand is not a recovery
// time. The online peak is the largest total of one poll, not of averages.
func TestReportCountsWhatWasSent(t *testing.T) {
	a := newApp(t, Options{DatabaseURL: pgtest.URL(t), DataDir: t.TempDir()})
	defer a.Store.Close()
	ctx := context.Background()
	to := time.Now().Truncate(time.Hour)
	at := to.Add(-24 * time.Hour)
	for _, inc := range []model.Incident{
		{ID: "1", Key: "a", Name: "a", Kind: model.KindDown, Started: at, Resolved: at.Add(time.Hour), Notified: true},
		{ID: "2", Key: "b", Name: "b", Kind: model.KindDown, Started: at, Resolved: at.Add(5 * time.Hour), Notified: true, ResolvedBy: "leo"},
		{ID: "3", Key: "c", Name: "c", Kind: model.KindDown, Started: at, Resolved: at.Add(time.Minute)}, // held, recovered quietly
	} {
		inc.Worst, inc.Severity = inc.Kind, model.Critical
		if err := a.Store.SaveIncident(ctx, inc); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Store.WriteNodeUsers(ctx, at.Add(10*time.Minute), map[string]int{"de": 100, "nl": 50}); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.WriteNodeUsers(ctx, at.Add(20*time.Minute), map[string]int{"de": 10, "nl": 200}); err != nil {
		t.Fatal(err)
	}
	r, err := a.BuildReport(ctx, to)
	if err != nil {
		t.Fatal(err)
	}
	if r.Opened != 2 || r.Resolved != 2 || r.ByHand != 1 || r.MTTR != time.Hour {
		t.Fatalf("opened %d resolved %d by hand %d mttr %v", r.Opened, r.Resolved, r.ByHand, r.MTTR)
	}
	if r.UsersPeak != 210 || r.UsersAvg != 180 {
		t.Fatalf("online: peak %d avg %d", r.UsersPeak, r.UsersAvg)
	}
}
