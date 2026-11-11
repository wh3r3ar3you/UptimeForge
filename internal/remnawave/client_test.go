package remnawave

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const token = "eyJhbGciOiJIUzI1NiJ9.test-api-token"

// Shapes follow @remnawave/backend-contract 3.4.15 (models/nodes.schema,
// commands/system/*, commands/bandwidth-stats/nodes/*).
const nodesJSON = `{"response":[
{"uuid":"0f4a9c3e-5b1d-4c2e-9a7f-1e2d3c4b5a60","id":1,"name":"DE-1","address":"de1.example.com","port":2222,
 "proxyUrl":null,"isConnected":true,"isDisabled":false,"isConnecting":false,
 "lastStatusChange":"2026-10-22T08:15:30.123Z","lastStatusMessage":null,
 "isTrafficTrackingActive":true,"trafficResetDay":1,"trafficLimitBytes":1099511627776,"trafficUsedBytes":53687091200,
 "notifyPercent":80,"viewPosition":1,"countryCode":"DE","consumptionMultiplier":1,"nodeConsumptionMultiplier":1.5,
 "tags":["EU"],"integrationUuids":[],"ips":[{"ip":"203.0.113.10","status":"INBOUND"}],
 "createdAt":"2026-01-10T12:00:00.000Z","updatedAt":"2026-10-22T08:15:30.123Z",
 "configProfile":{"activeConfigProfileUuid":"7d2e1f0a-3b4c-4d5e-8f9a-0b1c2d3e4f50","activeInbounds":[
  {"uuid":"9e8d7c6b-5a49-4382-9716-05f4e3d2c1b0","profileUuid":"7d2e1f0a-3b4c-4d5e-8f9a-0b1c2d3e4f50","tag":"VLESS_REALITY",
   "type":"vless","network":"tcp","security":"reality","port":443,"rawInbound":{"tag":"VLESS_REALITY","port":443}}]},
 "providerUuid":"5c4b3a29-1807-4f6e-9d5c-4b3a29180706","provider":{"uuid":"5c4b3a29-1807-4f6e-9d5c-4b3a29180706","name":"Hetzner",
  "faviconLink":null,"loginUrl":"https://console.hetzner.cloud","createdAt":"2026-01-01T00:00:00.000Z","updatedAt":"2026-01-01T00:00:00.000Z"},
 "activePluginUuid":null,
 "system":{"info":{"arch":"x64","cpus":4,"cpuModel":"AMD EPYC","memoryTotal":8323002368,"hostname":"de1","platform":"linux",
   "release":"6.8.0-45-generic","type":"Linux","version":"#45-Ubuntu SMP","networkInterfaces":["lo","eth0"]},
  "stats":{"memoryFree":5123002368,"memoryUsed":3200000000,"uptime":864000.52,"loadAvg":[0.42,0.35,0.3],
   "interface":{"interface":"eth0","rxBytesPerSec":1250000.5,"txBytesPerSec":1310000.25,"rxTotal":987654321012,"txTotal":1012345678901}}},
 "versions":{"xray":"26.3.27","node":"2.7.1"},"xrayUptime":86400,"usersOnline":137,"note":"main","futureField":{"x":1}},
{"uuid":"1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d","id":2,"name":"NL-1","address":"10.0.0.2","port":null,
 "proxyUrl":null,"isConnected":false,"isDisabled":false,"isConnecting":true,
 "lastStatusChange":null,"lastStatusMessage":"connect ECONNREFUSED 10.0.0.2:2222",
 "isTrafficTrackingActive":false,"trafficResetDay":null,"trafficLimitBytes":null,"trafficUsedBytes":null,
 "notifyPercent":null,"viewPosition":2,"countryCode":"NL","consumptionMultiplier":1,"nodeConsumptionMultiplier":1,
 "tags":[],"integrationUuids":[],"ips":[],"createdAt":"2026-02-01T00:00:00.000Z","updatedAt":"2026-10-23T01:00:00.000Z",
 "configProfile":{"activeConfigProfileUuid":null,"activeInbounds":[]},
 "providerUuid":null,"provider":null,"activePluginUuid":null,"system":null,"versions":null,"xrayUptime":0,"usersOnline":0,"note":null},
{"uuid":"2b3c4d5e-6f70-4b8c-9dae-1f2a3b4c5d6e","id":3,"name":"FI-1","address":"fi1.example.com","port":2222,
 "proxyUrl":null,"isConnected":false,"isDisabled":true,"isConnecting":false,
 "lastStatusChange":"2026-10-01T00:00:00.000Z","lastStatusMessage":null,
 "isTrafficTrackingActive":false,"trafficResetDay":null,"trafficLimitBytes":0,"trafficUsedBytes":0,
 "notifyPercent":null,"viewPosition":3,"countryCode":"FI","consumptionMultiplier":1,"nodeConsumptionMultiplier":1,
 "tags":[],"integrationUuids":[],"ips":[],"createdAt":"2026-02-01T00:00:00.000Z","updatedAt":"2026-10-01T00:00:00.000Z",
 "configProfile":{"activeConfigProfileUuid":null,"activeInbounds":[]},
 "providerUuid":null,"provider":null,"activePluginUuid":null,
 "system":{"info":{"arch":"arm64","cpus":2,"cpuModel":"Neoverse-N1","memoryTotal":4096000000,"hostname":"fi1","platform":"linux",
   "release":"6.1.0","type":"Linux","version":"#1 SMP","networkInterfaces":["eth0"]},
  "stats":{"memoryFree":1,"memoryUsed":2,"uptime":10,"loadAvg":[0,0,0],"interface":null}},
 "versions":null,"xrayUptime":0,"usersOnline":0,"note":null}
]}`

const statsJSON = `{"response":{"cpu":{"cores":8},"memory":{"total":16654016512,"free":9876543210,"used":6777473302},
"uptime":1234567.89,"timestamp":1792744800123,
"users":{"statusCounts":{"ACTIVE":1200,"DISABLED":30,"LIMITED":12,"EXPIRED":158},"totalUsers":1400},
"onlineStats":{"lastDay":640,"lastWeek":910,"neverOnline":85,"onlineNow":137},
"nodes":{"totalOnline":137,"totalBytesLifetime":"987654321098765"}}}`

const bandwidthJSON = `{"response":{
"bandwidthLastTwoDays":{"current":"1.50 GiB","previous":"2.00 GiB","difference":"-512.00 MiB"},
"bandwidthLastSevenDays":{"current":"120.25 GiB","previous":"0","difference":"120.25 GiB"},
"bandwidthLast30Days":{"current":"1.02 TiB","previous":"980.00 GiB","difference":"64.48 GiB"},
"bandwidthCalendarMonth":{"current":"800.00 GiB","previous":"1.10 TiB","difference":"-326.40 GiB"},
"bandwidthCurrentYear":{"current":"9.75 TiB","previous":"0","difference":"9.75 TiB"}}}`

const healthJSON = `{"response":{"runtimeMetrics":[
{"rss":312475648,"heapUsed":145227776,"heapTotal":170393600,"external":4521984,"arrayBuffers":1048576,
 "eventLoopDelayMs":10.42,"eventLoopP99Ms":21.5,"activeHandles":37,"uptime":86399.123,"pid":42,
 "timestamp":1792744800000,"instanceId":"0","instanceType":"api"}]}}`

func newServer(t *testing.T, routes map[string]string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Forwarded-For") == "" || r.Header.Get("X-Forwarded-Proto") != "https" {
			// what the panel's proxyCheckMiddleware does
			if c, _, err := w.(http.Hijacker).Hijack(); err == nil {
				c.Close()
			}
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"message":"Unauthorized","statusCode":401}`))
			return
		}
		if r.Method != http.MethodGet {
			t.Errorf("method %s", r.Method)
		}
		body, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"message":"Cannot GET ` + r.URL.Path + `","error":"Not Found","statusCode":404}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, token)
}

func TestNodes(t *testing.T) {
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr = r.Header.Clone()
		if r.URL.Path != "/api/nodes/" {
			t.Errorf("path %q", r.URL.Path)
		}
		w.Write([]byte(nodesJSON))
	}))
	defer srv.Close()
	c := New(srv.URL+"/", token)
	c.Headers = map[string]string{"Cookie": "panel_gate=abc"}
	nodes, err := c.Nodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"Authorization": "Bearer " + token, "X-Forwarded-Proto": "https", "X-Forwarded-For": "127.0.0.1",
		"Cookie": "panel_gate=abc", "Accept": "application/json",
	} {
		if got := hdr.Get(k); got != want {
			t.Errorf("header %s = %q, want %q", k, got, want)
		}
	}
	if len(nodes) != 3 {
		t.Fatalf("got %d nodes", len(nodes))
	}

	n := nodes[0]
	if n.Name != "DE-1" || *n.Port != 2222 || n.CountryCode != "DE" || !n.IsConnected || n.IsDisabled || n.UsersOnline != 137 ||
		n.XrayUptime != 86400 || n.Versions.Xray != "26.3.27" || *n.TrafficUsedBytes != 53687091200 ||
		*n.TrafficLimitBytes != 1<<40 || n.NodeConsumptionMultiplier != 1.5 || n.Provider.Name != "Hetzner" ||
		n.IPs[0].Status != "INBOUND" || n.ConfigProfile.ActiveInbounds[0].Tag != "VLESS_REALITY" || *n.Note != "main" {
		t.Errorf("node 0: %+v", n)
	}
	if !n.LastStatusChange.Equal(time.Date(2026, 10, 22, 8, 15, 30, 123e6, time.UTC)) {
		t.Errorf("lastStatusChange %v", n.LastStatusChange)
	}
	sys := n.System
	if sys.Info.CPUs != 4 || sys.Info.MemoryTotal != 8323002368 || sys.Stats.LoadAvg[0] != 0.42 || sys.Stats.Uptime != 864000.52 ||
		sys.Stats.Interface.RxBytesPerSec != 1250000.5 || sys.Stats.Interface.TxTotal != 1012345678901 {
		t.Errorf("system: %+v", sys)
	}

	n = nodes[1]
	if n.Port != nil || n.System != nil || n.Versions != nil || n.LastStatusChange != nil || n.Provider != nil ||
		n.TrafficLimitBytes != nil || n.TrafficUsedBytes != nil || !n.IsConnecting ||
		*n.LastStatusMessage != "connect ECONNREFUSED 10.0.0.2:2222" || n.ConfigProfile.ActiveConfigProfileUUID != nil {
		t.Errorf("node 1 nullables: %+v", n)
	}
	if nodes[2].System.Stats.Interface != nil {
		t.Error("null interface decoded as non-nil")
	}
}

func TestStats(t *testing.T) {
	var tz string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("no auth on %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/api/system/stats":
			w.Write([]byte(statsJSON))
		case "/api/system/stats/bandwidth":
			tz = r.URL.Query().Get("tz")
			w.Write([]byte(bandwidthJSON))
		case "/api/system/health":
			w.Write([]byte(healthJSON))
		case "/api/nodes/":
			w.Write([]byte(nodesJSON))
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := New(srv.URL+"/api", token)
	c.TZ = "Europe/Moscow"
	s, err := c.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tz != "Europe/Moscow" {
		t.Errorf("tz = %q", tz)
	}
	o := s.System.OnlineStats
	if o.OnlineNow != 137 || o.LastDay != 640 || o.LastWeek != 910 || o.NeverOnline != 85 ||
		s.System.Users.TotalUsers != 1400 || s.System.Users.StatusCounts["EXPIRED"] != 158 ||
		s.System.CPU.Cores != 8 || s.System.Memory.Used != 6777473302 || s.System.Timestamp != 1792744800123 {
		t.Errorf("system: %+v", s.System)
	}
	if s.NodesTotal != 3 || s.NodesOnline != 1 || s.NodesDisabled != 1 {
		t.Errorf("nodes total/online/disabled = %d/%d/%d", s.NodesTotal, s.NodesOnline, s.NodesDisabled)
	}
	if len(s.Runtime) != 1 || s.Runtime[0].RSS != 312475648 || s.Runtime[0].InstanceType != "api" || s.Runtime[0].EventLoopP99Ms != 21.5 {
		t.Errorf("runtime: %+v", s.Runtime)
	}
	for size, want := range map[Size]int64{
		s.Bandwidth.BandwidthLastTwoDays.Current:    1610612736,
		s.Bandwidth.BandwidthLastTwoDays.Difference: -536870912,
		s.Bandwidth.BandwidthLastSevenDays.Previous: 0,
		s.System.Nodes.TotalBytesLifetime:           987654321098765,
	} {
		if got, err := size.Bytes(); err != nil || got != want {
			t.Errorf("%q.Bytes() = %d, %v; want %d", size, got, err, want)
		}
	}
	if s.Bandwidth.BandwidthCurrentYear.Current != "9.75 TiB" {
		t.Errorf("current year %q", s.Bandwidth.BandwidthCurrentYear.Current)
	}
}

func TestStatsWithoutHealth(t *testing.T) {
	c := newServer(t, map[string]string{
		"/api/system/stats":           statsJSON,
		"/api/system/stats/bandwidth": bandwidthJSON,
		"/api/nodes/":                 nodesJSON,
	})
	s, err := c.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Runtime != nil || s.System.OnlineStats.OnlineNow != 137 {
		t.Errorf("%+v", s)
	}
}

func TestNodesMetricsAndUsage(t *testing.T) {
	var q string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/system/nodes/metrics":
			w.Write([]byte(`{"response":{"nodes":[{"nodeUuid":"0f4a9c3e-5b1d-4c2e-9a7f-1e2d3c4b5a60","nodeName":"DE-1",
				"countryEmoji":"🇩🇪","providerName":"unknown","usersOnline":137,
				"inboundsStats":[{"tag":"VLESS_REALITY","upload":"1.20 GiB","download":"35.70 GiB"}],
				"outboundsStats":[{"tag":"DIRECT","upload":"35.10 GiB","download":"1.10 GiB"},{"tag":"BLOCK","upload":"0","download":"0"}]}]}}`))
		case "/api/system/stats/nodes":
			w.Write([]byte(`{"response":{"lastSevenDays":[{"nodeName":"DE-1","date":"2026-10-23","totalBytes":"53687091200"}]}}`))
		case "/api/bandwidth-stats/nodes/":
			q = r.URL.RawQuery
			w.Write([]byte(`{"response":{"categories":["2026-10-22","2026-10-23"],"sparklineData":[1000,2000],
				"topNodes":[{"uuid":"0f4a9c3e-5b1d-4c2e-9a7f-1e2d3c4b5a60","color":"#3b82f6","name":"DE-1","countryCode":"DE","total":3000}],
				"series":[{"uuid":"0f4a9c3e-5b1d-4c2e-9a7f-1e2d3c4b5a60","name":"DE-1","color":"#3b82f6","countryCode":"DE","total":3000,"data":[1000,2000]}]}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c, ctx := New(srv.URL, token), context.Background()

	m, err := c.NodesMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 1 || m[0].UsersOnline != 137 || m[0].InboundsStats[0].Download != "35.70 GiB" || len(m[0].OutboundsStats) != 2 {
		t.Errorf("metrics: %+v", m)
	}
	d, err := c.NodesStatistics(ctx)
	if err != nil || len(d) != 1 || d[0].TotalBytes != "53687091200" {
		t.Errorf("statistics: %+v, %v", d, err)
	}
	u, err := c.NodesUsage(ctx, time.Date(2026, 10, 22, 23, 0, 0, 0, time.UTC), time.Date(2026, 10, 23, 5, 0, 0, 0, time.UTC), 5)
	if err != nil {
		t.Fatal(err)
	}
	if q != "end=2026-10-23&start=2026-10-22&topNodesLimit=5" {
		t.Errorf("query %q", q)
	}
	if u.Series[0].Data[1] != 2000 || u.TopNodes[0].Total != 3000 || u.Categories[0] != "2026-10-22" {
		t.Errorf("usage: %+v", u)
	}
}

func TestErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/system/stats":
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"timestamp":"2026-10-23T10:00:00.000Z","path":"/api/system/stats","message":"Get user stats error","errorCode":"A048"}`))
		case "/api/system/health":
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"message":"Forbidden resource","error":"Forbidden","statusCode":403}`))
		case "/api/system/stats/nodes":
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte("<html><body>502 Bad Gateway</body></html>"))
		case "/api/system/nodes/metrics":
			w.Write([]byte(`{"response":null}`))
		default:
			w.Write([]byte(`<!doctype html><title>Remnawave</title>`))
		}
	}))
	defer srv.Close()
	c, ctx := New(srv.URL, token), context.Background()

	_, err := c.SystemStats(ctx)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 500 || ae.Code != "A048" || ae.Message != "Get user stats error" {
		t.Fatalf("got %#v", err)
	}
	if want := "remnawave: GET /api/system/stats: HTTP 500 A048: Get user stats error"; err.Error() != want {
		t.Errorf("Error() = %q", err)
	}
	if _, err := c.Health(ctx); !errors.As(err, &ae) || ae.Status != 403 || ae.Message != "Forbidden resource" {
		t.Errorf("403: %v", err)
	}
	if _, err := c.NodesStatistics(ctx); !errors.As(err, &ae) || ae.Status != 502 || ae.Message != "Bad Gateway" {
		t.Errorf("502: %v", err)
	}
	if _, err := c.NodesMetrics(ctx); err == nil || !strings.Contains(err.Error(), `no "response"`) {
		t.Errorf("missing response: %v", err)
	}
	if _, err := c.Nodes(ctx); err == nil || !strings.Contains(err.Error(), "not a panel API reply") {
		t.Errorf("html: %v", err)
	}

	bad := newServer(t, nil)
	bad.Token = "wrong-secret-token"
	_, err = bad.Nodes(ctx)
	if !errors.As(err, &ae) || ae.Status != 401 || ae.Message != "Unauthorized" {
		t.Errorf("401: %v", err)
	}
	if strings.Contains(err.Error(), "wrong-secret-token") {
		t.Error("token leaked into error")
	}
}

// A panel URL can carry credentials (basic auth, a token in the query); an
// error about a bad URL reaches the settings page and must not show them.
func TestBadBaseURLHidesSecrets(t *testing.T) {
	for _, u := range []string{"https://admin:s3cret@:443", "panel.example.com/?key=s3cret", "https://admin:s3cret@panel.example.com:bad"} {
		_, err := New(u, token).Nodes(context.Background())
		if err == nil || strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%q: %v", u, err)
		}
	}
}

func TestForwardedHeaders(t *testing.T) {
	c := newServer(t, map[string]string{"/api/nodes/": `{"response":[]}`})
	if nodes, err := c.Nodes(context.Background()); err != nil || nodes == nil {
		t.Fatalf("%v %v", nodes, err)
	}

	c.Headers = map[string]string{"X-Forwarded-For": ""}
	_, err := c.Nodes(context.Background())
	if err == nil || !strings.Contains(err.Error(), "X-Forwarded-Proto") {
		t.Errorf("dropped connection: %v", err)
	}

	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Forwarded-Proto") != "" {
			t.Errorf("forwarded headers sent over https: %v", r.Header)
		}
		w.Write([]byte(`{"response":[]}`))
	}))
	defer tls.Close()
	ct := New(tls.URL, token)
	ct.HTTP = tls.Client()
	if _, err := ct.Nodes(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSizeBytes(t *testing.T) {
	for in, want := range map[Size]int64{
		"0": 0, "123456789": 123456789, "512.00 B": 512, "1.50 KiB": 1536, "-2.00 GiB": -2 << 30,
		"1.00 TiB": 1 << 40, "10 GB": 10e9, "1.5e3": 1500, " 3.00 MiB ": 3 << 20,
	} {
		if got, err := in.Bytes(); err != nil || got != want {
			t.Errorf("%q: %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []Size{"", "GiB", "1.5 XB", "1,5 GiB"} {
		if _, err := in.Bytes(); err == nil {
			t.Errorf("%q: no error", in)
		}
	}
}
