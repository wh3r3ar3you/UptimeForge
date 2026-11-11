package xray

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"
)

func TestTCPEndpoint(t *testing.T) {
	l, _ := ParseLink("vless://" + testUUID + "@10.0.0.1:443?type=tcp&security=none#n")
	hy, _ := ParseLink("hy2://pw@10.0.0.1:443/?sni=a#hy")
	for _, c := range []struct {
		ob   Outbound
		want string
	}{
		{l.Outbound, "10.0.0.1:443"},
		{hy.Outbound, ""},
		{Outbound{"protocol": "vmess", "settings": map[string]any{"vnext": []any{map[string]any{"address": "h.example", "port": float64(8443)}}}}, "h.example:8443"},
		{Outbound{"protocol": "trojan", "settings": map[string]any{"servers": []any{map[string]any{"address": "h.example", "port": float64(443)}}},
			"streamSettings": map[string]any{"network": "kcp"}}, ""},
	} {
		if got := c.ob.TCPEndpoint(); got != c.want {
			t.Errorf("%v: got %q, want %q", c.ob, got, c.want)
		}
	}
}

// A word in the user's probe URL must not decide the reason.
func TestClassifyIgnoresURL(t *testing.T) {
	err := &url.Error{Op: "Get", URL: "https://tls.example.com/x509", Err: io.ErrUnexpectedEOF}
	if got := classify(context.Background(), err); got != "handshake" {
		t.Fatalf("reason %q, want handshake", got)
	}
}

type quietErr struct{}

func (quietErr) Error() string   { return "net/http: TLS handshake timeout" }
func (quietErr) Timeout() bool   { return true }
func (quietErr) Temporary() bool { return true }

func TestClassifyTimeoutByType(t *testing.T) {
	err := &url.Error{Op: "Get", URL: "https://example.com/", Err: quietErr{}}
	if got := classify(context.Background(), err); got != "timeout" {
		t.Fatalf("reason %q, want timeout", got)
	}
	if got := classify(context.Background(), errors.New("net/http: TLS handshake timeout")); got != "timeout" {
		t.Fatalf("plain text: reason %q, want timeout", got)
	}
}

// A blackhole after the ClientHello is a timeout even when the transport's own
// handshake timeout fires before the check's.
func TestProbeHandshakeBlackholeIsTimeout(t *testing.T) {
	old := tlsHandshakeTimeout
	tlsHandshakeTimeout = 300 * time.Millisecond
	defer func() { tlsHandshakeTimeout = old }()

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
	defer func() {
		ln.Close()
		mu.Lock()
		for _, c := range held {
			c.Close()
		}
		mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r := Probe(ctx, (&net.Dialer{}).DialContext, ProbeOptions{URL: "https://" + ln.Addr().String() + "/secret-token"})
	if r.Reason != "timeout" {
		t.Fatalf("reason %q (%v), want timeout", r.Reason, r.Err)
	}
}
