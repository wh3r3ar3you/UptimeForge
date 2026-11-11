//go:build !lite

package checks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"
)

func init() {
	v3 := "snmp_version=3"
	registerType(TypeInfo{
		Type: "snmp", Category: "network", Keyword: true,
		Fields: []Field{fHost, fPort("161"),
			{Name: "snmp_version", Kind: "select", Default: "2c", Options: []string{"1", "2c", "3"}},
			{Name: "oid", Kind: "text", Required: true},
			{Name: "community", Kind: "password", Secret: true, Default: "public"},
			{Name: "expected_value", Kind: "text"},
			{Name: "expected_op", Kind: "select", Default: "==", Options: compareOps},
			{Name: "username", Kind: "text", ShowIf: v3},
			{Name: "security_level", Kind: "select", Default: "noAuthNoPriv", Options: []string{"noAuthNoPriv", "authNoPriv", "authPriv"}, ShowIf: v3},
			{Name: "auth_protocol", Kind: "select", Default: "SHA", Options: []string{"MD5", "SHA", "SHA256", "SHA512"}, ShowIf: v3},
			{Name: "auth_password", Kind: "password", Secret: true, ShowIf: v3},
			{Name: "priv_protocol", Kind: "select", Default: "AES", Options: []string{"DES", "AES", "AES256"}, ShowIf: v3},
			{Name: "priv_password", Kind: "password", Secret: true, ShowIf: v3},
		},
		target: targetAddr,
	}, checkSNMP)
}

func snmpClient(ctx context.Context, p params) *gosnmp.GoSNMP {
	x := &gosnmp.GoSNMP{
		Target:    "127.0.0.1", // see the swap in checkSNMP
		Port:      uint16(p.int("port")),
		Transport: "udp",
		Context:   ctx,
		Timeout:   2 * time.Second,
		Retries:   2,
		MaxOids:   1,
		Community: p["community"],
	}
	if deadline, ok := ctx.Deadline(); ok {
		// All attempts end well before the deadline, so a silent agent is
		// answered with its own hint, not the runner's bare deadline.
		x.Timeout = max(time.Until(deadline)*2/3/time.Duration(x.Retries+1), 100*time.Millisecond)
	}
	switch p["snmp_version"] {
	case "1":
		x.Version = gosnmp.Version1
	case "3":
		x.Version = gosnmp.Version3
		x.SecurityModel = gosnmp.UserSecurityModel
		usm := &gosnmp.UsmSecurityParameters{UserName: p["username"]}
		switch p["security_level"] {
		case "authNoPriv":
			x.MsgFlags = gosnmp.AuthNoPriv
		case "authPriv":
			x.MsgFlags = gosnmp.AuthPriv
		default:
			x.MsgFlags = gosnmp.NoAuthNoPriv
		}
		if p["security_level"] != "noAuthNoPriv" {
			usm.AuthenticationPassphrase = p["auth_password"]
			usm.AuthenticationProtocol = map[string]gosnmp.SnmpV3AuthProtocol{
				"MD5": gosnmp.MD5, "SHA": gosnmp.SHA, "SHA256": gosnmp.SHA256, "SHA512": gosnmp.SHA512}[p["auth_protocol"]]
		}
		if p["security_level"] == "authPriv" {
			usm.PrivacyPassphrase = p["priv_password"]
			usm.PrivacyProtocol = map[string]gosnmp.SnmpV3PrivProtocol{
				"DES": gosnmp.DES, "AES": gosnmp.AES, "AES256": gosnmp.AES256}[p["priv_protocol"]]
		}
		x.SecurityParameters = usm
	default:
		x.Version = gosnmp.Version2c
	}
	return x
}

// checkSNMP does one GET. gosnmp has no dialer hook, so it connects to a dummy
// loopback address (a UDP connect sends nothing), which sets up its internals,
// and then gets the probe's own socket.
func checkSNMP(ctx context.Context, s *Spec, env *Env) Result {
	p := paramsOf(s)
	x := snmpClient(ctx, p)
	if err := x.Connect(); err != nil {
		return fail("error", err)
	}
	x.Conn.Close()
	conn, err := env.Dial(ctx, "udp", p.addr())
	if err != nil {
		return fail(reasonOf(ctx, err), err)
	}
	x.Conn = conn
	defer conn.Close()
	stop := closeOnDone(ctx, conn)
	defer stop()

	start := time.Now()
	pkt, err := x.Get([]string{p["oid"]})
	if err != nil {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "request timeout") || ctx.Err() != nil:
			// v1 and v2c servers drop a wrong community silently.
			return fail("timeout", errors.New("no SNMP answer: agent is down, or the community or credentials are wrong"))
		case strings.Contains(msg, "unknown username") || strings.Contains(msg, "wrong digest") ||
			strings.Contains(msg, "authentication") || strings.Contains(msg, "decryption"):
			return fail("auth", err)
		}
		return failUDP(ctx, env, err)
	}
	r := Result{LatencyMS: ms(time.Since(start))}
	if pkt.Error != gosnmp.NoError {
		r.Status, r.Reason, r.Error = Down, "bad_response", fmt.Sprintf("agent answered %s", pkt.Error)
		if pkt.Error == gosnmp.AuthorizationError || pkt.Error == gosnmp.NoAccess {
			r.Reason = "auth"
		}
		return r
	}
	if len(pkt.Variables) == 0 {
		r.Status, r.Reason, r.Error = Down, "bad_response", "agent answered with no value"
		return r
	}
	v := pkt.Variables[0]
	switch v.Type {
	case gosnmp.NoSuchObject, gosnmp.NoSuchInstance, gosnmp.EndOfMibView:
		r.Status, r.Reason, r.Error = Down, "not_found", "no such OID: "+p["oid"]
		return r
	}
	r.Text = snmpText(v)
	r.Info = map[string]string{"value": clip(r.Text, 120)}
	if want := p["expected_value"]; want != "" && !compareValue(r.Text, p["expected_op"], want) {
		r.Status, r.Reason = Down, "value_mismatch"
		r.Error = fmt.Sprintf("%s is %s, expected %s %s", p["oid"], clip(r.Text, 120), p["expected_op"], want)
	}
	return r
}

func snmpText(v gosnmp.SnmpPDU) string {
	switch value := v.Value.(type) {
	case []byte:
		return string(value)
	case string:
		return value
	}
	return gosnmp.ToBigInt(v.Value).String()
}
