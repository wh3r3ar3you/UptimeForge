package xray

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"time"
)

const (
	DefaultProbeURL      = "https://www.gstatic.com/generate_204"
	DefaultDownloadURL   = "https://speed.cloudflare.com/__down?bytes=131072"
	DefaultDownloadBytes = 128 << 10
	DefaultStallTimeout  = 5 * time.Second
	DefaultTraceURL      = "https://www.cloudflare.com/cdn-cgi/trace"
)

// tlsHandshakeTimeout bounds the TLS handshake through the tunnel. It can fire
// before the check's timeout; classify words it as a timeout. A test shortens it.
var tlsHandshakeTimeout = 10 * time.Second

// ProbeOptions controls what a single probe run does.
type ProbeOptions struct {
	URL           string // light request; any status < 400 is fine
	Heavy         bool   // also download DownloadBytes from DownloadURL
	DownloadURL   string
	DownloadBytes int64
	StallTimeout  time.Duration
	Egress        bool // resolve the exit IP/location through TraceURL
	TraceURL      string
	Insecure      bool // skip TLS verification of the probe URLs (tests)
}

// ProbeResult is what a probe measured. Err is nil on success.
type ProbeResult struct {
	TTFB       time.Duration
	Total      time.Duration
	Download   time.Duration
	Bytes      int64
	Throughput float64 // bytes per second
	Stalled    bool
	EgressIP   string
	EgressLoc  string
	EgressErr  error // why the exit IP is unknown; it does not fail the probe
	Reason     string
	Err        error
}

// StallError means the tunnel stopped delivering data mid-transfer, the
// signature of volume-based DPI cut-offs (connection freezes after ~16 KB).
type StallError struct{ After int64 }

func (e *StallError) Error() string {
	return fmt.Sprintf("transfer stalled after %s", HumanBytes(e.After))
}

// Probe performs HTTP requests through dial and reports latency, throughput
// and where traffic exits.
func Probe(ctx context.Context, dial func(ctx context.Context, network, addr string) (net.Conn, error), o ProbeOptions) ProbeResult {
	start := time.Now()
	tr := &http.Transport{
		DialContext:         dial,
		TLSHandshakeTimeout: tlsHandshakeTimeout,
		TLSNextProto:        map[string]func(string, *tls.Conn) http.RoundTripper{}, // HTTP/1.1 only: predictable, one stream per conn
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: o.Insecure},
		DisableCompression:  true,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	var r ProbeResult
	fail := func(reason string, err error) ProbeResult {
		// The probe URLs are the user's and may carry a token: Go quotes them.
		r.Reason, r.Err, r.Total = reason, withoutURL(err), time.Since(start)
		return r
	}

	url := o.URL
	if url == "" {
		url = DefaultProbeURL
	}
	ttfb, status, err := lightRequest(ctx, client, url)
	if err != nil {
		return fail(classify(ctx, err), err)
	}
	if status >= 400 {
		return fail("http_status", fmt.Errorf("probe URL returned HTTP %d", status))
	}
	r.TTFB = ttfb

	if o.Heavy {
		dl, n, err := download(ctx, client, o)
		r.Download, r.Bytes = dl, n
		if dl > 0 {
			r.Throughput = float64(n) / dl.Seconds()
		}
		if err != nil {
			var se *StallError
			if errors.As(err, &se) {
				r.Stalled = true
				return fail("stalled", err)
			}
			return fail(classify(ctx, err), err)
		}
	}

	if o.Egress {
		trace := o.TraceURL
		if trace == "" {
			trace = DefaultTraceURL
		}
		// The exit location is informational; failing to get it doesn't fail
		// the probe. A check that expects an exit IP decides on EgressErr.
		r.EgressIP, r.EgressLoc, r.EgressErr = egress(ctx, client, trace)
		if r.EgressErr == nil && r.EgressIP == "" {
			r.EgressErr = errors.New("the trace answered without an IP")
		}
		r.EgressErr = withoutURL(r.EgressErr)
	}
	r.Total = time.Since(start)
	return r
}

func lightRequest(ctx context.Context, c *http.Client, url string) (time.Duration, int, error) {
	start := time.Now()
	var first time.Duration
	trace := &httptrace.ClientTrace{GotFirstResponseByte: func() { first = time.Since(start) }}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodGet, url, nil)
	if err != nil {
		return 0, 0, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, 0, err
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	return first, resp.StatusCode, nil
}

func download(ctx context.Context, c *http.Client, o ProbeOptions) (time.Duration, int64, error) {
	url, want := o.DownloadURL, o.DownloadBytes
	if url == "" {
		url = DefaultDownloadURL
	}
	if want <= 0 {
		want = DefaultDownloadBytes
	}
	stall := o.StallTimeout
	if stall <= 0 {
		stall = DefaultStallTimeout
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var got, lastProgress atomic.Int64
	var stalled atomic.Bool
	lastProgress.Store(time.Now().UnixNano())
	watchdogDone := make(chan struct{})
	defer close(watchdogDone)
	go func() {
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-watchdogDone:
				return
			case now := <-t.C:
				if now.UnixNano()-lastProgress.Load() > int64(stall) {
					stalled.Store(true)
					cancel()
					return
				}
			}
		}
	}()

	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, 0, err
	}
	resp, err := c.Do(req)
	if err != nil {
		if stalled.Load() {
			return time.Since(start), 0, &StallError{After: 0}
		}
		return 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return 0, 0, fmt.Errorf("download URL returned HTTP %d", resp.StatusCode)
	}
	buf := make([]byte, 32<<10)
	for got.Load() < want {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			got.Add(int64(n))
			lastProgress.Store(time.Now().UnixNano())
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			if stalled.Load() {
				return time.Since(start), got.Load(), &StallError{After: got.Load()}
			}
			return time.Since(start), got.Load(), err
		}
	}
	if n := got.Load(); n < want && (resp.ContentLength < 0 || n < resp.ContentLength) {
		return time.Since(start), n, fmt.Errorf("short download: %s of %s", HumanBytes(n), HumanBytes(want))
	}
	return time.Since(start), got.Load(), nil
}

func egress(ctx context.Context, c *http.Client, url string) (ip, loc string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("the trace answered HTTP %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 4<<10))
	for sc.Scan() {
		k, v, _ := strings.Cut(sc.Text(), "=")
		switch k {
		case "ip":
			ip = v
		case "loc":
			loc = v
		}
	}
	return ip, loc, nil
}

func classify(ctx context.Context, err error) string {
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	// The transport's TLS handshake timeout fires before the check's (a DPI
	// blackhole after the ClientHello) and says so only by type.
	if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
		return "timeout"
	}
	s := withoutURL(err).Error() // the probe URL is the user's: a word in it decides nothing
	switch {
	case strings.Contains(s, "handshake timeout") || strings.Contains(s, "i/o timeout"):
		return "timeout"
	case strings.Contains(s, "tls:") || strings.Contains(s, "x509") || strings.Contains(s, "REALITY"):
		return "tls"
	case strings.Contains(s, "connection refused") || strings.Contains(s, "actively refused"): // Linux, Windows
		return "refused"
	case strings.Contains(s, "no such host"):
		return "dns"
	case strings.Contains(s, "EOF") || strings.Contains(s, "reset") || strings.Contains(s, "closed"):
		return "handshake"
	}
	return "error"
}

// HumanBytes renders 16384 as "16.0 KB".
func HumanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
