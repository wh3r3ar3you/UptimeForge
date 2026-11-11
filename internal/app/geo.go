package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"

	"github.com/wh3r3ar3you/uptimeforge/internal/geoip"
	"github.com/wh3r3ar3you/uptimeforge/internal/xray"
)

// countryDB is the country database behind LocateProxy (a fake in tests).
type countryDB interface {
	Country(ctx context.Context, ip netip.Addr) (string, error)
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
