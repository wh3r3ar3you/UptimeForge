package app

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/hub"
	"github.com/wh3r3ar3you/uptimeforge/internal/model"
	"github.com/wh3r3ar3you/uptimeforge/internal/remnawave"
)

// RemnawaveView is the cached panel state shown in the UI.
type RemnawaveView struct {
	Configured bool                  `json:"configured"`
	Fetched    time.Time             `json:"fetched,omitzero"`
	Error      string                `json:"error,omitempty"`
	Stats      *remnawave.Stats      `json:"stats,omitempty"`
	Nodes      []remnawave.Node      `json:"nodes,omitempty"`
	Usage      *remnawave.NodesUsage `json:"usage,omitempty"`
}

type remnawaveState struct {
	view    RemnawaveView
	usageAt time.Time
	known   map[string]remnawave.Node // nodes seen on the panel, by UUID
	gone    map[string]int            // polls in a row a known node was missing
	trusted []remnawave.Node          // the last answer that was not a mass disappearance
	mass    bool                      // the current answer lost most of trusted
}

// pruneAfter is how many polls in a row a node must be missing from the
// panel before it counts as deleted: a node list caught mid-edit is not a
// deletion.
const pruneAfter = 3

// nodeDeletedTag marks checks paused because their node left the panel; they
// run again if a node with the same name or address comes back.
const nodeDeletedTag = "node-deleted"

func (a *App) Remnawave() RemnawaveView {
	a.mu.Lock()
	defer a.mu.Unlock()
	v := a.rw.view
	v.Configured = a.Hub.Config().Remnawave != nil
	return v
}

func (a *App) pollRemnawave(ctx context.Context) {
	a.seedUsers(ctx)
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		a.refreshRemnawave(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (a *App) refreshRemnawave(ctx context.Context) {
	cfg := a.Hub.Config()
	if cfg.Remnawave == nil {
		a.mu.Lock()
		a.rw = remnawaveState{}
		a.mu.Unlock()
		a.nodes.Store(nil)
		return
	}
	c := remnawave.New(cfg.Remnawave.URL, cfg.Remnawave.Token)
	c.Headers, c.TZ = cfg.Remnawave.Headers, cfg.Timezone
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// The node list alone drives node alerts; the home-page numbers are
	// extras, and a failing one must not stop the alerts.
	nodes, err := c.Nodes(ctx)
	var stats *remnawave.Stats
	var statsErr error
	if err == nil {
		stats, statsErr = c.PanelStats(ctx, nodes)
	}
	// The 7-day per-node traffic chart changes slowly.
	a.mu.Lock()
	needUsage := time.Since(a.rw.usageAt) > 10*time.Minute
	a.mu.Unlock()
	var usage *remnawave.NodesUsage
	if err == nil && needUsage {
		end := time.Now()
		usage, _ = c.NodesUsage(ctx, end.AddDate(0, 0, -7), end, 10)
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.rw.view.Fetched = time.Now()
	if err != nil {
		a.rw.view.Error = err.Error()
		return
	}
	a.rw.view.Error, a.rw.view.Nodes = "", nodes
	if statsErr == nil {
		a.rw.view.Stats = stats
	} else { // keep the last good numbers, with today's node counts
		a.rw.view.Error = "panel stats: " + statsErr.Error()
		if old := a.rw.view.Stats; old != nil {
			s := *old
			s.SetNodes(nodes)
			a.rw.view.Stats = &s
		}
	}
	a.nodes.Store(&nodes)
	deleted := a.trackDeleted(nodes)
	if !cfg.Remnawave.Prune {
		deleted = nil
	}
	users := make([]hub.NodeUsers, 0, len(nodes))
	counts := map[string]int{}
	for _, n := range nodes {
		status := ""
		if n.LastStatusMessage != nil {
			status = *n.LastStatusMessage
		}
		users = append(users, hub.NodeUsers{ID: n.UUID, Name: n.Name, Country: n.CountryCode, Users: n.UsersOnline,
			Connected: n.IsConnected, Disabled: n.IsDisabled, Status: status, Uptime: time.Duration(n.XrayUptime * float64(time.Second)),
			Address: n.Address, IPs: nodeIPs(n)})
		if !n.IsDisabled {
			counts[n.UUID] = n.UsersOnline
		}
	}
	at := a.rw.view.Fetched
	go func() { // outside a.mu: the hub and the store take their own locks
		a.Hub.ObserveUsers(at, users)
		wctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.Store.WriteNodeUsers(wctx, at, counts); err != nil {
			a.Log.Warn("store users online", "err", err)
		}
		if len(deleted) > 0 {
			a.prune(context.Background(), deleted)
		}
		a.restoreNodes(context.Background(), nodes)
	}()
	if usage != nil {
		a.rw.view.Usage, a.rw.usageAt = usage, time.Now()
	}
}

// trackDeleted returns the nodes that were on the panel and have now been
// missing for pruneAfter polls. Caller holds a.mu.
//
// Not a deletion: an empty list; an answer that lost most of the last
// trusted one (a broken answer or another panel: nothing is counted until
// an answer looks like the fleet again); and a node whose name or address
// is on the panel under a new id (re-created, or the panel reinstalled):
// it is forgotten, never pruned.
func (a *App) trackDeleted(nodes []remnawave.Node) []remnawave.Node {
	if len(nodes) == 0 {
		return nil
	}
	cur := make(map[string]remnawave.Node, len(nodes))
	for _, n := range nodes {
		cur[n.UUID] = n
	}
	if a.rw.gone == nil {
		a.rw.gone = map[string]int{}
	}
	missing := 0
	for _, n := range a.rw.trusted {
		if _, ok := cur[n.UUID]; !ok && !replaced(n, nodes) {
			missing++
		}
	}
	mass := missing*2 > len(a.rw.trusted)
	if mass && !a.rw.mass {
		a.logf("remnawave: most nodes vanished from the panel at once; not counting them as deleted",
			"missing", missing, "of", len(a.rw.trusted))
	}
	a.rw.mass = mass
	if !mass {
		a.rw.trusted = nodes
	}
	var out []remnawave.Node
	for id, n := range a.rw.known {
		if _, ok := cur[id]; ok {
			delete(a.rw.gone, id)
			continue
		}
		if replaced(n, nodes) {
			delete(a.rw.gone, id)
			continue
		}
		if !mass {
			a.rw.gone[id]++
		}
		if a.rw.gone[id] >= pruneAfter {
			delete(a.rw.gone, id)
			out = append(out, n)
			continue
		}
		cur[id] = n // still known until it has been gone long enough
	}
	a.rw.known = cur
	return out
}

func (a *App) logf(msg string, args ...any) {
	if a.Log != nil {
		a.Log.Warn(msg, args...)
	}
}

// replaced: a node with old's name or address is on the panel under another id.
func replaced(old remnawave.Node, nodes []remnawave.Node) bool {
	for _, n := range nodes {
		if n.UUID == old.UUID {
			continue
		}
		if k := nodeKey(old.Name); k != "" && k == nodeKey(n.Name) {
			return true
		}
		if old.Address != "" && strings.EqualFold(old.Address, n.Address) {
			return true
		}
	}
	return false
}

// prune pauses the checks of nodes deleted from the panel and tags them with
// nodeDeletedTag; restoreNodes resumes them if the node comes back. Nothing
// is deleted: an agent's token, history and settings cannot be brought
// back, and a wrong prune must be cheap to undo. Servers are left as they
// are. Objects from config.yaml are left alone.
func (a *App) prune(ctx context.Context, nodes []remnawave.Node) {
	for _, n := range nodes {
		var paused, servers []string
		for _, c := range a.Checks() {
			if c.Managed || c.Paused || !sameNode(n, c.ID, c.Name, targetHost(c.Spec.Target())) {
				continue
			}
			if err := a.setNodeDeleted(ctx, c.ID, true); err != nil {
				a.Log.Warn("remnawave prune: check", "node", n.Name, "check", c.ID, "err", err)
				continue
			}
			paused = append(paused, c.ID)
		}
		for _, p := range a.Probes() {
			if !p.Managed && p.ID != "hub" && sameNode(n, p.ID, p.Name, "") {
				servers = append(servers, p.ID)
			}
		}
		a.Log.Info("node deleted from the panel: its checks are paused", "node", n.Name,
			"paused", strings.Join(paused, ", "), "servers_kept", strings.Join(servers, ", "))
	}
}

// restoreNodes resumes checks paused by prune whose node is on the panel again.
func (a *App) restoreNodes(ctx context.Context, nodes []remnawave.Node) {
	for _, c := range a.Checks() {
		if c.Managed || !c.Paused || !slices.Contains(c.Tags, nodeDeletedTag) {
			continue
		}
		for _, n := range nodes {
			if !sameNode(n, c.ID, c.Name, targetHost(c.Spec.Target())) {
				continue
			}
			if err := a.setNodeDeleted(ctx, c.ID, false); err != nil {
				a.Log.Warn("remnawave: resume check", "node", n.Name, "check", c.ID, "err", err)
			} else {
				a.Log.Info("node is back on the panel: check resumed", "node", n.Name, "check", c.ID)
			}
			break
		}
	}
}

// setNodeDeleted pauses a check with the nodeDeletedTag note, or resumes it
// and drops the note.
func (a *App) setNodeDeleted(ctx context.Context, id string, deleted bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.managed.checks[id] {
		return ErrManaged
	}
	var c hub.CheckConfig
	if err := a.Store.GetObject(ctx, kindCheck, id, &c); err != nil {
		return err
	}
	c.Paused = deleted
	c.Tags = slices.DeleteFunc(c.Tags, func(t string) bool { return t == nodeDeletedTag })
	if deleted && len(c.Tags) < 16 { // hub's tag limit; a full list goes without the note
		c.Tags = append(c.Tags, nodeDeletedTag)
	}
	if err := a.Store.PutObject(ctx, kindCheck, id, c); err != nil {
		return err
	}
	return a.reload(ctx)
}

// sameNode says an object belongs to a panel node: the same name or id
// (ignoring case and a "(comment)" suffix), or a target on its address.
func sameNode(n remnawave.Node, id, name, host string) bool {
	if k := nodeKey(n.Name); k != "" && (k == nodeKey(id) || k == nodeKey(name)) {
		return true
	}
	if host == "" {
		return false
	}
	if strings.EqualFold(host, n.Address) {
		return true
	}
	for _, ip := range n.IPs {
		if ip.IP == host {
			return true
		}
	}
	return false
}

// nodeKey is a node name compared: case and a "(comment)" suffix ignored.
func nodeKey(s string) string {
	s, _, _ = strings.Cut(s, "(")
	return strings.ToLower(strings.TrimSpace(s))
}

// nodeSnapshot is read by the hub's enricher without taking app locks.
type nodeSnapshot = atomic.Pointer[[]remnawave.Node]

// enrichIncident attaches the Remnawave node behind a check and its users
// online. Matching is by the address the check connects to.
func (a *App) enrichIncident(inc *model.Incident) {
	nodes := a.nodes.Load()
	if nodes == nil || inc.Target == "" {
		return
	}
	host := targetHost(inc.Target)
	for _, n := range *nodes {
		match := strings.EqualFold(n.Address, host) || strings.EqualFold(n.Name, inc.Name)
		for _, ip := range n.IPs {
			match = match || ip.IP == host
		}
		if !match {
			continue
		}
		inc.Node = n.Name
		inc.Users = max(inc.Users, n.UsersOnline)
		return
	}
}

func targetHost(t string) string { return hub.HostOf(t) }

func nodeIPs(n remnawave.Node) []string {
	var out []string
	for _, ip := range n.IPs {
		out = append(out, ip.IP)
	}
	return out
}

// RemnawaveCheck is what a connection test found.
type RemnawaveCheck struct {
	Nodes  int `json:"nodes"`
	Online int `json:"online"` // nodes connected to the panel
	Users  int `json:"users"`  // users online across nodes
}

// TestRemnawave tries a panel before it is saved. An empty or masked token
// (or masked header) means the saved one, so the form can be tested without
// retyping it, but only against the saved panel URL: the saved token never
// goes to another host.
func (a *App) TestRemnawave(ctx context.Context, r hub.Remnawave) (RemnawaveCheck, error) {
	var cur hub.Remnawave
	if c := a.Hub.Config().Remnawave; c != nil {
		cur = *c
	}
	if r.Token == "" {
		r.Token = masked
	}
	same := sameURL(r.URL, cur.URL)
	var err error
	if r.Token, err = keepSecret("remnawave API token", r.Token, cur.Token, same); err != nil {
		return RemnawaveCheck{}, err
	}
	r.Headers = maps.Clone(r.Headers)
	if err := keepHeaders("remnawave", r.Headers, cur.Headers, same); err != nil {
		return RemnawaveCheck{}, err
	}
	var out RemnawaveCheck
	if strings.TrimSpace(r.URL) == "" || r.Token == "" {
		return out, fmt.Errorf("panel URL and API token are required")
	}
	c := remnawave.New(r.URL, r.Token)
	c.Headers = r.Headers
	nodes, err := c.Nodes(ctx)
	if err != nil {
		return out, err
	}
	for _, n := range nodes {
		out.Nodes++
		if n.IsConnected {
			out.Online++
		}
		out.Users += n.UsersOnline
	}
	return out, nil
}

// seedUsers hands the drop detector the stored history of users online, so
// a restarted hub judges drops from its first poll.
func (a *App) seedUsers(ctx context.Context) {
	cfg := a.Hub.Config()
	if cfg.Remnawave == nil {
		return
	}
	since := time.Now().Add(-cfg.Remnawave.DropRule().Window.D() - 3*time.Minute)
	rows, err := a.Store.NodeUsersSince(ctx, since)
	if err != nil {
		a.Log.Warn("load users online history", "err", err)
		return
	}
	hist := make([]hub.UsersHistory, len(rows))
	for i, r := range rows {
		hist[i] = hub.UsersHistory{Node: r.Node, At: r.At, Users: r.Users}
	}
	a.Hub.SeedUsers(hist)
	a.Log.Info("users online history loaded", "polls", len(rows))
}
