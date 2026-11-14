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
// fake in tests). Lookups answer from what is there; Prepare downloads.
type geoDB interface {
	Country(ctx context.Context, ip netip.Addr) (string, error)
	Network(ctx context.Context, ip netip.Addr) (geoip.Network, error)
	Prepare(ctx context.Context) error
}

const (
	// networkLookupTimeout bounds one agent's lookup. It never downloads:
	// keepGeo does, and looks the agent up again once a database is there.
	networkLookupTimeout = 10 * time.Second
	// geoRefresh is how often keepGeo looks whether a database is due a
	// refresh (the files are monthly).
	geoRefresh = 24 * time.Hour
)

// keepGeo downloads the geo databases in the background, not inside one
// agent's lookup, and keeps them fresh. It starts when something first wants
// a lookup (an agent from a public address, a proxy to locate): a hub whose
// agents are all on private addresses never downloads. A failed download is
// retried after a minute, doubling up to an hour. After every attempt the
// agents nothing is known about yet are looked up again, so whoever
// connected before a database arrived gets its network (and a checker its
// labels) without reconnecting. wait sleeps, false when ctx ended.
func (a *App) keepGeo(ctx context.Context, wait func(context.Context, time.Duration) bool) {
	select {
	case <-ctx.Done():
		return
	case <-a.geoNeed:
	}
	var backoff time.Duration
	for {
		err := a.geo.Prepare(ctx)
		if ctx.Err() != nil {
			return
		}
		for id, ip := range a.Hub.UnknownNetworks() {
			a.agentAddr(id, ip)
		}
		next := geoRefresh
		if err != nil {
			backoff = geoBackoff(backoff)
			next = backoff
			a.Log.Warn("geoip database download failed, will retry", "in", next, "err", err)
		} else {
			backoff = 0
		}
		if !wait(ctx, next) {
			return
		}
	}
}

// wantGeo starts keepGeo if it has not started yet.
func (a *App) wantGeo() {
	select {
	case a.geoNeed <- struct{}{}:
	default:
	}
}

// geoBackoff is the pause after a failed download that followed one of prev.
func geoBackoff(prev time.Duration) time.Duration {
	if prev <= 0 {
		return time.Minute
	}
	return min(2*prev, time.Hour)
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

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
	if ip.IsGlobalUnicast() && !ip.IsPrivate() {
		a.wantGeo()
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
		// No database yet: an admin waiting on the form may wait for the
		// download (the API gives it two minutes), which an agent's lookup
		// may not. keepGeo takes over from here.
		a.wantGeo()
		a.geo.Prepare(ctx) // one database may come down while the other fails
		if cc, err = a.geo.Country(ctx, ip); err != nil {
			return ProxyLocation{}, err
		}
	}
	return ProxyLocation{Host: l.Host, IP: ip.String(), Country: cc, Source: geoip.Attribution}, nil
}
