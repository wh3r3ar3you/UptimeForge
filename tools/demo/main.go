// Command demo runs a self-contained UptimeForge playground for UI work and
// screenshots: a hub with seeded history, real VLESS nodes, probes behind a
// simulated DPI box, an offline probe, a dead node, and fake Remnawave and
// Telegram APIs.
//
//	go run ./tools/demo            # then open http://localhost:8080, admin / admin12345
package main

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	xcore "github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf/serial"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/wh3r3ar3you/uptimeforge/internal/agent"
	"github.com/wh3r3ar3you/uptimeforge/internal/api"
	"github.com/wh3r3ar3you/uptimeforge/internal/app"
	"github.com/wh3r3ar3you/uptimeforge/internal/checks"
	"github.com/wh3r3ar3you/uptimeforge/internal/geoip"
	"github.com/wh3r3ar3you/uptimeforge/internal/hub"
	"github.com/wh3r3ar3you/uptimeforge/internal/model"
	"github.com/wh3r3ar3you/uptimeforge/internal/store"
)

const uuid = "b831381d-6324-4d53-ad4f-8cda48b30811"

const demoSchema = "uptimeforge_demo"

// demoDatabase wipes only its own schema, so pointing the demo at a real
// database cannot touch the data there.
func demoDatabase(base string) string {
	if base == "" {
		must(errors.New("set UF_DATABASE_URL (or --database-url) to a PostgreSQL server"))
	}
	u, err := url.Parse(base)
	must(err)
	db, err := sql.Open("pgx", base)
	must(err)
	defer db.Close()
	_, err = db.Exec(`DROP SCHEMA IF EXISTS ` + demoSchema + ` CASCADE`)
	must(err)
	_, err = db.Exec(`CREATE SCHEMA ` + demoSchema)
	must(err)
	q := u.Query()
	q.Set("search_path", demoSchema)
	u.RawQuery = q.Encode()
	return u.String()
}

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "hub address")
	dataDir := flag.String("data", "./demo-data", "data directory (wiped on start)")
	database := flag.String("database-url", os.Getenv("UF_DATABASE_URL"), "PostgreSQL URL; the schema "+demoSchema+" in it is wiped on start")
	uiDir := flag.String("ui", "web/dist", "built web UI to serve")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	must(os.RemoveAll(*dataDir))
	dbURL := demoDatabase(*database)

	nodeNL := startVLESS()
	nodeDE := startVLESS()
	target := startTarget()
	dpi := startDPI(nodeDE)
	tcpOK := startTCP()
	dead := freePort()
	rw := httptest.NewServer(fakeRemnawave())
	tg := httptest.NewServer(fakeTelegram(log))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	hubURL := "http://" + *listen
	a, err := app.New(app.Options{DataDir: *dataDir, DatabaseURL: dbURL, Listen: *listen, PublicURL: hubURL}, log)
	must(err)
	_, err = a.Store.CreateUser(ctx, "admin", "admin12345", store.RoleAdmin)
	must(err)

	remind := checks.Duration(30 * time.Minute)
	must(a.SaveSettings(ctx, hub.Config{
		Timezone: "Europe/Moscow", Lang: "ru", RegionLabel: "country",
		Alerts:    hub.Alerts{QuietHours: "01:00-08:00", RemindEvery: &remind},
		Telegram:  &hub.Telegram{Token: "1:demo", ChatID: -1001, APIURL: tg.URL},
		Remnawave: &hub.Remnawave{URL: rw.URL, Token: "demo"},
		Baseline:  []checks.Spec{{Type: "tcp", TCP: &checks.TCPParams{Address: tcpOK}}},
	}))

	via := "socks5://" + dpi
	probes := []hub.ProbeConfig{
		{ID: "hub", Name: "Hub · Amsterdam", Local: true, Labels: map[string]string{"country": "nl", "kind": "dc"}},
		{ID: "ru-home", Name: "Home · Moscow", Via: via, Labels: map[string]string{"country": "ru", "city": "msk", "kind": "home", "isp": "rostelecom"}},
		{ID: "ru-msk", Name: "DC · Moscow", Via: via, Labels: map[string]string{"country": "ru", "city": "msk", "kind": "dc"}},
		{ID: "de-hetzner", Name: "Hetzner · DE", Probing: true, Labels: map[string]string{"country": "de", "kind": "dc"}},
		{ID: "fi-dc", Name: "DC · FI", Probing: true, Labels: map[string]string{"country": "fi", "kind": "dc"}},
		{ID: "kz-ps", Name: "PS · Almaty", Probing: true, Labels: map[string]string{"country": "kz", "kind": "dc"}},
		// Checker agents: volunteers' home machines, invited with a one-time code.
		{ID: "vol-spb", Name: "Volunteer · Saint Petersburg", Role: hub.RoleChecker, Trusted: true, Labels: map[string]string{"country": "ru", "asn": "as8359", "isp": "mts"}},
		{ID: "vol-kzn", Name: "Volunteer · Kazan", Role: hub.RoleChecker, Labels: map[string]string{"country": "ru", "asn": "as3216", "isp": "beeline"}},
		{ID: "vol-ekb", Name: "Volunteer · Yekaterinburg", Role: hub.RoleChecker, Labels: map[string]string{"country": "ru", "asn": "as12389", "isp": "rostelecom"}},
	}
	tokens := map[string]string{}
	for i, p := range probes {
		tok, err := a.SaveProbe(ctx, p, i > 0)
		must(err)
		tokens[p.ID] = tok
	}

	link := func(port int, name string) string {
		return fmt.Sprintf("vless://%s@127.0.0.1:%d?type=tcp&security=none#%s", uuid, port, name)
	}
	xr := func(port int, name string) *checks.XrayParams {
		return &checks.XrayParams{Link: link(port, name), URL: target + "/204", DownloadURL: target + "/down", HeavyEvery: 2, StallTimeout: checks.Duration(2 * time.Second)}
	}
	list := []hub.CheckConfig{
		{Spec: checks.Spec{ID: "de-frankfurt-1", Type: "xray", Interval: checks.Duration(20 * time.Second), Xray: xr(nodeDE, "de-frankfurt-1")}, Name: "de-frankfurt-1 · Reality", Group: "VPN nodes"},
		{Spec: checks.Spec{ID: "nl-amsterdam", Type: "xray", Interval: checks.Duration(20 * time.Second), Xray: xr(nodeNL, "nl-amsterdam")}, Name: "nl-amsterdam · XHTTP", Group: "VPN nodes"},
		{Spec: checks.Spec{ID: "fi-helsinki", Type: "xray", Interval: checks.Duration(20 * time.Second), Xray: xr(nodeNL, "fi-helsinki")}, Name: "fi-helsinki · Reality", Group: "VPN nodes"},
		{Spec: checks.Spec{ID: "old-node", Type: "tcp", Interval: checks.Duration(20 * time.Second), TCP: &checks.TCPParams{Address: fmt.Sprintf("127.0.0.1:%d", dead)}}, Name: "kz-almaty · old node", Group: "VPN nodes"},
		{Spec: checks.Spec{ID: "ru-relay-wl", Type: "tcp", Interval: checks.Duration(20 * time.Second), TCP: &checks.TCPParams{Address: tcpOK}}, Name: "ru-relay · whitelist", Group: "Whitelist relays", Probes: []string{"country=ru"}},
		{Spec: checks.Spec{ID: "home-isps", Type: "http", Interval: checks.Duration(20 * time.Second), HTTP: &checks.HTTPParams{URL: target + "/204"}}, Name: "Landing from home ISPs", Group: "Whitelist relays", Probes: []string{"role=checker"}},
		{Spec: checks.Spec{ID: "tg-proxy", Type: "tcp", Interval: checks.Duration(20 * time.Second), TCP: &checks.TCPParams{Address: tcpOK}}, Name: "Telegram proxy", Group: "Telegram"},
		{Spec: checks.Spec{ID: "panel", Type: "http", Interval: checks.Duration(20 * time.Second), HTTP: &checks.HTTPParams{URL: target + "/204"}}, Name: "Remnawave panel", Group: "Infrastructure", Probes: []string{"country=nl", "country=de"}},
	}
	for _, c := range list {
		must(a.SaveCheck(ctx, c, true))
	}
	seed(ctx, a.Store, probes, list)
	must(a.Store.Rollup(ctx, time.Now()))
	for _, inc := range history() {
		must(a.Store.SaveIncident(ctx, inc))
	}

	srv := &http.Server{Addr: *listen, Handler: api.New(a, api.Options{UI: os.DirFS(*uiDir)}).Handler()}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("http", "err", err)
			stop()
		}
	}()
	time.Sleep(300 * time.Millisecond)
	hosts := map[string]string{"de-hetzner": "de2.example.com", "fi-dc": "fi-dc-1", "vol-spb": "home-nas", "vol-kzn": "minipc"}
	for _, id := range []string{"de-hetzner", "fi-dc", "vol-spb", "vol-kzn"} {
		go agent.New(ctx, agent.Config{HubURL: hubURL, Token: tokens[id], MetricsInterval: 5 * time.Second, Hostname: hosts[id]}, log.With("agent", id)).Run(ctx)
	}
	// The volunteers connect from loopback here: give them the networks a
	// real hub would read from their address.
	go func() {
		time.Sleep(3 * time.Second)
		for id, n := range map[string]geoip.Network{
			"vol-spb": {Country: "ru", ASN: "as8359", Org: "MTS PJSC"},
			"vol-kzn": {Country: "ru", ASN: "as3216", Org: "PJSC VimpelCom"},
			"vol-ekb": {Country: "ru", ASN: "as12389", Org: "PJSC Rostelecom"},
		} {
			a.Hub.SetNetwork(id, n)
		}
	}()
	log.Info("demo is running", "url", hubURL, "login", "admin / admin12345")
	a.Run(ctx)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func freePort() int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func startVLESS() int {
	port := freePort()
	cfg := fmt.Sprintf(`{"log":{"loglevel":"none"},"inbounds":[{"listen":"127.0.0.1","port":%d,"protocol":"vless",
		"settings":{"clients":[{"id":%q}],"decryption":"none"}}],"outbounds":[{"protocol":"freedom"}]}`, port, uuid)
	pb, err := serial.LoadJSONConfig(strings.NewReader(cfg))
	must(err)
	inst, err := xcore.New(pb)
	must(err)
	must(inst.Start())
	return port
}

func startTarget() string {
	mux := http.NewServeMux()
	mux.HandleFunc("/204", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Duration(20+rand.IntN(60)) * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/down", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "131072")
		for range 8 {
			w.Write(make([]byte, 16384))
			time.Sleep(5 * time.Millisecond)
		}
	})
	return httptest.NewServer(mux).URL
}

func startTCP() string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(io.Discard, c); c.Close() }() // like a real server waiting for the client
		}
	}()
	return ln.Addr().String()
}

// startDPI is a SOCKS5 proxy that freezes server->client traffic to the
// blocked port after 16 KB, like volume-based DPI does to VPN connections.
func startDPI(blockedPort int) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 262)
				if _, err := io.ReadFull(c, buf[:2]); err != nil {
					return
				}
				io.ReadFull(c, buf[:buf[1]])
				c.Write([]byte{5, 0})
				if _, err := io.ReadFull(c, buf[:4]); err != nil {
					return
				}
				var host string
				switch buf[3] {
				case 1:
					io.ReadFull(c, buf[:4])
					host = net.IP(buf[:4]).String()
				case 3:
					io.ReadFull(c, buf[:1])
					n := int(buf[0])
					io.ReadFull(c, buf[:n])
					host = string(buf[:n])
				default:
					return
				}
				io.ReadFull(c, buf[:2])
				port := int(binary.BigEndian.Uint16(buf[:2]))
				up, err := net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
				if err != nil {
					c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
					return
				}
				defer up.Close()
				c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
				done := make(chan struct{})
				go func() { io.Copy(up, c); close(done) }()
				if port == blockedPort {
					io.Copy(c, io.LimitReader(up, 16<<10))
					<-done
					return
				}
				io.Copy(c, up)
			}(c)
		}
	}()
	return ln.Addr().String()
}

// seed writes a week of plausible history: diurnal latency, a few outages,
// and the DE node blocked from Russia in the evenings.
func seed(ctx context.Context, st *store.Store, probes []hub.ProbeConfig, list []hub.CheckConfig) {
	r := rand.New(rand.NewPCG(1, 2))
	now := time.Now().Truncate(time.Minute)
	start := now.Add(-7 * 24 * time.Hour)
	base := map[string]float64{"hub": 18, "ru-home": 62, "ru-msk": 48, "de-hetzner": 24, "fi-dc": 31, "kz-ps": 90, "vol-spb": 41, "vol-kzn": 55, "vol-ekb": 70}
	var samples []store.Sample
	for _, c := range list {
		for _, p := range probes {
			if p.ID == "kz-ps" {
				continue
			}
			// Checkers run only what is pointed at them explicitly.
			mine := len(c.Probes) > 0 && c.Probes[0] == "role=checker"
			if p.Checker() != mine {
				continue
			}
			ru := p.Labels["country"] == "ru"
			if !mine && len(c.Probes) > 0 && !(c.Probes[0] == "country=ru" && ru) && !(c.Probes[0] == "id="+p.ID) &&
				!(strings.HasPrefix(c.Probes[0], "country=") && strings.TrimPrefix(c.Probes[0], "country=") == p.Labels["country"]) &&
				!(len(c.Probes) > 1 && strings.TrimPrefix(c.Probes[1], "country=") == p.Labels["country"]) {
				continue
			}
			for t := start; t.Before(now.Add(-2 * time.Minute)); t = t.Add(time.Minute) {
				hour := float64(t.Hour()) + float64(t.Minute())/60
				diurnal := 1 + 0.35*math.Max(0, math.Sin((hour-14)/24*2*math.Pi))
				lat := base[p.ID] * diurnal * (0.85 + r.Float64()*0.3)
				if c.Type == "xray" {
					lat *= 3.2
				}
				up := true
				reason := ""
				switch {
				case c.ID == "old-node" && t.After(now.Add(-26*time.Hour)):
					up, reason = false, "refused"
				case c.ID == "de-frankfurt-1" && ru && t.Hour() >= 19 && t.Hour() < 22 && t.Day()%2 == 0:
					up, reason = false, "stalled"
				case c.ID == "nl-amsterdam" && t.After(now.Add(-3*24*time.Hour-40*time.Minute)) && t.Before(now.Add(-3*24*time.Hour)):
					up, reason = false, "timeout"
				case r.Float64() < 0.002:
					up, reason = false, "timeout"
				}
				bps := 0.0
				if c.Type == "xray" && t.Minute()%5 == 0 {
					bps = (9e6 + r.Float64()*4e6) / diurnal
				}
				samples = append(samples, store.Sample{Check: c.ID, Probe: p.ID, At: t, Up: up, Latency: lat, TTFB: lat * 0.8, BPS: bps, Reason: reason})
			}
		}
	}
	must(st.WriteSamples(ctx, samples))

	var sys []store.SysSample
	for _, p := range []struct {
		id        string
		mem, disk float64
		load      float64
		gbps      float64
	}{{"hub", 8e9, 80e9, 0.6, 0.05}, {"de-hetzner", 16e9, 160e9, 2.4, 1.4}, {"fi-dc", 4e9, 40e9, 1.1, 0.6}} {
		for t := start; t.Before(now); t = t.Add(time.Minute) {
			hour := float64(t.Hour())
			d := 0.5 + 0.5*math.Max(0, math.Sin((hour-12)/24*2*math.Pi))
			sys = append(sys, store.SysSample{
				Probe: p.id, At: t, CPU: 8 + 40*d + r.Float64()*6, MemUsed: p.mem * (0.35 + 0.1*d), MemTotal: p.mem,
				DiskUsed: p.disk * 0.42, DiskTotal: p.disk, RX: p.gbps * 1.25e8 * (0.3 + d) * (0.9 + r.Float64()*0.2),
				TX: p.gbps * 1.25e8 * (0.32 + d) * (0.9 + r.Float64()*0.2), Load1: p.load * (0.5 + d), Load5: p.load * (0.55 + d*0.9), Load15: p.load * (0.6 + d*0.8),
				Ext: demoExt(p.id, t, d, p.mem, p.gbps, r),
			})
		}
	}
	must(st.WriteSys(ctx, sys))
}

// demoExt draws a noisy neighbour on fi-dc in the evenings (steal) and a
// nightly backup on de-hetzner (iowait and disk writes).
func demoExt(id string, t time.Time, d, mem, gbps float64, r *rand.Rand) *store.SysExt {
	cpu := 8 + 40*d
	e := &store.SysExt{CPUUser: cpu * 0.62, CPUSystem: cpu * 0.24, CPUIRQ: cpu * 0.1, CPUIowait: 0.3 + r.Float64()*0.6,
		MemCache: mem * 0.28, SwapTotal: 2e9, SwapUsed: 1.2e8, DiskRead: 2e5 + r.Float64()*3e5, DiskWrite: 6e5 + r.Float64()*8e5,
		DiskROps: 6 + r.Float64()*10, DiskWOps: 30 + r.Float64()*40, DiskUtil: 2 + r.Float64()*4,
		RXPkts: gbps * 1.25e8 * (0.3 + d) / 900, TXPkts: gbps * 1.25e8 * (0.32 + d) / 1100, TCP: 1500 + 9000*d, Procs: 180 + 20*d}
	if id == "fi-dc" && t.Hour() >= 18 && t.Hour() < 23 {
		e.CPUSteal = 9 + r.Float64()*14
	} else {
		e.CPUSteal = r.Float64() * 0.8
	}
	if id == "de-hetzner" && t.Hour() == 3 {
		e.CPUIowait, e.DiskWrite, e.DiskWOps, e.DiskUtil, e.ProcsBlocked = 18+r.Float64()*12, 1.4e8+r.Float64()*4e7, 900+r.Float64()*300, 70+r.Float64()*25, 3
	}
	if r.Float64() < 0.01 {
		e.NetErr = r.Float64() * 40
	}
	return e
}

func history() []model.Incident {
	now := time.Now()
	var out []model.Incident
	for d := 2; d <= 7; d += 2 {
		day := time.Date(now.Year(), now.Month(), now.Day(), 19, 0, 0, 0, time.Local).AddDate(0, 0, -d)
		if day.Day()%2 != 0 {
			day = day.AddDate(0, 0, 1)
		}
		out = append(out, model.Incident{ID: fmt.Sprintf("de-%d", d), Key: "de-frankfurt-1", Name: "de-frankfurt-1 · Reality", Group: "VPN nodes", Type: "xray",
			Target: "VLESS · de1.example.com:443", Kind: model.KindBlocked, Worst: model.KindBlocked, Region: "ru", Severity: model.Critical,
			PeakFailing: 2, PeakTotal: 5, Started: day, Updated: day.Add(3 * time.Hour), Resolved: day.Add(3 * time.Hour), AckBy: "@leo", Notified: true})
	}
	t := now.Add(-3*24*time.Hour - 40*time.Minute)
	out = append(out, model.Incident{ID: "nl-1", Key: "nl-amsterdam", Name: "nl-amsterdam · XHTTP", Group: "VPN nodes", Type: "xray", Kind: model.KindDown,
		Worst: model.KindDown, Severity: model.Critical, PeakFailing: 5, PeakTotal: 5, Started: t, Updated: t.Add(40 * time.Minute), Resolved: t.Add(40 * time.Minute), Notified: true})
	return out
}

func fakeTelegram(log *slog.Logger) http.Handler {
	id := 100
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		body, _ := io.ReadAll(r.Body)
		switch method {
		case "getMe":
			io.WriteString(w, `{"ok":true,"result":{"username":"uptimeforge_demo_bot"}}`)
		case "getUpdates":
			time.Sleep(2 * time.Second)
			io.WriteString(w, `{"ok":true,"result":[]}`)
		case "sendMessage", "sendRichMessage":
			id++
			log.Info("telegram ⇢ "+method, "body", string(body))
			fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d}}`, id)
		default:
			log.Info("telegram ⇢ "+method, "body", string(body))
			io.WriteString(w, `{"ok":true,"result":true}`)
		}
	})
}

func fakeRemnawave() http.Handler {
	type node struct {
		name, cc, addr string
		users          int
		rx             float64
		connected      bool
	}
	nodes := []node{
		{"kz-almaty-1", "KZ", "kz1.example.com", 412, 2.4e9, true}, {"ee-tallinn-1", "EE", "ee1.example.com", 236, 1.15e9, true},
		{"de-frankfurt-1", "DE", "de1.example.com", 37, 0.27e9, true}, {"de-frankfurt-2", "DE", "de2.example.com", 288, 0.39e9, true},
		{"ru-moscow-relay", "RU", "ru1.example.com", 181, 0.09e9, true}, {"ru-spb-relay", "RU", "ru2.example.com", 154, 0.15e9, true},
		{"de-berlin-1", "DE", "de3.example.com", 403, 0.64e9, true}, {"cy-limassol-1", "CY", "cy1.example.com", 0, 0, false},
	}
	start := time.Now()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch strings.TrimSuffix(r.URL.Path, "/") {
		case "/api/nodes":
			var parts []string
			for i, n := range nodes {
				// de-berlin-1 gets banned 35 minutes in (the detector needs half an
				// hour of history) and recovers 6 minutes later.
				if m := time.Since(start).Minutes(); n.name == "de-berlin-1" && m > 35 && m < 41 {
					n.users = 2
				}
				status := `"lastStatusMessage":null`
				if !n.connected {
					status = `"lastStatusMessage":"connect ETIMEDOUT 203.0.113.9:2222"`
				}
				parts = append(parts, fmt.Sprintf(`{"uuid":"00000000-0000-4000-8000-%012d","id":%d,"name":%q,"address":%q,"port":2222,"proxyUrl":null,
"isConnected":%v,"isDisabled":false,"isConnecting":false,"lastStatusChange":"2026-10-20T10:00:00Z",%s,"isTrafficTrackingActive":true,
"trafficResetDay":1,"trafficLimitBytes":0,"trafficUsedBytes":%d,"notifyPercent":null,"viewPosition":%d,"countryCode":%q,
"consumptionMultiplier":1,"nodeConsumptionMultiplier":1,"tags":[],"integrationUuids":[],"ips":[],"createdAt":"2026-01-01T00:00:00Z",
"updatedAt":"2026-10-20T10:00:00Z","configProfile":{"activeConfigProfileUuid":null,"activeInbounds":[]},"providerUuid":null,"provider":null,
"activePluginUuid":null,"system":{"info":{"arch":"x64","cpus":4,"cpuModel":"AMD EPYC","memoryTotal":8323002368,"hostname":%q,"platform":"linux",
"release":"6.8.0","type":"Linux","version":"#1","networkInterfaces":["eth0"]},"stats":{"memoryFree":4123002368,"memoryUsed":4200000000,
"uptime":%d,"loadAvg":[1.2,1.1,0.9],"interface":{"interface":"eth0","rxBytesPerSec":%f,"txBytesPerSec":%f,"rxTotal":1,"txTotal":1}}},
"versions":{"xray":"26.3.27","node":"2.7.1"},"xrayUptime":%d,"usersOnline":%d,"note":null}`,
					i+1, i+1, n.name, n.addr, n.connected, status, int64(n.rx*86400*12), i+1, n.cc, n.name, 86400*(5+i*4), n.rx/8, n.rx/8*1.04, 86400*(3+i), n.users))
			}
			fmt.Fprintf(w, `{"response":[%s]}`, strings.Join(parts, ","))
		case "/api/system/stats":
			io.WriteString(w, `{"response":{"cpu":{"cores":8},"memory":{"total":8323002368,"free":3600000000,"used":4723002368},"uptime":3456789,
"timestamp":1792744800123,"users":{"statusCounts":{"ACTIVE":1000,"DISABLED":50,"LIMITED":20,"EXPIRED":130},"totalUsers":1200},
"onlineStats":{"lastDay":1500,"lastWeek":1600,"neverOnline":200,"onlineNow":1000},"nodes":{"totalOnline":1000,"totalBytesLifetime":"200000000000000"}}}`)
		case "/api/system/stats/bandwidth":
			io.WriteString(w, `{"response":{"bandwidthLastTwoDays":{"current":"2 TiB","previous":"2.5 TiB","difference":"-0.5 TiB"},
"bandwidthLastSevenDays":{"current":"14 TiB","previous":"13 TiB","difference":"1 TiB"},
"bandwidthLast30Days":{"current":"60 TiB","previous":"55 TiB","difference":"5 TiB"},
"bandwidthCalendarMonth":{"current":"25 TiB","previous":"58 TiB","difference":"-33 TiB"},
"bandwidthCurrentYear":{"current":"180 TiB","previous":"0 B","difference":"180 TiB"}}}`)
		case "/api/system/health":
			io.WriteString(w, `{"response":{"runtimeMetrics":[
{"rss":439820000,"heapUsed":120000000,"heapTotal":160000000,"external":1,"arrayBuffers":1,"eventLoopDelayMs":0.23,"eventLoopP99Ms":1.2,"activeHandles":10,"uptime":3456,"pid":1,"timestamp":1792744800123,"instanceId":"0","instanceType":"api"},
{"rss":157450000,"heapUsed":60000000,"heapTotal":80000000,"external":1,"arrayBuffers":1,"eventLoopDelayMs":0.21,"eventLoopP99Ms":1.1,"activeHandles":8,"uptime":3456,"pid":2,"timestamp":1792744800123,"instanceId":"0","instanceType":"scheduler"},
{"rss":512000000,"heapUsed":148000000,"heapTotal":190000000,"external":1,"arrayBuffers":1,"eventLoopDelayMs":0.25,"eventLoopP99Ms":1.3,"activeHandles":12,"uptime":3456,"pid":3,"timestamp":1792744800123,"instanceId":"0","instanceType":"processor"}]}}`)
		case "/api/bandwidth-stats/nodes":
			var top []string
			for i, n := range nodes {
				top = append(top, fmt.Sprintf(`{"uuid":"00000000-0000-4000-8000-%012d","color":"#22b8cf","name":%q,"countryCode":%q,"total":%d}`, i+1, n.name, n.cc, int64(n.rx*86400*7)))
			}
			fmt.Fprintf(w, `{"response":{"categories":["2026-10-17","2026-10-18","2026-10-19","2026-10-20","2026-10-21","2026-10-22","2026-10-23"],
"sparklineData":[1,2,3,4,5,6,7],"topNodes":[%s],"series":[]}}`, strings.Join(top, ","))
		default:
			http.NotFound(w, r)
		}
	})
}
