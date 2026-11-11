package checks

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/xray"
)

// startSOCKS runs an in-process xray SOCKS server: a stand-in for a probe's upstream proxy.
func startSOCKS(t *testing.T) string {
	t.Helper()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	host, port, _ := net.SplitHostPort(addr)
	inst, err := xray.StartServer(`{"log":{"loglevel":"none"},"inbounds":[{"listen":"` + host + `","port":` + port +
		`,"protocol":"socks","settings":{"auth":"noauth"}}],"outbounds":[{"protocol":"freedom"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { inst.Close() })
	return addr
}

func TestTCPThroughViaDetectsDeadPort(t *testing.T) {
	socks := startSOCKS(t)
	via, _ := xray.ParseLink("socks5://" + socks)
	viaJSON, _ := json.Marshal(via.Outbound)
	inst, err := xray.Start(nil, via.Outbound, "")
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	env := func() *Env { return &Env{Dial: inst.DialContext, Via: viaJSON} }

	open, _ := net.Listen("tcp", "127.0.0.1:0")
	defer open.Close()
	go func() {
		for {
			c, err := open.Accept()
			if err != nil {
				return
			}
			defer c.Close() // hold it open, like a server waiting for the client
		}
	}()
	dead, _ := net.Listen("tcp", "127.0.0.1:0")
	deadAddr := dead.Addr().String()
	dead.Close()

	if r := Run(context.Background(), &Spec{ID: "a", Type: "tcp", TCP: &TCPParams{Address: open.Addr().String()}}, env()); r.Status != Up {
		t.Fatalf("open port through via: %+v", r)
	}
	if r := Run(context.Background(), &Spec{ID: "b", Type: "tcp", TCP: &TCPParams{Address: deadAddr}}, env()); r.Status != Down {
		t.Fatalf("dead port through via reported up: %+v", r)
	}
}

// Failure text reaches alerts and viewers; a token in the URL must stay out of it.
func TestHTTPErrorHidesURL(t *testing.T) {
	s := &Spec{ID: "h", Type: "http", Timeout: Duration(2 * time.Second), HTTP: &HTTPParams{URL: "http://127.0.0.1:1/x?token=hunter2"}}
	r := Run(context.Background(), s, &Env{})
	if r.Status != Down || r.Reason != "refused" && r.Reason != "error" || strings.Contains(r.Error, "hunter2") {
		t.Fatalf("%+v", r)
	}
}

// An HTTPS check through an xray proxy: the dialer reports ConnectDone from
// xray's goroutine while the transport does the TLS handshake in its own.
// The unguarded phases raced only now and then (TestViewerDoesNotSeeSecrets
// caught it once in twelve runs), so this exercises the path rather than
// proving the fix.
func TestHTTPSThroughViaPhases(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) }))
	defer srv.Close()
	socks := startSOCKS(t)
	via, _ := xray.ParseLink("socks5://" + socks)
	viaJSON, _ := json.Marshal(via.Outbound)
	inst, err := xray.Start(nil, via.Outbound, "")
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	for i := range 20 {
		s := &Spec{ID: "h", Type: "http", HTTP: &HTTPParams{URL: srv.URL, Insecure: true}}
		r := Run(context.Background(), s, &Env{Dial: inst.DialContext, Via: viaJSON})
		if r.Status != Up || r.Phases["tls"] == 0 {
			t.Fatalf("run %d: %+v", i, r)
		}
	}
}
