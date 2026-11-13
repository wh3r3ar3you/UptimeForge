package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/geoip"
	"github.com/wh3r3ar3you/uptimeforge/internal/hub"
	"github.com/wh3r3ar3you/uptimeforge/internal/store"
	"github.com/wh3r3ar3you/uptimeforge/internal/xray"
)

// geoDB is the database behind LocateProxy and the networks of agents (a
// fake in tests).
type geoDB interface {
	Country(ctx context.Context, ip netip.Addr) (string, error)
	Network(ctx context.Context, ip netip.Addr) (geoip.Network, error)
}

// networkLookupTimeout bounds one agent's lookup: a database being downloaded
// must not keep its labels waiting forever; the next connect tries again.
const networkLookupTimeout = 10 * time.Second

// agentAddr is the hub's OnAgentAddr: it looks the address up off the hub's
// path, hands the result to the hub and saves the labels a checker got from
// it with the probe, so they are there after a restart. Without a database
// (or for a private address) the hub still learns the address.
func (a *App) agentAddr(probeID string, ip netip.Addr) {
	a.mu.Lock()
	parent := a.ctx
	a.mu.Unlock()
	if parent == nil {
		parent = context.Background()
	}
	go func() {
		ctx, cancel := context.WithTimeout(parent, networkLookupTimeout)
		defer cancel()
		n, err := a.geo.Network(ctx, ip)
		if err != nil {
			if ip.IsGlobalUnicast() && !ip.IsPrivate() {
				a.Log.Warn("agent network lookup failed", "probe", probeID, "err", err)
			}
			n = geoip.Network{IP: ip}
		}
		a.Hub.SetNetwork(probeID, n)
		if err := a.saveNetworkLabels(ctx, probeID); err != nil && parent.Err() == nil {
			a.Log.Warn("save the labels of an agent's network", "probe", probeID, "err", err)
		}
	}()
}

// saveNetworkLabels copies the network labels the hub gave a checker into its
// saved probe object. A probe from the config file is not saved; neither is a
// label the admin locked.
func (a *App) saveNetworkLabels(ctx context.Context, probeID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.managed.probes[probeID] {
		return nil
	}
	var live map[string]string
	for _, p := range a.Hub.Config().Probes {
		if p.ID == probeID {
			live = p.Labels
		}
	}
	var obj hub.ProbeConfig
	if err := a.Store.GetObject(ctx, kindProbe, probeID, &obj); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil // deleted meanwhile
		}
		return err
	}
	if !obj.Checker() {
		return nil
	}
	changed := false
	for _, k := range hub.NetworkLabels {
		v := live[k]
		if v == "" || obj.Labels[k] == v || slices.Contains(obj.LabelsLocked, k) {
			continue
		}
		if obj.Labels == nil {
			obj.Labels = map[string]string{}
		}
		obj.Labels[k], changed = v, true
	}
	if !changed {
		return nil
	}
	return a.Store.PutObject(ctx, kindProbe, probeID, obj)
}

// ProxyLocation is where a proxy's server is: what the "add server" form
// fills the country from.
type ProxyLocation struct {
	Host    string `json:"host"`
	IP      string `json:"ip"`
	Country string `json:"country"` // lower-case ISO code, "" when unknown
	Source  string `json:"source"`
}

// LocateProxy finds the country of the server a proxy link points at. The
// link carries credentials, so no error repeats it.
func (a *App) LocateProxy(ctx context.Context, link string) (ProxyLocation, error) {
	l, err := xray.ParseLink(link)
	if err != nil || l.Host == "" {
		return ProxyLocation{}, invalid(errors.New("cannot read the proxy link"))
	}
	ip, err := netip.ParseAddr(l.Host)
	if err != nil {
		addrs, rerr := net.DefaultResolver.LookupNetIP(ctx, "ip", l.Host)
		if rerr != nil || len(addrs) == 0 {
			return ProxyLocation{}, invalid(fmt.Errorf("%s does not resolve", l.Host))
		}
		ip = addrs[0]
		for _, x := range addrs { // IPv4 first: that is what most proxies listen on
			if x.Unmap().Is4() {
				ip = x
				break
			}
		}
	}
	ip = ip.Unmap()
	cc, err := a.geo.Country(ctx, ip)
	if err != nil {
		return ProxyLocation{}, err
	}
	return ProxyLocation{Host: l.Host, IP: ip.String(), Country: cc, Source: geoip.Attribution}, nil
}
