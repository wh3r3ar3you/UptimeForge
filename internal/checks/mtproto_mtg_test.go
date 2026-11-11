package checks

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/9seconds/mtg/v2/essentials"
	"github.com/9seconds/mtg/v2/mtglib"
)

// The fake-TLS client is cross-checked against mtg, an independent MTProxy
// server, with every DC address redirected to the fake DC.

type mtgNetwork struct {
	dc     string
	mu     sync.Mutex
	dialed []string
}

func (n *mtgNetwork) Dial(network, address string) (essentials.Conn, error) {
	return n.DialContext(context.Background(), network, address)
}

func (n *mtgNetwork) DialContext(ctx context.Context, _, address string) (essentials.Conn, error) {
	n.mu.Lock()
	n.dialed = append(n.dialed, address)
	n.mu.Unlock()
	c, err := (&net.Dialer{}).DialContext(ctx, "tcp", n.dc)
	if err != nil {
		return nil, err
	}
	return c.(*net.TCPConn), nil
}

func (n *mtgNetwork) last() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.dialed) == 0 {
		return ""
	}
	return n.dialed[len(n.dialed)-1]
}

func (n *mtgNetwork) MakeHTTPClient(func(context.Context, string, string) (essentials.Conn, error)) *http.Client {
	return &http.Client{}
}

func (n *mtgNetwork) NativeDialer() *net.Dialer { return &net.Dialer{} }

type mtgNop struct{ allow bool }

func (mtgNop) SeenBefore([]byte) bool                  { return false }
func (l mtgNop) Contains(net.IP) bool                  { return l.allow }
func (mtgNop) Run(time.Duration)                       {}
func (mtgNop) Shutdown()                               {}
func (mtgNop) Send(context.Context, mtglib.Event)      {}
func (l mtgNop) Named(string) mtglib.Logger            { return l }
func (l mtgNop) BindInt(string, int) mtglib.Logger     { return l }
func (l mtgNop) BindStr(string, string) mtglib.Logger  { return l }
func (l mtgNop) BindJSON(string, string) mtglib.Logger { return l }
func (mtgNop) Printf(string, ...any)                   {}
func (mtgNop) Info(string)                             {}
func (mtgNop) InfoError(string, error)                 {}
func (mtgNop) Warning(string)                          {}
func (mtgNop) WarningError(string, error)              {}
func (mtgNop) Debug(string)                            {}
func (mtgNop) DebugError(string, error)                {}

func startMTG(t *testing.T, secret mtglib.Secret, dc, front string) (string, *mtgNetwork) {
	t.Helper()
	nw := &mtgNetwork{dc: dc}
	frontHost, frontPort, _ := net.SplitHostPort(front)
	port, _ := strconv.Atoi(frontPort)
	proxy, err := mtglib.NewProxy(mtglib.ProxyOpts{
		Secret:             secret,
		Network:            nw,
		AntiReplayCache:    mtgNop{},
		IPBlocklist:        mtgNop{},
		IPAllowlist:        mtgNop{allow: true},
		EventStream:        mtgNop{},
		Logger:             mtgNop{},
		DomainFrontingIP:   frontHost,
		DomainFrontingPort: uint(port),
	})
	if err != nil {
		t.Fatal(err)
	}
	ln := listen(t)
	go proxy.Serve(ln)
	t.Cleanup(func() {
		ln.Close()
		proxy.Shutdown()
	})
	return ln.Addr().String(), nw
}

func TestMTProtoAgainstMTG(t *testing.T) {
	dc := startFakeDC(t, "")
	secret := mtglib.GenerateSecret("www.example.com")
	addr, nw := startMTG(t, secret, dc.addr, tlsSite(t))
	host, port, _ := net.SplitHostPort(addr)

	// mtg's own encodings of the secret, in both link forms.
	for i, link := range []string{
		"tg://proxy?server=" + host + "&port=" + port + "&secret=" + secret.Hex(),
		"https://t.me/proxy?server=" + host + "&port=" + port + "&secret=" + url.QueryEscape(secret.Base64()),
	} {
		want := []int{2, 4}[i]
		s := &Spec{ID: "mtg", Type: "mtproto", Timeout: Duration(5 * time.Second), MTProto: &MTProtoParams{Link: link, DC: want}}
		if err := s.Validate(); err != nil {
			t.Fatal(err)
		}
		expectUp(t, Run(context.Background(), s, &Env{}), secretFakeTLS, want)
		h, errs := dc.last()
		if h.tag != tagPadded || h.dc != want || len(errs) > 0 {
			t.Fatalf("DC saw %+v (want dc %d), errors %v", h, want, errs)
		}
		if a := nw.last(); !slices.Contains(essentials.TelegramCoreAddresses[want], a) {
			t.Fatalf("mtg dialed %s for DC %d", a, want)
		}
	}

	sec, err := parseMTProxySecret(secret.Hex())
	if err != nil || sec.Domain != secret.Host || string(sec.Key) != string(secret.Key[:]) {
		t.Fatalf("parsed mtg secret as %+v, %v", sec, err)
	}
	wrongKey := *sec
	wrongKey.Key = newSecret(t, secretFakeTLS).Key
	wrongDomain := *sec
	wrongDomain.Domain = "www.example.org"
	for _, s := range []*mtSecret{&wrongKey, &wrongDomain} {
		// mtg fronts unknown clients to the real site, whose ServerHello
		// carries no valid HMAC.
		expectDown(t, runMTProto(t, addr, s, 2, 5*time.Second), "faketls")
	}
}
