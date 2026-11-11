package checks

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

func init() {
	registerType(TypeInfo{
		Type: "sip-options", Category: "network",
		Fields: []Field{fHost, fPort("5060"),
			{Name: "transport", Kind: "select", Default: "udp", Options: []string{"udp", "tcp", "tls"}},
			fInsecure,
		},
		target: targetAddr,
	}, checkSIP)
}

// checkSIP sends OPTIONS (RFC 3261 11) and wants a final 2xx.
func checkSIP(ctx context.Context, s *Spec, env *Env) Result {
	p := paramsOf(s)
	network := "tcp"
	if p["transport"] == "udp" {
		network = "udp"
	}
	start := time.Now()
	conn, err := env.Dial(ctx, network, p.addr())
	if err != nil {
		return fail(reasonOf(ctx, err), err)
	}
	defer func() { conn.Close() }()
	if p["transport"] == "tls" {
		tc := tls.Client(conn, clientTLS(p, p["host"]))
		stop := closeOnDone(ctx, conn)
		err := tc.HandshakeContext(ctx)
		stop()
		if err != nil {
			return fail(reasonOf(ctx, err), err)
		}
		conn = tc
	}
	callID := sipToken()
	req := sipOptions(p, conn.LocalAddr(), callID)
	var status int
	var phrase string
	if network == "udp" {
		_, err = udpExchange(ctx, conn, []byte(req), func(b []byte) bool {
			code, why, ok := sipParse(string(b), callID)
			if !ok || code < 200 { // 1xx is only progress
				return false
			}
			status, phrase = code, why
			return true
		})
		if err != nil {
			return failUDP(ctx, env, fmt.Errorf("no SIP answer: %w", err))
		}
	} else {
		stop := closeOnDone(ctx, conn)
		defer stop()
		if _, err := conn.Write([]byte(req)); err != nil {
			return fail(reasonOf(ctx, err), err)
		}
		// Provisional answers and the final one fit easily; a stream that
		// never ends a line or a head is cut.
		status, phrase, err = sipReadStream(bufio.NewReader(&capReader{conn, 256 << 10}), callID)
		if err != nil {
			if errors.Is(err, errSIPBad) || errors.Is(err, errTooMuch) {
				return fail("bad_response", err)
			}
			return fail(reasonOf(ctx, err), fmt.Errorf("no SIP answer: %w", err))
		}
	}
	r := Result{LatencyMS: ms(time.Since(start)), Info: map[string]string{"status_code": strconv.Itoa(status)}}
	if status < 200 || status >= 300 {
		r.Status, r.Reason, r.Error = Down, "sip_status", fmt.Sprintf("SIP %d %s", status, clip(phrase, 80))
	}
	return r
}

var errSIPBad = errors.New("not a SIP answer")

func sipOptions(p params, local net.Addr, callID string) string {
	transport := strings.ToUpper(p["transport"])
	uri := "sip:" + p.addr()
	if transport != "UDP" {
		uri += ";transport=" + strings.ToLower(transport)
	}
	if transport == "TLS" {
		uri = "sips:" + p.addr()
	}
	via := "uptimeforge.invalid"
	if local != nil {
		via = local.String()
	}
	return "OPTIONS " + uri + " SIP/2.0\r\n" +
		"Via: SIP/2.0/" + transport + " " + via + ";branch=z9hG4bK" + sipToken() + ";rport\r\n" +
		"Max-Forwards: 70\r\n" +
		"From: <sip:uptimeforge@uptimeforge.invalid>;tag=" + sipToken() + "\r\n" +
		"To: <" + uri + ">\r\n" +
		"Call-ID: " + callID + "\r\n" +
		"CSeq: 1 OPTIONS\r\n" +
		"User-Agent: UptimeForge/1.0\r\n" +
		"Accept: application/sdp\r\n" +
		"Content-Length: 0\r\n\r\n"
}

func sipToken() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// sipParse reads a response head. A reply to another call is not ours.
func sipParse(head, callID string) (code int, phrase string, ok bool) {
	lines := strings.Split(head, "\r\n")
	parts := strings.SplitN(lines[0], " ", 3)
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "SIP/2.0") {
		return 0, "", false
	}
	code, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, "", false
	}
	if !strings.Contains(head, callID) {
		return 0, "", false
	}
	if len(parts) == 3 {
		phrase = parts[2]
	}
	return code, phrase, true
}

func sipReadStream(br *bufio.Reader, callID string) (int, string, error) {
	for {
		var head strings.Builder
		for head.Len() < 64<<10 {
			line, err := br.ReadString('\n')
			if err != nil {
				return 0, "", err
			}
			head.WriteString(strings.TrimRight(line, "\r\n") + "\r\n")
			if line == "\r\n" || line == "\n" {
				break
			}
		}
		code, phrase, ok := sipParse(head.String(), callID)
		if !ok {
			return 0, "", errSIPBad
		}
		if code >= 200 {
			return code, phrase, nil
		}
	}
}
