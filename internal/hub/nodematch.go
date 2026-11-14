package hub

import (
	"net/netip"
	"strings"
)

// NodeRef is a Remnawave panel node as servers are matched to it.
type NodeRef struct {
	ID, Name string
	Address  string   // what clients connect to: a host name or an address
	IPs      []string // its addresses: from the panel and what Address resolves to
}

// Ref is the node as servers are matched to it.
func (n NodeUsers) Ref() NodeRef {
	return NodeRef{ID: n.ID, Name: n.Name, Address: n.Address, IPs: n.IPs}
}

// ProbeRef is a server as it is matched to a panel node.
type ProbeRef struct {
	Link     string // the node's uuid when linked by hand (ProbeConfig.RemnawaveNode)
	ID, Name string
	Host     string     // the host name its agent reports
	Addr     netip.Addr // where its agent connects from
}

// Match kinds, strongest first: a server linked by hand matches only its
// node; otherwise a name beats an address.
const (
	noMatch = iota
	matchAddr
	matchName
	matchLink
)

func nodeMatch(p ProbeRef, n NodeRef) int {
	if p.Link != "" {
		if p.Link == n.ID {
			return matchLink
		}
		return noMatch
	}
	if k := nodeKey(n.Name); k != "" && (nodeKey(p.Name) == k || nodeKey(p.ID) == k) {
		return matchName
	}
	if p.Host != "" && n.Address != "" && strings.EqualFold(p.Host, n.Address) {
		return matchAddr
	}
	if a := p.Addr.Unmap(); publicAddr(a) {
		for _, s := range append([]string{n.Address}, n.IPs...) {
			if ip, err := netip.ParseAddr(s); err == nil && ip.Unmap() == a {
				return matchAddr
			}
		}
	}
	return noMatch
}

// IsNodeProbe says a server is the one behind a panel node: the node it is
// linked to by hand; or, when it names none, the node of the same name or id
// (ignoring case and a "(…)" note), the node whose address is its agent's
// host name, or the node whose address is the public address its agent
// connects from. A private address (an agent behind the hub's own NAT, say)
// says nothing.
func IsNodeProbe(p ProbeRef, n NodeRef) bool { return nodeMatch(p, n) != noMatch }

// MatchNode is the panel node of a server, the strongest match first (see
// IsNodeProbe), the panel's order among equals.
func MatchNode(p ProbeRef, nodes []NodeRef) (NodeRef, bool) {
	best, at := noMatch, -1
	for i, n := range nodes {
		if m := nodeMatch(p, n); m > best {
			best, at = m, i
		}
	}
	if at < 0 {
		return NodeRef{}, false
	}
	return nodes[at], true
}

// publicAddr is an address one machine owns on the internet: not private,
// loopback, link-local or carrier-grade NAT.
func publicAddr(a netip.Addr) bool {
	return a.IsValid() && a.IsGlobalUnicast() && !a.IsPrivate() && !cgnat.Contains(a)
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// ProbeAddr is where a probe's agent connects from, as the status shows it.
func (p ProbeStatus) ProbeAddr() netip.Addr {
	if p.Network == nil {
		return netip.Addr{}
	}
	a, _ := netip.ParseAddr(p.Network.IP)
	return a
}

// Ref is the server as it is matched to a panel node.
func (p ProbeStatus) Ref() ProbeRef {
	return ProbeRef{Link: p.RemnawaveNode, ID: p.ID, Name: p.Name, Host: p.Host, Addr: p.ProbeAddr()}
}
