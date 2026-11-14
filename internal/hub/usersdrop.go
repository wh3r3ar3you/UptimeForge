package hub

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/checks"
	"github.com/wh3r3ar3you/uptimeforge/internal/model"
)

// UsersDrop raises an incident when the users online on a Remnawave node
// collapse: a thousand people an hour ago and forty now, while the rest of
// the fleet is where it was, means the node is alive but clients no longer
// reach it, which is how a DPI ban looks.
type UsersDrop struct {
	Off      bool            `yaml:"off" json:"off,omitempty"`
	Percent  int             `yaml:"percent" json:"percent,omitempty"`     // drop that counts, default 80
	MinUsers int             `yaml:"min_users" json:"min_users,omitempty"` // smaller nodes are noise, default 30
	Window   checks.Duration `yaml:"window" json:"window,omitempty"`       // what "usual" is measured over, default 1h
}

// DropRule is the effective rule with defaults filled in.
func (r *Remnawave) DropRule() UsersDrop {
	d := UsersDrop{Percent: 80, MinUsers: 30, Window: checks.Duration(time.Hour)}
	if r == nil || r.Drop == nil {
		return d
	}
	o := *r.Drop
	if o.Percent == 0 {
		o.Percent = d.Percent
	}
	if o.MinUsers == 0 {
		o.MinUsers = d.MinUsers
	}
	if o.Window == 0 {
		o.Window = d.Window
	}
	return o
}

func validateDrop(d *UsersDrop) error {
	if d == nil || d.Off {
		return nil
	}
	if d.Percent != 0 && (d.Percent < 10 || d.Percent > 100) {
		return fmt.Errorf("remnawave.drop.percent must be between 10 and 100")
	}
	if d.MinUsers < 0 {
		return fmt.Errorf("remnawave.drop.min_users must not be negative")
	}
	if w := d.Window.D(); w != 0 && (w < 10*time.Minute || w > 6*time.Hour) {
		return fmt.Errorf("remnawave.drop.window must be between 10m and 6h")
	}
	return nil
}

// NodeUsers is one node as the panel reported it.
type NodeUsers struct {
	ID, Name, Country   string
	Users               int
	Connected, Disabled bool
	Status              string // the panel's last status message, why it lost the node
	Address             string // what clients connect to; checks on it depend on the node
	IPs                 []string
	Uptime              time.Duration // xray uptime, 0 when unknown
}

// UsersHistory is a stored poll, fed back after a restart.
type UsersHistory struct {
	Node  string
	At    time.Time
	Users int
}

type userSample struct {
	at    time.Time
	users int
}

type usersTrack struct {
	samples       []userSample
	lowRun, upRun int
	lowSince      time.Time // first poll of the current low run
	cliff         bool      // the low run began with a fall from a normal level in one poll
	downSince     time.Time // the panel lost the node at this poll; zero while connected
	downFrom      int       // users online before it was lost
	quiet         time.Time // users are not judged before this: clients are reconnecting
}

const (
	dropConfirm = 2                // polls in a row before an alert or a recovery
	dropFresh   = 90 * time.Second // the latest samples are what is judged, not the baseline

	// A ban drains a node over minutes as clients give up; a node emptied in
	// one poll is the panel losing it or xray restarting, and on a real node
	// such falls to zero lasted up to two minutes before the users were back.
	// So a cliff must hold this long before it counts as a ban.
	cliffHold = 5 * time.Minute
	// The panel lost the node: that is an outage, not a ban, and it is
	// reported as one once it lasts past a reboot. Agents count as offline
	// after 90 seconds, so on a server with an agent its alert comes first.
	nodeDownAfter = 3 * time.Minute
	// After a node is back or xray restarts, clients reconnect over minutes.
	reconnectQuiet = 5 * time.Minute
)

// median of the samples aged dropFresh..window+dropFresh at `at`; ok is
// false until history covers at least half the window. The median, not the
// mean or max, so a spike or a dip inside the window does not move it.
func median(samples []userSample, at time.Time, window time.Duration) (int, bool) {
	if len(samples) == 0 || at.Sub(samples[0].at) < window/2 {
		return 0, false
	}
	var vs []int
	for _, s := range samples {
		if age := at.Sub(s.at); age >= dropFresh && age <= window+dropFresh {
			vs = append(vs, s.users)
		}
	}
	if len(vs) < 3 {
		return 0, false
	}
	slices.Sort(vs)
	return vs[len(vs)/2], true
}

func trim(samples []userSample, at time.Time, window time.Duration) []userSample {
	cut := 0
	for cut < len(samples) && at.Sub(samples[cut].at) > window+dropFresh+time.Minute {
		cut++
	}
	return samples[cut:]
}

// SeedUsers loads stored polls, so a restarted hub has its baseline at once
// instead of going blind for half a window.
func (h *Hub) SeedUsers(history []UsersHistory) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.users == nil {
		h.users = map[string]*usersTrack{}
	}
	totals := map[time.Time]int{}
	for _, x := range history {
		t := h.users[x.Node]
		if t == nil {
			t = &usersTrack{}
			h.users[x.Node] = t
		}
		t.samples = append(t.samples, userSample{x.At, x.Users})
		totals[x.At] += x.Users
	}
	h.usersTotal = h.usersTotal[:0]
	for at, n := range totals {
		h.usersTotal = append(h.usersTotal, userSample{at, n})
	}
	slices.SortFunc(h.usersTotal, func(a, b userSample) int { return a.at.Compare(b.at) })
	for _, t := range h.users {
		slices.SortFunc(t.samples, func(a, b userSample) int { return a.at.Compare(b.at) })
	}
}

// ObserveUsers takes one poll of the panel. A failed poll must not be fed
// in: no data is not "zero users".
//
// A node counts as collapsed when, for two polls in a row, its users are
// Percent below its usual level (the median over Window) and its share of
// all users online fell as much. The share is what tells a ban from the
// evening: when everyone goes to sleep every node loses users, but each
// keeps its share; a banned node loses its share to the others.
func (h *Hub) ObserveUsers(at time.Time, nodes []NodeUsers) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg.Remnawave == nil {
		return
	}
	rule := h.cfg.Remnawave.DropRule()
	window := rule.Window.D()
	if h.users == nil {
		h.users = map[string]*usersTrack{}
	}
	total := 0
	for _, n := range nodes {
		if !n.Disabled {
			total += n.Users
		}
	}
	h.usersTotal = trim(append(h.usersTotal, userSample{at, total}), at, window)
	baseTotal, haveTotal := median(h.usersTotal, at, window)
	keep := float64(100-rule.Percent) / 100 // what is left after a drop that counts

	h.nodes, h.nodesSeen = h.nodes[:0], true
	for _, n := range nodes {
		if !n.Disabled {
			h.nodes = append(h.nodes, n)
		}
	}
	h.computeParents()
	seen := map[string]bool{}
	type drop struct {
		n         NodeUsers
		before    int
		relFell   bool // the node lost its share of the fleet, not just users
		fleetKnow bool
	}
	var dropping []drop
	var down []NodeUsers
	eligible := 0
	for _, n := range nodes {
		if n.Disabled {
			continue
		}
		seen[n.ID] = true
		t := h.users[n.ID]
		if t == nil {
			t = &usersTrack{}
			h.users[n.ID] = t
		}
		key := "rw:" + n.ID
		if !n.Connected {
			// Down, not banned. No sample either: no data is not zero users.
			if t.downSince.IsZero() {
				t.downSince, t.downFrom = at, usual(t.samples, at, window)
			}
			t.lowRun, t.upRun, t.quiet = 0, 0, at.Add(reconnectQuiet)
			if !rule.Off {
				down = append(down, n)
			}
			continue
		}
		if !t.downSince.IsZero() {
			// Back on the panel: a node_down closed by hand is over. Lifted here,
			// not by the users judged below, which a small or new node never is.
			h.recovered(key)
		}
		t.downSince = time.Time{}
		prev := -1
		if len(t.samples) > 0 {
			prev = t.samples[len(t.samples)-1].users
		}
		t.samples = trim(append(t.samples, userSample{at, n.Users}), at, window)
		if rule.Off {
			continue
		}
		inc := h.incidents[key]
		if inc != nil && inc.Kind == model.KindNodeDown {
			// Back on the panel: the outage is over. Users return over the
			// next minutes, which is not judged.
			t.lowRun, t.upRun = 0, 0
			h.transition(key, model.KindOK, "", []model.ProbeLine{h.nodeLine(n, true)}, meta{name: n.Name, typ: "users"}, at)
			continue
		}
		if inc != nil {
			before := 0
			if inc.Drop != nil {
				before = inc.Drop.Before
			}
			if n.Users*2 >= before {
				t.upRun++
			} else {
				t.upRun = 0
			}
			d := &model.UsersDrop{Before: before, Now: n.Users, Connected: n.Connected}
			if t.upRun >= dropConfirm {
				t.upRun, t.lowRun = 0, 0
				h.transition(key, model.KindOK, "", []model.ProbeLine{h.nodeLine(n, true)}, meta{name: n.Name, typ: "users", drop: d}, at)
			} else {
				h.transition(key, model.KindUsersDrop, "", []model.ProbeLine{h.nodeLine(n, false)}, h.dropMeta(n, d, inc.Note), at)
			}
			continue
		}
		if at.Before(t.quiet) || (n.Uptime > 0 && n.Uptime < reconnectQuiet) {
			t.lowRun = 0 // just back or xray just restarted: clients are reconnecting
			continue
		}
		base, ok := median(t.samples, at, window)
		if !ok || base < rule.MinUsers {
			t.lowRun = 0
			continue
		}
		eligible++
		if float64(n.Users) <= float64(base)*keep {
			if t.lowRun == 0 {
				t.lowSince, t.cliff = at, prev*2 >= base
			}
			t.lowRun++
		} else {
			t.lowRun = 0
			if n.Users*2 >= base {
				h.recovered(key) // back to at least half of usual: a manual close is lifted
			}
		}
		if t.lowRun >= dropConfirm && (!t.cliff || at.Sub(t.lowSince) >= cliffHold) {
			// A node holding half the fleet cannot lose much share: judge it alone.
			d := drop{n: n, before: base, fleetKnow: haveTotal && total > 0 && base*2 < baseTotal}
			if d.fleetKnow {
				shareNow, shareBefore := float64(n.Users)/float64(total), float64(base)/float64(baseTotal)
				d.relFell = shareNow <= shareBefore*keep
			}
			dropping = append(dropping, d)
		}
	}
	// Most nodes at once is not a ban of one node: say so rather than guess.
	mass := len(dropping) >= 3 && len(dropping)*2 >= eligible
	for _, d := range dropping {
		note := ""
		switch {
		case mass:
			note = "mass"
		case d.fleetKnow && !d.relFell:
			continue // the whole fleet fell with it: the evening, not a ban
		}
		t := h.users[d.n.ID]
		t.lowRun, t.upRun = 0, 0
		ud := &model.UsersDrop{Before: d.before, Now: d.n.Users, Connected: d.n.Connected}
		h.transition("rw:"+d.n.ID, model.KindUsersDrop, "", []model.ProbeLine{h.nodeLine(d.n, false)}, h.dropMeta(d.n, ud, note), at)
	}
	// The panel losing most nodes at once is the panel or its network.
	massDown := len(down) >= 3 && len(down)*2 >= len(seen)
	for _, n := range down {
		key, t := "rw:"+n.ID, h.users[n.ID]
		inc := h.incidents[key]
		if inc == nil && (at.Sub(t.downSince) < nodeDownAfter || h.agentDown(n, t.downSince)) {
			continue
		}
		before := t.downFrom
		if inc != nil && inc.Drop != nil {
			before = max(before, inc.Drop.Before)
		}
		note := ""
		if massDown {
			note = "mass"
		}
		l := model.ProbeLine{Probe: n.ID, Name: n.Name, Region: strings.ToLower(n.Country), Reason: "node_down", Error: clip(n.Status, 200)}
		h.transition(key, model.KindNodeDown, "", []model.ProbeLine{l},
			meta{name: n.Name, typ: "users", severity: "critical", note: note, drop: &model.UsersDrop{Before: before}}, at)
	}
	for id := range h.users {
		if !seen[id] {
			delete(h.users, id)
		}
	}
	for key, inc := range h.incidents { // nodes deleted or disabled in the panel
		if id, ok := strings.CutPrefix(key, "rw:"); ok && !seen[id] {
			inc.Note = "removed"
			h.transition(key, model.KindOK, "", nil, meta{}, at)
		}
	}
	h.forgetClosed(func(key string) bool {
		id, ok := strings.CutPrefix(key, "rw:")
		return ok && !seen[id]
	})
}

// usual is the node's users online before now: the median when there is
// enough history, else the last poll.
func usual(samples []userSample, at time.Time, window time.Duration) int {
	if m, ok := median(samples, at, window); ok {
		return m
	}
	if len(samples) > 0 {
		return samples[len(samples)-1].users
	}
	return 0
}

// agentAlertWindow is how much earlier than the panel losing the node the
// agent's offline alert may have opened and still be about the same outage.
const agentAlertWindow = 10 * time.Minute

// agentDown says an agent on the node's server is offline and its alert
// already says the server is down: open, announced, not muted, and opened
// around when the panel lost the node (lostAt). Or the agent dropped just
// now and its alert is still within its grace. An agent closed by hand,
// muted, or offline since long before (never connected, say) says nothing
// about this outage, so node_down does not yield to it. Probes and panel
// nodes are matched by IsNodeProbe.
func (h *Hub) agentDown(n NodeUsers, lostAt time.Time) bool {
	now := time.Now()
	for id, p := range h.probes {
		if p.online || !IsNodeProbe(ProbeRef{Link: p.cfg.RemnawaveNode, ID: id, Name: p.cfg.Name, Host: p.hello.Hostname, Addr: p.addr}, n.Ref()) {
			continue
		}
		key := "probe:" + id
		inc := h.incidents[key]
		if inc == nil {
			if !h.closedByHand(key) && now.Sub(p.since) <= h.offlineAfter(p) {
				return true // its alert is on the way
			}
			continue
		}
		if inc.Notified && !now.Before(inc.MutedUntil) && !inc.Started.Before(lostAt.Add(-agentAlertWindow)) {
			return true
		}
	}
	return false
}

func nodeKey(s string) string {
	s, _, _ = strings.Cut(s, "(")
	return strings.ToLower(strings.TrimSpace(s))
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func (h *Hub) nodeLine(n NodeUsers, ok bool) model.ProbeLine {
	l := model.ProbeLine{Probe: n.ID, Name: n.Name, Region: strings.ToLower(n.Country), OK: ok}
	if !ok {
		l.Reason = "users_drop"
	}
	return l
}

func (h *Hub) dropMeta(n NodeUsers, d *model.UsersDrop, note string) meta {
	return meta{name: n.Name, typ: "users", severity: "critical", note: note, drop: d}
}

// mergeDrop keeps the baseline and the lowest count across updates.
func mergeDrop(old, cur *model.UsersDrop) *model.UsersDrop {
	if cur == nil {
		return old
	}
	v := *cur
	v.Low = v.Now
	if old != nil {
		if old.Before > 0 {
			v.Before = old.Before
		}
		if old.Low < v.Low {
			v.Low = old.Low
		}
	}
	return &v
}
