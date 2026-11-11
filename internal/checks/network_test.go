package checks

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/wh3r3ar3you/uptimeforge/internal/xray"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"
	"layeh.com/radius"
	"layeh.com/radius/rfc2865"
	"layeh.com/radius/rfc2869"
)

// udpServer answers each datagram with what handle returns; nil means silence.
func udpServer(t *testing.T, handle func([]byte) []byte) (string, string) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 65535)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if out := handle(buf[:n]); out != nil {
				pc.WriteTo(out, addr)
			}
		}
	}()
	return hostPort(t, pc.LocalAddr().String())
}

// tcpServer runs handle for every connection and closes it afterwards.
func tcpServer(t *testing.T, handle func(net.Conn)) (string, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				handle(c)
			}()
		}
	}()
	return hostPort(t, ln.Addr().String())
}

func ntpReply(skew time.Duration, stratum byte, leap byte) func([]byte) []byte {
	return func(req []byte) []byte {
		if len(req) != ntpPacketLen {
			return nil
		}
		out := make([]byte, ntpPacketLen)
		out[0] = leap<<6 | 4<<3 | ntpModeSrv
		out[1] = stratum
		copy(out[12:], "XXXX")
		copy(out[24:32], req[40:48])
		now := time.Now().Add(skew)
		put := func(b []byte) {
			binary.BigEndian.PutUint32(b, uint32(now.Unix()+ntpEpochDelta))
			binary.BigEndian.PutUint32(b[4:], uint32((int64(now.Nanosecond())<<32)/1e9))
		}
		put(out[32:])
		put(out[40:])
		return out
	}
}

func TestNTP(t *testing.T) {
	spec := func(host, port string, limit string) *Spec {
		p := map[string]string{"host": host, "port": port}
		if limit != "" {
			p["max_offset"] = limit
		}
		return &Spec{Type: "ntp", Timeout: Duration(3 * time.Second), Params: p}
	}
	h, p := udpServer(t, ntpReply(0, 2, 0))
	r := runSpec(t, spec(h, p, "1000"))
	wantUp(t, r)
	if r.Info["stratum"] != "2" {
		t.Fatalf("%+v", r)
	}
	h, p = udpServer(t, ntpReply(5*time.Second, 2, 0))
	r = runSpec(t, spec(h, p, "1000"))
	wantDown(t, r, "ntp_offset")
	wantUp(t, runSpec(t, spec(h, p, ""))) // no limit, no opinion
	h, p = udpServer(t, ntpReply(0, 0, 0))
	r = runSpec(t, spec(h, p, ""))
	wantDown(t, r, "bad_response")
	if !strings.Contains(r.Error, "XXXX") {
		t.Fatalf("must show the kiss code: %+v", r)
	}
	h, p = udpServer(t, ntpReply(0, 2, 3))
	wantDown(t, runSpec(t, spec(h, p, "")), "bad_response")
	// A reply to somebody else's request is not an answer.
	h, p = udpServer(t, func(req []byte) []byte {
		b := ntpReply(0, 2, 0)(req)
		b[24] ^= 0xff
		return b
	})
	s := spec(h, p, "")
	s.Timeout = Duration(500 * time.Millisecond)
	wantDown(t, runSpec(t, s), "timeout")
}

func TestSilentUDPServerIsTimeoutAndNoLeak(t *testing.T) {
	h, p := udpServer(t, func([]byte) []byte { return nil })
	specs := []*Spec{
		{Type: "ntp", Params: map[string]string{"host": h, "port": p}},
		{Type: "radius", Params: map[string]string{"host": h, "port": p, "username": "u", "password": "p", "secret": "s"}},
		{Type: "sip-options", Params: map[string]string{"host": h, "port": p}},
		{Type: "gamedig", Params: map[string]string{"game": "source", "host": h, "port": p}},
		{Type: "gamedig", Params: map[string]string{"game": "minecraft-bedrock", "host": h, "port": p}},
	}
	before := runtime.NumGoroutine()
	for _, s := range specs {
		s.Timeout = Duration(200 * time.Millisecond)
		start := time.Now()
		r := runSpec(t, s)
		wantDown(t, r, "timeout")
		if time.Since(start) > 2*time.Second {
			t.Errorf("%s ignored its timeout: %s", s.Type, time.Since(start))
		}
	}
	waitGoroutines(t, before)
}

// waitGoroutines fails if the count does not come back to the baseline.
func waitGoroutines(t *testing.T, base int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= base {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	buf := make([]byte, 1<<16)
	t.Errorf("goroutines leaked: %d > %d\n%s", runtime.NumGoroutine(), base, buf[:runtime.Stack(buf, true)])
}

func smtpServer(t *testing.T, banner string, startTLS bool, ehlo string) (string, string) {
	cert := testCert(t)
	return tcpServer(t, func(c net.Conn) {
		fmt.Fprintf(c, "%s\r\n", banner)
		var conn net.Conn = c
		br := bufio.NewReader(conn)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			switch strings.Fields(line)[0] {
			case "EHLO":
				if ehlo != "" {
					fmt.Fprintf(conn, "%s\r\n", ehlo)
					continue
				}
				if startTLS {
					fmt.Fprint(conn, "250-fake\r\n250 STARTTLS\r\n")
				} else {
					fmt.Fprint(conn, "250 fake\r\n")
				}
			case "STARTTLS":
				fmt.Fprint(conn, "220 go\r\n")
				tc := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}})
				if tc.Handshake() != nil {
					return
				}
				conn, startTLS = tc, false
				br = bufio.NewReader(conn)
			case "QUIT":
				fmt.Fprint(conn, "221 bye\r\n")
				return
			}
		}
	})
}

func TestSMTP(t *testing.T) {
	spec := func(h, p, sec string, keyword string) *Spec {
		return &Spec{Type: "smtp", Keyword: keyword, Params: map[string]string{"host": h, "port": p, "smtp_security": sec, "insecure": "true"}}
	}
	h, p := smtpServer(t, "220 mail.test ESMTP", false, "")
	r := runSpec(t, spec(h, p, "none", "ESMTP"))
	wantUp(t, r)
	if r.Info["banner"] != "mail.test ESMTP" {
		t.Fatalf("%+v", r)
	}
	wantDown(t, runSpec(t, spec(h, p, "none", "Exchange")), "keyword")
	wantDown(t, runSpec(t, spec(h, p, "starttls", "")), "tls") // not offered

	h, p = smtpServer(t, "220 mail.test ESMTP", true, "")
	wantUp(t, runSpec(t, spec(h, p, "starttls", "")))
	// The certificate is self-signed, and only insecure lets it pass.
	strict := spec(h, p, "starttls", "")
	delete(strict.Params, "insecure")
	wantDown(t, runSpec(t, strict), "tls")

	h, p = smtpServer(t, "554 go away", false, "")
	wantDown(t, runSpec(t, spec(h, p, "none", "")), "bad_response")
	h, p = smtpServer(t, "220 ok", false, "502 no EHLO")
	wantDown(t, runSpec(t, spec(h, p, "none", "")), "bad_response")
	h, p = smtpServer(t, "SSH-2.0-OpenSSH", false, "")
	wantDown(t, runSpec(t, spec(h, p, "none", "")), "bad_response")

	cert := testCert(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				fmt.Fprint(c, "220 smtps ready\r\n")
				br := bufio.NewReader(c)
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					if strings.HasPrefix(line, "EHLO") {
						fmt.Fprint(c, "250 fake\r\n")
					}
				}
			}()
		}
	}()
	h, p = hostPort(t, ln.Addr().String())
	wantUp(t, runSpec(t, spec(h, p, "smtps", "smtps")))
}

func sipReply(status string) func([]byte) []byte {
	return func(req []byte) []byte {
		head := string(req)
		if !strings.HasPrefix(head, "OPTIONS ") {
			return nil
		}
		var callID string
		for _, l := range strings.Split(head, "\r\n") {
			if strings.HasPrefix(l, "Call-ID:") {
				callID = strings.TrimSpace(strings.TrimPrefix(l, "Call-ID:"))
			}
		}
		return []byte("SIP/2.0 " + status + "\r\nCall-ID: " + callID + "\r\nCSeq: 1 OPTIONS\r\nContent-Length: 0\r\n\r\n")
	}
}

func TestSIPOptions(t *testing.T) {
	spec := func(h, p, tr string) *Spec {
		return &Spec{Type: "sip-options", Params: map[string]string{"host": h, "port": p, "transport": tr, "insecure": "true"}}
	}
	h, p := udpServer(t, sipReply("200 OK"))
	r := runSpec(t, spec(h, p, "udp"))
	wantUp(t, r)
	if r.Info["status_code"] != "200" {
		t.Fatalf("%+v", r)
	}
	h, p = udpServer(t, sipReply("503 Service Unavailable"))
	r = runSpec(t, spec(h, p, "udp"))
	wantDown(t, r, "sip_status")
	if !strings.Contains(r.Error, "503") {
		t.Fatalf("%+v", r)
	}
	// 100 Trying is only progress.
	var n int
	h, p = udpServer(t, func(req []byte) []byte {
		n++
		if n == 1 {
			return sipReply("100 Trying")(req)
		}
		return sipReply("200 OK")(req)
	})
	wantUp(t, runSpec(t, spec(h, p, "udp")))

	stream := func(status string) func(net.Conn) {
		return func(c net.Conn) {
			br := bufio.NewReader(c)
			var req strings.Builder
			for {
				line, err := br.ReadString('\n')
				if err != nil {
					return
				}
				req.WriteString(line)
				if line == "\r\n" {
					break
				}
			}
			if status == "garbage" {
				fmt.Fprint(c, "HTTP/1.1 400 Bad Request\r\n\r\n")
				return
			}
			c.Write(sipReply("100 Trying")([]byte(req.String())))
			c.Write(sipReply(status)([]byte(req.String())))
		}
	}
	h, p = tcpServer(t, stream("200 OK"))
	wantUp(t, runSpec(t, spec(h, p, "tcp")))
	h, p = tcpServer(t, stream("404 Not Found"))
	wantDown(t, runSpec(t, spec(h, p, "tcp")), "sip_status")
	h, p = tcpServer(t, stream("garbage"))
	wantDown(t, runSpec(t, spec(h, p, "tcp")), "bad_response")

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{testCert(t)}})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				stream("200 OK")(c)
			}()
		}
	}()
	h, p = hostPort(t, ln.Addr().String())
	wantUp(t, runSpec(t, spec(h, p, "tls")))
}

func radiusServer(t *testing.T, secret string, requireAuthenticator bool) (string, string) {
	return udpServer(t, func(b []byte) []byte {
		pkt, err := radius.Parse(b, []byte(secret))
		if err != nil {
			return nil
		}
		given := rfc2869.MessageAuthenticator_Get(pkt)
		if requireAuthenticator {
			if len(given) != md5.Size {
				return nil
			}
			check := append([]byte(nil), b...)
			// The attribute is the only one of its type, find it and zero the MAC.
			for i := 20; i < len(check); i += int(check[i+1]) {
				if check[i] == byte(rfc2869.MessageAuthenticator_Type) {
					clear(check[i+2 : i+18])
				}
			}
			mac := hmac.New(md5.New, []byte(secret))
			mac.Write(check)
			if !hmac.Equal(mac.Sum(nil), given) {
				return nil
			}
		}
		code := radius.CodeAccessReject
		if rfc2865.UserName_GetString(pkt) == "bob" && rfc2865.UserPassword_GetString(pkt) == "pw" {
			code = radius.CodeAccessAccept
		}
		out, err := pkt.Response(code).Encode()
		if err != nil {
			return nil
		}
		return out
	})
}

func TestRADIUS(t *testing.T) {
	h, p := radiusServer(t, "shared", true)
	spec := func(pass, secret string) *Spec {
		return &Spec{Type: "radius", Timeout: Duration(2 * time.Second),
			Params: map[string]string{"host": h, "port": p, "username": "bob", "password": pass, "secret": secret}}
	}
	wantUp(t, runSpec(t, spec("pw", "shared")))
	r := runSpec(t, spec("wrong", "shared"))
	wantDown(t, r, "auth")
	// A wrong shared secret: this server cannot read the request and stays
	// silent, which is what real ones do.
	s := spec("pw", "other")
	s.Timeout = Duration(300 * time.Millisecond)
	r = runSpec(t, s)
	wantDown(t, r, "timeout")
	if !strings.Contains(r.Error, "secret") {
		t.Fatalf("must mention the secret as a cause: %+v", r)
	}
	if strings.Contains(r.Error, "other") {
		t.Fatalf("leak: %+v", r)
	}
}

func TestRedis(t *testing.T) {
	server := func(pass string, reply string) (string, string) {
		return tcpServer(t, func(c net.Conn) {
			br := bufio.NewReader(c)
			authed := pass == ""
			for {
				line, err := br.ReadString('\n')
				if err != nil || !strings.HasPrefix(line, "*") {
					return
				}
				var n int
				fmt.Sscanf(line, "*%d", &n)
				args := make([]string, n)
				for i := range args {
					br.ReadString('\n') // length
					a, _ := br.ReadString('\n')
					args[i] = strings.TrimSpace(a)
				}
				switch args[0] {
				case "AUTH":
					if args[len(args)-1] == pass {
						authed = true
						fmt.Fprint(c, "+OK\r\n")
					} else {
						fmt.Fprint(c, "-WRONGPASS invalid username-password pair\r\n")
					}
				case "PING":
					if !authed {
						fmt.Fprint(c, "-NOAUTH Authentication required.\r\n")
					} else {
						fmt.Fprint(c, reply+"\r\n")
					}
				case "QUIT":
					return
				}
			}
		})
	}
	spec := func(h, p, user, pass string) *Spec {
		return &Spec{Type: "redis", Params: map[string]string{"host": h, "port": p, "username": user, "password": pass}}
	}
	h, p := server("", "+PONG")
	wantUp(t, runSpec(t, spec(h, p, "", "")))
	h, p = server("pw", "+PONG")
	wantUp(t, runSpec(t, spec(h, p, "", "pw")))
	wantUp(t, runSpec(t, spec(h, p, "bob", "pw")))
	wantDown(t, runSpec(t, spec(h, p, "", "")), "auth")
	r := runSpec(t, spec(h, p, "", "wrongpass"))
	wantDown(t, r, "auth")
	if strings.Contains(r.Error, "wrongpass") {
		t.Fatalf("leak: %+v", r)
	}
	h, p = server("", "-LOADING Redis is loading the dataset in memory")
	r = runSpec(t, spec(h, p, "", ""))
	wantDown(t, r, "bad_response")
	if !strings.Contains(r.Error, "LOADING") {
		t.Fatalf("%+v", r)
	}
	h, p = closedPort(t)
	wantDown(t, runSpec(t, spec(h, p, "", "")), "refused")
}

func TestPostgres(t *testing.T) {
	dsn := os.Getenv("UF_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("UF_TEST_DATABASE_URL is not set")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	pass, _ := u.User.Password()
	spec := func(extra map[string]string) *Spec {
		p := map[string]string{"host": u.Hostname(), "port": u.Port(), "username": u.User.Username(), "password": pass,
			"database": strings.TrimPrefix(u.Path, "/"), "tls_mode": "disable"}
		for k, v := range extra {
			p[k] = v
		}
		return &Spec{Type: "postgres", Timeout: Duration(10 * time.Second), Params: p}
	}
	wantUp(t, runSpec(t, spec(nil)))
	wantUp(t, runSpec(t, spec(map[string]string{"tls_mode": "prefer"})))
	s := spec(map[string]string{"query": "SELECT 'hello ' || 'world', 2"})
	s.Keyword = "hello world"
	wantUp(t, runSpec(t, s))
	s = spec(map[string]string{"query": "SELECT 'hello'"})
	s.Keyword = "absent"
	wantDown(t, runSpec(t, s), "keyword")
	s = spec(map[string]string{"query": "SELECT 1 WHERE false"})
	s.Keyword = "x"
	wantDown(t, runSpec(t, s), "keyword")
	wantDown(t, runSpec(t, spec(map[string]string{"query": "SELECT * FROM no_such_table"})), "query")
	r := runSpec(t, spec(map[string]string{"password": "wrong-password-value"}))
	wantDown(t, r, "auth")
	if strings.Contains(r.Error, "wrong-password-value") {
		t.Fatalf("leak: %+v", r)
	}
	wantDown(t, runSpec(t, spec(map[string]string{"database": "no_such_db"})), "not_found")
}

func TestPostgresAndMySQLRefused(t *testing.T) {
	h, p := closedPort(t)
	wantDown(t, runSpec(t, &Spec{Type: "postgres", Params: map[string]string{"host": h, "port": p, "username": "u", "password": "topsecret"}}), "refused")
	r := runSpec(t, &Spec{Type: "mysql", Params: map[string]string{"host": h, "port": p, "username": "u", "password": "topsecret"}})
	wantDown(t, r, "refused")
	if strings.Contains(r.Error, "topsecret") {
		t.Fatalf("leak: %+v", r)
	}
}

func sftpServer(t *testing.T, password string, clientKey ssh.PublicKey, withSFTP bool) (string, string, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if string(pw) == password {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if clientKey != nil && string(key.Marshal()) == string(clientKey.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
	}
	cfg.AddHostKey(signer)
	h, p := tcpServer(t, func(c net.Conn) {
		sc, chans, reqs, err := ssh.NewServerConn(c, cfg)
		if err != nil {
			return
		}
		defer sc.Close()
		go ssh.DiscardRequests(reqs)
		for nc := range chans {
			ch, requests, err := nc.Accept()
			if err != nil {
				return
			}
			go func() {
				for req := range requests {
					ok := withSFTP && req.Type == "subsystem" && strings.HasSuffix(string(req.Payload), "sftp")
					req.Reply(ok, nil)
					if ok {
						srv, err := sftp.NewServer(ch)
						if err == nil {
							srv.Serve()
							srv.Close()
						}
						return
					}
				}
			}()
		}
	})
	return h, p, ssh.FingerprintSHA256(signer.PublicKey())
}

func TestSFTP(t *testing.T) {
	_, clientPriv, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKey(clientPriv, "")
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := string(pem.EncodeToMemory(block))
	clientSigner, _ := ssh.NewSignerFromKey(clientPriv)

	h, p, fingerprint := sftpServer(t, "pw", clientSigner.PublicKey(), true)
	dir := t.TempDir()
	file := filepath.ToSlash(filepath.Join(dir, "there"))
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := func(extra map[string]string) *Spec {
		ps := map[string]string{"host": h, "port": p, "username": "bob", "password": "pw"}
		for k, v := range extra {
			ps[k] = v
		}
		return &Spec{Type: "sftp", Params: ps}
	}
	wantUp(t, runSpec(t, spec(nil)))
	wantUp(t, runSpec(t, spec(map[string]string{"path": file})))
	wantDown(t, runSpec(t, spec(map[string]string{"path": file + ".missing"})), "not_found")
	r := runSpec(t, spec(map[string]string{"password": "wrongpass"}))
	wantDown(t, r, "auth")
	if strings.Contains(r.Error, "wrongpass") {
		t.Fatalf("leak: %+v", r)
	}
	wantUp(t, runSpec(t, spec(map[string]string{"host_key": fingerprint})))
	wantDown(t, runSpec(t, spec(map[string]string{"host_key": "SHA256:nope"})), "host_key")
	keyed := &Spec{Type: "sftp", Params: map[string]string{"host": h, "port": p, "username": "bob", "auth": "key", "private_key": keyPEM}}
	wantUp(t, runSpec(t, keyed))
	if err := (&Spec{ID: "x", Type: "sftp", Params: map[string]string{"host": h, "username": "bob", "auth": "key", "private_key": "garbage"}}).Validate(); err == nil {
		t.Fatal("a broken key passed validation")
	}

	h2, p2, _ := sftpServer(t, "pw", nil, false)
	wantDown(t, runSpec(t, &Spec{Type: "sftp", Params: map[string]string{"host": h2, "port": p2, "username": "bob", "password": "pw"}}), "bad_response")
	h3, p3 := closedPort(t)
	wantDown(t, runSpec(t, &Spec{Type: "sftp", Params: map[string]string{"host": h3, "port": p3, "username": "bob", "password": "pw"}}), "refused")
}

func TestGameSource(t *testing.T) {
	const challenge = "\xde\xad\xbe\xef"
	info := append(append([]byte{0xff, 0xff, 0xff, 0xff, 'I', 17}, "Name\x00de_dust\x00cs\x00CS\x00"...), 0x0a, 0x00, 7, 32)
	h, p := udpServer(t, func(b []byte) []byte {
		if !strings.HasPrefix(string(b), string(a2sQuery)) {
			return nil
		}
		if len(b) == len(a2sQuery) { // no challenge yet
			return append([]byte{0xff, 0xff, 0xff, 0xff, 'A'}, challenge...)
		}
		if string(b[len(a2sQuery):]) != challenge {
			return nil
		}
		return info
	})
	r := runSpec(t, &Spec{Type: "gamedig", Params: map[string]string{"game": "source", "host": h, "port": p}})
	wantUp(t, r)
	if r.Info["name"] != "Name" || r.Info["players"] != "7" || r.Info["max_players"] != "32" {
		t.Fatalf("%+v", r)
	}
	h, p = udpServer(t, func(b []byte) []byte { return []byte{0xff, 0xff, 0xff, 0xff, 'Z', 0} })
	wantDown(t, runSpec(t, &Spec{Type: "gamedig", Params: map[string]string{"game": "source", "host": h, "port": p}}), "bad_response")
}

func TestGameMinecraft(t *testing.T) {
	h, p := tcpServer(t, func(c net.Conn) {
		br := bufio.NewReader(c)
		n, err := readVarint(br)
		if err != nil {
			return
		}
		frame := make([]byte, n)
		if _, err := io.ReadFull(br, frame); err != nil {
			return
		}
		r := strings.NewReader(string(frame))
		if id, _ := readVarint(r); id != 0 {
			t.Errorf("handshake id %d", id)
		}
		if proto, _ := readVarint(r); uint32(proto) != 0xffffffff {
			t.Errorf("protocol %x", uint32(proto))
		}
		if len2, _ := readVarint(br); len2 != 1 {
			t.Errorf("status request frame %d", len2)
		}
		body := `{"version":{"name":"1.20"},"players":{"max":20,"online":3}}`
		resp := appendVarint([]byte{0}, uint32(len(body)))
		resp = append(resp, body...)
		c.Write(append(appendVarint(nil, uint32(len(resp))), resp...))
	})
	r := runSpec(t, &Spec{Type: "gamedig", Params: map[string]string{"game": "minecraft", "host": h, "port": p}})
	wantUp(t, r)
	if r.Info["version"] != "1.20" || r.Info["players"] != "3" {
		t.Fatalf("%+v", r)
	}
	h, p = tcpServer(t, func(c net.Conn) {
		buf := make([]byte, 1024)
		c.Read(buf) // closing with unread data would be a reset, not this bad answer
		fmt.Fprint(c, "\x05hello")
		c.Read(buf)
	})
	wantDown(t, runSpec(t, &Spec{Type: "gamedig", Params: map[string]string{"game": "minecraft", "host": h, "port": p}}), "bad_response")
	h, p = closedPort(t)
	wantDown(t, runSpec(t, &Spec{Type: "gamedig", Params: map[string]string{"game": "minecraft", "host": h, "port": p}}), "refused")
}

func TestGameBedrock(t *testing.T) {
	motd := "MCPE;Dedicated;589;1.20.0;4;10;123;world;Survival;1;19132;19133;"
	h, p := udpServer(t, func(b []byte) []byte {
		if len(b) < 33 || b[0] != 1 || string(b[9:25]) != string(raknetMagic) {
			return nil
		}
		out := append([]byte{0x1c}, b[1:9]...)
		out = append(out, make([]byte, 8)...)
		out = append(out, raknetMagic...)
		out = binary.BigEndian.AppendUint16(out, uint16(len(motd)))
		return append(out, motd...)
	})
	r := runSpec(t, &Spec{Type: "gamedig", Params: map[string]string{"game": "minecraft-bedrock", "host": h, "port": p}})
	wantUp(t, r)
	if r.Info["name"] != "Dedicated" || r.Info["players"] != "4" {
		t.Fatalf("%+v", r)
	}
}

func TestDialUsesProbeDialer(t *testing.T) {
	sentinel := errors.New("dialer was used")
	var used int
	env := func() *Env {
		return &Env{Dial: func(context.Context, string, string) (net.Conn, error) {
			used++
			return nil, sentinel
		}}
	}
	for _, s := range []Spec{
		{Type: "ntp", Params: map[string]string{"host": "x.invalid"}},
		{Type: "smtp", Params: map[string]string{"host": "x.invalid"}},
		{Type: "sip-options", Params: map[string]string{"host": "x.invalid"}},
		{Type: "radius", Params: map[string]string{"host": "x.invalid", "username": "u", "password": "p", "secret": "s"}},
		{Type: "sftp", Params: map[string]string{"host": "x.invalid", "username": "u", "password": "p"}},
		{Type: "postgres", Params: map[string]string{"host": "x.invalid", "username": "u"}},
		{Type: "mysql", Params: map[string]string{"host": "x.invalid", "username": "u"}},
		{Type: "redis", Params: map[string]string{"host": "x.invalid"}},
		{Type: "gamedig", Params: map[string]string{"game": "source", "host": "x.invalid", "port": "1"}},
		{Type: "gamedig", Params: map[string]string{"game": "minecraft", "host": "x.invalid", "port": "1"}},
		{Type: "websocket-upgrade", Params: map[string]string{"url": "ws://x.invalid/"}},
		{Type: "rabbitmq", Params: map[string]string{"nodes": "http://x.invalid", "username": "u", "password": "p"}},
		{Type: "steam", Params: map[string]string{"host": "192.0.2.1", "port": "1", "api_key": "k"}},
		{Type: "docker", Params: map[string]string{"container": "c", "docker_host": "tcp://x.invalid:2375"}},
		{Type: "globalping", Params: map[string]string{"target": "x.invalid"}},
	} {
		s.ID, s.Timeout = "d", Duration(2*time.Second)
		used = 0
		if err := s.Validate(); err != nil {
			t.Fatalf("%s: %v", s.Type, err)
		}
		r := Run(context.Background(), &s, env())
		if used == 0 || r.Status != Down {
			t.Errorf("%s: did not go through Env.Dial: %+v", s.Type, r)
		}
	}
}

// A proxy probe sends its datagrams through the tunnel too, and xray connections
// ignore deadlines, so these must rest on ctx alone.
func TestThroughUpstreamProxy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, port := hostPort(t, ln.Addr().String())
	ln.Close()
	server, err := xray.StartServer(`{"log":{"loglevel":"none"},"inbounds":[{"listen":"` + host + `","port":` + port +
		`,"protocol":"socks","settings":{"auth":"noauth","udp":true,"ip":"` + host + `"}}],"outbounds":[{"protocol":"freedom"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	proxy := func(addr string) func() *Env {
		via, _ := xray.ParseLink("socks5://" + addr)
		viaJSON, _ := json.Marshal(via.Outbound)
		inst, err := xray.Start(nil, via.Outbound, "")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { inst.Close() })
		return func() *Env { return &Env{Dial: inst.DialContext, Via: viaJSON} }
	}
	env := proxy(net.JoinHostPort(host, port))

	run := func(s *Spec) Result {
		s.ID, s.Timeout = "v", Duration(5*time.Second)
		return Run(context.Background(), s, env())
	}
	h, p := udpServer(t, ntpReply(0, 2, 0))
	wantUp(t, run(&Spec{Type: "ntp", Params: map[string]string{"host": h, "port": p}}))
	h, p = radiusServer(t, "shared", true)
	wantUp(t, run(&Spec{Type: "radius", Params: map[string]string{"host": h, "port": p, "username": "bob", "password": "pw", "secret": "shared"}}))
	h, p = udpServer(t, sipReply("200 OK"))
	wantUp(t, run(&Spec{Type: "sip-options", Params: map[string]string{"host": h, "port": p}}))

	h, p = smtpServer(t, "220 via ESMTP", false, "")
	wantUp(t, run(&Spec{Type: "smtp", Params: map[string]string{"host": h, "port": p}}))

	// Silence must end with ctx although reads through the tunnel have no deadline.
	h, p = udpServer(t, func([]byte) []byte { return nil })
	start := time.Now()
	s := &Spec{ID: "v", Type: "ntp", Timeout: Duration(500 * time.Millisecond), Params: map[string]string{"host": h, "port": p}}
	wantDown(t, Run(context.Background(), s, env()), "timeout")
	if time.Since(start) > 3*time.Second {
		t.Errorf("took %s", time.Since(start))
	}

	// A proxy that does not carry UDP is not the NTP server's fault.
	noUDP := proxy(startSOCKS(t))
	s = &Spec{ID: "v", Type: "ntp", Timeout: Duration(2 * time.Second), Params: map[string]string{"host": "127.0.0.1", "port": "123"}}
	wantDown(t, Run(context.Background(), s, noUDP()), "unavailable")
}

func TestParamsFromYAML(t *testing.T) {
	var s Spec
	err := yaml.Unmarshal([]byte("id: y\ntype: redis\nkeyword_invert: true\nparams:\n  host: db\n  port: 6380\n  tls: true\n"), &s)
	if err != nil {
		t.Fatal(err)
	}
	if s.Params["port"] != "6380" || s.Params["tls"] != "true" || !s.KeywordInvert {
		t.Fatalf("%+v", s)
	}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestParamsDoNotLeakInJSON(t *testing.T) {
	raw, err := json.Marshal(Result{CheckID: "x", Text: "body"})
	if err != nil || strings.Contains(string(raw), "body") {
		t.Fatalf("%s %v", raw, err)
	}
}

func TestRADIUSWrongSecretAnswered(t *testing.T) {
	h, p := udpServer(t, func(b []byte) []byte {
		pkt, err := radius.Parse(b, []byte("server-side"))
		if err != nil {
			return nil
		}
		out, _ := pkt.Response(radius.CodeAccessAccept).Encode()
		return out
	})
	r := runSpec(t, &Spec{Type: "radius", Timeout: Duration(300 * time.Millisecond),
		Params: map[string]string{"host": h, "port": p, "username": "bob", "password": "pw", "secret": "client-side"}})
	wantDown(t, r, "bad_response")
}
