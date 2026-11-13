package hub

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/wh3r3ar3you/uptimeforge/internal/checks"
	"github.com/wh3r3ar3you/uptimeforge/internal/model"
	"github.com/wh3r3ar3you/uptimeforge/internal/proto"
	"github.com/wh3r3ar3you/uptimeforge/internal/version"
)

// Handler serves the agent endpoint, Prometheus metrics and a health check.
// The web UI and its API are mounted next to it by the api package.
func (h *Hub) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/agent/ws", h.handleAgent)
	mux.HandleFunc("GET /metrics", h.handleMetrics)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if !h.Healthy() {
			http.Error(w, "evaluation loop stalled", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	return mux
}

// Healthy is false when the evaluation loop stopped ticking.
func (h *Hub) Healthy() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return time.Since(h.lastTick) < 3*tickEvery
}

func bearer(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

type agentConn struct {
	conn   *websocket.Conn
	assign chan []byte // capacity 1: only the newest assignment matters
	out    chan []byte // other hub -> agent messages
	cancel context.CancelFunc
}

func (c *agentConn) send(msg []byte) bool {
	select {
	case c.out <- msg:
		return true
	default:
		return false
	}
}

func (c *agentConn) sendAssign(msg []byte) {
	for {
		select {
		case c.assign <- msg:
			return
		default:
			select {
			case <-c.assign: // drop the stale one
			default:
			}
		}
	}
}

func (h *Hub) handleAgent(w http.ResponseWriter, r *http.Request) {
	hash := HashToken(bearer(r))
	var p *probe
	var id string // p.cfg is swapped by Apply under h.mu; the id of a probe never changes
	h.mu.Lock()
	for _, x := range h.probes {
		if x.cfg.TokenHash != "" && subtle.ConstantTimeCompare([]byte(hash), []byte(x.cfg.TokenHash)) == 1 {
			p, id = x, x.cfg.ID
		}
	}
	h.mu.Unlock()
	if p == nil {
		http.Error(w, "unknown probe token", http.StatusUnauthorized)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(agentReadLimit)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	hctx, hcancel := context.WithTimeout(ctx, 10*time.Second)
	_, data, err := conn.Read(hctx)
	hcancel()
	var env proto.Envelope
	var hello proto.Hello
	if err != nil || json.Unmarshal(data, &env) != nil || env.Type != proto.TypeHello || json.Unmarshal(env.Data, &hello) != nil {
		conn.Close(websocket.StatusPolicyViolation, "expected hello")
		return
	}
	if hello.Proto != proto.Version {
		conn.Close(websocket.StatusPolicyViolation, fmt.Sprintf("protocol %d not supported, hub speaks %d", hello.Proto, proto.Version))
		return
	}

	ac := &agentConn{conn: conn, assign: make(chan []byte, 1), out: make(chan []byte, 16), cancel: cancel}
	ip := ClientIP(r)
	if !h.attach(p, ac, hello, hash, ip) {
		conn.Close(websocket.StatusPolicyViolation, "probe token revoked")
		return
	}
	defer h.detach(p, ac)
	log := h.log.With("probe", id, "remote", ip.String())
	log.Info("agent connected", "version", hello.Version, "host", hello.Hostname, "os", hello.OS+"/"+hello.Arch)
	if h.OnAgentAddr != nil && ip.IsValid() {
		h.OnAgentAddr(id, ip)
	}

	go func() {
		ping := time.NewTicker(30 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-ac.out:
				wctx, c := context.WithTimeout(ctx, 15*time.Second)
				err := conn.Write(wctx, websocket.MessageText, msg)
				c()
				if err != nil {
					cancel()
					return
				}
			case msg := <-ac.assign:
				wctx, c := context.WithTimeout(ctx, 15*time.Second)
				err := conn.Write(wctx, websocket.MessageText, msg)
				c()
				if err != nil {
					cancel()
					return
				}
			case <-ping.C:
				pctx, c := context.WithTimeout(ctx, 15*time.Second)
				err := conn.Ping(pctx)
				c()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()

	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			log.Info("agent disconnected", "err", err)
			return
		}
		var env proto.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			log.Warn("bad message", "err", err)
			return
		}
		switch env.Type {
		case proto.TypeResults:
			var rs proto.Results
			if err := json.Unmarshal(env.Data, &rs); err != nil {
				log.Warn("bad results", "err", err)
				return
			}
			h.Ingest(id, rs.Results)
		case proto.TypeMetrics:
			var m proto.Metrics
			if json.Unmarshal(env.Data, &m) == nil {
				h.IngestMetrics(id, m)
			}
		case proto.TypeSysInfo:
			var si proto.SysInfo
			if json.Unmarshal(env.Data, &si) == nil {
				h.mu.Lock()
				p.sysinfo = &si
				h.mu.Unlock()
			}
		case proto.TypeRunResult:
			var rr proto.RunResult
			if json.Unmarshal(env.Data, &rr) == nil {
				h.runResult(rr)
			}
		case proto.TypeUpdateResult:
			var ur proto.UpdateResult
			if json.Unmarshal(env.Data, &ur) == nil {
				h.updateResult(id, ur)
			}
		}
	}
}

// attach makes ac the probe's connection. The token is checked again here,
// under the lock: it may have been rotated, or the probe removed or made
// virtual, while the agent was still in the handshake. ip is where the agent
// connects from; from another address its network is unknown until
// SetNetwork.
func (h *Hub) attach(p *probe, ac *agentConn, hello proto.Hello, tokenHash string, ip netip.Addr) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.probes[p.cfg.ID] != p || p.cfg.Virtual() || p.cfg.TokenHash == "" ||
		subtle.ConstantTimeCompare([]byte(tokenHash), []byte(p.cfg.TokenHash)) != 1 {
		return false
	}
	if p.conn != nil {
		p.conn.cancel() // a newer connection from the same probe wins
	}
	now := time.Now()
	p.conn, p.online, p.since, p.hello = ac, true, now, hello
	if ip = ip.Unmap(); ip != p.addr {
		p.addr, p.network = ip, nil
		if ip.IsValid() {
			p.network = &NetworkView{IP: ip.String(), At: now}
		}
	}
	h.seen[p.cfg.ID] = true
	p.lastAssign = nil
	h.transition("probe:"+p.cfg.ID, model.KindOK, "", nil, meta{}, now)
	h.pushAssignments()
	if len(p.watchers) > 0 {
		h.renewLive(p) // someone is already watching: no need to wait for the next renewal
	}
	h.rolloutAttached(p.cfg.ID)
	h.rolloutStep(h.now()) // back from an update, or due for one
	return true
}

func (h *Hub) detach(p *probe, ac *agentConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if p.conn != ac {
		return
	}
	now := time.Now()
	p.conn, p.online, p.since = nil, false, now
	h.evaluateAll(now)
	h.rolloutStep(h.now()) // an agent told to update is missing from now
}

// Status is the read model for the API and, later, the web UI.
type Status struct {
	Time      time.Time        `json:"time"`
	Probes    []ProbeStatus    `json:"probes"`
	Checks    []CheckStatus    `json:"checks"`
	Incidents []model.Incident `json:"incidents"`
}

type ProbeStatus struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Labels    map[string]string `json:"labels"`
	Region    string            `json:"region,omitempty"`
	Mode      string            `json:"mode"`
	Online    bool              `json:"online"`
	Internet  bool              `json:"internet"`
	Since     time.Time         `json:"since"`
	Version   string            `json:"version,omitempty"`
	Host      string            `json:"host,omitempty"`
	SysInfo   *proto.SysInfo    `json:"sysinfo,omitempty"`
	Metrics   *proto.Metrics    `json:"metrics,omitempty"`
	MetricsAt time.Time         `json:"metrics_at,omitzero"`
	Checks    int               `json:"checks"`
	Failing   int               `json:"failing"`
	// Network is where an agent connects from (nil until it has).
	Network *NetworkView `json:"network,omitempty"`
}

type CheckStatus struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Group string   `json:"group,omitempty"`
	Tags  []string `json:"tags,omitempty"`
	// Parents are what this check's failure would be a consequence of;
	// ParentsAuto says they were found by address, not set by hand.
	Parents     []string           `json:"parents,omitempty"`
	ParentsAuto bool               `json:"parents_auto,omitempty"`
	Type        string             `json:"type"`
	Target      string             `json:"target,omitempty"`
	Parent      string             `json:"parent,omitempty"` // subscription it was expanded from
	Interval    checks.Duration    `json:"interval"`
	Verdict     model.Kind         `json:"verdict"`
	Region      string             `json:"region,omitempty"`
	Probes      []CheckProbeStatus `json:"probes"`
}

type CheckProbeStatus struct {
	Probe   string         `json:"probe"`
	Failing bool           `json:"failing"`
	Last    *checks.Result `json:"last,omitempty"`
	At      time.Time      `json:"at,omitzero"`
}

func (h *Hub) Snapshot() Status {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := Status{Time: time.Now(), Probes: []ProbeStatus{}, Checks: []CheckStatus{}, Incidents: []model.Incident{}} // "[]", not null, for the UI
	for _, id := range h.probeOrder {
		p := h.probes[id]
		mode := "agent"
		if p.cfg.Via != "" {
			mode = "via"
		} else if p.cfg.Local {
			mode = "local"
		}
		ps := ProbeStatus{ID: id, Name: p.cfg.Name, Labels: p.cfg.Labels, Region: p.cfg.Labels[h.cfg.RegionLabel], Mode: mode,
			Online: p.online, Internet: !p.noNet, Since: p.since, Version: p.hello.Version, Host: p.hello.Hostname,
			SysInfo: p.sysinfo, Metrics: p.metrics, MetricsAt: p.metricsAt}
		if p.cfg.Local {
			ps.Version = version.Version
		}
		if p.network != nil {
			n := *p.network
			ps.Network = &n
		}
		for _, cid := range h.checkOrder {
			ch := h.checks[cid]
			if ch.cfg.baseline || !contains(ch.probes, id) {
				continue
			}
			ps.Checks++
			if st := ch.state[id]; st != nil && st.down {
				ps.Failing++
			}
		}
		s.Probes = append(s.Probes, ps)
	}
	for _, id := range h.checkOrder {
		ch := h.checks[id]
		if ch.cfg.baseline {
			continue
		}
		cs := CheckStatus{ID: id, Name: ch.cfg.Name, Group: ch.cfg.Group, Tags: ch.cfg.Tags, Parents: ch.parents, ParentsAuto: len(ch.cfg.DependsOn) == 0 && len(ch.parents) > 0, Type: ch.cfg.Type, Target: ch.cfg.target,
			Parent: ch.cfg.parent, Interval: ch.cfg.Interval, Verdict: ch.kind, Region: ch.region}
		for _, pid := range ch.probes {
			ps := CheckProbeStatus{Probe: pid}
			if st := ch.state[pid]; st != nil {
				last := st.last
				ps.Failing, ps.Last, ps.At = st.down, &last, st.at
			}
			cs.Probes = append(cs.Probes, ps)
		}
		s.Checks = append(s.Checks, cs)
	}
	for _, inc := range h.incidents {
		s.Incidents = append(s.Incidents, cloneIncident(inc))
	}
	sort.Slice(s.Incidents, func(i, j int) bool { return s.Incidents[i].Started.Before(s.Incidents[j].Started) })
	return s
}

var verdictValue = map[model.Kind]int{model.KindUnknown: -1, model.KindOK: 0, model.KindPartial: 1, model.KindBlocked: 2, model.KindDown: 3}

// handleMetrics exposes the current state in Prometheus text format, so
// VictoriaMetrics/Prometheus keep history and Grafana keeps working.
func (h *Hub) handleMetrics(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	var b strings.Builder
	help := func(name, typ, text string) { fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, text, name, typ) }
	lbl := func(kv ...string) string {
		var parts []string
		for i := 0; i+1 < len(kv); i += 2 {
			if kv[i+1] == "" {
				continue
			}
			parts = append(parts, kv[i]+`="`+labelEscaper.Replace(kv[i+1])+`"`)
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	num := func(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

	help("uptimeforge_probe_online", "gauge", "1 if the probe is connected (virtual probes are always online).")
	for _, id := range h.probeOrder {
		p := h.probes[id]
		fmt.Fprintf(&b, "uptimeforge_probe_online%s %d\n", lbl("probe", id, "region", p.cfg.Labels[h.cfg.RegionLabel]), b2i(p.online))
	}
	help("uptimeforge_probe_internet", "gauge", "0 if the probe's baseline checks fail.")
	for _, id := range h.probeOrder {
		fmt.Fprintf(&b, "uptimeforge_probe_internet%s %d\n", lbl("probe", id), b2i(!h.probes[id].noNet))
	}

	help("uptimeforge_check_verdict", "gauge", "Verdict across probes: -1 unknown, 0 ok, 1 partial, 2 blocked, 3 down.")
	for _, id := range h.checkOrder {
		ch := h.checks[id]
		if ch.cfg.baseline {
			continue
		}
		fmt.Fprintf(&b, "uptimeforge_check_verdict%s %d\n", lbl("check", id, "name", ch.cfg.Name, "group", ch.cfg.Group, "type", ch.cfg.Type), verdictValue[ch.kind])
	}

	type series struct{ name, typ, help string }
	perProbe := []series{
		{"uptimeforge_check_up", "gauge", "1 if the last run succeeded on the probe."},
		{"uptimeforge_check_failing", "gauge", "1 if the probe counts the check as failing (after retries)."},
		{"uptimeforge_check_latency_ms", "gauge", "Duration of the last run."},
		{"uptimeforge_check_ttfb_ms", "gauge", "Time to first byte through the tunnel (xray) or to the server (http)."},
		{"uptimeforge_check_throughput_bytes", "gauge", "Throughput of the last heavy xray run, bytes per second."},
	}
	for _, s := range perProbe {
		help(s.name, s.typ, s.help)
		for _, id := range h.checkOrder {
			ch := h.checks[id]
			if ch.cfg.baseline {
				continue
			}
			for _, pid := range ch.probes {
				st := ch.state[pid]
				if st == nil {
					continue
				}
				l := lbl("check", id, "group", ch.cfg.Group, "type", ch.cfg.Type, "probe", pid,
					"region", h.probes[pid].cfg.Labels[h.cfg.RegionLabel])
				var v float64
				switch s.name {
				case "uptimeforge_check_up":
					v = float64(b2i(st.last.Status == checks.Up))
				case "uptimeforge_check_failing":
					v = float64(b2i(st.down))
				case "uptimeforge_check_latency_ms":
					v = st.last.LatencyMS
				case "uptimeforge_check_ttfb_ms":
					t, ok := st.last.Phases["ttfb"]
					if !ok {
						continue
					}
					v = t
				case "uptimeforge_check_throughput_bytes":
					if st.last.Throughput == 0 {
						continue
					}
					v = st.last.Throughput
				}
				fmt.Fprintf(&b, "%s%s %s\n", s.name, l, num(v))
			}
		}
	}

	help("uptimeforge_incidents_open", "gauge", "Open incidents by severity.")
	counts := map[model.Severity]int{model.Critical: 0, model.Warning: 0}
	for _, inc := range h.incidents {
		counts[inc.Severity]++
	}
	for _, sev := range []model.Severity{model.Critical, model.Warning} {
		fmt.Fprintf(&b, "uptimeforge_incidents_open%s %d\n", lbl("severity", string(sev)), counts[sev])
	}
	w.Write([]byte(b.String()))
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
