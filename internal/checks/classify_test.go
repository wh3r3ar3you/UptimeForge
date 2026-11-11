package checks

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

// blackhole accepts connections and never answers: what a DPI box that drops
// everything after the ClientHello looks like from the probe.
func blackhole(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var held []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		for _, c := range held {
			c.Close()
		}
		mu.Unlock()
	})
	return ln.Addr().String()
}

// shortHandshake makes the transport's own TLS handshake timeout fire well
// before the check's timeout, as the 10 s one does before the 15 s default.
func shortHandshake(t *testing.T) {
	old := tlsHandshakeTimeout
	tlsHandshakeTimeout = 300 * time.Millisecond
	t.Cleanup(func() { tlsHandshakeTimeout = old })
}

// A blackhole after the ClientHello is a timeout, not a generic error.
func TestHTTPHandshakeBlackholeIsTimeout(t *testing.T) {
	shortHandshake(t)
	addr := blackhole(t)
	s := &Spec{ID: "x", Type: "http", Timeout: Duration(5 * time.Second), HTTP: &HTTPParams{URL: "https://" + addr + "/"}}
	if r := Run(context.Background(), s, &Env{}); r.Reason != "timeout" {
		t.Fatalf("reason %q (%s), want timeout", r.Reason, r.Error)
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "the far end went quiet" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// Timeouts are recognised by type, whatever their text says.
func TestReasonOfTimeoutByType(t *testing.T) {
	for _, err := range []error{timeoutErr{}, errors.New("net/http: TLS handshake timeout")} {
		if got := reasonOf(context.Background(), err); got != "timeout" {
			t.Errorf("%v: reason %q, want timeout", err, got)
		}
	}
}

// A closed port behind an xray link is a refused connection, not a failed
// handshake: the server is down, nothing is wrong with the keys.
func TestXrayRefusedServer(t *testing.T) {
	host, port := closedPort(t)
	link := "vless://11111111-2222-3333-4444-555555555555@" + net.JoinHostPort(host, port) + "?type=tcp&security=none#n"
	s := &Spec{ID: "x", Type: "xray", Timeout: Duration(10 * time.Second), Xray: &XrayParams{Link: link, HeavyEvery: -1, URL: "http://127.0.0.1:9/"}}
	if r := Run(context.Background(), s, &Env{Seq: 1}); r.Reason != "refused" {
		t.Fatalf("reason %q (%s), want refused", r.Reason, r.Error)
	}
}
