package checks

import (
	"bytes"
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

// chatty serves a TCP server that sends chunk until 4 MB are out, then holds
// the connection: a server that never ends its line or its reply.
func chatty(t *testing.T, chunk string) (string, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	block := bytes.Repeat([]byte(chunk), 64<<10/len(chunk)+1)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				for sent := 0; sent < 4<<20; sent += len(block) {
					if _, err := c.Write(block); err != nil {
						return
					}
				}
				io.Copy(io.Discard, c) // until the client goes away
			}()
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", strconv.Itoa(addr.Port)
}

// A reply that does not end is cut at a limit and is a bad response, not a
// buffer that grows until the timeout.
func TestEndlessRepliesAreCut(t *testing.T) {
	for _, c := range []struct{ typ, chunk string }{
		{"redis", "x"},
		{"smtp", "220-still talking\r\n"},
		{"sip-options", "x"},
	} {
		h, p := chatty(t, c.chunk)
		params := map[string]string{"host": h, "port": p}
		if c.typ == "sip-options" {
			params["transport"] = "tcp"
		}
		start := time.Now()
		r := runSpec(t, &Spec{Type: c.typ, Timeout: Duration(3 * time.Second), Params: params})
		if r.Reason != "bad_response" || time.Since(start) > 2*time.Second {
			t.Errorf("%s: reason %q after %v (%s), want bad_response at once", c.typ, r.Reason, time.Since(start).Round(time.Millisecond), r.Error)
		}
	}
}
