package checks

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptrace"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ohler55/ojg/jp"
	"github.com/ohler55/ojg/oj"
)

func init() {
	register("http", checkHTTP)
	register("tcp", checkTCP)
	register("tls", checkTLS)
	register("dns", checkDNS)
}

func checkHTTP(ctx context.Context, s *Spec, env *Env) Result {
	p := s.HTTP
	tlsCfg := &tls.Config{InsecureSkipVerify: p.Insecure}
	if p.TLSCert != "" {
		cert, err := tls.X509KeyPair([]byte(p.TLSCert), []byte(p.TLSKey))
		if err != nil {
			return fail("config", fmt.Errorf("client certificate: %w", err))
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}
	tr := &http.Transport{
		DialContext:         env.Dial,
		TLSClientConfig:     tlsCfg,
		TLSHandshakeTimeout: tlsHandshakeTimeout,
		DisableKeepAlives:   true,
	}
	defer tr.CloseIdleConnections()
	maxRedirects := 10
	if p.MaxRedirects != nil {
		maxRedirects = *p.MaxRedirects
	}
	client := &http.Client{Transport: tr, CheckRedirect: func(_ *http.Request, via []*http.Request) error {
		switch {
		case maxRedirects == 0:
			return http.ErrUseLastResponse // the redirect itself is the answer
		case len(via) > maxRedirects:
			return errTooManyRedirects
		}
		return nil
	}}

	// The trace context reaches the dialer too: through an xray proxy,
	// ConnectDone fires in xray's goroutine while the transport runs the TLS
	// handshake in its own, so the phases are guarded.
	var mu sync.Mutex
	phases := map[string]float64{}
	var start, connStart, tlsStart time.Time
	set := func(k string, since *time.Time) {
		mu.Lock()
		phases[k] = ms(time.Since(*since))
		mu.Unlock()
	}
	mark := func(t *time.Time) {
		mu.Lock()
		*t = time.Now()
		mu.Unlock()
	}
	trace := &httptrace.ClientTrace{
		ConnectStart:         func(string, string) { mark(&connStart) },
		ConnectDone:          func(string, string, error) { set("connect", &connStart) },
		TLSHandshakeStart:    func() { mark(&tlsStart) },
		TLSHandshakeDone:     func(tls.ConnectionState, error) { set("tls", &tlsStart) },
		GotFirstResponseByte: func() { set("ttfb", &start) },
	}
	method := p.Method
	if method == "" {
		method = http.MethodGet
		if p.Body != "" {
			method = http.MethodPost // most servers ignore a GET's body, which would look like a pass
		}
	}
	var reqBody io.Reader
	if p.Body != "" {
		reqBody = strings.NewReader(p.Body)
	}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), method, p.URL, reqBody)
	if err != nil {
		return fail("config", plainErr(err))
	}
	req.Header.Set("User-Agent", "UptimeForge/1.0")
	if p.Body != "" && json.Valid([]byte(p.Body)) {
		req.Header.Set("Content-Type", "application/json")
	}
	switch {
	case p.BearerToken != "":
		req.Header.Set("Authorization", "Bearer "+p.BearerToken)
	case p.BasicUser != "" || p.BasicPassword != "":
		req.SetBasicAuth(p.BasicUser, p.BasicPassword)
	}
	setHeaders(req, p.Headers)
	start = time.Now()
	resp, err := client.Do(req)
	if err != nil {
		err = plainErr(err) // the URL may carry a token, and this text reaches alerts and viewers
		if errors.Is(err, errTooManyRedirects) {
			return fail("redirects", fmt.Errorf("more than %d redirects", maxRedirects))
		}
		return fail(reasonOf(ctx, err), err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fail(reasonOf(ctx, err), err)
	}
	mu.Lock()
	got := maps.Clone(phases) // a late hook from the dialer must not write into the result
	mu.Unlock()
	r := Result{LatencyMS: ms(time.Since(start)), Phases: got, Bytes: int64(len(body)),
		Info: map[string]string{"status_code": strconv.Itoa(resp.StatusCode)}, Text: string(body)}
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		r.Info["cert_days_left"] = strconv.Itoa(daysLeft(resp.TLS.PeerCertificates[0].NotAfter))
	}

	switch {
	case !p.statusOK(resp.StatusCode):
		r.Status, r.Reason, r.Error = Down, "http_status", fmt.Sprintf("HTTP %d", resp.StatusCode)
	case p.Keyword != "" && !p.InvertKeyword && !strings.Contains(r.Text, p.Keyword):
		r.Status, r.Reason, r.Error = Down, "keyword", fmt.Sprintf("keyword %q not found", p.Keyword)
	case p.Keyword != "" && p.InvertKeyword && strings.Contains(r.Text, p.Keyword):
		r.Status, r.Reason, r.Error = Down, "keyword", fmt.Sprintf("keyword %q found, expected it to be absent", p.Keyword)
	case p.JSONPath != "":
		if reason, err := p.matchJSON(body); err != nil {
			r.Status, r.Reason, r.Error = Down, reason, err.Error()
		}
	}
	return r
}

var errTooManyRedirects = errors.New("too many redirects")

func (p *HTTPParams) validate() error {
	for _, s := range p.AcceptedStatus {
		if _, _, err := statusRange(s); err != nil {
			return err
		}
	}
	if (p.TLSCert == "") != (p.TLSKey == "") {
		return errors.New("tls_cert and tls_key go together")
	}
	if p.TLSCert != "" {
		if _, err := tls.X509KeyPair([]byte(p.TLSCert), []byte(p.TLSKey)); err != nil {
			return fmt.Errorf("client certificate: %w", err)
		}
	}
	if p.MaxRedirects != nil && *p.MaxRedirects < 0 {
		return errors.New("max_redirects must not be negative")
	}
	if p.JSONPath != "" {
		if _, err := jp.ParseString(p.JSONPath); err != nil {
			return fmt.Errorf("json_path: %w", err)
		}
		if p.JSONOp != "" && !slices.Contains(compareOps, p.JSONOp) {
			return fmt.Errorf("json_op must be one of %s", strings.Join(compareOps, " "))
		}
	}
	return nil
}

func (p *HTTPParams) statusOK(code int) bool {
	if len(p.ExpectStatus) == 0 && len(p.AcceptedStatus) == 0 {
		return code >= 200 && code < 400
	}
	if slices.Contains(p.ExpectStatus, code) {
		return true
	}
	return slices.ContainsFunc(p.AcceptedStatus, func(s string) bool {
		lo, hi, err := statusRange(s)
		return err == nil && code >= lo && code <= hi
	})
}

// statusRange reads "200-299" or a single "404".
func statusRange(s string) (lo, hi int, err error) {
	a, b, isRange := strings.Cut(strings.TrimSpace(s), "-")
	lo, err = strconv.Atoi(a)
	hi = lo
	if err == nil && isRange {
		hi, err = strconv.Atoi(b)
	}
	if err != nil || lo < 100 || hi > 599 || lo > hi {
		return 0, 0, fmt.Errorf("bad status range %q, expected like 200-299 or 404", s)
	}
	return lo, hi, nil
}

// matchJSON applies JSONPath to the body. A path can select several values:
// "!=" must hold for all of them, the other operators for any.
func (p *HTTPParams) matchJSON(body []byte) (reason string, err error) {
	x, err := jp.ParseString(p.JSONPath)
	if err != nil {
		return "config", fmt.Errorf("json_path: %w", err)
	}
	doc, err := oj.Parse(body)
	if err != nil {
		return "bad_response", errors.New("response is not valid JSON")
	}
	values := x.Get(doc)
	if len(values) == 0 {
		return "value_mismatch", fmt.Errorf("%s selects nothing in the response", p.JSONPath)
	}
	got := make([]string, len(values))
	for i, v := range values {
		got[i] = jsonText(v)
	}
	match := func(v string) bool { return compareValue(v, p.JSONOp, p.JSONExpect) }
	ok := slices.ContainsFunc(got, match)
	if p.JSONOp == "!=" {
		ok = !slices.ContainsFunc(got, func(v string) bool { return !match(v) })
	}
	if !ok {
		op := p.JSONOp
		if op == "" {
			op = "=="
		}
		return "value_mismatch", fmt.Errorf("%s is %s, expected %s %s", p.JSONPath, clip(strings.Join(got, ", "), 200), op, p.JSONExpect)
	}
	return "", nil
}

func jsonText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return oj.JSON(v)
}

func clip(s string, n int) string {
	if len(s) > n {
		return strings.ToValidUTF8(s[:n], "") + "…" // the cut can fall inside a rune
	}
	return s
}

func checkTCP(ctx context.Context, s *Spec, env *Env) Result {
	start := time.Now()
	conn, err := env.Dial(ctx, "tcp", s.TCP.Address)
	if err != nil {
		return fail(reasonOf(ctx, err), err)
	}
	defer conn.Close()
	latency := ms(time.Since(start))
	if env.Via != nil {
		// Through a tunnel the dial returns before the far end connects; a
		// refused or unreachable target shows up as the stream closing at once.
		if err := tunnelSettled(ctx, conn); err != nil {
			return fail("refused", fmt.Errorf("%s: connection closed by the far end (%v)", s.TCP.Address, err))
		}
	}
	return Result{LatencyMS: latency}
}

// tunnelSettled waits briefly for the remote side: data or silence means the
// connection is up, an immediate EOF/reset means the proxy couldn't connect.
func tunnelSettled(ctx context.Context, conn net.Conn) error {
	errc := make(chan error, 1)
	go func() {
		var b [1]byte
		_, err := conn.Read(b[:])
		errc <- err
	}()
	select {
	case err := <-errc:
		return err
	case <-time.After(1500 * time.Millisecond):
		return nil
	case <-ctx.Done():
		return nil
	}
}

func checkTLS(ctx context.Context, s *Spec, env *Env) Result {
	p := s.TLS
	start := time.Now()
	raw, err := env.Dial(ctx, "tcp", p.Address)
	if err != nil {
		return fail(reasonOf(ctx, err), err)
	}
	connected := time.Since(start)
	sni := p.ServerName
	if sni == "" {
		sni, _, _ = net.SplitHostPort(p.Address)
	}
	conn := tls.Client(raw, &tls.Config{ServerName: sni})
	defer conn.Close()
	if err := conn.HandshakeContext(ctx); err != nil {
		return fail(reasonOf(ctx, err), err)
	}
	cert := conn.ConnectionState().PeerCertificates[0]
	days := daysLeft(cert.NotAfter)
	r := Result{
		LatencyMS: ms(time.Since(start)),
		Phases:    map[string]float64{"connect": ms(connected), "tls": ms(time.Since(start) - connected)},
		Info: map[string]string{
			"cert_days_left": strconv.Itoa(days),
			"cert_not_after": cert.NotAfter.UTC().Format(time.DateOnly),
			"cert_issuer":    cert.Issuer.CommonName,
		},
	}
	if days < p.expiryDays() {
		r.Status, r.Reason = Down, "cert_expiring"
		r.Error = fmt.Sprintf("certificate expires in %d days (%s)", days, r.Info["cert_not_after"])
	}
	return r
}

// expiryDays is how many days a certificate must still have; 0 never alerts.
func (p *TLSParams) expiryDays() int {
	if p.MinDays == nil {
		return 7
	}
	return max(*p.MinDays, 0)
}

func checkDNS(ctx context.Context, s *Spec, env *Env) Result {
	p := s.DNS
	// A record type, an explicit server or an upstream proxy asks the server
	// directly. Only a plain address check goes through the system resolver.
	if p.RecordType != "" || p.Server != "" || env.Via != nil {
		return checkDNSRecord(ctx, p, env)
	}
	start := time.Now()
	addrs, err := net.DefaultResolver.LookupHost(ctx, p.Name)
	if err != nil {
		return fail(reasonOf(ctx, err), err)
	}
	r := Result{LatencyMS: ms(time.Since(start)), Info: map[string]string{"answers": strings.Join(addrs, ",")},
		Text: strings.Join(addrs, "\n")}
	if p.Expect != "" && !slices.Contains(addrs, p.Expect) {
		r.Status, r.Reason, r.Error = Down, "dns_mismatch", fmt.Sprintf("expected %s, got %s", p.Expect, strings.Join(addrs, ", "))
	}
	return r
}

// serverAddr is the DNS server to ask as host:port. Port overrides one inside
// Server; a server without either uses the DNS port.
func (p *DNSParams) serverAddr() string {
	server := p.Server
	if server == "" {
		server = "1.1.1.1"
	}
	host, port, err := net.SplitHostPort(server)
	if err != nil {
		host, port = strings.Trim(server, "[]"), "53"
	}
	if p.Port != 0 {
		port = strconv.Itoa(p.Port)
	}
	return net.JoinHostPort(host, port)
}

func daysLeft(t time.Time) int { return int(time.Until(t).Hours() / 24) }
