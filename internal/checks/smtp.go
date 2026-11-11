package checks

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

func init() {
	registerType(TypeInfo{
		Type: "smtp", Category: "network", Keyword: true,
		Fields: []Field{fHost, fPort("25"),
			{Name: "smtp_security", Kind: "select", Default: "none", Options: []string{"none", "starttls", "smtps"}},
			fInsecure,
		},
		target: targetAddr,
	}, checkSMTP)
}

// checkSMTP greets and leaves: nothing is sent, the answer to EHLO is what
// tells a live mail server from a port that only accepts connections.
func checkSMTP(ctx context.Context, s *Spec, env *Env) Result {
	p := paramsOf(s)
	start := time.Now()
	raw, err := env.Dial(ctx, "tcp", p.addr())
	if err != nil {
		return fail(reasonOf(ctx, err), err)
	}
	conn := raw
	defer func() { conn.Close() }()
	stop := closeOnDone(ctx, raw)
	defer stop()

	if p["smtp_security"] == "smtps" {
		tc := tls.Client(raw, clientTLS(p, p["host"]))
		if err := tc.HandshakeContext(ctx); err != nil {
			return fail(reasonOf(ctx, err), err)
		}
		conn = tc
	}
	tp := textproto.NewConn(capConn(conn, smtpReplyCap))
	banner, err := smtpRead(tp, 220)
	if err != nil {
		return smtpFail(ctx, "greeting", err)
	}
	caps, err := smtpEhlo(tp)
	if err != nil {
		return smtpFail(ctx, "EHLO", err)
	}
	info := map[string]string{"banner": clip(firstLine(banner), 120)}
	if p["smtp_security"] == "starttls" {
		if !strings.Contains(strings.ToUpper(caps), "STARTTLS") {
			return Result{Status: Down, Reason: "tls", Error: "server does not offer STARTTLS", Text: banner, Info: info}
		}
		if err := tp.PrintfLine("STARTTLS"); err != nil {
			return smtpFail(ctx, "STARTTLS", err)
		}
		if _, err := smtpRead(tp, 220); err != nil {
			return smtpFail(ctx, "STARTTLS", err)
		}
		tc := tls.Client(raw, clientTLS(p, p["host"]))
		if err := tc.HandshakeContext(ctx); err != nil {
			return fail(reasonOf(ctx, err), err)
		}
		conn = tc
		tp = textproto.NewConn(capConn(tc, smtpReplyCap))
		if _, err := smtpEhlo(tp); err != nil {
			return smtpFail(ctx, "EHLO after STARTTLS", err)
		}
	}
	tp.PrintfLine("QUIT")
	return Result{LatencyMS: ms(time.Since(start)), Text: banner, Info: info}
}

func smtpEhlo(tp *textproto.Conn) (string, error) {
	if err := tp.PrintfLine("EHLO uptimeforge.invalid"); err != nil {
		return "", err
	}
	return smtpRead(tp, 250)
}

// A whole SMTP dialogue (banner, EHLO, STARTTLS) is a few KB; a server that
// keeps talking is cut here.
const (
	smtpReplyCap = 256 << 10
	smtpMaxLines = 100
)

// smtpRead reads a possibly multi-line reply. textproto's ReadResponse keeps
// reading after a line it could not parse when that line has a dash in the
// fourth position, so "SSH-2.0-OpenSSH" would wait out the timeout.
func smtpRead(tp *textproto.Conn, expect int) (string, error) {
	var msg []string
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return "", err
		}
		if len(msg) >= smtpMaxLines {
			return "", textproto.ProtocolError("a reply of more than 100 lines")
		}
		if len(line) < 4 || line[3] != ' ' && line[3] != '-' {
			return "", textproto.ProtocolError("not an SMTP answer")
		}
		code, err := strconv.Atoi(line[:3])
		if err != nil || code < 100 {
			return "", textproto.ProtocolError("not an SMTP answer")
		}
		msg = append(msg, line[4:])
		if line[3] == ' ' {
			text := strings.Join(msg, "\n")
			if code != expect {
				return "", &textproto.Error{Code: code, Msg: text}
			}
			return text, nil
		}
	}
}

func smtpFail(ctx context.Context, stage string, err error) Result {
	var perr *textproto.Error
	var proto textproto.ProtocolError
	if errors.As(err, &perr) {
		return fail("bad_response", fmt.Errorf("%s: %d %s", stage, perr.Code, clip(perr.Msg, 200)))
	}
	if errors.As(err, &proto) || errors.Is(err, errTooMuch) {
		return fail("bad_response", fmt.Errorf("%s: not an SMTP answer", stage))
	}
	return fail(reasonOf(ctx, err), fmt.Errorf("%s: %w", stage, err))
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
