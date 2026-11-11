package checks

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

var dnsRecordTypes = map[string]uint16{
	"A": dns.TypeA, "AAAA": dns.TypeAAAA, "CNAME": dns.TypeCNAME, "MX": dns.TypeMX, "NS": dns.TypeNS,
	"TXT": dns.TypeTXT, "SRV": dns.TypeSRV, "CAA": dns.TypeCAA, "PTR": dns.TypePTR, "SOA": dns.TypeSOA,
}

func dnsRecordNames() []string {
	return []string{"A", "AAAA", "CNAME", "MX", "NS", "TXT", "SRV", "CAA", "PTR", "SOA"}
}

func (p *DNSParams) validate() error {
	if p.RecordType != "" {
		if _, ok := dnsRecordTypes[p.RecordType]; !ok {
			return fmt.Errorf("record_type must be one of %s", strings.Join(dnsRecordNames(), ", "))
		}
	}
	if p.Port < 0 || p.Port > 65535 {
		return errors.New("port must be 1-65535")
	}
	return nil
}

// checkDNSRecord asks the server for one record type, or for A and AAAA when
// no type is set: an explicit server must answer itself, while Go's resolver
// would still read the hosts file first.
func checkDNSRecord(ctx context.Context, p *DNSParams, env *Env) Result {
	types := []string{p.RecordType}
	if p.RecordType == "" {
		types = []string{"A", "AAAA"}
	}
	start := time.Now()
	got := make([][]string, len(types))
	failed := make([]*Result, len(types))
	var wg sync.WaitGroup
	for i, typ := range types {
		wg.Go(func() { got[i], failed[i] = dnsAsk(ctx, p, env, typ) })
	}
	wg.Wait()
	latency := ms(time.Since(start))
	var answers []string
	for i := range types {
		if failed[i] != nil {
			failed[i].LatencyMS = latency
			return *failed[i]
		}
		answers = append(answers, got[i]...)
	}
	if len(answers) == 0 {
		what := p.RecordType
		if what == "" {
			what = "A/AAAA"
		}
		return Result{Status: Down, Reason: "dns", LatencyMS: latency, Error: fmt.Sprintf("no %s records for %s", what, p.Name)}
	}
	r := Result{LatencyMS: latency, Text: strings.Join(answers, "\n"), Info: map[string]string{"answers": strings.Join(answers, ",")}}
	if p.Expect != "" && !slices.Contains(answers, p.Expect) {
		r.Status, r.Reason, r.Error = Down, "dns_mismatch", fmt.Sprintf("expected %s, got %s", p.Expect, strings.Join(answers, ", "))
	}
	return r
}

// dnsAsk asks the server for one record type and goes to TCP when the answer
// does not fit a datagram. Through an upstream proxy it is TCP from the start.
// A failed exchange or an error code comes back as a result.
func dnsAsk(ctx context.Context, p *DNSParams, env *Env, typ string) ([]string, *Result) {
	name := p.Name
	if typ == "PTR" && net.ParseIP(name) != nil {
		name, _ = dns.ReverseAddr(name)
	}
	q := new(dns.Msg)
	q.SetQuestion(dns.Fqdn(name), dnsRecordTypes[typ])
	q.SetEdns0(4096, false)
	server := p.serverAddr()

	var resp *dns.Msg
	var err error
	if env.Via == nil {
		for range 2 {
			resp, err = dnsExchange(ctx, env, "udp", server, q)
			if err == nil && !resp.Truncated || ctx.Err() != nil {
				break
			}
		}
	}
	if env.Via != nil || resp != nil && resp.Truncated {
		resp, err = dnsExchange(ctx, env, "tcp", server, q)
	}
	if err != nil {
		reason := udpReasonOf(ctx, err)
		if env.Via != nil {
			reason = reasonOf(ctx, err)
		}
		r := fail(reason, fmt.Errorf("%s: %w", server, err))
		return nil, &r
	}
	if resp.Rcode != dns.RcodeSuccess {
		return nil, &Result{Status: Down, Reason: "dns",
			Error: fmt.Sprintf("%s: %s for %s %s", server, dns.RcodeToString[resp.Rcode], p.Name, typ)}
	}
	var answers []string
	for _, rr := range resp.Answer {
		if rr.Header().Rrtype == dnsRecordTypes[typ] {
			answers = append(answers, dnsRRText(rr))
		}
	}
	return answers, nil
}

func dnsExchange(ctx context.Context, env *Env, network, server string, q *dns.Msg) (*dns.Msg, error) {
	attempt, cancel := ctx, func() {}
	if network == "udp" {
		// One lost datagram must leave time for the second try.
		deadline, ok := ctx.Deadline()
		if ok {
			attempt, cancel = context.WithTimeout(ctx, max(time.Until(deadline)/2, 500*time.Millisecond))
		}
	}
	defer cancel()
	conn, err := env.Dial(attempt, network, server)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stop := closeOnDone(attempt, conn)
	defer stop()
	c := &dns.Client{Net: network, UDPSize: 4096}
	if d, ok := attempt.Deadline(); ok {
		c.Timeout = time.Until(d)
	}
	resp, _, err := c.ExchangeWithConn(q, &dns.Conn{Conn: conn, UDPSize: 4096})
	return resp, err
}

// dnsRRText words an answer the way it is usually typed into an expectation:
// the value without the owner, TTL and trailing dots.
func dnsRRText(rr dns.RR) string {
	dot := func(s string) string { return strings.TrimSuffix(s, ".") }
	switch v := rr.(type) {
	case *dns.A:
		return v.A.String()
	case *dns.AAAA:
		return v.AAAA.String()
	case *dns.CNAME:
		return dot(v.Target)
	case *dns.NS:
		return dot(v.Ns)
	case *dns.PTR:
		return dot(v.Ptr)
	case *dns.MX:
		return strconv.Itoa(int(v.Preference)) + " " + dot(v.Mx)
	case *dns.TXT:
		return strings.Join(v.Txt, "")
	case *dns.SRV:
		return fmt.Sprintf("%d %d %d %s", v.Priority, v.Weight, v.Port, dot(v.Target))
	case *dns.CAA:
		return fmt.Sprintf("%d %s %q", v.Flag, v.Tag, v.Value)
	case *dns.SOA:
		return fmt.Sprintf("%s %s %d %d %d %d %d", dot(v.Ns), dot(v.Mbox), v.Serial, v.Refresh, v.Retry, v.Expire, v.Minttl)
	}
	return rr.String()
}
