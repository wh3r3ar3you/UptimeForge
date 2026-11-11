package checks

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"errors"
	"fmt"
	"time"

	"layeh.com/radius"
	"layeh.com/radius/rfc2865"
	"layeh.com/radius/rfc2869"
)

func init() {
	registerType(TypeInfo{
		Type: "radius", Category: "network",
		Fields: []Field{fHost, fPort("1812"), required(fUsername), required(fPassword),
			{Name: "secret", Kind: "password", Required: true, Secret: true},
			{Name: "called_station_id", Kind: "text", Advanced: true},
			{Name: "calling_station_id", Kind: "text", Advanced: true},
		},
		target: targetAddr,
	}, checkRADIUS)
}

// checkRADIUS sends an Access-Request and is up on Access-Accept. The exchange
// is ours rather than radius.Client's because that one dials by itself.
func checkRADIUS(ctx context.Context, s *Spec, env *Env) Result {
	p := paramsOf(s)
	secret := []byte(p["secret"])
	pkt := radius.New(radius.CodeAccessRequest, secret)
	if err := rfc2865.UserName_SetString(pkt, p["username"]); err != nil {
		return fail("config", errors.New("bad username"))
	}
	if err := rfc2865.UserPassword_SetString(pkt, p["password"]); err != nil {
		return fail("config", errors.New("bad password length"))
	}
	if v := p["called_station_id"]; v != "" {
		rfc2865.CalledStationID_SetString(pkt, v)
	}
	if v := p["calling_station_id"]; v != "" {
		rfc2865.CallingStationID_SetString(pkt, v)
	}
	req, err := radiusSigned(pkt, secret)
	if err != nil {
		return fail("config", err)
	}
	start := time.Now()
	conn, err := env.Dial(ctx, "udp", p.addr())
	if err != nil {
		return fail(reasonOf(ctx, err), err)
	}
	defer conn.Close()
	var forged bool
	raw, err := udpExchange(ctx, conn, req, func(b []byte) bool {
		if len(b) < 20 || b[1] != req[1] {
			return false
		}
		if !radius.IsAuthenticResponse(b, req, secret) {
			forged = true
			return false
		}
		return true
	})
	if err != nil {
		if forged {
			return fail("bad_response", errors.New("answer fails authentication: the shared secret is wrong"))
		}
		if ctx.Err() != nil {
			// Servers drop requests with a wrong secret silently, so say both.
			return fail("timeout", errors.New("no answer: server is down, or it dropped the request because of a wrong shared secret"))
		}
		return failUDP(ctx, env, err)
	}
	reply, err := radius.Parse(raw, secret)
	if err != nil {
		return fail("bad_response", fmt.Errorf("unreadable answer: %w", err))
	}
	r := Result{LatencyMS: ms(time.Since(start))}
	switch reply.Code {
	case radius.CodeAccessAccept:
	case radius.CodeAccessReject:
		r.Status, r.Reason, r.Error = Down, "auth", "Access-Reject: the server refused these credentials"
	default:
		r.Status, r.Reason, r.Error = Down, "bad_response", "server answered "+reply.Code.String()
	}
	return r
}

// radiusSigned adds a Message-Authenticator. Hardened servers (the BlastRADIUS
// fixes) silently drop requests without one, which would read as an outage.
func radiusSigned(pkt *radius.Packet, secret []byte) ([]byte, error) {
	if err := rfc2869.MessageAuthenticator_Set(pkt, make([]byte, md5.Size)); err != nil {
		return nil, err
	}
	raw, err := pkt.Encode()
	if err != nil {
		return nil, err
	}
	mac := hmac.New(md5.New, secret)
	mac.Write(raw)
	if err := rfc2869.MessageAuthenticator_Set(pkt, mac.Sum(nil)); err != nil {
		return nil, err
	}
	return pkt.Encode()
}
