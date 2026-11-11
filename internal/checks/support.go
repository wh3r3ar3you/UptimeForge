package checks

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/xray"
)

// Drivers put DSNs and URLs into their errors, and a Result's Error reaches
// alerts and viewers.
var (
	scrubURLCreds = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^\s/@]+@`)
	// xray-core quotes the key it rejects: invalid "password": <pbk>.
	scrubKeyValue = regexp.MustCompile(`(?i)\b(password|passwd|pwd|pass|token|secret|api[_-]?key|access[_-]?key|authorization|` +
		`short[_-]?id|public[_-]?key|private[_-]?key|pbk|sid|uuid)("?\s*[=:]\s*"?)[^\s&;,"]+`)
)

// outboundSecretKeys are the keys of an Xray outbound whose values are
// credentials: VLESS/VMess ids, passwords, Reality keys and short ids.
var outboundSecretKeys = map[string]bool{
	"id": true, "password": true, "pass": true, "publickey": true, "privatekey": true, "shortid": true,
	"secret": true, "presharedkey": true, "auth": true, "seed": true,
}

// outboundSecrets collects the credential values of an Xray outbound. Very
// short values (a 2-digit short id) are left to the key=value scrubber:
// replacing them everywhere would mangle the text without hiding anything.
func outboundSecrets(v any, out []string) []string {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if s, ok := val.(string); ok && outboundSecretKeys[strings.ToLower(k)] && len(s) >= 6 {
				out = append(out, s)
				continue
			}
			out = outboundSecrets(val, out)
		}
	case []any:
		for _, val := range x {
			out = outboundSecrets(val, out)
		}
	}
	return out
}

const scrubbed = "***"

// scrubText strips the given secret values, credentials in URLs and
// key=value credentials from a text.
func scrubText(text string, secrets []string) string {
	if text == "" {
		return text
	}
	for _, v := range secrets {
		if v == "" {
			continue
		}
		text = strings.ReplaceAll(text, v, scrubbed)
		for _, enc := range []string{url.QueryEscape(v), url.PathEscape(v)} {
			if enc != v {
				text = strings.ReplaceAll(text, enc, scrubbed)
			}
		}
	}
	text = scrubURLCreds.ReplaceAllString(text, "${1}"+scrubbed+"@")
	return scrubKeyValue.ReplaceAllString(text, "${1}${2}"+scrubbed)
}

// secretValues lists what must not appear in this spec's results.
func secretValues(s *Spec, via json.RawMessage) []string {
	var out []string
	if t, ok := TypeOf(s.Type); ok && t.target != nil {
		for _, f := range t.Fields {
			if f.Secret {
				out = append(out, s.Params[f.Name])
			}
		}
	}
	if h := s.HTTP; h != nil {
		out = append(out, h.BasicPassword, h.BearerToken, h.TLSKey)
	}
	if x := s.Xray; x != nil {
		out = append(out, x.Link, x.Subscription)
		if l, err := xray.ParseLink(x.Link); err == nil {
			out = outboundSecrets(map[string]any(l.Outbound), out)
		}
		out = outboundSecrets(x.Outbound, out)
	}
	if m := s.MTProto; m != nil {
		out = append(out, m.Secret, m.Link)
		if u, err := url.Parse(m.Link); err == nil {
			out = append(out, u.Query().Get("secret"))
		}
	}
	// The probe's own upstream proxy: its errors reach every result.
	if len(via) > 0 {
		var ob map[string]any
		if json.Unmarshal(via, &ob) == nil {
			out = outboundSecrets(ob, out)
		}
	}
	return out
}

// ScrubVia strips the credentials of a probe's upstream proxy, and any
// key=value or URL credentials, from a text bound for a log.
func ScrubVia(text string, via json.RawMessage) string {
	return scrubText(text, secretValues(&Spec{}, via))
}

func scrubResult(r *Result, s *Spec, via json.RawMessage) {
	secrets := secretValues(s, via)
	r.Error = scrubText(r.Error, secrets)
	for k, v := range r.Info {
		r.Info[k] = scrubText(v, secrets)
	}
}

// plainErr drops the url.Error wrapper, which quotes the whole URL.
func plainErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// closeOnDone closes c when ctx ends. Deadlines are not enough: connections
// through an upstream proxy ignore them. The caller defers the returned stop.
func closeOnDone(ctx context.Context, c io.Closer) func() bool {
	return context.AfterFunc(ctx, func() { c.Close() })
}

// errTooMuch is a reply past the size a protocol's answer can have.
var errTooMuch = errors.New("the server sent more than an answer can be")

// capReader fails reads past n bytes: a server that never ends its line or
// its reply must not grow a buffer until the timeout.
type capReader struct {
	r io.Reader
	n int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.n <= 0 {
		return 0, errTooMuch
	}
	if int64(len(p)) > c.n {
		p = p[:c.n]
	}
	n, err := c.r.Read(p)
	c.n -= int64(n)
	return n, err
}

// capConn is rw with its reads capped at n bytes.
func capConn(rw io.ReadWriteCloser, n int64) io.ReadWriteCloser {
	return struct {
		io.Reader
		io.Writer
		io.Closer
	}{&capReader{rw, n}, rw, rw}
}

// clientTLS returns the settings of a connection to host from the shared
// tls and insecure fields.
func clientTLS(p params, host string) *tls.Config {
	return &tls.Config{ServerName: host, InsecureSkipVerify: p.bool("insecure")}
}

// udpReasonOf is reasonOf for datagrams. An ICMP port unreachable surfaces as
// a reset on Windows and a refused on Linux; both mean nothing listens there.
func udpReasonOf(ctx context.Context, err error) string {
	r := reasonOf(ctx, err)
	if r == "reset" {
		return "refused"
	}
	return r
}

// failUDP words a failed datagram exchange. An upstream proxy without UDP closes
// the stream at once, which is about the proxy and not about what is checked.
func failUDP(ctx context.Context, env *Env, err error) Result {
	if env.Via != nil && ctx.Err() == nil && strings.Contains(err.Error(), "closed pipe") {
		return fail("unavailable", errors.New("this probe's upstream proxy does not carry UDP"))
	}
	return fail(udpReasonOf(ctx, err), err)
}

// udpExchange sends req and re-sends it until accept takes a reply or ctx ends.
// conn is closed when ctx ends. Replies accept refuses are skipped, they can be
// late answers to an earlier attempt.
func udpExchange(ctx context.Context, conn net.Conn, req []byte, accept func([]byte) bool) ([]byte, error) {
	stop := closeOnDone(ctx, conn)
	defer stop()
	if _, err := conn.Write(req); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				conn.Write(req) // a failure here shows up in the read below
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	buf := make([]byte, 65535)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return nil, err
		}
		if accept(buf[:n]) {
			return slices.Clone(buf[:n]), nil
		}
	}
}

// tlsHandshakeTimeout bounds a TLS handshake inside the check's own timeout.
// It can fire first: reasonOf words it as a timeout. A test shortens it.
var tlsHandshakeTimeout = 10 * time.Second

// newHTTPClient makes a one-shot client that reaches the world like this probe
// does. Call CloseIdleConnections on it when done.
func newHTTPClient(env *Env, insecure bool) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext:         env.Dial,
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: insecure},
		TLSHandshakeTimeout: tlsHandshakeTimeout,
		DisableKeepAlives:   true,
	}}
}

// parseHeaders reads a JSON object of strings, the form Kuma stores headers in.
func parseHeaders(text string) (map[string]string, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	var h map[string]string
	if err := json.Unmarshal([]byte(text), &h); err != nil {
		return nil, errors.New("must be a JSON object of strings")
	}
	return h, nil
}

// setHeaders puts user headers on req. Go sends req.Host and ignores a Host
// entry in the header map, so that one goes to req.Host (virtual hosts,
// domain fronting).
func setHeaders(req *http.Request, h map[string]string) {
	for k, v := range h {
		if strings.EqualFold(k, "Host") {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
}

// compareOps are the operators of value comparisons, in form order.
var compareOps = []string{"==", "!=", ">", "<", ">=", "<=", "contains"}

// compareValue compares as numbers when both sides are numbers, as text
// otherwise. Ordering operators need numbers: "10" > "9" must not be decided
// by which string sorts first.
func compareValue(got, op, want string) bool {
	gn, gerr := strconv.ParseFloat(strings.TrimSpace(got), 64)
	wn, werr := strconv.ParseFloat(strings.TrimSpace(want), 64)
	numeric := gerr == nil && werr == nil
	switch op {
	case "", "==":
		if numeric {
			return gn == wn
		}
		return got == want
	case "!=":
		if numeric {
			return gn != wn
		}
		return got != want
	case "contains":
		return strings.Contains(got, want)
	case ">":
		return numeric && gn > wn
	case "<":
		return numeric && gn < wn
	case ">=":
		return numeric && gn >= wn
	case "<=":
		return numeric && gn <= wn
	}
	return false
}

// runCmd runs a local tool without a shell. A test can replace it.
var runCmd = func(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error) {
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	// A killed tool can leave children holding the pipes.
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	return out.Bytes(), errb.Bytes(), err
}

// failCmd words a tool that could not run or exited with an error it has no
// more specific meaning for. A missing binary is the probe's own state, not
// the state of what is checked.
func failCmd(ctx context.Context, tool string, stdout, stderr []byte, err error) Result {
	if errors.Is(err, exec.ErrNotFound) {
		return fail("unavailable", fmt.Errorf("%s is not installed on this probe", tool))
	}
	if ctx.Err() != nil {
		return fail("timeout", fmt.Errorf("%s did not finish in time", tool))
	}
	return fail("error", fmt.Errorf("%s: %s", tool, cmdOutput(stdout, stderr, err)))
}

// cmdOutput is the trimmed output of a tool, or its exit error if it said nothing.
func cmdOutput(stdout, stderr []byte, err error) string {
	out := strings.TrimSpace(string(stderr))
	if out == "" {
		out = strings.TrimSpace(string(stdout))
	}
	if out == "" && err != nil {
		out = err.Error()
	}
	if len(out) > 200 {
		out = out[:200] + "…"
	}
	return out
}
