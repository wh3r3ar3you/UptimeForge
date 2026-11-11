package checks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"gopkg.in/yaml.v3"

	"github.com/wh3r3ar3you/uptimeforge/internal/xray"
)

// With an explicit server and no record type the answer comes from that
// server: the hosts file, which Go's own resolver still reads, has no say.
func TestDNSServerSkipsHostsFile(t *testing.T) {
	addr := startDNS(t, func(w dns.ResponseWriter, q *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(q)
		switch q.Question[0].Qtype {
		case dns.TypeA:
			rr, _ := dns.NewRR(q.Question[0].Name + " 60 IN A 192.0.2.5")
			m.Answer = append(m.Answer, rr)
		case dns.TypeAAAA:
			rr, _ := dns.NewRR(q.Question[0].Name + " 60 IN AAAA 2001:db8::5")
			m.Answer = append(m.Answer, rr)
		}
		w.WriteMsg(m)
	})
	r := runSpec(t, &Spec{Type: "dns", DNS: &DNSParams{Name: "localhost", Server: addr, Expect: "192.0.2.5"}})
	wantUp(t, r)
	if r.Info["answers"] != "192.0.2.5,2001:db8::5" {
		t.Fatalf("answers %q, want both records of the server", r.Info["answers"])
	}
	// Only AAAA records still pass, as with the system resolver.
	v6 := startDNS(t, func(w dns.ResponseWriter, q *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(q)
		if q.Question[0].Qtype == dns.TypeAAAA {
			rr, _ := dns.NewRR(q.Question[0].Name + " 60 IN AAAA 2001:db8::6")
			m.Answer = append(m.Answer, rr)
		}
		w.WriteMsg(m)
	})
	wantUp(t, runSpec(t, &Spec{Type: "dns", DNS: &DNSParams{Name: "v6.test", Server: v6}}))
	empty := startDNS(t, func(w dns.ResponseWriter, q *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(q)
		w.WriteMsg(m)
	})
	wantDown(t, runSpec(t, &Spec{Type: "dns", DNS: &DNSParams{Name: "none.test", Server: empty}}), "dns")
}

// startVLESS runs an in-process VLESS server without TLS on loopback and
// returns a share link to it.
func startVLESS(t *testing.T) string {
	t.Helper()
	const id = "b831381d-6324-4d53-ad4f-8cda48b30811"
	host, port := closedPort(t)
	inst, err := xray.StartServer(`{"log":{"loglevel":"none"},"inbounds":[{"listen":"` + host + `","port":` + port +
		`,"protocol":"vless","settings":{"clients":[{"id":"` + id + `"}],"decryption":"none"}}],"outbounds":[{"protocol":"freedom"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { inst.Close() })
	return "vless://" + id + "@" + net.JoinHostPort(host, port) + "?type=tcp&security=none#local"
}

// expect_egress cannot pass when the exit IP is unknown: that is exactly the
// case of traffic leaving somewhere unexpected and the trace being blocked.
func TestExpectEgressUnknownIsDown(t *testing.T) {
	link := startVLESS(t)
	var trace atomic.Value
	trace.Store("fl=1\nip=203.0.113.7\nloc=DE\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/trace" {
			fmt.Fprint(w, trace.Load().(string))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	old := xrayTraceURL
	xrayTraceURL = srv.URL + "/trace"
	defer func() { xrayTraceURL = old }()

	spec := func() *Spec {
		return &Spec{Type: "xray", Timeout: Duration(10 * time.Second),
			Xray: &XrayParams{Link: link, URL: srv.URL + "/204", HeavyEvery: -1, ExpectEgress: []string{"203.0.113.0/24"}}}
	}
	wantUp(t, runSpec(t, spec()))
	trace.Store("fl=1\n") // the trace answers without an IP
	r := runSpec(t, spec())
	wantDown(t, r, "egress_unknown")
}

// xray-core quotes the key it rejects ("password": value), which the
// key=value scrubber did not expect.
func TestScrubXrayConfigErrors(t *testing.T) {
	for _, c := range []struct{ in, leak string }{
		{`infra/conf: Failed to build REALITY config. > infra/conf: invalid "password": PBKVALUE`, "PBKVALUE"},
		{`infra/conf: invalid "shortId": abcdef0123`, "abcdef0123"},
		{`invalid "publicKey": "Q4E8c0Cy0qMq"`, "Q4E8c0Cy0qMq"},
	} {
		if got := scrubText(c.in, nil); strings.Contains(got, c.leak) {
			t.Errorf("%q -> %q", c.in, got)
		}
	}
}

// Whatever xray or the parser puts into an error, the values of the link and
// of the probe's own upstream proxy do not reach the result.
func TestXraySecretsNotInResult(t *testing.T) {
	for _, link := range []string{
		"vless://SECRETUUID-not-valid@127.0.0.1:1?security=reality&pbk=PBKVALUE&sid=SIDVALUE01&sni=a.com#n",
		"vless://11111111-2222-3333-4444-555555555555@[::1:1?pbk=PBKVALUE&sid=SIDVALUE01#n",
	} {
		r := runSpec(t, &Spec{Type: "xray", Timeout: Duration(3 * time.Second), Xray: &XrayParams{Link: link, HeavyEvery: -1}})
		for _, leak := range []string{"PBKVALUE", "SIDVALUE01", "SECRETUUID", "11111111-2222-3333-4444-555555555555"} {
			if strings.Contains(r.Error, leak) {
				t.Errorf("%s leaks %s: %s", link, leak, r.Error)
			}
		}
	}
	// The probe's upstream proxy is a secret too.
	via := []byte(`{"protocol":"trojan","settings":{"address":"127.0.0.1","port":1,"password":"VIAPASSWORD"},"streamSettings":{"security":"reality","realitySettings":{"publicKey":"VIAPBKVALUE"}}}`)
	link := "vless://11111111-2222-3333-4444-555555555555@127.0.0.1:1?type=tcp&security=none#n"
	r := Run(context.Background(), &Spec{ID: "c", Type: "xray", Timeout: Duration(3 * time.Second), Xray: &XrayParams{Link: link, HeavyEvery: -1}},
		&Env{Via: via, ViaErr: errors.New(`probe upstream proxy: xray config: invalid "password": VIAPASSWORD and VIAPBKVALUE`)})
	if strings.Contains(r.Error, "VIAPASSWORD") || strings.Contains(r.Error, "VIAPBKVALUE") {
		t.Errorf("upstream proxy secrets leak: %s", r.Error)
	}
}

func TestMTProtoSecretValues(t *testing.T) {
	const sec = "ee0123456789abcdef0123456789abcdef6578616d706c652e636f6d"
	got := secretValues(&Spec{Type: "mtproto", MTProto: &MTProtoParams{Address: "1.2.3.4:443", Secret: sec}}, nil)
	if !slices.Contains(got, sec) {
		t.Errorf("address+secret: %q", got)
	}
	got = secretValues(&Spec{Type: "mtproto", MTProto: &MTProtoParams{Link: "tg://proxy?server=1.2.3.4&port=443&secret=" + sec}}, nil)
	if !slices.Contains(got, sec) {
		t.Errorf("link: %q", got)
	}
}

// The probe URL can carry a token and Go quotes it in its errors.
func TestProbeErrorWithoutURL(t *testing.T) {
	host, port := closedPort(t)
	r := runSpec(t, &Spec{Type: "xray", Xray: &XrayParams{Link: startVLESS(t), HeavyEvery: -1,
		URL: "http://" + net.JoinHostPort(host, port) + "/probe/URLTOKEN"}})
	if r.Status != Down || strings.Contains(r.Error, "URLTOKEN") {
		t.Fatalf("%+v", r)
	}
}

// unreachableFor builds the ICMP "destination unreachable" a router sends
// about an echo to dst with id and seq: it quotes the original IP header and
// the start of the echo.
func unreachableFor(t *testing.T, dst net.IP, id, seq int) *icmp.Message {
	t.Helper()
	echo, err := (&icmp.Message{Type: ipv4.ICMPTypeEcho, Body: &icmp.Echo{ID: id, Seq: seq, Data: []byte("UptimeForge")}}).Marshal(nil)
	if err != nil {
		t.Fatal(err)
	}
	var quoted []byte
	if v4 := dst.To4(); v4 != nil {
		h := make([]byte, 20)
		h[0], h[9] = 0x45, 1
		copy(h[12:16], net.IPv4(192, 0, 2, 99).To4())
		copy(h[16:20], v4)
		quoted = append(h, echo...)
	} else {
		echo, _ = (&icmp.Message{Type: ipv6.ICMPTypeEchoRequest, Body: &icmp.Echo{ID: id, Seq: seq, Data: []byte("UptimeForge")}}).Marshal(nil)
		h := make([]byte, 40)
		h[0], h[6] = 0x60, 58
		copy(h[8:24], net.ParseIP("2001:db8::99"))
		copy(h[24:40], dst.To16())
		quoted = append(h, echo...)
	}
	typ := icmp.Type(ipv4.ICMPTypeDestinationUnreachable)
	if dst.To4() == nil {
		typ = ipv6.ICMPTypeDestinationUnreachable
	}
	return &icmp.Message{Type: typ, Body: &icmp.DstUnreach{Data: quoted}}
}

// A raw socket sees every unreachable on the host: one about another ping
// must not fail this one.
func TestPingUnreachableIsOurs(t *testing.T) {
	for _, ip := range []net.IP{net.IPv4(198, 51, 100, 7), net.ParseIP("2001:db8::7")} {
		if !ownUnreachable(unreachableFor(t, ip, 42, 3), ip, 42, 3) {
			t.Errorf("%s: our own unreachable was ignored", ip)
		}
		other := net.IPv4(198, 51, 100, 8)
		if ip.To4() == nil {
			other = net.ParseIP("2001:db8::8")
		}
		for name, m := range map[string]*icmp.Message{
			"other host": unreachableFor(t, other, 42, 3),
			"other id":   unreachableFor(t, ip, 43, 3),
			"other seq":  unreachableFor(t, ip, 42, 4),
			"truncated":  {Type: ipv4.ICMPTypeDestinationUnreachable, Body: &icmp.DstUnreach{Data: []byte{0x45, 0}}},
		} {
			if ownUnreachable(m, ip, 42, 3) {
				t.Errorf("%s: an unreachable about %s was taken as ours", ip, name)
			}
		}
	}
}

// min_days: 0 turns the expiry alert off; left out it is 7 days.
func TestTLSMinDaysZeroDisables(t *testing.T) {
	for in, want := range map[string]int{`{"address":"a:443"}`: 7, `{"address":"a:443","min_days":0}`: 0, `{"address":"a:443","min_days":30}`: 30} {
		var p TLSParams
		if err := json.Unmarshal([]byte(in), &p); err != nil {
			t.Fatal(err)
		}
		if got := p.expiryDays(); got != want {
			t.Errorf("%s: threshold %d, want %d", in, got, want)
		}
		// It survives the trip to the agent.
		b, _ := json.Marshal(p)
		var back TLSParams
		json.Unmarshal(b, &back)
		if back.expiryDays() != want {
			t.Errorf("%s: after JSON %s threshold %d", in, b, back.expiryDays())
		}
	}
	var p TLSParams
	if err := yaml.Unmarshal([]byte("address: a:443\nmin_days: 0\n"), &p); err != nil || p.expiryDays() != 0 {
		t.Errorf("yaml min_days 0: %d, %v", p.expiryDays(), err)
	}
}

// Globalping takes the HTTP port as a number; a string is a validation error.
func TestGlobalpingPortIsNumber(t *testing.T) {
	bodies := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			bodies <- body
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"id":"m1"}`)
			return
		}
		fmt.Fprint(w, `{"status":"finished","results":[{"probe":{},"result":{"status":"finished","statusCode":200,"rawBody":""}}]}`)
	}))
	defer srv.Close()
	old := globalpingBase
	globalpingBase = srv.URL
	defer func() { globalpingBase = old }()

	runSpec(t, &Spec{Type: "globalping", Params: map[string]string{"subtype": "http", "target": "https://example.com:8443/p"}})
	opts, _ := (<-bodies)["measurementOptions"].(map[string]any)
	if port, ok := opts["port"].(float64); !ok || port != 8443 {
		t.Fatalf("port sent as %#v, want the number 8443", opts["port"])
	}
}

// A Host header in the check is what the server sees: Go sends req.Host and
// drops a Host entry of the header map.
func TestHostHeader(t *testing.T) {
	seen := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Host
		if r.Header.Get("Upgrade") == "websocket" {
			c, rw, _ := w.(http.Hijacker).Hijack()
			defer c.Close()
			fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", wsAcceptFor(r.Header.Get("Sec-WebSocket-Key")))
			rw.Flush()
			io.Copy(io.Discard, rw)
		}
	}))
	defer srv.Close()

	wantUp(t, runSpec(t, &Spec{Type: "http", HTTP: &HTTPParams{URL: srv.URL, Headers: map[string]string{"host": "vhost.example"}}}))
	if got := <-seen; got != "vhost.example" {
		t.Fatalf("http: server saw Host %q", got)
	}
	ws := "ws" + strings.TrimPrefix(srv.URL, "http")
	wantUp(t, runSpec(t, &Spec{Type: "websocket-upgrade", Params: map[string]string{"url": ws, "headers": `{"Host":"ws.example"}`}}))
	if got := <-seen; got != "ws.example" {
		t.Fatalf("websocket: server saw Host %q", got)
	}
}
