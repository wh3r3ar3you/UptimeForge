package app

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/hub"
	"github.com/wh3r3ar3you/uptimeforge/internal/remnawave"
)

// resolvedIP marks an address the hub resolved from a node's host name, as
// opposed to one the panel lists.
const resolvedIP = "RESOLVED"

const (
	addrTTL     = 10 * time.Minute // how long a node's resolved addresses are kept
	addrTimeout = 3 * time.Second  // one lookup
)

// nodeAddrs caches what the panel nodes' host names resolve to, so a server
// is matched to its node by the address its agent connects from even when
// the panel lists the node by name only.
type nodeAddrs struct {
	mu sync.Mutex
	m  map[string]addrEntry
	// lookup resolves a host name (net.DefaultResolver; tests replace it).
	lookup func(ctx context.Context, host string) ([]string, error)
}

type addrEntry struct {
	ips []string
	at  time.Time
}

// resolve adds to each node the addresses its host name resolves to, looked
// up in parallel and cached: a failed lookup keeps the last known ones.
func (c *nodeAddrs) resolve(ctx context.Context, nodes []remnawave.Node) {
	c.mu.Lock()
	if c.m == nil {
		c.m = map[string]addrEntry{}
	}
	lookup := c.lookup
	if lookup == nil {
		lookup = net.DefaultResolver.LookupHost
	}
	var stale []string
	for _, n := range nodes {
		h := n.Address
		if h == "" || isAddr(h) || slices.Contains(stale, h) {
			continue
		}
		if e, ok := c.m[h]; !ok || time.Since(e.at) > addrTTL {
			stale = append(stale, h)
		}
	}
	c.mu.Unlock()

	var wg sync.WaitGroup
	for _, h := range stale {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lctx, cancel := context.WithTimeout(ctx, addrTimeout)
			defer cancel()
			ips, err := lookup(lctx, h)
			c.mu.Lock()
			defer c.mu.Unlock()
			if err != nil {
				if e, ok := c.m[h]; ok {
					c.m[h] = addrEntry{ips: e.ips, at: time.Now().Add(-addrTTL + time.Minute)} // retry in a minute
				}
				return
			}
			c.m[h] = addrEntry{ips: ips, at: time.Now()}
		}()
	}
	wg.Wait()

	c.mu.Lock()
	defer c.mu.Unlock()
	for i, n := range nodes {
		for _, ip := range c.m[n.Address].ips {
			if !slices.ContainsFunc(n.IPs, func(x remnawave.NodeIP) bool { return x.IP == ip }) {
				nodes[i].IPs = append(nodes[i].IPs, remnawave.NodeIP{IP: ip, Status: resolvedIP})
			}
		}
	}
}

func isAddr(s string) bool {
	_, err := netip.ParseAddr(s)
	return err == nil
}

// NodeRefs are the panel's nodes as servers are matched to them (nil when no
// panel is connected).
func (a *App) NodeRefs() []hub.NodeRef {
	nodes := a.nodes.Load()
	if nodes == nil {
		return nil
	}
	out := make([]hub.NodeRef, 0, len(*nodes))
	for _, n := range *nodes {
		out = append(out, hub.NodeRef{ID: n.UUID, Name: n.Name, Address: n.Address, IPs: nodeIPs(n)})
	}
	return out
}
