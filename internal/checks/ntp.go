package checks

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"time"
)

func init() {
	registerType(TypeInfo{
		Type: "ntp", Category: "network",
		Fields: []Field{fHost, fPort("123"), {Name: "max_offset", Kind: "number"}},
		target: targetAddr,
	}, checkNTP)
}

const (
	ntpPacketLen = 48
	ntpLeapAlarm = 3
	ntpModeSrv   = 4
	ntpModeBcast = 5
	// ntpEpochDelta is seconds between the NTP epoch (1900) and Unix time.
	ntpEpochDelta = 2208988800
)

func ntpTime(b []byte) time.Time {
	sec := binary.BigEndian.Uint32(b)
	frac := binary.BigEndian.Uint32(b[4:])
	nanos := (int64(frac) * 1e9) >> 32
	return time.Unix(int64(sec)-ntpEpochDelta, nanos)
}

// checkNTP does an SNTP exchange (RFC 4330). The offset needs the send time of
// the very datagram answered, so each attempt is a fresh socket and nonce, not
// a retransmit.
func checkNTP(ctx context.Context, s *Spec, env *Env) Result {
	p := paramsOf(s)
	var lastErr error
	for range 3 {
		b, t1, t4, err := ntpExchange(ctx, env, p.addr())
		if err == nil {
			return ntpResult(b, p, t1, t4)
		}
		lastErr = err
		if ctx.Err() != nil || udpReasonOf(ctx, err) != "timeout" {
			break
		}
	}
	return failUDP(ctx, env, lastErr)
}

func ntpExchange(ctx context.Context, env *Env, addr string) (b []byte, t1, t4 time.Time, err error) {
	attempt, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, err := env.Dial(attempt, "udp", addr)
	if err != nil {
		return nil, t1, t4, err
	}
	defer conn.Close()
	stop := closeOnDone(attempt, conn)
	defer stop()

	req := make([]byte, ntpPacketLen)
	req[0] = 4<<3 | 3 // version 4, client
	rand.Read(req[40:48])
	t1 = time.Now()
	if _, err := conn.Write(req); err != nil {
		return nil, t1, t4, err
	}
	buf := make([]byte, 512)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			if attempt.Err() != nil {
				err = errors.New("no NTP reply: i/o timeout")
			}
			return nil, t1, t4, err
		}
		t4 = time.Now()
		// The server echoes our transmit field as its origin; anything else is stale or spoofed.
		if n >= ntpPacketLen && bytes.Equal(buf[24:32], req[40:48]) {
			return buf[:n], t1, t4, nil
		}
	}
}

func ntpResult(b []byte, p params, t1, t4 time.Time) Result {
	leap, mode, stratum := b[0]>>6, b[0]&7, b[1]
	switch {
	case mode != ntpModeSrv && mode != ntpModeBcast:
		return fail("bad_response", fmt.Errorf("NTP reply in mode %d", mode))
	case stratum == 0:
		return fail("bad_response", fmt.Errorf("server refuses to serve time (kiss code %q)", bytes.TrimRight(b[12:16], "\x00")))
	case leap == ntpLeapAlarm:
		return fail("bad_response", errors.New("server clock is not synchronized"))
	}
	t2, t3 := ntpTime(b[32:]), ntpTime(b[40:])
	if t3.Before(t2) {
		return fail("bad_response", errors.New("server sent impossible timestamps"))
	}
	offset := (t2.Sub(t1) + t3.Sub(t4)) / 2
	r := Result{LatencyMS: ms(t4.Sub(t1) - t3.Sub(t2)), Info: map[string]string{
		"stratum":   strconv.Itoa(int(stratum)),
		"offset_ms": strconv.FormatFloat(ms(offset), 'f', 1, 64),
	}}
	if limit := p.float("max_offset"); limit > 0 && abs(ms(offset)) > limit {
		r.Status, r.Reason = Down, "ntp_offset"
		r.Error = fmt.Sprintf("clock offset %.1f ms, limit %.0f ms", ms(offset), limit)
	}
	return r
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
