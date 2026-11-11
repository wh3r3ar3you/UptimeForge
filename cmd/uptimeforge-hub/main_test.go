package main

import (
	"net/http"
	"testing"
)

// A client that trickles a request or holds idle connections open must not
// tie up the hub forever.
func TestServerTimeouts(t *testing.T) {
	srv := newServer(":0", http.NotFoundHandler())
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 || srv.IdleTimeout <= 0 {
		t.Fatalf("timeouts: header %v, read %v, idle %v", srv.ReadHeaderTimeout, srv.ReadTimeout, srv.IdleTimeout)
	}
	if srv.WriteTimeout != 0 {
		t.Fatal("a write timeout would cut agent downloads and long tool runs")
	}
}
