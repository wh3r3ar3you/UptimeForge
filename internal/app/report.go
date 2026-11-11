package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/model"
	"github.com/wh3r3ar3you/uptimeforge/internal/notify"
	"github.com/wh3r3ar3you/uptimeforge/internal/remnawave"
	"github.com/wh3r3ar3you/uptimeforge/internal/store"
)

const (
	reportWeek    = 7 * 24 * time.Hour
	reportLate    = 24 * time.Hour // a report later than this is skipped, not sent
	reportSetting = "report_last"  // ISO week of the last report sent (or skipped)
)

// nextReport is the first scheduled time strictly after now, in loc.
func nextReport(now time.Time, day time.Weekday, hh, mm int, loc *time.Location) time.Time {
	n := now.In(loc)
	t := time.Date(n.Year(), n.Month(), n.Day(), hh, mm, 0, 0, loc).AddDate(0, 0, int(day-n.Weekday()+7)%7)
	if !t.After(n) {
		t = t.AddDate(0, 0, 7)
	}
	return t
}

func isoWeek(t time.Time) string {
	y, w := t.ISOWeek()
	return fmt.Sprintf("%d-W%02d", y, w)
}

// reportDue says whether this week's report should go out now: its time has
// come, it was not sent yet, and it is less than a day late. A report missed
// by more than a day is marked as done and skipped.
func (a *App) reportDue(ctx context.Context, now time.Time) (bool, string, error) {
	off, day, hh, mm := a.Hub.Config().Alerts.Report.Schedule()
	if off {
		return false, "", nil
	}
	last := nextReport(now, day, hh, mm, a.Hub.Location()).AddDate(0, 0, -7)
	week := isoWeek(last)
	var sent string
	found, err := a.Store.GetSetting(ctx, reportSetting, &sent)
	if err != nil {
		return false, week, err
	}
	if !found { // a new hub starts counting from this week, not with a report on its first day
		return false, week, a.markReport(ctx, week)
	}
	if sent == week {
		return false, week, nil
	}
	if now.Sub(last) >= reportLate {
		a.Log.Info("weekly report skipped: the hub was down when it was due", "week", week)
		return false, week, a.markReport(ctx, week)
	}
	return true, week, nil
}

func (a *App) markReport(ctx context.Context, week string) error {
	return a.Store.PutSetting(ctx, reportSetting, week)
}

// overlap is how much of [from, to) an incident covered.
func overlap(inc model.Incident, from, to time.Time) time.Duration {
	end := inc.Resolved
	if end.IsZero() || end.After(to) {
		end = to
	}
	start := inc.Started
	if start.Before(from) {
		start = from
	}
	return max(0, end.Sub(start))
}

func downtimes(m map[string]*notify.Downtime) []notify.Downtime {
	out := make([]notify.Downtime, 0, len(m))
	for _, d := range m {
		out = append(out, *d)
	}
	slices.SortFunc(out, func(a, b notify.Downtime) int {
		return cmp.Or(cmp.Compare(b.Down, a.Down), strings.Compare(a.Name, b.Name))
	})
	return out
}

// uptimes are the mean uptime of the checks over [from, to) by what was
// alerted, overall and per group; -1 when there are no checks.
func (a *App) uptimes(ctx context.Context, from, to time.Time) (float64, []notify.GroupUptime, error) {
	snap := a.Hub.Snapshot()
	if len(snap.Checks) == 0 {
		return -1, nil, nil
	}
	incs, err := a.Store.Incidents(ctx, store.IncidentFilter{From: from, To: to})
	if err != nil {
		return 0, nil, err
	}
	down := map[string]time.Duration{}
	for _, inc := range incs {
		if cmp.Or(inc.Worst, inc.Kind) != model.KindPartial {
			down[inc.Key] += overlap(inc, from, to)
		}
	}
	span := to.Sub(from)
	var total float64
	groups := map[string][]float64{}
	for _, c := range snap.Checks {
		u := max(0, 1-down[c.ID].Seconds()/span.Seconds())
		total += u
		g := cmp.Or(c.Group, "—")
		groups[g] = append(groups[g], u)
	}
	var out []notify.GroupUptime
	for name, us := range groups {
		var s float64
		for _, u := range us {
			s += u
		}
		out = append(out, notify.GroupUptime{Name: name, Uptime: s / float64(len(us))})
	}
	slices.SortFunc(out, func(a, b notify.GroupUptime) int { return strings.Compare(a.Name, b.Name) })
	return total / float64(len(snap.Checks)), out, nil
}

// usersOnline is the peak and the average of all users online over
// [from, to), each poll's total across nodes.
func (a *App) usersOnline(ctx context.Context, from, to time.Time) (peak, avg int, err error) {
	return a.Store.UsersOnline(ctx, from, to)
}

// BuildReport is the week before to: uptime and its change, the worst
// checks, nodes down, bans, incidents, users online and traffic.
func (a *App) BuildReport(ctx context.Context, to time.Time) (notify.Report, error) {
	from := to.Add(-reportWeek)
	r := notify.Report{From: from, To: to}
	var err error
	if r.Uptime, r.Groups, err = a.uptimes(ctx, from, to); err != nil {
		return r, err
	}
	if r.UptimePrev, _, err = a.uptimes(ctx, from.Add(-reportWeek), from); err != nil {
		return r, err
	}
	incs, err := a.Store.Incidents(ctx, store.IncidentFilter{From: from, To: to})
	if err != nil {
		return r, err
	}
	worst, nodes, bans := map[string]*notify.Downtime{}, map[string]*notify.Downtime{}, map[string]*notify.Downtime{}
	add := func(m map[string]*notify.Downtime, name string, d time.Duration) {
		x := m[name]
		if x == nil {
			x = &notify.Downtime{Name: name}
			m[name] = x
		}
		x.Down += d
		x.Count++
	}
	var mttr time.Duration
	var recovered int
	for _, inc := range incs {
		if inc.CauseKey != "" {
			continue // folded into its parent, never announced
		}
		d, kind := overlap(inc, from, to), cmp.Or(inc.Worst, inc.Kind)
		switch {
		case kind == model.KindNodeDown, kind == model.KindProbeOffline:
			add(nodes, inc.Name, d)
		case kind == model.KindUsersDrop:
			add(bans, inc.Name, d)
		case strings.HasPrefix(inc.Key, "host:"), strings.HasPrefix(inc.Key, "probe-net:"), kind == model.KindPartial:
		default:
			add(worst, inc.Name, d)
		}
		if !inc.Notified {
			continue // held or settled and never announced: nobody heard of it
		}
		if !inc.Started.Before(from) {
			r.Opened++
		}
		if !inc.Resolved.IsZero() && !inc.Resolved.Before(from) && inc.Resolved.Before(to) {
			r.Resolved++
			if inc.ResolvedBy != "" {
				r.ByHand++ // closed by a person, not recovered: no recovery time
			} else {
				mttr += inc.Resolved.Sub(inc.Started)
				recovered++
			}
		}
	}
	if recovered > 0 {
		r.MTTR = mttr / time.Duration(recovered)
	}
	r.Worst, r.Nodes, r.Bans = downtimes(worst), downtimes(nodes), downtimes(bans)
	if r.UsersPeak, r.UsersAvg, err = a.usersOnline(ctx, from, to); err != nil {
		return r, err
	}
	if _, r.UsersAvgPrev, err = a.usersOnline(ctx, from.Add(-reportWeek), from); err != nil {
		return r, err
	}
	r.Traffic, r.TrafficUsers, err = a.weekTraffic(ctx, from, to)
	return r, err
}

// weekTraffic is the users' traffic as the Remnawave panel counted it, by
// UTC day, when a panel is set and answers. Otherwise it is the servers'
// traffic, each server's larger direction: a relay sends out what it takes
// in, so rx+tx would count every byte twice.
func (a *App) weekTraffic(ctx context.Context, from, to time.Time) (uint64, bool, error) {
	if rw := a.Hub.Config().Remnawave; rw != nil {
		c := remnawave.New(rw.URL, rw.Token)
		c.Headers = rw.Headers
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		u, err := c.NodesUsage(ctx, from.AddDate(0, 0, 1), to, 1)
		cancel()
		if err == nil {
			var sum int64
			for _, b := range u.SparklineData {
				sum += max(0, b)
			}
			return uint64(sum), true, nil
		}
		a.Log.Warn("weekly report: panel traffic unavailable, using the servers'", "err", err)
	}
	traffic, err := a.Store.TrafficSince(ctx, from)
	if err != nil {
		return 0, false, err
	}
	var sum float64
	for _, t := range traffic {
		sum += max(0, t.RX, t.TX)
	}
	return uint64(sum), false, nil
}

// reportFormatter renders with the hub's language, zone and templates.
func (a *App) reportFormatter() *notify.Formatter {
	cfg := a.Hub.Config()
	return &notify.Formatter{Lang: cfg.Lang, Loc: a.Hub.Location(), PublicURL: cfg.PublicURL, Custom: templates(cfg.Templates),
		OnError: func(err error) { a.Log.Warn("notification template failed, the default was sent", "err", err) }}
}

// ReportPreview is the report as it would go out now.
func (a *App) ReportPreview(ctx context.Context) (rich string, err error) {
	r, err := a.BuildReport(ctx, time.Now())
	if err != nil {
		return "", err
	}
	return a.reportFormatter().RichReport(r), nil
}

// SendReport builds this week's report and posts it to Telegram.
func (a *App) SendReport(ctx context.Context) error {
	a.mu.Lock()
	tg := a.tg
	a.mu.Unlock()
	if tg == nil {
		return errors.New("telegram is not configured")
	}
	rich, err := a.ReportPreview(ctx)
	if err != nil {
		return err
	}
	return tg.SendReport(ctx, rich, notify.RichToHTML(rich))
}

// reportLoop sends the weekly report when it is due, checking at least
// hourly so a changed schedule or clock is picked up.
func (a *App) reportLoop(ctx context.Context) {
	for {
		now := time.Now()
		if due, week, err := a.reportDue(ctx, now); err != nil {
			a.Log.Warn("weekly report", "err", err)
		} else if due {
			if err := a.SendReport(ctx); err != nil {
				a.Log.Warn("weekly report not sent", "err", err)
			} else if err := a.markReport(ctx, week); err != nil {
				a.Log.Warn("weekly report", "err", err)
			}
		}
		_, day, hh, mm := a.Hub.Config().Alerts.Report.Schedule()
		wait := min(time.Until(nextReport(now, day, hh, mm, a.Hub.Location())), time.Hour)
		select {
		case <-ctx.Done():
			return
		case <-time.After(max(wait, time.Second)):
		}
	}
}
