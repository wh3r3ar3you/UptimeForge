package hub

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/model"
)

// HostOf extracts the host from "VLESS · host:port", "host:port" or a URL.
func HostOf(target string) string {
	if _, rest, ok := strings.Cut(target, " · "); ok {
		target = rest
	}
	if u, err := url.Parse(target); err == nil && u.Host != "" {
		return u.Hostname()
	}
	if h, _, err := net.SplitHostPort(target); err == nil {
		return h
	}
	return target
}

// PortOf is the port a target connects to: explicit, or the scheme default
// of a URL (443 for https, 80 for http); "" when unknown.
func PortOf(target string) string {
	if _, rest, ok := strings.Cut(target, " · "); ok {
		target = rest
	}
	if u, err := url.Parse(target); err == nil && u.Host != "" {
		if p := u.Port(); p != "" {
			return p
		}
		switch u.Scheme {
		case "https", "wss":
			return "443"
		case "http", "ws":
			return "80"
		}
		return ""
	}
	if _, p, err := net.SplitHostPort(target); err == nil {
		return p
	}
	return ""
}

// liveness are the check types that say whether a host is up at all; they
// are the parents of the services on that host, not of each other.
var liveness = map[string]bool{"tcp": true, "ping": true, "tls": true}

// DependsOnNone is the depends_on value that turns dependencies off.
const DependsOnNone = "none"

// depSet is how a check's parents combine. Any of Any down explains the
// failure: a reachability check of the same host, or a parent set by hand.
// Nodes are the panel's nodes on the check's address, each with its signals
// (the panel lost it, its agent is offline): all of them down explains it,
// one of two nodes behind one name does not.
type depSet struct {
	Any   []string
	Nodes [][]string
}

func (d depSet) empty() bool { return len(d.Any) == 0 && len(d.Nodes) == 0 }

// parentsOf finds what explains c failing: a reachability check of the same
// host, the panel's nodes on that address and their agents; or what
// c.DependsOn names instead. Caller holds h.mu.
func (h *Hub) parentsOf(c CheckConfig) depSet {
	if len(c.DependsOn) > 0 {
		if c.DependsOn[0] == DependsOnNone {
			return depSet{}
		}
		var out []string
		for _, d := range c.DependsOn {
			switch {
			case strings.HasPrefix(d, "node:"):
				name := nodeKey(strings.TrimPrefix(d, "node:"))
				for _, n := range h.nodes {
					if nodeKey(n.Name) == name {
						out = append(out, "rw:"+n.ID)
					}
				}
			case strings.HasPrefix(d, "probe:"):
				if _, ok := h.probes[strings.TrimPrefix(d, "probe:")]; ok {
					out = append(out, d)
				}
			default:
				out = append(out, d)
			}
		}
		return depSet{Any: out}
	}
	host := strings.ToLower(HostOf(c.target))
	if host == "" || liveness[c.Type] {
		// A reachability check is itself the signal that a host is down: it
		// has no parents of its own, or it would hold the very alert others
		// fold into.
		return depSet{}
	}
	var ds depSet
	port := PortOf(c.target)
	for _, id := range h.checkOrder {
		o := h.checks[id].cfg
		// Another port of the same host is another service: only the same
		// port, or a ping of the host, speaks for this one.
		if o.ID != c.ID && liveness[o.Type] && strings.EqualFold(HostOf(o.target), host) && (o.Type == "ping" || PortOf(o.target) == port) {
			ds.Any = append(ds.Any, o.ID)
		}
	}
	for _, n := range h.nodes {
		if !strings.EqualFold(n.Address, host) && !slices.Contains(n.IPs, host) {
			continue
		}
		group := []string{"rw:" + n.ID}
		for id, p := range h.probes {
			if nodeKey(p.cfg.Name) == nodeKey(n.Name) || nodeKey(id) == nodeKey(n.Name) {
				group = append(group, "probe:"+id)
			}
		}
		ds.Nodes = append(ds.Nodes, group)
	}
	return ds
}

// computeParents refreshes every check's parents; the panel's nodes and the
// set of checks both feed them. Caller holds h.mu.
func (h *Hub) computeParents() {
	for _, id := range h.checkOrder {
		ch := h.checks[id]
		ch.deps = h.parentsOf(ch.cfg)
		flat := slices.Clone(ch.deps.Any)
		for _, g := range ch.deps.Nodes {
			flat = append(flat, g...)
		}
		slices.Sort(flat)
		ch.parents = slices.Compact(flat)
	}
}

// downKind says a parent of this kind takes its children down: a host down
// or blocked, a node the panel lost, an agent offline.
func downKind(k model.Kind) bool {
	switch k {
	case model.KindDown, model.KindBlocked, model.KindNodeDown, model.KindProbeOffline:
		return true
	}
	return false
}

// markDown keeps h.downAt in step with key's incident moving from kind
// `from` to `to` at now: set when it becomes a kind that takes children
// down (or a worse one, blocked to down), dropped when it stops being one.
// Callers hold h.mu.
func (h *Hub) markDown(key string, from, to model.Kind, now time.Time) {
	switch {
	case !downKind(to):
		delete(h.downAt, key)
	case !downKind(from) || to.Rank() > from.Rank():
		h.downAt[key] = now
	}
}

// downSince is when parent incident p became the kind that takes children
// down; an incident restored after a restart has only its start.
func (h *Hub) downSince(p *model.Incident) time.Time {
	if at, ok := h.downAt[p.Key]; ok {
		return at
	}
	return p.Started
}

// explains says the open incident of parent k accounts for inc: it is the
// kind that takes children down, no milder than inc (a port blocked from
// some networks does not explain a service down from all of them), and it
// became that around when inc opened. A parent down for hours does not
// explain a service that fails only now: that is a new problem. One that was
// partial for hours and went down just now does.
func (h *Hub) explains(k string, ch *check, inc *model.Incident) bool {
	kind, region, since, ok := h.parentState(k)
	if !ok {
		return false
	}
	if kind == model.KindBlocked && !failsOnlyIn(inc, region) {
		return false // blocked in ru explains nothing down, or failing elsewhere
	}
	window := h.parentWait(ch) + 2*max(ch.cfg.Interval.D(), 30*time.Second)
	return !since.Before(inc.Started.Add(-window))
}

// parentState is parent k's kind and since when it has been one that takes
// children down; ok is false when it is not one. That is its open incident,
// or, for an incident closed by hand, what the hub sees now: closing the
// node's card while the node is still down must not release its services
// one by one. Callers hold h.mu.
func (h *Hub) parentState(k string) (kind model.Kind, region string, since time.Time, ok bool) {
	if p := h.incidents[k]; p != nil {
		return p.Kind, p.Region, h.downSince(p), downKind(p.Kind)
	}
	at, marked := h.downAt[k]
	if !marked || !h.closedByHand(k) {
		return "", "", time.Time{}, false
	}
	kind, region = h.stateOf(k)
	return kind, region, at, downKind(kind)
}

// stateOf is what key's incident would say now, from the hub's own view of
// the check, the panel's node or the agent.
func (h *Hub) stateOf(key string) (model.Kind, string) {
	if kind, id, ok := strings.Cut(key, ":"); ok {
		switch kind {
		case "rw":
			if t := h.users[id]; t != nil && !t.downSince.IsZero() {
				return model.KindNodeDown, ""
			}
		case "probe":
			if p := h.probes[id]; p != nil && !p.online {
				return model.KindProbeOffline, ""
			}
		}
		return model.KindOK, ""
	}
	if ch := h.checks[key]; ch != nil {
		return ch.kind, ch.region
	}
	return model.KindOK, ""
}

// failsOnlyIn says every probe failing inc is in region: blocked there, or
// partial on some of its networks.
func failsOnlyIn(inc *model.Incident, region string) bool {
	switch inc.Kind {
	case model.KindBlocked:
		return inc.Region == region
	case model.KindPartial:
		failing := 0
		for _, l := range inc.Lines {
			if !l.OK {
				if l.Region != region {
					return false
				}
				failing++
			}
		}
		return failing > 0
	}
	return false
}

// causeOf is the open parent incident that explains inc, ch's incident, or "".
func (h *Hub) causeOf(ch *check, inc *model.Incident) string {
	for _, k := range ch.deps.Any {
		if h.explains(k, ch, inc) {
			return k
		}
	}
	if len(ch.deps.Nodes) == 0 {
		return ""
	}
	first := ""
	for _, group := range ch.deps.Nodes {
		down := ""
		for _, k := range group {
			if h.explains(k, ch, inc) {
				down = k
				break
			}
		}
		if down == "" {
			return "" // a node on this address is alive
		}
		if first == "" {
			first = down
		}
	}
	return first
}

// maxParentWait caps how long a check with parents holds its alert.
const maxParentWait = 3 * time.Minute

// parentWait is how long a check holds its alert for a parent to confirm
// an outage first: two of the slowest parent's intervals, at most 3 min.
func (h *Hub) parentWait(ch *check) time.Duration {
	wait := 30 * time.Second
	keys := slices.Clone(ch.deps.Any)
	for _, g := range ch.deps.Nodes {
		keys = append(keys, g...)
	}
	for _, k := range keys {
		var d time.Duration
		switch {
		case strings.HasPrefix(k, "rw:"):
			d = 2 * 30 * time.Second // the panel is polled every 30 s
		case strings.HasPrefix(k, "probe:"):
			d = h.cfg.OfflineGrace.D() + 30*time.Second
		default:
			if p := h.checks[k]; p != nil {
				d = 2 * p.cfg.Interval.D()
			}
		}
		wait = max(wait, d)
	}
	return min(wait, maxParentWait)
}

// holdBack says whether a new incident waits instead of being announced:
// its parent explains it (then the parent's card lists it), or a parent may
// yet confirm an outage, or a partial outage is settling. Caller holds h.mu.
func (h *Hub) holdBack(inc *model.Incident, now time.Time) bool {
	if inc.Notified {
		return false
	}
	ch := h.checks[inc.Key]
	if ch != nil && !ch.deps.empty() {
		if cause := h.causeOf(ch, inc); cause != "" {
			h.attachCause(inc, cause)
			return true
		}
		until, ok := h.holdUntil[inc.Key]
		if !ok || inc.CauseKey != "" { // first seen, or its parent no longer explains it
			h.detachCause(inc)
			until = now.Add(h.parentWait(ch))
			h.holdUntil[inc.Key] = until
		}
		if now.Before(until) || now.Before(h.nodePending(ch)) {
			return true
		}
	}
	if h.awaitingPanel(ch, now) {
		return true
	}
	return h.settling(inc, now)
}

// nodePending is until when a node parent of ch may still be confirmed
// down: the panel has lost it, and node_down needs nodeDownAfter of that
// plus a poll. The zero time when no node is pending, or when node_down
// cannot open at all (the rule is off): then there is nothing to wait for.
func (h *Hub) nodePending(ch *check) time.Time {
	var until time.Time
	if h.cfg.Remnawave == nil || h.cfg.Remnawave.DropRule().Off {
		return until
	}
	for _, g := range ch.deps.Nodes {
		for _, k := range g {
			id, ok := strings.CutPrefix(k, "rw:")
			if !ok || h.incidents[k] != nil {
				continue
			}
			if t := h.users[id]; t != nil && !t.downSince.IsZero() {
				if u := t.downSince.Add(nodeDownAfter + time.Minute); u.After(until) {
					until = u
				}
			}
		}
	}
	return until
}

// awaitingPanel says the hub has just started and the panel has not sent
// its nodes yet, so a service that may run on one of them waits for them
// rather than alert on its own; at most maxParentWait after the start.
func (h *Hub) awaitingPanel(ch *check, now time.Time) bool {
	if ch == nil || h.nodesSeen || h.cfg.Remnawave == nil || len(ch.cfg.DependsOn) > 0 || liveness[ch.cfg.Type] || HostOf(ch.cfg.target) == "" {
		return false
	}
	return now.Before(h.started.Add(maxParentWait))
}

// detachCause undoes attachCause: inc is no longer its parent's doing.
func (h *Hub) detachCause(inc *model.Incident) {
	cause := inc.CauseKey
	inc.CauseKey = ""
	p := h.incidents[cause]
	if p == nil || !slices.Contains(p.Affected, inc.Name) {
		return
	}
	// A new slice: copies of p already handed out must not change under
	// their readers.
	p.Affected = slices.DeleteFunc(slices.Clone(p.Affected), func(n string) bool { return n == inc.Name })
	if p.Notified {
		h.emit(model.Event{Type: model.EventRefresh, Incident: *p})
	} else {
		h.persist(p)
	}
}

// attachCause marks inc as explained by cause and adds it to the parent's
// list of affected checks, editing the parent's card quietly.
func (h *Hub) attachCause(inc *model.Incident, cause string) {
	inc.CauseKey = cause
	p := h.incidents[cause]
	if p == nil || slices.Contains(p.Affected, inc.Name) {
		return
	}
	p.Affected = append(slices.Clone(p.Affected), inc.Name)
	if p.Notified {
		h.emit(model.Event{Type: model.EventRefresh, Incident: *p})
	} else {
		h.persist(p)
	}
}

// validateDeps checks depends_on: "none" alone, known checks, no cycles.
func validateDeps(cs []CheckConfig) error {
	ids := map[string][]string{}
	for _, c := range cs {
		ids[c.ID] = nil
	}
	for _, c := range cs {
		for _, d := range c.DependsOn {
			switch {
			case d == DependsOnNone:
				if len(c.DependsOn) > 1 {
					return fmt.Errorf("check %s: %q must be alone", c.ID, DependsOnNone)
				}
			case strings.HasPrefix(d, "node:"), strings.HasPrefix(d, "probe:"):
			default:
				if _, ok := ids[d]; !ok {
					return fmt.Errorf("check %s: unknown check %q", c.ID, d)
				}
				ids[c.ID] = append(ids[c.ID], d)
			}
		}
	}
	// Depth-first search for a cycle among check-to-check edges.
	state := map[string]int{} // 0 new, 1 on the path, 2 done
	var path []string
	var visit func(id string) error
	visit = func(id string) error {
		state[id] = 1
		path = append(path, id)
		for _, next := range ids[id] {
			switch state[next] {
			case 1:
				i := slices.Index(path, next)
				return fmt.Errorf("check %s: dependency cycle %s", next, strings.Join(append(slices.Clone(path[i:]), next), " → "))
			case 0:
				if err := visit(next); err != nil {
					return err
				}
			}
		}
		path = path[:len(path)-1]
		state[id] = 2
		return nil
	}
	for _, c := range cs {
		if state[c.ID] == 0 {
			if err := visit(c.ID); err != nil {
				return err
			}
		}
	}
	return nil
}
