package checks

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/miekg/dns"
)

func TestHTTPKeywordAndRanges(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/404":
			w.WriteHeader(404)
		case "/echo":
			b, _ := io.ReadAll(r.Body)
			fmt.Fprintf(w, "%s %s %s", r.Method, r.Header.Get("Content-Type"), b)
			return
		}
		fmt.Fprint(w, "hello world")
	}))
	defer srv.Close()
	spec := func(path string, p HTTPParams) *Spec {
		p.URL = srv.URL + path
		return &Spec{Type: "http", HTTP: &p}
	}
	wantUp(t, runSpec(t, spec("/", HTTPParams{})))
	wantDown(t, runSpec(t, spec("/404", HTTPParams{})), "http_status")
	wantUp(t, runSpec(t, spec("/404", HTTPParams{AcceptedStatus: []string{"400-404"}})))
	wantUp(t, runSpec(t, spec("/404", HTTPParams{AcceptedStatus: []string{"404"}})))
	wantDown(t, runSpec(t, spec("/404", HTTPParams{AcceptedStatus: []string{"200-299", "500"}})), "http_status")
	wantUp(t, runSpec(t, spec("/404", HTTPParams{ExpectStatus: []int{404}})))
	wantDown(t, runSpec(t, spec("/", HTTPParams{ExpectStatus: []int{404}})), "http_status")

	wantUp(t, runSpec(t, spec("/", HTTPParams{Keyword: "world"})))
	wantDown(t, runSpec(t, spec("/", HTTPParams{Keyword: "absent"})), "keyword")
	wantUp(t, runSpec(t, spec("/", HTTPParams{Keyword: "absent", InvertKeyword: true})))
	wantDown(t, runSpec(t, spec("/", HTTPParams{Keyword: "world", InvertKeyword: true})), "keyword")

	s := spec("/", HTTPParams{})
	s.Keyword = "absent"
	wantDown(t, runSpec(t, s), "keyword")

	s = spec("/echo", HTTPParams{Body: `{"a":1}`, Keyword: `POST application/json {"a":1}`})
	wantUp(t, runSpec(t, s))
}

func TestHTTPAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		switch {
		case ok && u == "bob" && p == "pw":
		case r.Header.Get("Authorization") == "Bearer tok":
		default:
			w.WriteHeader(401)
		}
	}))
	defer srv.Close()
	wantDown(t, runSpec(t, &Spec{Type: "http", HTTP: &HTTPParams{URL: srv.URL}}), "http_status")
	wantUp(t, runSpec(t, &Spec{Type: "http", HTTP: &HTTPParams{URL: srv.URL, BasicUser: "bob", BasicPassword: "pw"}}))
	wantUp(t, runSpec(t, &Spec{Type: "http", HTTP: &HTTPParams{URL: srv.URL, BearerToken: "tok"}}))
}

func TestHTTPRedirects(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/a":
			http.Redirect(w, r, "/b", http.StatusFound)
		case "/b":
			http.Redirect(w, r, "/c", http.StatusFound)
		default:
			fmt.Fprint(w, "landed")
		}
	}))
	defer srv.Close()
	n := func(v int) *int { return &v }
	wantUp(t, runSpec(t, &Spec{Type: "http", HTTP: &HTTPParams{URL: srv.URL + "/a", Keyword: "landed"}}))
	// 0 does not follow: the 302 itself is the answer.
	hits.Store(0)
	r := runSpec(t, &Spec{Type: "http", HTTP: &HTTPParams{URL: srv.URL + "/a", MaxRedirects: n(0)}})
	wantUp(t, r)
	if r.Info["status_code"] != "302" || hits.Load() != 1 {
		t.Fatalf("%+v hits=%d", r, hits.Load())
	}
	wantDown(t, runSpec(t, &Spec{Type: "http", HTTP: &HTTPParams{URL: srv.URL + "/a", MaxRedirects: n(1)}}), "redirects")
	wantUp(t, runSpec(t, &Spec{Type: "http", HTTP: &HTTPParams{URL: srv.URL + "/a", MaxRedirects: n(2)}}))
}

func TestHTTPJSONQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/text" {
			fmt.Fprint(w, "not json")
			return
		}
		fmt.Fprint(w, `{"status":"ok","count":12,"nested":{"items":[{"id":1},{"id":2}]},"big":10}`)
	}))
	defer srv.Close()
	q := func(path, op, want string) Result {
		return runSpec(t, &Spec{Type: "http", HTTP: &HTTPParams{URL: srv.URL, JSONPath: path, JSONOp: op, JSONExpect: want}})
	}
	wantUp(t, q("$.status", "==", "ok"))
	wantUp(t, q("$.status", "", "ok"))
	wantDown(t, q("$.status", "==", "bad"), "value_mismatch")
	wantUp(t, q("$.status", "!=", "bad"))
	wantUp(t, q("$.status", "contains", "o"))
	wantUp(t, q("$.count", ">", "9")) // numerically; as text "12" < "9"
	wantDown(t, q("$.count", "<", "9"), "value_mismatch")
	wantUp(t, q("$.count", ">=", "12"))
	wantUp(t, q("$.count", "<=", "12"))
	wantDown(t, q("$.status", ">", "1"), "value_mismatch")
	wantUp(t, q("$.nested.items[*].id", "==", "2"))
	wantDown(t, q("$.nested.items[*].id", "!=", "2"), "value_mismatch")
	wantDown(t, q("$.missing", "==", "1"), "value_mismatch")
	wantDown(t, runSpec(t, &Spec{Type: "http", HTTP: &HTTPParams{URL: srv.URL + "/text", JSONPath: "$.a", JSONExpect: "1"}}), "bad_response")
}

func TestHTTPMTLS(t *testing.T) {
	serverCert, certPEM, keyPEM := testCertPEM(t)
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM([]byte(certPEM))
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{serverCert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}
	srv.Config.ErrorLog = nil
	srv.StartTLS()
	defer srv.Close()
	wantUp(t, runSpec(t, &Spec{Type: "http", HTTP: &HTTPParams{URL: srv.URL, Insecure: true, TLSCert: certPEM, TLSKey: keyPEM}}))
	if r := runSpec(t, &Spec{Type: "http", HTTP: &HTTPParams{URL: srv.URL, Insecure: true}}); r.Status != Down {
		t.Fatalf("no client cert passed: %+v", r)
	}
}

// startDNS runs a miekg server on UDP and TCP on the same port.
func startDNS(t *testing.T, h dns.HandlerFunc) string {
	t.Helper()
	// DNS answers on UDP and TCP of one port: take a free UDP port and try
	// again while its TCP twin is taken by something else.
	var (
		pc   net.PacketConn
		ln   net.Listener
		port int
	)
	for try := 0; ; try++ {
		var err error
		if pc, err = net.ListenPacket("udp", "127.0.0.1:0"); err != nil {
			t.Fatal(err)
		}
		port = pc.LocalAddr().(*net.UDPAddr).Port
		if ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			break
		}
		pc.Close()
		if try == 20 {
			t.Fatal(err)
		}
	}
	udp := &dns.Server{PacketConn: pc, Handler: h}
	tcp := &dns.Server{Listener: ln, Handler: h}
	go udp.ActivateAndServe()
	go tcp.ActivateAndServe()
	t.Cleanup(func() { udp.Shutdown(); tcp.Shutdown() })
	return fmt.Sprintf("127.0.0.1:%d", port)
}

func TestDNSRecordTypes(t *testing.T) {
	zone := map[string]string{
		"a.test. A":                   "a.test. 60 IN A 192.0.2.1",
		"a.test. AAAA":                "a.test. 60 IN AAAA 2001:db8::1",
		"c.test. CNAME":               "c.test. 60 IN CNAME a.test.",
		"a.test. MX":                  "a.test. 60 IN MX 10 mail.a.test.",
		"a.test. NS":                  "a.test. 60 IN NS ns1.a.test.",
		"a.test. TXT":                 `a.test. 60 IN TXT "v=spf1 -all"`,
		"a.test. SRV":                 "a.test. 60 IN SRV 1 2 443 s.a.test.",
		"a.test. CAA":                 `a.test. 60 IN CAA 0 issue "letsencrypt.org"`,
		"a.test. SOA":                 "a.test. 60 IN SOA ns1.a.test. root.a.test. 7 1 2 3 4",
		"1.2.0.192.in-addr.arpa. PTR": "1.2.0.192.in-addr.arpa. 60 IN PTR a.test.",
	}
	addr := startDNS(t, func(w dns.ResponseWriter, q *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(q)
		key := q.Question[0].Name + " " + dns.TypeToString[q.Question[0].Qtype]
		if rr, ok := zone[key]; ok {
			parsed, err := dns.NewRR(rr)
			if err != nil {
				t.Error(err)
			}
			m.Answer = append(m.Answer, parsed)
		} else if strings.HasPrefix(key, "gone.") {
			m.Rcode = dns.RcodeNameError
		}
		w.WriteMsg(m)
	})
	cases := []struct{ typ, name, want string }{
		{"A", "a.test", "192.0.2.1"}, {"AAAA", "a.test", "2001:db8::1"}, {"CNAME", "c.test", "a.test"},
		{"MX", "a.test", "10 mail.a.test"}, {"NS", "a.test", "ns1.a.test"}, {"TXT", "a.test", "v=spf1 -all"},
		{"SRV", "a.test", "1 2 443 s.a.test"}, {"CAA", "a.test", `0 issue "letsencrypt.org"`},
		{"SOA", "a.test", "ns1.a.test root.a.test 7 1 2 3 4"}, {"PTR", "192.0.2.1", "a.test"},
	}
	for _, c := range cases {
		r := runSpec(t, &Spec{Type: "dns", DNS: &DNSParams{Name: c.name, RecordType: c.typ, Server: addr, Expect: c.want}})
		wantUp(t, r)
		if r.Info["answers"] != c.want {
			t.Errorf("%s: %+v", c.typ, r)
		}
	}
	host, port := hostPort(t, addr)
	var portN int
	fmt.Sscan(port, &portN)
	wantUp(t, runSpec(t, &Spec{Type: "dns", DNS: &DNSParams{Name: "a.test", RecordType: "A", Server: host, Port: portN}}))
	wantDown(t, runSpec(t, &Spec{Type: "dns", DNS: &DNSParams{Name: "a.test", RecordType: "A", Server: addr, Expect: "192.0.2.9"}}), "dns_mismatch")
	wantDown(t, runSpec(t, &Spec{Type: "dns", DNS: &DNSParams{Name: "none.test", RecordType: "A", Server: addr}}), "dns")
	r := runSpec(t, &Spec{Type: "dns", DNS: &DNSParams{Name: "gone.test", RecordType: "A", Server: addr}})
	wantDown(t, r, "dns")
	if !strings.Contains(r.Error, "NXDOMAIN") {
		t.Fatalf("%+v", r)
	}
	s := &Spec{Type: "dns", Keyword: "spf1", DNS: &DNSParams{Name: "a.test", RecordType: "TXT", Server: addr}}
	wantUp(t, runSpec(t, s))
	s = &Spec{Type: "dns", Keyword: "dkim", DNS: &DNSParams{Name: "a.test", RecordType: "TXT", Server: addr}}
	wantDown(t, runSpec(t, s), "keyword")
	// The old path, with no record type, still works and has text too.
	s = &Spec{Type: "dns", Keyword: "192.0.2.1", DNS: &DNSParams{Name: "a.test", Server: addr, Expect: "192.0.2.1"}}
	wantUp(t, runSpec(t, s))
}

func TestDNSTruncatedGoesToTCP(t *testing.T) {
	addr := startDNS(t, func(w dns.ResponseWriter, q *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(q)
		if _, isUDP := w.RemoteAddr().(*net.UDPAddr); isUDP {
			m.Truncated = true
		} else {
			rr, _ := dns.NewRR("a.test. 60 IN A 192.0.2.7")
			m.Answer = append(m.Answer, rr)
		}
		w.WriteMsg(m)
	})
	wantUp(t, runSpec(t, &Spec{Type: "dns", DNS: &DNSParams{Name: "a.test", RecordType: "A", Server: addr, Expect: "192.0.2.7"}}))
}

func wsServer(t *testing.T, accept func(string) string, proto string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/plain" {
			fmt.Fprint(w, "no upgrade here")
			return
		}
		c, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer c.Close()
		fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n", accept(r.Header.Get("Sec-WebSocket-Key")))
		if proto != "" {
			fmt.Fprintf(rw, "Sec-WebSocket-Protocol: %s\r\n", proto)
		}
		fmt.Fprint(rw, "\r\n")
		rw.Flush()
		io.Copy(io.Discard, rw) // until the client closes
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestWebSocketUpgrade(t *testing.T) {
	good := wsAcceptFor
	bad := func(string) string { return "nonsense" }
	ws := func(u string, extra map[string]string) *Spec {
		p := map[string]string{"url": "ws" + strings.TrimPrefix(u, "http")}
		for k, v := range extra {
			p[k] = v
		}
		return &Spec{Type: "websocket-upgrade", Params: p}
	}
	wantUp(t, runSpec(t, ws(wsServer(t, good, "").URL, nil)))
	wantDown(t, runSpec(t, ws(wsServer(t, good, "").URL+"/plain", nil)), "http_status")
	badAccept := wsServer(t, bad, "")
	wantDown(t, runSpec(t, ws(badAccept.URL, nil)), "bad_response")
	wantUp(t, runSpec(t, ws(badAccept.URL, map[string]string{"ignore_accept": "true"})))
	wantDown(t, runSpec(t, ws(wsServer(t, good, "").URL, map[string]string{"subprotocol": "chat"})), "bad_response")
	wantUp(t, runSpec(t, ws(wsServer(t, good, "chat").URL, map[string]string{"subprotocol": "chat"})))
	host, port := closedPort(t)
	wantDown(t, runSpec(t, &Spec{Type: "websocket-upgrade", Params: map[string]string{"url": "ws://" + host + ":" + port + "/"}}), "refused")
}

func TestGlobalping(t *testing.T) {
	var state atomic.Int32
	var measurement atomic.Value
	var status atomic.Int32
	status.Store(http.StatusAccepted)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/measurements":
			if r.Header.Get("Authorization") == "Bearer bad" {
				w.WriteHeader(401)
				return
			}
			if code := status.Load(); code != http.StatusAccepted {
				w.WriteHeader(int(code))
				return
			}
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if body["type"] == nil || body["target"] == nil {
				t.Errorf("bad create: %v", body)
			}
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, `{"id":"m1"}`)
		case r.URL.Path == "/v1/measurements/m1":
			if state.Add(1) == 1 {
				fmt.Fprint(w, `{"status":"in-progress","results":[]}`)
				return
			}
			fmt.Fprint(w, measurement.Load().(string))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	old := globalpingBase
	globalpingBase = srv.URL
	defer func() { globalpingBase = old }()

	ans := func(result string) {
		state.Store(0)
		measurement.Store(`{"status":"finished","results":[{"probe":{"city":"Riga","country":"LV","network":"N"},"result":` + result + `}]}`)
	}
	ping := &Spec{Type: "globalping", Keyword: "packets", Params: map[string]string{"subtype": "ping", "target": "example.com", "location": "EU"}}
	ans(`{"status":"finished","rawOutput":"3 packets","stats":{"avg":12.5,"loss":0}}`)
	r := runSpec(t, ping)
	wantUp(t, r)
	if r.LatencyMS != 12.5 || r.Info["location"] != "Riga, LV" {
		t.Fatalf("%+v", r)
	}
	ans(`{"status":"finished","rawOutput":"x","stats":{"avg":0,"loss":100}}`)
	wantDown(t, runSpec(t, &Spec{Type: "globalping", Params: map[string]string{"target": "example.com"}}), "timeout")
	ans(`{"status":"failed","rawOutput":"unknown host"}`)
	wantDown(t, runSpec(t, &Spec{Type: "globalping", Params: map[string]string{"target": "example.com"}}), "remote_failed")

	web := func(kw string) *Spec {
		return &Spec{Type: "globalping", Keyword: kw, Params: map[string]string{"subtype": "http", "target": "https://example.com/p?x=1"}}
	}
	ans(`{"status":"finished","statusCode":200,"rawBody":"<h1>hello</h1>"}`)
	wantUp(t, runSpec(t, web("hello")))
	ans(`{"status":"finished","statusCode":200,"rawBody":"<h1>hello</h1>"}`)
	wantDown(t, runSpec(t, web("absent")), "keyword")
	ans(`{"status":"finished","statusCode":500,"rawBody":"x"}`)
	wantDown(t, runSpec(t, web("")), "http_status")

	ans(`{"status":"finished","answers":[{"value":"192.0.2.1"}]}`)
	wantUp(t, runSpec(t, &Spec{Type: "globalping", Keyword: "192.0.2.1", Params: map[string]string{"subtype": "dns", "target": "example.com"}}))
	ans(`{"status":"finished","answers":[]}`)
	wantDown(t, runSpec(t, &Spec{Type: "globalping", Params: map[string]string{"subtype": "dns", "target": "example.com"}}), "dns")

	wantDown(t, runSpec(t, &Spec{Type: "globalping", Params: map[string]string{"target": "example.com", "token": "bad"}}), "auth")
	status.Store(http.StatusTooManyRequests)
	wantDown(t, runSpec(t, &Spec{Type: "globalping", Params: map[string]string{"target": "example.com"}}), "rate_limited")
}

func TestPingLoopback(t *testing.T) {
	r := runSpec(t, &Spec{Type: "ping", Params: map[string]string{"host": "127.0.0.1", "ping_count": "2"}})
	if r.Status == Down && r.Reason == "unavailable" {
		if !strings.Contains(r.Error, "ping_group_range") {
			t.Fatalf("must say what to change: %+v", r)
		}
		t.Skip("ICMP is forbidden here: " + r.Error)
	}
	wantUp(t, r)
	if r.Info["loss_percent"] != "0" {
		t.Fatalf("%+v", r)
	}
}

func TestPingThroughProxyRefuses(t *testing.T) {
	s := &Spec{ID: "p", Type: "ping", Params: map[string]string{"host": "127.0.0.1"}}
	r := Run(context.Background(), s, &Env{Via: json.RawMessage(`{}`)})
	wantDown(t, r, "unavailable")
}
