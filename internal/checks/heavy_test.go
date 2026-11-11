//go:build !lite

package checks

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kfake"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Chromium keeps its sandbox, the one barrier between a hostile page and the
// probe host, unless asked or impossible (root).
func TestChromiumSandbox(t *testing.T) {
	t.Setenv("UF_BROWSER_NO_SANDBOX", "")
	if got, root := chromiumNoSandbox(), os.Geteuid() == 0; got != root {
		t.Fatalf("no sandbox = %v as root=%v", got, root)
	}
	t.Setenv("UF_BROWSER_NO_SANDBOX", "1")
	if !chromiumNoSandbox() {
		t.Fatal("UF_BROWSER_NO_SANDBOX=1 must turn the sandbox off")
	}
}

const healthProto = `syntax = "proto3";
package grpc.health.v1;
message HealthCheckRequest { string service = 1; }
message HealthCheckResponse {
  enum ServingStatus { UNKNOWN = 0; SERVING = 1; NOT_SERVING = 2; SERVICE_UNKNOWN = 3; }
  ServingStatus status = 1;
}
service Health {
  rpc Check(HealthCheckRequest) returns (HealthCheckResponse);
  rpc Watch(HealthCheckRequest) returns (stream HealthCheckResponse);
}
`

func TestGRPC(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		if md, _ := metadata.FromIncomingContext(ctx); len(md.Get("x-deny")) > 0 {
			return nil, status.Error(codes.PermissionDenied, "denied")
		}
		return h(ctx, req)
	}))
	hs := health.NewServer()
	hs.SetServingStatus("ok", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(srv, hs)
	go srv.Serve(ln)
	defer srv.Stop()
	h, p := hostPort(t, ln.Addr().String())
	spec := func(extra map[string]string) *Spec {
		ps := map[string]string{"host": h, "port": p, "tls": "false", "proto": healthProto,
			"service": "grpc.health.v1.Health", "method": "Check", "body": `{"service":"ok"}`}
		for k, v := range extra {
			ps[k] = v
		}
		return &Spec{Type: "grpc-keyword", Params: ps}
	}
	s := spec(nil)
	s.Keyword = "SERVING"
	wantUp(t, runSpec(t, s))
	s = spec(map[string]string{"service": "Health"}) // short name
	s.Keyword = "NOT_THERE"
	wantDown(t, runSpec(t, s), "keyword")
	r := runSpec(t, spec(map[string]string{"body": `{"service":"nope"}`}))
	wantDown(t, r, "grpc_status")
	if !strings.Contains(r.Error, "NotFound") {
		t.Fatalf("%+v", r)
	}
	wantDown(t, runSpec(t, spec(map[string]string{"metadata": `{"x-deny":"1"}`})), "auth")
	h2, p2 := closedPort(t)
	wantDown(t, runSpec(t, spec(map[string]string{"host": h2, "port": p2})), "refused")

	for name, extra := range map[string]map[string]string{
		"broken proto": {"proto": "syntax = nope"},
		"no service":   {"service": "Other"},
		"no method":    {"method": "Other"},
		"streaming":    {"method": "Watch"},
		"bad metadata": {"metadata": "not json"},
	} {
		s := spec(extra)
		s.ID = "x"
		if err := s.Validate(); err == nil {
			t.Errorf("%s passed validation", name)
		}
	}
}

func TestBrowserWithoutChromium(t *testing.T) {
	old := findChromium
	findChromium = func() string { return "" }
	defer func() { findChromium = old }()
	r := runSpec(t, &Spec{Type: "real-browser", Params: map[string]string{"url": "http://127.0.0.1:1/"}})
	wantDown(t, r, "unavailable")
	if !strings.Contains(r.Error, "Chromium") {
		t.Fatalf("%+v", r)
	}
}

func TestBrowserThroughProxyRefuses(t *testing.T) {
	s := &Spec{ID: "b", Type: "real-browser", Params: map[string]string{"url": "http://127.0.0.1:1/"}}
	wantDown(t, Run(context.Background(), s, &Env{Via: json.RawMessage(`{}`)}), "unavailable")
}

// The real browser, on machines that have one.
func TestBrowserReal(t *testing.T) {
	if findChromium() == "" {
		t.Skip("no Chromium here")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gone" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><p id="there">hi</p>`)
	}))
	defer srv.Close()
	spec := func(path, sel string) *Spec {
		return &Spec{Type: "real-browser", Timeout: Duration(40 * time.Second),
			Params: map[string]string{"url": srv.URL + path, "wait_selector": sel}}
	}
	wantUp(t, runSpec(t, spec("/", "#there")))
	wantDown(t, runSpec(t, spec("/gone", "")), "http_status")
	h, p := closedPort(t)
	wantDown(t, runSpec(t, &Spec{Type: "real-browser", Timeout: Duration(40 * time.Second),
		Params: map[string]string{"url": "http://" + h + ":" + p + "/"}}), "refused")
}

// mqttBroker is the small part of MQTT 3.1.1 a subscribe needs. connack is the
// return code, payload what is published after the SUBSCRIBE ("" = nothing).
func mqttBroker(t *testing.T, connack byte, payload string) (string, string) {
	return tcpServer(t, func(c net.Conn) {
		br := bufio.NewReader(c)
		for {
			hdr, err := br.ReadByte()
			if err != nil {
				return
			}
			var length, shift int
			for {
				b, err := br.ReadByte()
				if err != nil {
					return
				}
				length |= int(b&0x7f) << shift
				shift += 7
				if b&0x80 == 0 {
					break
				}
			}
			body := make([]byte, length)
			if _, err := io.ReadFull(br, body); err != nil {
				return
			}
			switch hdr >> 4 {
			case 1: // CONNECT
				c.Write([]byte{0x20, 2, 0, connack})
			case 8: // SUBSCRIBE
				c.Write([]byte{0x90, 3, body[0], body[1], 0})
				if payload != "" {
					topic := "probe/topic"
					msg := []byte{0x30, byte(2 + len(topic) + len(payload)), 0, byte(len(topic))}
					msg = append(append(msg, topic...), payload...)
					c.Write(msg)
				}
			case 12: // PINGREQ
				c.Write([]byte{0xd0, 0})
			case 14: // DISCONNECT
				return
			}
		}
	})
}

func TestMQTT(t *testing.T) {
	spec := func(h, p, keyword string) *Spec {
		return &Spec{Type: "mqtt", Keyword: keyword, Timeout: Duration(2 * time.Second),
			Params: map[string]string{"host": h, "port": p, "topic": "probe/topic", "username": "u", "password": "pw"}}
	}
	base := runtime.NumGoroutine()
	h, p := mqttBroker(t, 0, "temperature=21")
	wantUp(t, runSpec(t, spec(h, p, "temperature")))
	wantDown(t, runSpec(t, spec(h, p, "absent")), "keyword")
	h, p = mqttBroker(t, 5, "")
	r := runSpec(t, spec(h, p, ""))
	wantDown(t, r, "auth")
	h, p = mqttBroker(t, 4, "")
	wantDown(t, runSpec(t, spec(h, p, "")), "auth")
	h, p = mqttBroker(t, 0, "") // nothing is ever published
	s := spec(h, p, "")
	s.Timeout = Duration(300 * time.Millisecond)
	wantDown(t, runSpec(t, s), "no_message")
	h, p = closedPort(t)
	wantDown(t, runSpec(t, spec(h, p, "")), "refused")
	waitGoroutines(t, base+4) // the four fake listeners above still run
}

func TestKafkaProducer(t *testing.T) {
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(1, "probe"))
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Close()
	brokers := strings.Join(cluster.ListenAddrs(), ",")
	spec := func(topic string) *Spec {
		return &Spec{Type: "kafka-producer", Timeout: Duration(5 * time.Second),
			Params: map[string]string{"brokers": brokers, "topic": topic}}
	}
	wantUp(t, runSpec(t, spec("probe")))
	wantDown(t, runSpec(t, spec("no-such-topic")), "not_found")
	h, p := closedPort(t)
	dead := &Spec{Type: "kafka-producer", Timeout: Duration(2 * time.Second), Params: map[string]string{"brokers": h + ":" + p, "topic": "probe"}}
	if r := runSpec(t, dead); r.Status != Down || r.Reason != "refused" && r.Reason != "timeout" {
		t.Fatalf("%+v", r)
	}
}

func TestKafkaSASL(t *testing.T) {
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(1, "probe"),
		kfake.EnableSASL(), kfake.Superuser("PLAIN", "bob", "pw"), kfake.Superuser("SCRAM-SHA-256", "bob", "pw"))
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Close()
	brokers := strings.Join(cluster.ListenAddrs(), ",")
	spec := func(mech, pass string) *Spec {
		return &Spec{Type: "kafka-producer", Timeout: Duration(5 * time.Second),
			Params: map[string]string{"brokers": brokers, "topic": "probe", "sasl": mech, "username": "bob", "password": pass}}
	}
	wantUp(t, runSpec(t, spec("plain", "pw")))
	wantUp(t, runSpec(t, spec("scram-sha-256", "pw")))
	// kfake drops the connection on a refused login where real brokers send error
	// 58, so only the leak can be seen here. The wording is the next assertion.
	r := runSpec(t, spec("plain", "wrong-pass-value"))
	if r.Status != Down || strings.Contains(r.Error, "wrong-pass-value") {
		t.Fatalf("%+v", r)
	}
	wantDown(t, failKafka(context.Background(), fmt.Errorf("x: %w", kerr.SaslAuthenticationFailed)), "auth")
	wantDown(t, failKafka(context.Background(), kerr.TopicAuthorizationFailed), "auth")
}

// snmpAgent answers GET with value for oid, or noSuchObject for the rest.
func snmpAgent(t *testing.T, community, oid string, value gosnmp.SnmpPDU) (string, string) {
	return udpServer(t, func(b []byte) []byte {
		dec := &gosnmp.GoSNMP{Logger: gosnmp.NewLogger(nil)}
		req, err := dec.SnmpDecodePacket(b)
		if err != nil || req.Community != community {
			return nil
		}
		want := req.Variables[0].Name
		pdu := gosnmp.SnmpPDU{Name: want, Type: gosnmp.NoSuchObject}
		if strings.TrimPrefix(want, ".") == strings.TrimPrefix(oid, ".") {
			pdu = value
			pdu.Name = want
		}
		resp := &gosnmp.SnmpPacket{Version: req.Version, Community: community, PDUType: gosnmp.GetResponse,
			RequestID: req.RequestID, Variables: []gosnmp.SnmpPDU{pdu}}
		out, err := resp.MarshalMsg()
		if err != nil {
			t.Error(err)
			return nil
		}
		return out
	})
}

func TestSNMP(t *testing.T) {
	const oid = "1.3.6.1.2.1.1.5.0"
	h, p := snmpAgent(t, "public", oid, gosnmp.SnmpPDU{Type: gosnmp.OctetString, Value: []byte("router-1")})
	spec := func(extra map[string]string) *Spec {
		ps := map[string]string{"host": h, "port": p, "oid": oid}
		for k, v := range extra {
			ps[k] = v
		}
		return &Spec{Type: "snmp", Timeout: Duration(2 * time.Second), Params: ps}
	}
	r := runSpec(t, spec(nil))
	wantUp(t, r)
	if r.Info["value"] != "router-1" {
		t.Fatalf("%+v", r)
	}
	wantUp(t, runSpec(t, spec(map[string]string{"snmp_version": "1"})))
	s := spec(nil)
	s.Keyword = "router"
	wantUp(t, runSpec(t, s))
	wantUp(t, runSpec(t, spec(map[string]string{"expected_value": "router-1"})))
	wantUp(t, runSpec(t, spec(map[string]string{"expected_value": "router", "expected_op": "contains"})))
	wantDown(t, runSpec(t, spec(map[string]string{"expected_value": "switch"})), "value_mismatch")
	wantDown(t, runSpec(t, spec(map[string]string{"oid": "1.3.6.1.2.1.1.6.0"})), "not_found")
	r = runSpec(t, spec(map[string]string{"community": "wrong-community-value"}))
	wantDown(t, r, "timeout")
	if strings.Contains(r.Error, "wrong-community-value") || !strings.Contains(r.Error, "community") {
		t.Fatalf("%+v", r)
	}

	h, p = snmpAgent(t, "public", oid, gosnmp.SnmpPDU{Type: gosnmp.Integer, Value: 42})
	wantUp(t, runSpec(t, &Spec{Type: "snmp", Timeout: Duration(2 * time.Second),
		Params: map[string]string{"host": h, "port": p, "oid": oid, "expected_value": "40", "expected_op": ">"}}))
}

func TestHeavyDriversUseProbeDialer(t *testing.T) {
	sentinel := errors.New("dialer was used")
	for _, s := range []Spec{
		{Type: "sqlserver", Params: map[string]string{"host": "x.invalid", "username": "u", "password": "topsecret"}},
		{Type: "oracledb", Params: map[string]string{"host": "x.invalid", "username": "u", "password": "topsecret", "database": "orcl"}},
		{Type: "mongodb", Params: map[string]string{"host": "x.invalid", "username": "u", "password": "topsecret"}},
		{Type: "mqtt", Params: map[string]string{"host": "x.invalid", "topic": "t"}},
		{Type: "kafka-producer", Params: map[string]string{"brokers": "x.invalid:9092", "topic": "t"}},
		{Type: "snmp", Params: map[string]string{"host": "x.invalid", "oid": "1.3"}},
		{Type: "grpc-keyword", Params: map[string]string{"host": "x.invalid", "tls": "false", "proto": healthProto, "service": "Health", "method": "Check"}},
	} {
		used := 0
		env := &Env{Dial: func(context.Context, string, string) (net.Conn, error) {
			used++
			return nil, sentinel
		}}
		s.ID, s.Timeout = "d", Duration(2*time.Second)
		if err := s.Validate(); err != nil {
			t.Fatalf("%s: %v", s.Type, err)
		}
		r := Run(context.Background(), &s, env)
		if used == 0 || r.Status != Down {
			t.Errorf("%s: did not go through Env.Dial: %+v", s.Type, r)
		}
		if strings.Contains(r.Error, "topsecret") {
			t.Errorf("%s: leak: %+v", s.Type, r)
		}
	}
}

func TestDatabaseFailureWording(t *testing.T) {
	wantDown(t, failOracle(context.Background(), errors.New("ORA-01017: invalid username/password")), "auth")
	wantDown(t, failOracle(context.Background(), errors.New("ORA-12514: listener does not know of service")), "not_found")
	wantDown(t, failOracle(context.Background(), errors.New("ORA-00942: table or view does not exist")), "query")
	wantDown(t, failMongo(context.Background(), errors.New("server selection error: context deadline exceeded, ... connection refused")), "refused")
	wantDown(t, failMongo(context.Background(), errors.New("auth error: sasl conversation error: unable to authenticate")), "auth")
}

func TestRealDriversRefused(t *testing.T) {
	h, p := closedPort(t)
	for _, s := range []Spec{
		{Type: "sqlserver", Params: map[string]string{"host": h, "port": p, "username": "u", "password": "topsecret"}},
		{Type: "oracledb", Params: map[string]string{"host": h, "port": p, "username": "u", "password": "topsecret", "database": "orcl"}},
		{Type: "mongodb", Params: map[string]string{"host": h, "port": p, "username": "u", "password": "topsecret"}},
	} {
		s := s
		s.Timeout = Duration(5 * time.Second)
		r := runSpec(t, &s)
		wantDown(t, r, "refused")
		if strings.Contains(r.Error, "topsecret") {
			t.Errorf("%s: leak: %+v", s.Type, r)
		}
	}
}
