package checks

import (
	"bytes"
	"cmp"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	mrand "math/rand/v2"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
)

func init() { register("mtproto", checkMTProto) }

// mtprotoDCs are asked when a check does not pin a datacenter.
var mtprotoDCs = []int{1, 2, 3, 4, 5}

// Secret types: plain uses obfuscated2 with intermediate framing, dd adds
// random padding, faketls wraps the padded stream in fake TLS 1.3 records.
const (
	secretPlain   = "plain"
	secretDD      = "dd"
	secretFakeTLS = "faketls"
)

const (
	tagIntermediate = 0xeeeeeeee
	tagPadded       = 0xdddddddd

	ctorReqPQMulti = 0xbe7e8ef1
	ctorResPQ      = 0x05162463

	recHandshake = 0x16
	recCCS       = 0x14
	recAlert     = 0x15
	recAppData   = 0x17
)

var errFakeTLS = errors.New("fake TLS")

// hintFakeTLS names every cause seen from the outside. telemt rejects clients
// more than two minutes off and fronts them to its mask site, so a wrong clock
// on the probe looks exactly like a wrong secret.
const hintFakeTLS = "wrong secret, not an MTProxy, or the probe's clock is off"

type mtSecret struct {
	Type   string
	Key    []byte // 16 bytes
	Domain string // SNI, faketls only
}

type mtProxy struct {
	addr string
	sec  *mtSecret
	dc   int
}

// checkMTProto asks Telegram DCs for resPQ through the proxy, which proves the
// proxy accepts the secret and can reach Telegram, not just that it listens.
// One silent DC keeps the check up: it may be Telegram's own outage, and users
// on the other DCs still connect. Info.dc_down shows it. No answer at all is down.
func checkMTProto(ctx context.Context, s *Spec, env *Env) Result {
	px, err := s.MTProto.parse()
	if err != nil {
		return fail("config", err)
	}
	if px.dc != 0 {
		return askDC(ctx, px, px.dc, env)
	}
	rs := make([]Result, len(mtprotoDCs))
	var wg sync.WaitGroup
	for i, dc := range mtprotoDCs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rs[i] = askDC(ctx, px, dc, env)
		}()
	}
	wg.Wait()
	return mergeDCs(rs)
}

// mergeDCs reports the fastest DC that answered, or the first failure if none did.
func mergeDCs(rs []Result) Result {
	var best *Result
	var lat, down []string
	for i := range rs {
		r, dc := &rs[i], strconv.Itoa(mtprotoDCs[i])
		if r.Status == Down {
			down = append(down, dc)
			continue
		}
		lat = append(lat, dc+":"+strconv.Itoa(int(math.Round(r.LatencyMS))))
		if best == nil || r.LatencyMS < best.LatencyMS {
			best = r
		}
	}
	r := rs[0]
	if best != nil {
		r = *best
	}
	delete(r.Info, "dc")
	if len(lat) > 0 {
		r.Info["dc_ms"] = strings.Join(lat, " ")
	}
	if len(down) > 0 {
		r.Info["dc_down"] = strings.Join(down, ",")
	}
	return r
}

func askDC(ctx context.Context, px *mtProxy, dc int, env *Env) Result {
	r := Result{Phases: map[string]float64{}, Info: map[string]string{"secret_type": px.sec.Type, "dc": strconv.Itoa(dc)}}
	down := func(reason string, err error) Result {
		r.Status, r.Reason, r.Error = Down, reason, err.Error()
		return r
	}

	start := time.Now()
	conn, err := env.Dial(ctx, "tcp", px.addr)
	if err != nil {
		return down(reasonOf(ctx, err), err)
	}
	defer conn.Close()
	r.Phases["connect"] = ms(time.Since(start))
	if d, ok := ctx.Deadline(); ok {
		conn.SetDeadline(d)
	}
	// Tunnelled conns may ignore deadlines.
	defer context.AfterFunc(ctx, func() { conn.Close() })()

	if px.sec.Type == secretFakeTLS {
		t := time.Now()
		if err := fakeTLSHandshake(conn, px.sec); err != nil {
			if errors.Is(err, errFakeTLS) {
				return down("faketls", err)
			}
			return down(reasonOf(ctx, err), fmt.Errorf("fake TLS handshake: %w", cmp.Or(ctx.Err(), err)))
		}
		r.Phases["tls"] = ms(time.Since(t))
	}

	mc := newMTConn(conn, px.sec, dc)
	nonce := make([]byte, 16)
	rand.Read(nonce)
	t := time.Now()
	if err := mc.send(reqPQMulti(nonce)); err != nil {
		return down(reasonOf(ctx, err), err)
	}
	resp, err := mc.recv()
	if err != nil {
		// Proxies drop or stall clients with a wrong secret, and drop them when
		// Telegram is unreachable; either way the DC never answered.
		return down("handshake", fmt.Errorf("no resPQ from DC %d: %w", dc, cmp.Or(ctx.Err(), err)))
	}
	if err := checkResPQ(resp, nonce); err != nil {
		return down("handshake", err)
	}
	r.Phases["rtt"] = ms(time.Since(t))
	r.LatencyMS = ms(time.Since(start))
	return r
}

func (p *MTProtoParams) parse() (*mtProxy, error) {
	px := &mtProxy{addr: p.Address, dc: p.DC}
	var err error
	if p.Link != "" {
		px.addr, px.sec, err = parseMTProxyLink(p.Link)
	} else if _, _, err = net.SplitHostPort(p.Address); err == nil {
		px.sec, err = parseMTProxySecret(p.Secret)
	}
	if err == nil && (px.dc < math.MinInt16 || px.dc > math.MaxInt16) {
		err = fmt.Errorf("mtproto dc %d out of range", px.dc)
	}
	if err != nil {
		return nil, err
	}
	return px, nil
}

func (p *MTProtoParams) address() string {
	if px, err := p.parse(); err == nil {
		return px.addr
	}
	return p.Address
}

func parseMTProxyLink(link string) (string, *mtSecret, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		return "", nil, err
	}
	host := strings.ToLower(u.Host)
	isWeb := (u.Scheme == "https" || u.Scheme == "http") && (host == "t.me" || host == "telegram.me")
	if !(u.Scheme == "tg" && host == "proxy") && !(isWeb && strings.TrimSuffix(u.Path, "/") == "/proxy") {
		return "", nil, errors.New("not an MTProxy link: want tg://proxy?… or https://t.me/proxy?…")
	}
	// '+' in a secret is base64, not an encoded space.
	q, err := url.ParseQuery(strings.ReplaceAll(u.RawQuery, "+", "%2B"))
	if err != nil {
		return "", nil, err
	}
	server := strings.Trim(q.Get("server"), "[]")
	port, err := strconv.ParseUint(q.Get("port"), 10, 16)
	if server == "" || err != nil || port == 0 {
		return "", nil, errors.New("MTProxy link needs server and port")
	}
	sec, err := parseMTProxySecret(q.Get("secret"))
	return net.JoinHostPort(server, strconv.FormatUint(port, 10)), sec, err
}

// parseMTProxySecret accepts hex and base64 in either alphabet, with or
// without padding.
func parseMTProxySecret(s string) (*mtSecret, error) {
	s = strings.TrimSpace(s)
	var candidates [][]byte
	if b, err := hex.DecodeString(s); err == nil {
		candidates = append(candidates, b)
	}
	b64 := strings.TrimRight(strings.NewReplacer("+", "-", "/", "_").Replace(s), "=")
	if b, err := base64.RawURLEncoding.DecodeString(b64); err == nil {
		candidates = append(candidates, b)
	}
	if len(candidates) == 0 {
		return nil, errors.New("MTProxy secret is neither hex nor base64")
	}
	var first error
	for _, b := range candidates {
		sec, err := decodeMTSecret(b)
		if err == nil {
			return sec, nil
		}
		first = cmp.Or(first, err)
	}
	return nil, first
}

func decodeMTSecret(b []byte) (*mtSecret, error) {
	switch {
	case len(b) == 16:
		return &mtSecret{Type: secretPlain, Key: b}, nil
	case len(b) == 17 && b[0] == 0xdd:
		return &mtSecret{Type: secretDD, Key: b[1:]}, nil
	case len(b) > 17 && b[0] == 0xee:
		d := string(b[17:])
		if len(d) > 253 || strings.Trim(strings.ToLower(d), "abcdefghijklmnopqrstuvwxyz0123456789.-") != "" {
			return nil, fmt.Errorf("MTProxy fake-TLS secret has invalid domain %q", d)
		}
		return &mtSecret{Type: secretFakeTLS, Key: b[1:17], Domain: d}, nil
	}
	return nil, fmt.Errorf("unsupported MTProxy secret of %d bytes: want 16 bytes, dd+16 or ee+16+domain", len(b))
}

// fakeTLSHandshake sends a browser-like ClientHello whose random is
// HMAC(secret, hello) with a timestamp in the last 4 bytes, then verifies that
// the ServerHello, ChangeCipherSpec and first application data record are
// signed with HMAC(secret, client random || records).
func fakeTLSHandshake(conn net.Conn, sec *mtSecret) error {
	hello, err := clientHello(sec.Domain)
	if err != nil {
		return err
	}
	random := hello[11:43]
	clear(random)
	mac := hmac.New(sha256.New, sec.Key)
	mac.Write(hello)
	copy(random, mac.Sum(nil))
	ts := binary.LittleEndian.Uint32(random[28:]) ^ uint32(time.Now().Unix())
	binary.LittleEndian.PutUint32(random[28:], ts)
	clientRandom := slices.Clone(random)
	if _, err := conn.Write(hello); err != nil {
		return err
	}

	var answer []byte
	for i, want := range []byte{recHandshake, recCCS, recAppData} {
		rec, err := readRecord(conn)
		if err != nil {
			return err
		}
		switch {
		case rec[0] == recAlert:
			return fmt.Errorf("%w: server sent alert % x, not an MTProxy", errFakeTLS, rec[5:])
		case rec[0] != want || i == 0 && !isServerHello(rec):
			return fmt.Errorf("%w: unexpected record %#x after ClientHello, not an MTProxy", errFakeTLS, rec[0])
		}
		answer = append(answer, rec...)
	}
	serverRandom := slices.Clone(answer[11:43])
	clear(answer[11:43])
	mac = hmac.New(sha256.New, sec.Key)
	mac.Write(clientRandom)
	mac.Write(answer)
	if !hmac.Equal(mac.Sum(nil), serverRandom) {
		return fmt.Errorf("%w: ServerHello HMAC mismatch (%s)", errFakeTLS, hintFakeTLS)
	}
	return nil
}

// helloID is the ClientHello the check sends: a compact Chrome one, the
// size Telegram clients send (~517 bytes, one packet). Current Chrome adds a
// post-quantum key share and ECH and grows to ~1.8 KB; on some Russian ISPs
// DPI silently drops such a handshake to a proxy while Telegram's own gets
// through (a Russian ISP to a proxy abroad), so the check reported a
// working proxy as down.
var helloID = utls.HelloChrome_120

func clientHello(domain string) ([]byte, error) {
	u := utls.UClient(nil, &utls.Config{ServerName: domain}, helloID)
	if err := u.BuildHandshakeState(); err != nil {
		return nil, err
	}
	raw := u.HandshakeState.Hello.Raw
	return append([]byte{recHandshake, 3, 1, byte(len(raw) >> 8), byte(len(raw))}, raw...), nil
}

// isServerHello reports whether rec holds a ServerHello and nothing else, as
// fake-TLS servers send it. Real TLS 1.2 servers coalesce more messages.
func isServerHello(rec []byte) bool {
	p := rec[5:]
	return len(p) >= 38 && p[0] == 2 && 4+(int(p[1])<<16|int(p[2])<<8|int(p[3])) == len(p)
}

// readRecord returns one TLS record including its 5-byte header.
func readRecord(r io.Reader) ([]byte, error) {
	rec := make([]byte, 5, 1024)
	if _, err := io.ReadFull(r, rec); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(rec[3:]))
	if rec[1] != 3 || n > 1<<14+2048 {
		return nil, fmt.Errorf("%w: not a TLS record (% x)", errFakeTLS, rec)
	}
	rec = append(rec, make([]byte, n)...)
	_, err := io.ReadFull(r, rec[5:])
	return rec, err
}

// tlsReader yields the payload of application data records.
type tlsReader struct {
	r   io.Reader
	buf []byte
}

func (t *tlsReader) Read(p []byte) (int, error) {
	for len(t.buf) == 0 {
		rec, err := readRecord(t.r)
		if err != nil {
			return 0, err
		}
		switch rec[0] {
		case recAppData:
			t.buf = rec[5:]
		case recCCS:
		default:
			return 0, fmt.Errorf("unexpected TLS record %#x", rec[0])
		}
	}
	n := copy(p, t.buf)
	t.buf = t.buf[n:]
	return n, nil
}

// mtConn is the client side of obfuscated2 with (padded) intermediate
// framing, optionally inside fake-TLS records.
type mtConn struct {
	conn    net.Conn
	r       io.Reader
	enc     cipher.Stream
	init    []byte // sent with the first packet
	padded  bool
	faketls bool
}

func newMTConn(conn net.Conn, sec *mtSecret, dc int) *mtConn {
	c := &mtConn{conn: conn, padded: sec.Type != secretPlain, faketls: sec.Type == secretFakeTLS}
	tag := uint32(tagIntermediate)
	if c.padded {
		tag = tagPadded
	}
	var dec cipher.Stream
	c.init, c.enc, dec = obfs2Init(sec.Key, tag, dc)
	var src io.Reader = conn
	if c.faketls {
		src = &tlsReader{r: conn}
	}
	c.r = cipher.StreamReader{S: dec, R: src}
	return c
}

func (c *mtConn) send(payload []byte) error {
	pad := 0
	if c.padded {
		pad = mrand.IntN(16)
	}
	pkt := binary.LittleEndian.AppendUint32(nil, uint32(len(payload)+pad))
	pkt = append(pkt, payload...)
	pkt = append(pkt, make([]byte, pad)...)
	rand.Read(pkt[len(pkt)-pad:])
	c.enc.XORKeyStream(pkt, pkt)
	first := c.init != nil
	pkt, c.init = append(c.init, pkt...), nil
	if c.faketls {
		rec := []byte{recAppData, 3, 3, byte(len(pkt) >> 8), byte(len(pkt))}
		if first {
			rec = append([]byte{recCCS, 3, 3, 0, 1, 1}, rec...)
		}
		pkt = append(rec, pkt...)
	}
	_, err := c.conn.Write(pkt)
	return err
}

func (c *mtConn) recv() ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(c.r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.LittleEndian.Uint32(hdr[:])
	if n < 4 || n > 1<<16 {
		return nil, fmt.Errorf("implausible packet length %d (wrong secret?)", n)
	}
	pkt := make([]byte, n)
	_, err := io.ReadFull(c.r, pkt)
	return pkt, err
}

// obfs2Init builds the 64-byte obfuscated2 init and the client's streams.
func obfs2Init(secret []byte, tag uint32, dc int) (init []byte, enc, dec cipher.Stream) {
	init = make([]byte, 64)
	for {
		rand.Read(init)
		switch binary.LittleEndian.Uint32(init) {
		case 0x44414548, 0x54534f50, 0x20544547, 0x4954504f, 0x02010316, tagIntermediate, tagPadded:
			continue // HEAD, POST, GET , OPTI, TLS hello and transport tags
		}
		if init[0] != 0xef && binary.LittleEndian.Uint32(init[4:]) != 0 {
			break
		}
	}
	binary.LittleEndian.PutUint32(init[56:], tag)
	binary.LittleEndian.PutUint16(init[60:], uint16(int16(dc)))
	enc, dec = obfs2Streams(init, secret)
	encrypted := make([]byte, 64)
	enc.XORKeyStream(encrypted, init)
	copy(init[56:], encrypted[56:])
	return init, enc, dec
}

// obfs2Streams derives the streams of the side that sent init: the forward
// one encrypts, the one from the reversed key and IV decrypts.
func obfs2Streams(init, secret []byte) (fwd, rev cipher.Stream) {
	r := slices.Clone(init[8:56])
	slices.Reverse(r)
	return aesCTR(init[8:40], init[40:56], secret), aesCTR(r[:32], r[32:], secret)
}

func aesCTR(key, iv, secret []byte) cipher.Stream {
	if secret != nil {
		h := sha256.Sum256(append(slices.Clone(key), secret...))
		key = h[:]
	}
	b, _ := aes.NewCipher(key)
	return cipher.NewCTR(b, iv)
}

// reqPQMulti is an unencrypted req_pq_multi: auth_key_id 0, message_id,
// length, then the TL body.
func reqPQMulti(nonce []byte) []byte {
	now := time.Now()
	b := make([]byte, 20, 40)
	binary.LittleEndian.PutUint64(b[8:], uint64(now.Unix())<<32|(uint64(now.Nanosecond())<<32/1e9)&^3)
	binary.LittleEndian.PutUint32(b[16:], 20)
	b = binary.LittleEndian.AppendUint32(b, ctorReqPQMulti)
	return append(b, nonce...)
}

func checkResPQ(p, nonce []byte) error {
	if len(p) == 4 {
		return fmt.Errorf("DC answered with transport error %d", int32(binary.LittleEndian.Uint32(p)))
	}
	if len(p) < 40 || binary.LittleEndian.Uint64(p) != 0 {
		return fmt.Errorf("not an unencrypted MTProto message (%d bytes)", len(p))
	}
	body := p[20:]
	if n := int(binary.LittleEndian.Uint32(p[16:])); n < 20 || n > len(body) {
		return fmt.Errorf("bad message length %d", n)
	}
	if c := binary.LittleEndian.Uint32(body); c != ctorResPQ {
		return fmt.Errorf("expected resPQ, got constructor %#08x", c)
	}
	if !bytes.Equal(body[4:20], nonce) {
		return errors.New("resPQ nonce mismatch")
	}
	return nil
}
