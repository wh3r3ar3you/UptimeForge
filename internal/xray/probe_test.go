package xray

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	xcore "github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf/serial"
)

const testUUID = "b831381d-6324-4d53-ad4f-8cda48b30811"

func validate(t *testing.T, o Outbound) {
	t.Helper()
	cfg, err := BuildConfig(o, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := serial.LoadJSONConfig(bytes.NewReader(cfg)); err != nil {
		t.Fatalf("xray rejected outbound: %v\n%s", err, cfg)
	}
}

func TestOutboundsAcceptedByXray(t *testing.T) {
	for _, link := range []string{
		"vless://6b347655-eed3-42a8-9345-83ed50df2484@de1.example.com:443?type=tcp&security=reality&pbk=ncDrKG0PBim2ueW_moKKMcYliasKNCrZqmYjvu_6gVE&fp=chrome&sni=www.microsoft.com&sid=7db0f07cf92cf698&flow=xtls-rprx-vision#r",
		"vless://6b347655-eed3-42a8-9345-83ed50df2484@h.example:443?type=xhttp&security=tls&sni=cdn.example&path=%2Fx&mode=auto#xh",
		"trojan://pass@h.example:443?type=grpc&serviceName=svc#grpc",
		"hy2://pw@hy.example:443/?sni=hy.example&obfs=salamander&obfs-password=xyz#hy",
		"ss://2022-blake3-aes-128-gcm:MTIzNDU2Nzg5MDEyMzQ1Ng%3D%3D@1.2.3.4:8388#ss",
		"socks5://u:p@10.0.0.1:1080",
	} {
		l, err := ParseLink(link)
		if err != nil {
			t.Fatalf("%s: %v", link, err)
		}
		validate(t, l.Outbound)
	}
}

// startVLESSServer runs an in-process VLESS (no TLS) server on loopback.
func startVLESSServer(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	cfg := fmt.Sprintf(`{
	  "log": {"loglevel": "none"},
	  "inbounds": [{"listen": "127.0.0.1", "port": %d, "protocol": "vless",
	    "settings": {"clients": [{"id": %q}], "decryption": "none"}}],
	  "outbounds": [{"protocol": "freedom"}]
	}`, port, testUUID)
	pb, err := serial.LoadJSONConfig(strings.NewReader(cfg))
	if err != nil {
		t.Fatal(err)
	}
	inst, err := xcore.New(pb)
	if err != nil {
		t.Fatal(err)
	}
	if err := inst.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { inst.Close() })
	return port
}

// startTarget serves the endpoints a probe hits on the far side of the tunnel.
func startTarget(t *testing.T) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/204", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/down", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "131072")
		w.Write(make([]byte, 131072))
	})
	// /stall imitates a DPI box freezing the flow after 16 KB.
	mux.HandleFunc("/stall", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "131072")
		w.Write(make([]byte, 16<<10))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	mux.HandleFunc("/trace", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "fl=1\nip=203.0.113.7\nloc=DE\n")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(func() { close(release); srv.Close() })
	return srv
}

func TestProbeThroughVLESS(t *testing.T) {
	port := startVLESSServer(t)
	target := startTarget(t)

	link, err := ParseLink(fmt.Sprintf("vless://%s@127.0.0.1:%d?type=tcp&security=none#local", testUUID, port))
	if err != nil {
		t.Fatal(err)
	}
	inst, err := Start(link.Outbound, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	t.Run("ok", func(t *testing.T) {
		r := Probe(ctx, inst.DialContext, ProbeOptions{
			URL: target.URL + "/204", Heavy: true, DownloadURL: target.URL + "/down",
			Egress: true, TraceURL: target.URL + "/trace",
		})
		if r.Err != nil {
			t.Fatalf("probe failed: %v (%s)", r.Err, r.Reason)
		}
		if r.Bytes != 131072 || r.EgressIP != "203.0.113.7" || r.EgressLoc != "DE" || r.TTFB <= 0 {
			t.Fatalf("unexpected result: %+v", r)
		}
	})

	t.Run("stall after 16KB", func(t *testing.T) {
		start := time.Now()
		r := Probe(ctx, inst.DialContext, ProbeOptions{
			URL: target.URL + "/204", Heavy: true, DownloadURL: target.URL + "/stall", StallTimeout: time.Second,
		})
		if !r.Stalled || r.Reason != "stalled" || r.Bytes != 16<<10 {
			t.Fatalf("expected stall at 16 KB, got %+v", r)
		}
		if !strings.Contains(r.Err.Error(), "16.0 KB") {
			t.Fatalf("error should name the cut-off point: %v", r.Err)
		}
		if time.Since(start) > 5*time.Second {
			t.Fatal("stall detection is too slow")
		}
	})
}

func TestProbeWrongUUIDFails(t *testing.T) {
	port := startVLESSServer(t)
	target := startTarget(t)
	link, _ := ParseLink(fmt.Sprintf("vless://00000000-0000-0000-0000-000000000000@127.0.0.1:%d?type=tcp#bad", port))
	inst, err := Start(link.Outbound, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r := Probe(ctx, inst.DialContext, ProbeOptions{URL: target.URL + "/204"})
	if r.Err == nil {
		t.Fatal("probe with a wrong UUID must fail")
	}
}

func TestProbeChainedThroughVia(t *testing.T) {
	// The "via" hop is itself a VLESS server: target -> via -> internet.
	viaPort := startVLESSServer(t)
	targetPort := startVLESSServer(t)
	srv := startTarget(t)
	via, _ := ParseLink(fmt.Sprintf("vless://%s@127.0.0.1:%d#via", testUUID, viaPort))
	tgt, _ := ParseLink(fmt.Sprintf("vless://%s@127.0.0.1:%d#target", testUUID, targetPort))
	inst, err := Start(tgt.Outbound, via.Outbound, "")
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if r := Probe(ctx, inst.DialContext, ProbeOptions{URL: srv.URL + "/204"}); r.Err != nil {
		t.Fatalf("chained probe failed: %v", r.Err)
	}

	// Proof that traffic really takes the via hop: a broken via breaks the chain.
	badVia, _ := ParseLink(fmt.Sprintf("vless://00000000-0000-0000-0000-000000000000@127.0.0.1:%d#via", viaPort))
	broken, err := Start(tgt.Outbound, badVia.Outbound, "")
	if err != nil {
		t.Fatal(err)
	}
	defer broken.Close()
	if r := Probe(ctx, broken.DialContext, ProbeOptions{URL: srv.URL + "/204"}); r.Err == nil {
		t.Fatal("probe succeeded through a broken via hop: chaining is not applied")
	}
}
