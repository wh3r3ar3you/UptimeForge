package checks

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"strconv"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

func init() {
	registerType(TypeInfo{
		Type: "ping", Category: "network", Needs: "icmp",
		Fields: []Field{fHost, {Name: "ping_count", Kind: "number", Default: "3"}},
		target: targetParam("host"),
	}, checkPing)
}

const hintICMP = `this probe may not send ICMP. Allow unprivileged ping with sysctl net.ipv4.ping_group_range="0 2147483647" ` +
	`(in Compose: sysctls), or give the agent cap_add: NET_RAW with no-new-privileges off`

// checkPing is up when any echo comes back. A probe behind an upstream proxy
// cannot ping through it, and pinging around it would measure the wrong network.
func checkPing(ctx context.Context, s *Spec, env *Env) Result {
	if env.Via != nil {
		return fail("unavailable", errors.New("ping cannot go through this probe's upstream proxy"))
	}
	p := paramsOf(s)
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", p["host"])
	if err != nil {
		return fail(reasonOf(ctx, err), err)
	}
	ip := ips[0]
	for _, c := range ips {
		if c.To4() != nil { // like ping(8): v4 unless the name has nothing else
			ip = c
			break
		}
	}
	conn, privileged, err := listenICMP(ip)
	if err != nil {
		return fail("unavailable", fmt.Errorf("%s (%v)", hintICMP, err))
	}
	defer conn.Close()
	stop := closeOnDone(ctx, conn)
	defer stop()

	count := max(p.int("ping_count"), 1)
	id := rand.IntN(0xffff) // the kernel replaces it on an unprivileged socket
	var sum time.Duration
	var got int
	var lastErr error
	for seq := 1; seq <= count && ctx.Err() == nil; seq++ {
		rtt, err := pingOnce(ctx, conn, ip, id, seq, privileged)
		if err != nil {
			lastErr = err
			if isPermission(err) {
				return fail("unavailable", fmt.Errorf("%s (%v)", hintICMP, err))
			}
			continue
		}
		got++
		sum += rtt
	}
	loss := 100 * (count - got) / count
	if got == 0 {
		reason := "timeout"
		switch {
		case errors.Is(lastErr, errICMPUnreachable):
			reason = "unreachable"
		case lastErr != nil && reasonOf(ctx, lastErr) != "error":
			reason = reasonOf(ctx, lastErr) // sending failed, e.g. no route
		}
		return Result{Status: Down, Reason: reason, Error: fmt.Sprintf("no echo reply from %s to %d requests", ip, count),
			Info: map[string]string{"loss_percent": "100"}}
	}
	return Result{LatencyMS: ms(sum / time.Duration(got)),
		Info: map[string]string{"loss_percent": strconv.Itoa(loss), "address": ip.String()}}
}

func isPermission(err error) bool { return errors.Is(err, os.ErrPermission) }

// listenICMP prefers the unprivileged datagram socket: agents run as non-root
// with all capabilities dropped, where a raw socket is simply not possible.
func listenICMP(ip net.IP) (*icmp.PacketConn, bool, error) {
	udp, raw, addr := "udp4", "ip4:icmp", "0.0.0.0"
	if ip.To4() == nil {
		udp, raw, addr = "udp6", "ip6:ipv6-icmp", "::"
	}
	conn, err := icmp.ListenPacket(udp, addr)
	if err == nil {
		return conn, false, nil
	}
	conn, rawErr := icmp.ListenPacket(raw, addr)
	if rawErr == nil {
		return conn, true, nil
	}
	return nil, false, fmt.Errorf("%v; raw socket: %v", err, rawErr)
}

var errICMPUnreachable = errors.New("destination unreachable")

func pingOnce(ctx context.Context, conn *icmp.PacketConn, ip net.IP, id, seq int, privileged bool) (time.Duration, error) {
	var typ, replyType icmp.Type = ipv4.ICMPTypeEcho, ipv4.ICMPTypeEchoReply
	var unreachable icmp.Type = ipv4.ICMPTypeDestinationUnreachable
	proto := 1
	if ip.To4() == nil {
		typ, replyType, unreachable, proto = ipv6.ICMPTypeEchoRequest, ipv6.ICMPTypeEchoReply, ipv6.ICMPTypeDestinationUnreachable, 58
	}
	msg := icmp.Message{Type: typ, Body: &icmp.Echo{ID: id, Seq: seq, Data: []byte("UptimeForge")}}
	wire, err := msg.Marshal(nil)
	if err != nil {
		return 0, err
	}
	var dst net.Addr = &net.UDPAddr{IP: ip}
	if privileged {
		dst = &net.IPAddr{IP: ip}
	}
	start := time.Now()
	if _, err := conn.WriteTo(wire, dst); err != nil {
		return 0, err
	}
	wait := 2 * time.Second
	if d, ok := ctx.Deadline(); ok && time.Until(d) < wait {
		wait = time.Until(d)
	}
	conn.SetReadDeadline(start.Add(wait))
	buf := make([]byte, 1500)
	for {
		n, peer, err := conn.ReadFrom(buf)
		if err != nil {
			return 0, err
		}
		m, err := icmp.ParseMessage(proto, buf[:n])
		if err != nil {
			continue
		}
		// A raw socket sees every unreachable on the machine: only one about
		// this echo (destination, ID and sequence in the quoted packet) counts.
		if m.Type == unreachable && privileged && ownUnreachable(m, ip, id, seq) {
			return 0, errICMPUnreachable
		}
		echo, ok := m.Body.(*icmp.Echo)
		if !ok || m.Type != replyType || echo.Seq != seq {
			continue
		}
		// A raw socket sees every echo on the machine; a datagram one only its own.
		if privileged && (echo.ID != id || peerIP(peer) == nil || !peerIP(peer).Equal(ip)) {
			continue
		}
		return time.Since(start), nil
	}
}

// ownUnreachable reports whether a destination unreachable quotes our echo:
// the original IP header (destination ip) and the first 8 bytes of the echo
// (type, code, checksum, ID, sequence).
func ownUnreachable(m *icmp.Message, ip net.IP, id, seq int) bool {
	du, ok := m.Body.(*icmp.DstUnreach)
	if !ok {
		return false
	}
	d := du.Data
	var dst net.IP
	var echo []byte
	var echoType byte
	if v4 := ip.To4(); v4 != nil {
		if len(d) < 20 || d[0]>>4 != 4 {
			return false
		}
		ihl := int(d[0]&0x0f) * 4
		if ihl < 20 || len(d) < ihl+8 || d[9] != 1 {
			return false
		}
		dst, echo, echoType, ip = net.IP(d[16:20]), d[ihl:], 8, v4
	} else {
		// Extension headers are not followed: an echo request carries none.
		if len(d) < 48 || d[0]>>4 != 6 || d[6] != 58 {
			return false
		}
		dst, echo, echoType = net.IP(d[24:40]), d[40:], 128
	}
	return dst.Equal(ip) && echo[0] == echoType &&
		int(binary.BigEndian.Uint16(echo[4:6])) == id && int(binary.BigEndian.Uint16(echo[6:8])) == seq
}

func peerIP(a net.Addr) net.IP {
	switch v := a.(type) {
	case *net.UDPAddr:
		return v.IP
	case *net.IPAddr:
		return v.IP
	}
	return nil
}
