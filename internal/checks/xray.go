package checks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/xray"
)

func init() { register("xray", checkXray) }

// DefaultHeavyEvery: every Nth run downloads DownloadBytes through the tunnel.
// Light runs only prove the handshake works; heavy runs catch volume-based
// throttling, which a 204 probe never sees.
const DefaultHeavyEvery = 5

// xrayTraceURL is where the exit IP is looked up; empty means xray's default.
// A test points it at a local server.
var xrayTraceURL = ""

func checkXray(ctx context.Context, s *Spec, env *Env) Result {
	p := s.Xray
	var target xray.Outbound
	switch {
	case p.Link != "":
		l, err := xray.ParseLink(p.Link)
		if err != nil {
			return fail("config", err)
		}
		target = l.Outbound
	case p.Outbound != nil:
		target = p.Outbound
	default:
		return fail("config", fmt.Errorf("xray check without link or outbound"))
	}
	var via xray.Outbound
	if env.Via != nil {
		if err := json.Unmarshal(env.Via, &via); err != nil {
			return fail("config", fmt.Errorf("probe via: %w", err))
		}
	}
	inst, err := xray.Start(target, via, "")
	if err != nil {
		return fail("config", err)
	}
	defer inst.Close()

	every := p.HeavyEvery
	if every == 0 {
		every = DefaultHeavyEvery
	}
	heavy := every > 0 && (env.Seq%every == 0 || env.Escalated)
	egress := heavy || len(p.ExpectEgress) > 0
	if p.Egress != nil {
		egress = *p.Egress || len(p.ExpectEgress) > 0
	}

	pr := xray.Probe(ctx, inst.DialContext, xray.ProbeOptions{
		URL:           p.URL,
		Heavy:         heavy,
		DownloadURL:   p.DownloadURL,
		DownloadBytes: p.DownloadBytes,
		StallTimeout:  p.StallTimeout.D(),
		Egress:        egress,
		TraceURL:      xrayTraceURL,
	})
	// Latency is the handshake plus first byte through the tunnel: what a user
	// feels. Download time belongs to throughput, not latency.
	r := Result{
		Heavy:      heavy,
		LatencyMS:  ms(pr.TTFB),
		Phases:     map[string]float64{"ttfb": ms(pr.TTFB), "total": ms(pr.Total)},
		Bytes:      pr.Bytes,
		Throughput: pr.Throughput,
		Info:       map[string]string{},
	}
	if heavy {
		r.Phases["download"] = ms(pr.Download)
	}
	if pr.EgressIP != "" {
		r.Info["egress_ip"], r.Info["egress_loc"] = pr.EgressIP, pr.EgressLoc
	}
	if pr.Err != nil {
		r.Status, r.Reason, r.Error = Down, pr.Reason, pr.Err.Error()
		r.LatencyMS = ms(pr.Total)
		if pr.Reason == "handshake" && env.Via == nil {
			// xray hides why its own connection failed: the tunnel just closes.
			// A direct dial tells a server that is down from a broken handshake.
			if addr := target.TCPEndpoint(); addr != "" && serverRefuses(ctx, env, addr) {
				r.Reason, r.Error = "refused", fmt.Sprintf("server %s refused the connection", addr)
			}
		}
		return r
	}
	switch {
	case len(p.ExpectEgress) == 0:
	case pr.EgressIP == "":
		// Unknown is not a match: a blocked trace is what a wrong exit can look like.
		r.Status, r.Reason = Down, "egress_unknown"
		r.Error = fmt.Sprintf("could not determine the exit IP (%v), expected %s", pr.EgressErr, strings.Join(p.ExpectEgress, ", "))
	case !ipMatches(pr.EgressIP, p.ExpectEgress):
		r.Status, r.Reason = Down, "egress"
		r.Error = fmt.Sprintf("traffic exits via %s (%s), expected %s", pr.EgressIP, pr.EgressLoc, strings.Join(p.ExpectEgress, ", "))
	}
	return r
}

// serverRefuses reports whether a direct TCP connection to addr is refused.
func serverRefuses(ctx context.Context, env *Env, addr string) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	c, err := env.Dial(ctx, "tcp", addr)
	if err != nil {
		return reasonOf(ctx, err) == "refused"
	}
	c.Close()
	return false
}

// ipMatches reports whether ip equals one of the entries or lies in one of the CIDRs.
func ipMatches(ip string, allowed []string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	for _, a := range allowed {
		if pfx, err := netip.ParsePrefix(a); err == nil && pfx.Contains(addr) {
			return true
		}
		if x, err := netip.ParseAddr(a); err == nil && x == addr {
			return true
		}
	}
	return false
}
