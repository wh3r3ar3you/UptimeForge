package checks

import (
	"bytes"
	"context"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// testKey yields '+' and '/' in standard base64 at every alignment.
var testKey = bytes.Repeat([]byte{0xfb, 0xff, 0xbf, 0xff}, 4)

func secretBytes(typ string, key []byte, domain string) []byte {
	switch typ {
	case secretDD:
		return append([]byte{0xdd}, key...)
	case secretFakeTLS:
		return append(append([]byte{0xee}, key...), domain...)
	}
	return slices.Clone(key)
}

func TestParseMTProxySecret(t *testing.T) {
	const domain = "www.cloudflare.com"
	hexLooking := "0123456789abcdef012345" // valid hex of 11 bytes, valid base64 of 16
	hexLookingKey, _ := base64.RawURLEncoding.DecodeString(hexLooking)
	type want struct {
		typ    string
		key    []byte
		domain string
	}
	cases := map[string]want{hexLooking: {secretPlain, hexLookingKey, ""}}
	for _, typ := range []string{secretPlain, secretDD, secretFakeTLS} {
		b := secretBytes(typ, testKey, domain)
		w := want{typ, testKey, ""}
		if typ == secretFakeTLS {
			w.domain = domain
		}
		std := base64.StdEncoding.EncodeToString(b)
		if !strings.ContainsAny(std, "+/") {
			t.Fatalf("%s: test key does not exercise the base64 alphabet: %s", typ, std)
		}
		for _, s := range []string{
			hex.EncodeToString(b),
			strings.ToUpper(hex.EncodeToString(b)),
			std,
			base64.RawStdEncoding.EncodeToString(b),
			base64.URLEncoding.EncodeToString(b),
			base64.RawURLEncoding.EncodeToString(b),
			" " + hex.EncodeToString(b) + "\n",
		} {
			cases[s] = w
		}
	}
	for in, w := range cases {
		sec, err := parseMTProxySecret(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if sec.Type != w.typ || !bytes.Equal(sec.Key, w.key) || sec.Domain != w.domain {
			t.Errorf("%q: got %s %x %q, want %s %x %q", in, sec.Type, sec.Key, sec.Domain, w.typ, w.key, w.domain)
		}
	}

	key := hex.EncodeToString(testKey)
	for _, in := range []string{
		"",
		"not a secret!",
		key[:30],            // 15 bytes
		"ab" + key,          // 17 bytes without dd
		"ee" + key,          // fake TLS without domain
		"ee" + key + "00ff", // binary domain
		"ee" + key + hex.EncodeToString([]byte("bad domain")),
		"dd" + key + "00", // 18 bytes
	} {
		if sec, err := parseMTProxySecret(in); err == nil {
			t.Errorf("%q: accepted as %+v", in, sec)
		}
	}
}

func TestParseMTProxyLink(t *testing.T) {
	ee := secretBytes(secretFakeTLS, testKey, "ya.ru")
	b64 := base64.StdEncoding.EncodeToString(ee)
	cases := []struct{ link, addr, typ string }{
		{"tg://proxy?server=203.0.113.7&port=443&secret=dd" + hex.EncodeToString(testKey), "203.0.113.7:443", secretDD},
		{"tg://proxy?server=mt.example.com&port=8443&secret=" + url.QueryEscape(b64), "mt.example.com:8443", secretFakeTLS},
		{"https://t.me/proxy?server=mt.example.com&port=8443&secret=" + b64, "mt.example.com:8443", secretFakeTLS},
		{"https://t.me/proxy?server=mt.example.com&port=443&secret=" + base64.RawURLEncoding.EncodeToString(ee), "mt.example.com:443", secretFakeTLS},
		{"http://telegram.me/proxy/?server=2001:db8::1&port=443&secret=" + hex.EncodeToString(testKey), "[2001:db8::1]:443", secretPlain},
		{"tg://proxy?server=[2001:db8::1]&port=443&secret=" + hex.EncodeToString(testKey), "[2001:db8::1]:443", secretPlain},
	}
	for _, c := range cases {
		addr, sec, err := parseMTProxyLink(c.link)
		if err != nil {
			t.Errorf("%s: %v", c.link, err)
			continue
		}
		if addr != c.addr || sec.Type != c.typ || !bytes.Equal(sec.Key, testKey) {
			t.Errorf("%s: got %s %s %x", c.link, addr, sec.Type, sec.Key)
		}
	}

	good := "secret=" + hex.EncodeToString(testKey)
	for _, link := range []string{
		"tg://socks?server=203.0.113.7&port=1080",
		"https://example.com/proxy?server=203.0.113.7&port=443&" + good,
		"https://t.me/joinchat?server=203.0.113.7&port=443&" + good,
		"tg://proxy?server=203.0.113.7&" + good,
		"tg://proxy?server=203.0.113.7&port=70000&" + good,
		"tg://proxy?port=443&" + good,
		"tg://proxy?server=203.0.113.7&port=443",
		"tg://proxy?server=203.0.113.7&port=443&secret=xyz",
		"vless://id@203.0.113.7:443",
	} {
		if _, _, err := parseMTProxyLink(link); err == nil {
			t.Errorf("%s: accepted", link)
		}
	}
}

func TestMTProtoSpec(t *testing.T) {
	var s Spec
	err := yaml.Unmarshal([]byte(`
id: mt
type: mtproto
mtproto:
  link: tg://proxy?server=203.0.113.7&port=443&secret=dd`+hex.EncodeToString(testKey)+`
  dc: 4
`), &s)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if s.Target() != "203.0.113.7:443" || s.MTProto.DC != 4 {
		t.Fatalf("target %q, dc %d", s.Target(), s.MTProto.DC)
	}

	s.MTProto = &MTProtoParams{Address: "mt.example.com:443", Secret: hex.EncodeToString(testKey)}
	if err := s.Validate(); err != nil || s.Target() != "mt.example.com:443" {
		t.Fatalf("address+secret: %v, target %q", err, s.Target())
	}
	for _, p := range []*MTProtoParams{
		nil,
		{Address: "mt.example.com:443"},
		{Secret: hex.EncodeToString(testKey)},
		{Address: "mt.example.com", Secret: hex.EncodeToString(testKey)},
		{Address: "mt.example.com:443", Secret: "nope"},
		{Address: "mt.example.com:443", Secret: hex.EncodeToString(testKey), DC: 1 << 20},
		{Link: "tg://proxy?server=203.0.113.7&port=443&secret=nope"},
	} {
		s.MTProto = p
		if err := s.Validate(); err == nil {
			t.Errorf("%+v: accepted", p)
		}
	}
}

// fakeDC is a Telegram DC that only knows req_pq_multi.
type fakeDC struct {
	addr string
	mode string       // "", "badnonce" or "error"
	dead map[int]bool // DCs that answer with a transport error

	mu     sync.Mutex
	hellos []dcHello
	errs   []error
}

type dcHello struct {
	tag uint32
	dc  int
}

func startFakeDC(t *testing.T, mode string) *fakeDC {
	ln := listen(t)
	d := &fakeDC{addr: ln.Addr().String(), mode: mode}
	go serve(ln, d.serve)
	return d
}

func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln
}

func serve(ln net.Listener, h func(net.Conn)) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			c.SetDeadline(time.Now().Add(10 * time.Second))
			h(c)
		}()
	}
}

func (d *fakeDC) last() (dcHello, []error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.hellos) == 0 {
		return dcHello{}, d.errs
	}
	return d.hellos[len(d.hellos)-1], d.errs
}

func (d *fakeDC) serve(c net.Conn) {
	init := make([]byte, 64)
	if _, err := io.ReadFull(c, init); err != nil {
		return
	}
	dec, enc := obfs2Streams(init, nil) // the client's forward stream decrypts
	plain := make([]byte, 64)
	dec.XORKeyStream(plain, init)
	h := dcHello{binary.LittleEndian.Uint32(plain[56:]), int(int16(binary.LittleEndian.Uint16(plain[60:])))}
	d.mu.Lock()
	d.hellos = append(d.hellos, h)
	d.mu.Unlock()

	r, w := cipher.StreamReader{S: dec, R: c}, cipher.StreamWriter{S: enc, W: c}
	for {
		var hdr [4]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return
		}
		n := binary.LittleEndian.Uint32(hdr[:])
		if n > 1<<16 {
			d.fail(fmt.Errorf("packet length %d", n))
			return
		}
		pkt := make([]byte, n)
		if _, err := io.ReadFull(r, pkt); err != nil {
			return
		}
		nonce, err := parseReqPQMulti(pkt, h.tag == tagPadded)
		if err != nil {
			d.fail(err)
			return
		}
		var resp []byte
		mode := d.mode
		if d.dead[h.dc] {
			mode = "error"
		}
		switch mode {
		case "error":
			resp = binary.LittleEndian.AppendUint32(nil, uint32(0xffffffff-404+1)) // -404
		case "badnonce":
			nonce[0] ^= 1
			fallthrough
		default:
			resp = resPQ(nonce)
		}
		pad := 0
		if h.tag == tagPadded {
			pad = 7
		}
		out := binary.LittleEndian.AppendUint32(nil, uint32(len(resp)+pad))
		out = append(append(out, resp...), make([]byte, pad)...)
		if _, err := w.Write(out); err != nil {
			return
		}
	}
}

func (d *fakeDC) fail(err error) {
	d.mu.Lock()
	d.errs = append(d.errs, err)
	d.mu.Unlock()
}

func parseReqPQMulti(p []byte, padded bool) ([]byte, error) {
	if len(p) < 40 || !padded && len(p) != 40 || len(p) > 40+15 {
		return nil, fmt.Errorf("req_pq_multi: %d bytes", len(p))
	}
	msgID := binary.LittleEndian.Uint64(p[8:])
	if skew := time.Since(time.Unix(int64(msgID>>32), 0)); skew < -time.Minute || skew > time.Minute || msgID%4 != 0 {
		return nil, fmt.Errorf("req_pq_multi: bad message id %#x", msgID)
	}
	if binary.LittleEndian.Uint64(p) != 0 || binary.LittleEndian.Uint32(p[16:]) != 20 || binary.LittleEndian.Uint32(p[20:]) != 0xbe7e8ef1 {
		return nil, fmt.Errorf("req_pq_multi: bad header % x", p[:24])
	}
	return slices.Clone(p[24:40]), nil
}

// resPQ answers like a DC: nonce, server_nonce, pq as a TL string and one
// key fingerprint.
func resPQ(nonce []byte) []byte {
	body := binary.LittleEndian.AppendUint32(nil, 0x05162463)
	body = append(body, nonce...)
	body = append(body, make([]byte, 16)...)
	rand.Read(body[len(body)-16:])
	body = append(body, 8, 0x17, 0xed, 0x48, 0x94, 0x1a, 0x08, 0xf9, 0x81, 0, 0, 0)
	body = binary.LittleEndian.AppendUint32(body, 0x1cb5c415)
	body = binary.LittleEndian.AppendUint32(body, 1)
	body = binary.LittleEndian.AppendUint64(body, 0xc3b42b026ce86b21)
	msg := make([]byte, 8, 20+len(body))
	msg = binary.LittleEndian.AppendUint64(msg, uint64(time.Now().Unix())<<32|1)
	msg = binary.LittleEndian.AppendUint32(msg, uint32(len(body)))
	return append(msg, body...)
}

// fakeProxy is a minimal MTProxy: it terminates the client's obfuscated2
// (inside fake TLS for ee secrets), opens its own obfuscated2 connection to
// the DC and relays. Bad fake-TLS hellos are fronted to front.
type fakeProxy struct {
	addr   string
	sec    *mtSecret
	dcAddr string
	front  string
}

func startFakeProxy(t *testing.T, sec *mtSecret, dcAddr, front string) *fakeProxy {
	ln := listen(t)
	p := &fakeProxy{addr: ln.Addr().String(), sec: sec, dcAddr: dcAddr, front: front}
	go serve(ln, p.serve)
	return p
}

func (p *fakeProxy) serve(c net.Conn) {
	var src io.Reader = c
	var dst io.Writer = c
	if p.sec.Type == secretFakeTLS {
		hello, err := readRecord(c)
		if err != nil {
			return
		}
		if !validClientHello(hello, p.sec) {
			if f, err := net.Dial("tcp", p.front); err == nil {
				defer f.Close()
				f.Write(hello)
				go io.Copy(f, c)
				io.Copy(c, f)
			}
			return
		}
		if _, err := c.Write(serverWelcome(p.sec.Key, hello)); err != nil {
			return
		}
		src, dst = &tlsReader{r: c}, tlsWriter{c}
	}

	init := make([]byte, 64)
	if _, err := io.ReadFull(src, init); err != nil {
		return
	}
	cdec, cenc := obfs2Streams(init, p.sec.Key)
	plain := make([]byte, 64)
	cdec.XORKeyStream(plain, init)
	tag, want := binary.LittleEndian.Uint32(plain[56:]), uint32(tagPadded)
	if p.sec.Type == secretPlain {
		want = tagIntermediate
	}
	if tag != want {
		return // wrong secret
	}
	up, err := net.Dial("tcp", p.dcAddr)
	if err != nil {
		return
	}
	defer up.Close()
	uinit, uenc, udec := obfs2Init(nil, tag, int(int16(binary.LittleEndian.Uint16(plain[60:]))))
	if _, err := up.Write(uinit); err != nil {
		return
	}
	go func() {
		io.Copy(cipher.StreamWriter{S: uenc, W: up}, cipher.StreamReader{S: cdec, R: src})
		up.Close()
	}()
	io.Copy(cipher.StreamWriter{S: cenc, W: dst}, cipher.StreamReader{S: udec, R: up})
}

func validClientHello(rec []byte, sec *mtSecret) bool {
	if len(rec) < 44 || rec[0] != recHandshake || rec[5] != 1 || !bytes.Contains(rec, []byte(sec.Domain)) {
		return false
	}
	zeroed := slices.Clone(rec)
	clear(zeroed[11:43])
	mac := hmac.New(sha256.New, sec.Key)
	mac.Write(zeroed)
	sum := mac.Sum(nil)
	for i := range sum {
		sum[i] ^= rec[11+i]
	}
	ts := time.Unix(int64(binary.LittleEndian.Uint32(sum[28:])), 0)
	return bytes.Equal(sum[:28], make([]byte, 28)) && time.Since(ts).Abs() < time.Minute
}

// serverWelcome is ServerHello + ChangeCipherSpec + noise, signed with
// HMAC(key, client random || welcome) in the server random.
func serverWelcome(key, hello []byte) []byte {
	sid := hello[44 : 44+int(hello[43])]
	sh := append([]byte{3, 3}, make([]byte, 32)...)
	sh = append(append(sh, byte(len(sid))), sid...)
	sh = append(sh, 0x13, 0x01, 0)
	ext := []byte{0, 0x2b, 0, 2, 3, 4, 0, 0x33, 0, 0x24, 0, 0x1d, 0, 0x20}
	ext = append(ext, make([]byte, 32)...)
	rand.Read(ext[len(ext)-32:])
	sh = append(binary.BigEndian.AppendUint16(sh, uint16(len(ext))), ext...)
	out := []byte{recHandshake, 3, 3, 0, byte(len(sh) + 4), 2, 0, 0, byte(len(sh))}
	out = append(out, sh...)
	out = append(out, recCCS, 3, 3, 0, 1, 1)
	noise := make([]byte, 1500)
	rand.Read(noise)
	out = append(binary.BigEndian.AppendUint16(append(out, recAppData, 3, 3), uint16(len(noise))), noise...)
	mac := hmac.New(sha256.New, key)
	mac.Write(hello[11:43])
	mac.Write(out)
	copy(out[11:43], mac.Sum(nil))
	return out
}

type tlsWriter struct{ w io.Writer }

func (t tlsWriter) Write(p []byte) (int, error) {
	for rest := p; len(rest) > 0; {
		n := min(len(rest), 1<<14)
		if _, err := t.w.Write(append([]byte{recAppData, 3, 3, byte(n >> 8), byte(n)}, rest[:n]...)); err != nil {
			return 0, err
		}
		rest = rest[n:]
	}
	return len(p), nil
}

func newSecret(t *testing.T, typ string) *mtSecret {
	t.Helper()
	key := make([]byte, 16)
	rand.Read(key)
	sec, err := decodeMTSecret(secretBytes(typ, key, "www.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	return sec
}

func mtLink(sec *mtSecret, host, port string) string {
	b := secretBytes(sec.Type, sec.Key, sec.Domain)
	return fmt.Sprintf("tg://proxy?server=%s&port=%s&secret=%s", host, port, url.QueryEscape(base64.StdEncoding.EncodeToString(b)))
}

// runMTProto runs the check against mtproxy.test:443, which only the env's
// Dial resolves (to addr), so the check must not dial on its own.
func runMTProto(t *testing.T, addr string, sec *mtSecret, dc int, timeout time.Duration) Result {
	t.Helper()
	s := &Spec{ID: "mt", Type: "mtproto", Timeout: Duration(timeout),
		MTProto: &MTProtoParams{Link: mtLink(sec, "mtproxy.test", "443"), DC: dc}}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	env := &Env{Dial: func(ctx context.Context, network, a string) (net.Conn, error) {
		if a != "mtproxy.test:443" {
			return nil, fmt.Errorf("unexpected dial to %s", a)
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}
	return Run(context.Background(), s, env)
}

func expectUp(t *testing.T, r Result, typ string, dc int) {
	t.Helper()
	if r.Status != Up {
		t.Fatalf("down: %s: %s", r.Reason, r.Error)
	}
	_, hasTLS := r.Phases["tls"]
	// Presence only: on loopback with a coarse clock (Windows) both can read 0.
	_, hasConnect := r.Phases["connect"]
	_, hasRTT := r.Phases["rtt"]
	if r.Info["secret_type"] != typ || r.Info["dc"] != fmt.Sprint(dc) || !hasConnect || !hasRTT || hasTLS != (typ == secretFakeTLS) {
		t.Fatalf("unexpected result %+v", r)
	}
}

func expectDown(t *testing.T, r Result, reason string) {
	t.Helper()
	if r.Status != Down || r.Reason != reason {
		t.Fatalf("want %s, got %s/%s: %s", reason, r.Status, r.Reason, r.Error)
	}
}

// tlsSite stands in for the real site behind a fake-TLS domain.
func tlsSite(t *testing.T) string {
	s := httptest.NewUnstartedServer(http.NotFoundHandler())
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.StartTLS()
	t.Cleanup(s.Close)
	return s.Listener.Addr().String()
}

func TestMTProtoFakeProxy(t *testing.T) {
	dc := startFakeDC(t, "")
	frontAddr := tlsSite(t)

	for _, typ := range []string{secretPlain, secretDD, secretFakeTLS} {
		t.Run(typ, func(t *testing.T) {
			sec := newSecret(t, typ)
			px := startFakeProxy(t, sec, dc.addr, frontAddr)
			for _, want := range []int{4, -2} {
				r := runMTProto(t, px.addr, sec, want, 5*time.Second)
				expectUp(t, r, typ, want)
				h, errs := dc.last()
				wantTag := uint32(tagPadded)
				if typ == secretPlain {
					wantTag = tagIntermediate
				}
				if h.dc != want || h.tag != wantTag || len(errs) > 0 {
					t.Fatalf("DC saw %+v (want dc %d, tag %#x), errors %v", h, want, wantTag, errs)
				}
			}

			wrong := *sec
			wrong.Key = newSecret(t, typ).Key
			reason := "handshake"
			if typ == secretFakeTLS {
				reason = "faketls"
			}
			expectDown(t, runMTProto(t, px.addr, &wrong, 2, 5*time.Second), reason)
		})
	}
}

func TestMTProtoAllDCs(t *testing.T) {
	sec := newSecret(t, secretFakeTLS)
	ask := func(t *testing.T, dead ...int) Result {
		dc := startFakeDC(t, "")
		dc.dead = map[int]bool{}
		for _, n := range dead {
			dc.dead[n] = true
		}
		return runMTProto(t, startFakeProxy(t, sec, dc.addr, tlsSite(t)).addr, sec, 0, 5*time.Second)
	}
	t.Run("all answer", func(t *testing.T) {
		r := ask(t)
		if r.Status != Up || r.Info["dc_down"] != "" || strings.Count(r.Info["dc_ms"], ":") != 5 || r.Info["dc"] != "" {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("one silent DC is shown, not an outage", func(t *testing.T) {
		r := ask(t, 4)
		if r.Status != Up || r.Info["dc_down"] != "4" || strings.Count(r.Info["dc_ms"], ":") != 4 || strings.Contains(r.Info["dc_ms"], "4:") {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("no DC answers", func(t *testing.T) {
		r := ask(t, 1, 2, 3, 4, 5)
		if r.Status != Down || r.Reason != "handshake" || r.Info["dc_down"] != "1,2,3,4,5" {
			t.Fatalf("%+v", r)
		}
	})
}

func TestMTProtoFailures(t *testing.T) {
	web := httptest.NewServer(http.NotFoundHandler())
	defer web.Close()
	stall := listen(t)
	go serve(stall, func(c net.Conn) { io.Copy(io.Discard, c) })
	closed := listen(t)
	closed.Close()
	ft, dd := newSecret(t, secretFakeTLS), newSecret(t, secretDD)

	cases := []struct {
		name, addr string
		sec        *mtSecret
		reason     string
	}{
		{"refused", closed.Addr().String(), dd, "refused"},
		{"real TLS server", tlsSite(t), ft, "faketls"},
		{"HTTP server", web.Listener.Addr().String(), ft, "faketls"},
		{"silent fake TLS", stall.Addr().String(), ft, "timeout"},
		{"silent obfuscated2", stall.Addr().String(), dd, "handshake"},
		{"DC unreachable", startFakeProxy(t, dd, closed.Addr().String(), "").addr, dd, "handshake"},
		{"DC transport error", startFakeProxy(t, dd, startFakeDC(t, "error").addr, "").addr, dd, "handshake"},
		{"resPQ nonce mismatch", startFakeProxy(t, ft, startFakeDC(t, "badnonce").addr, "").addr, ft, "handshake"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			expectDown(t, runMTProto(t, c.addr, c.sec, 2, time.Second), c.reason)
		})
	}
}

// The fake-TLS ClientHello must fit in one packet, as Telegram's does: a
// 1.8 KB current-Chrome hello with a post-quantum key share is dropped by
// DPI on some ISPs while Telegram itself gets through, and the check called
// a working proxy down.
func TestClientHelloLooksLikeTelegrams(t *testing.T) {
	for range 20 { // the extension order and padding vary between hellos
		h, err := clientHello("am246.videofinder.link")
		if err != nil {
			t.Fatal(err)
		}
		if len(h) > 700 {
			t.Fatalf("ClientHello is %d bytes; Telegram sends ~517, one packet", len(h))
		}
	}
}
