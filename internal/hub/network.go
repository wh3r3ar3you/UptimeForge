package hub

import (
	"context"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/geoip"
)

// NetworkLabels are the labels a checker gets from the network it connects
// from: the country, the autonomous system and a short operator name.
var NetworkLabels = []string{"country", "asn", "isp"}

// NetworkView is where an agent connects from. IP is for admins only: the API
// clears it for viewers.
type NetworkView struct {
	IP      string    `json:"ip,omitempty"`
	Country string    `json:"country,omitempty"`
	ASN     string    `json:"asn,omitempty"`
	Org     string    `json:"org,omitempty"`
	ISP     string    `json:"isp,omitempty"`
	At      time.Time `json:"at"`
}

type clientIPKey struct{}

// WithClientIP tells the agent endpoint the client's address when something in
// front of it (a trusted reverse proxy) knows better than RemoteAddr.
func WithClientIP(ctx context.Context, ip netip.Addr) context.Context {
	return context.WithValue(ctx, clientIPKey{}, ip.Unmap())
}

// ClientIP is the address set by WithClientIP, otherwise RemoteAddr's.
func ClientIP(r *http.Request) netip.Addr {
	if ip, ok := r.Context().Value(clientIPKey{}).(netip.Addr); ok && ip.IsValid() {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip, _ := netip.ParseAddr(host)
	return ip.Unmap()
}

// networkLabels is the checker's label set for network n, or nil when it
// would not change. A field the databases did not know leaves its label as it
// is, and so does a label the admin set by hand.
func networkLabels(p *ProbeConfig, n geoip.Network) map[string]string {
	want := map[string]string{"country": n.Country, "asn": n.ASN, "isp": geoip.ShortISP(n.Org)}
	var out map[string]string
	for _, k := range NetworkLabels {
		v := want[k]
		if v == "" || p.Labels[k] == v || slices.Contains(p.LabelsLocked, k) {
			continue
		}
		if out == nil {
			out = maps.Clone(p.Labels)
		}
		out[k] = v
	}
	return out
}

// SetNetwork records where an agent connects from. A checker is labelled by
// it (country, asn, isp), and when that changes its labels it is reassigned:
// what it measured from the old network is dropped, so a line it reported
// there neither keeps an incident open nor counts towards a new one.
// A lookup that raced a reconnect from another address is ignored.
func (h *Hub) SetNetwork(probeID string, n geoip.Network) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.probes[probeID]
	if p == nil || p.cfg.Virtual() {
		return
	}
	n.IP = n.IP.Unmap()
	if p.addr.IsValid() && n.IP.IsValid() && n.IP != p.addr {
		return
	}
	now := time.Now()
	p.network = &NetworkView{Country: n.Country, ASN: n.ASN, Org: n.Org, ISP: geoip.ShortISP(n.Org), At: now}
	if n.IP.IsValid() {
		p.network.IP = n.IP.String()
	}
	if !p.cfg.Checker() {
		return
	}
	labels := networkLabels(p.cfg, n)
	if labels == nil {
		return
	}
	h.setLabels(probeID, labels)
	for _, ch := range h.checks {
		delete(ch.state, probeID)
	}
	h.rebuild()
	h.evaluateAll(now)
}

// setLabels gives one probe new labels. The config is copied, not changed in
// place: Config and Snapshot hand it out, and it is read without the lock.
// Callers hold h.mu.
func (h *Hub) setLabels(probeID string, labels map[string]string) {
	cfg := *h.cfg
	cfg.Probes = slices.Clone(h.cfg.Probes)
	for i := range cfg.Probes {
		pc := &cfg.Probes[i]
		if pc.ID == probeID {
			pc.Labels = labels
		}
		if p := h.probes[pc.ID]; p != nil {
			p.cfg = pc
		}
	}
	h.cfg = &cfg
}

// keepNetworkLabels puts a checker's network labels back on a new config: the
// app may compose it from a probe object saved before they were known.
// Callers hold h.mu.
func keepNetworkLabels(p *probe) {
	if !p.cfg.Checker() || p.network == nil {
		return
	}
	n := geoip.Network{Country: p.network.Country, ASN: p.network.ASN, Org: p.network.Org}
	if labels := networkLabels(p.cfg, n); labels != nil {
		p.cfg.Labels = labels
	}
}
