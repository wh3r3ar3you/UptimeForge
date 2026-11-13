package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/app"
	"github.com/wh3r3ar3you/uptimeforge/internal/hub"
	"github.com/wh3r3ar3you/uptimeforge/internal/store"
	"github.com/wh3r3ar3you/uptimeforge/internal/version"
)

type ProbeRow struct {
	hub.ProbeStatus
	Config  hub.ProbeConfig `json:"config"`
	Managed bool            `json:"managed"`
	// Traffic24h is bytes moved over the last day, from stored metrics.
	Traffic24h *store.Traffic `json:"traffic_24h,omitempty"`
}

func (s *Server) listProbes(w http.ResponseWriter, r *http.Request) {
	snap := s.app.Hub.Snapshot()
	items := map[string]app.ProbeItem{}
	viewer := isViewer(r)
	for _, p := range s.app.Probes() {
		p.TokenHash = ""
		if viewer {
			p.ProbeConfig = redactProbe(p.ProbeConfig)
		}
		items[p.ID] = p
	}
	if viewer {
		redactProbeStatus(snap.Probes)
	}
	traffic := s.traffic24h(r.Context())
	rows := make([]ProbeRow, 0, len(snap.Probes))
	for _, ps := range snap.Probes {
		it := items[ps.ID]
		row := ProbeRow{ProbeStatus: ps, Config: it.ProbeConfig, Managed: it.Managed}
		if tr, ok := traffic[ps.ID]; ok {
			row.Traffic24h = &tr
		}
		rows = append(rows, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"probes": rows, "hub_url": s.hubURL(r)})
}

// traffic24h is every probe's last-day traffic, cached for a minute: the
// servers list refreshes every few seconds and the sum barely moves.
func (s *Server) traffic24h(ctx context.Context) map[string]store.Traffic {
	s.trafficMu.Lock()
	defer s.trafficMu.Unlock()
	if s.traffic != nil && time.Since(s.trafficAt) < time.Minute {
		return s.traffic
	}
	t, err := s.app.Store.TrafficSince(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		s.log.Warn("traffic 24h", "err", err)
		return s.traffic
	}
	s.traffic, s.trafficAt = t, time.Now()
	return t
}

func (s *Server) getPrefs(w http.ResponseWriter, r *http.Request) {
	p, err := s.app.Store.Prefs(r.Context(), userFrom(r.Context()).ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(p)
}

func (s *Server) putPrefs(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, store.MaxPrefs+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.app.Store.SetPrefs(r.Context(), userFrom(r.Context()).ID, body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}

// hubURL is the address agents and links should use: UF_PUBLIC_URL, or the
// one the request came to. A Host that is not a plain host[:port] gives ""
// (it would end up in shell commands).
func (s *Server) hubURL(r *http.Request) string {
	if u := s.app.Hub.Config().PublicURL; u != "" {
		return strings.TrimRight(u, "/")
	}
	scheme := "http"
	if s.trust.secure(r) {
		scheme = "https"
	}
	host := s.trust.host(r)
	if !validHost(host) {
		return ""
	}
	return scheme + "://" + host
}

// Install is what the UI shows after creating an agent probe.
type Install struct {
	Token      string `json:"token"`
	Compose    string `json:"compose"`     // compose.yml
	ComposeCmd string `json:"compose_cmd"` // one paste: writes compose.yml and starts it
	Script     string `json:"script"`
	Docker     string `json:"docker"`
	Binary     string `json:"binary"`
	// Warning is set when the hub URL is plain http: the token and the
	// agent download cross the network in the clear.
	Warning string `json:"warning,omitempty"`
}

// agentImageFor is the agent image a hub of this version installs: its own
// release, so hub and agents stay in step; a development build takes latest.
func agentImageFor(v string) string {
	if releaseVersion.MatchString(v) {
		return "intoxxx/uptimeforge-agent:" + v
	}
	return "intoxxx/uptimeforge-agent:latest"
}

var releaseVersion = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?$`)

// ReleasesURL is where the GitHub releases of UptimeForge are: the installer
// downloads an agent from $ReleasesURL/v<hub version>/ when the hub has none.
const ReleasesURL = "https://github.com/wh3r3ar3you/uptimeforge/releases/download"

// composeFile runs the agent next to whatever else the server does: on the
// host's network so the probe sees the world from there (it changes no routes
// and listens on no ports), without privileges and with bounded resources.
// The launcher keeps the updates it installs in the /state volume, the one
// writable place, so they outlive the container.
func composeFile(hubURL, token string) string {
	return fmt.Sprintf(`services:
  uptimeforge-agent:
    image: %s
    container_name: uptimeforge-agent
    restart: unless-stopped
    network_mode: host
    read_only: true
    tmpfs: [/tmp]
    cap_drop: [ALL]
    security_opt: ["no-new-privileges:true"]
    mem_limit: 512m
    cpus: 1
    environment:
      UF_HUB: %s
      UF_TOKEN: %s
      UF_STATE_DIR: /state
    volumes:
      - uptimeforge-agent-state:/state
volumes:
  uptimeforge-agent-state:
`, agentImageFor(version.Version), strconv.Quote(hubURL), strconv.Quote(token))
}

func (s *Server) install(r *http.Request, token string) *Install {
	if token == "" {
		return nil
	}
	return s.installFor(s.hubURL(r), token)
}

// plainHTTPWarning goes with every install command for a hub on http://.
const plainHTTPWarning = "WARNING: the hub URL is plain http: the agent token, the agent's reports and the agent download " +
	"cross the network unencrypted and can be read or swapped on the way. Put the hub behind https and set UF_PUBLIC_URL."

func (s *Server) installFor(hubURL, token string) *Install {
	in := s.installCommands(hubURL, token)
	if strings.HasPrefix(strings.ToLower(hubURL), "http://") {
		in.Warning = plainHTTPWarning
		note := "# " + plainHTTPWarning + "\n"
		in.Script, in.Docker, in.Binary, in.ComposeCmd = note+in.Script, note+in.Docker, note+in.Binary, note+in.ComposeCmd
	}
	return in
}

func (s *Server) installCommands(hubURL, token string) *Install {
	compose := composeFile(hubURL, token)
	return &Install{
		Token:      token,
		Compose:    compose,
		ComposeCmd: "mkdir -p /opt/uptimeforge-agent && cd /opt/uptimeforge-agent && cat > compose.yml <<'EOF'\n" + compose + "EOF\ndocker compose up -d",
		Script: fmt.Sprintf("curl -fsSL %s | sudo UF_HUB=%s UF_TOKEN=%s sh",
			shellWord(hubURL+"/install.sh"), shellWord(hubURL), shellWord(token)),
		Docker: fmt.Sprintf("docker run -d --name uptimeforge-agent --restart unless-stopped --network host -v uptimeforge-agent-state:/state -e UF_HUB=%s -e UF_TOKEN=%s %s",
			shellWord(hubURL), shellWord(token), agentImageFor(version.Version)),
		Binary: fmt.Sprintf("uptimeforge-agent --hub %s --token %s", shellWord(hubURL), shellWord(token)),
	}
}

func (s *Server) createProbe(w http.ResponseWriter, r *http.Request) {
	var p hub.ProbeConfig
	if err := decode(r, &p); err != nil {
		s.fail(w, err)
		return
	}
	token, err := s.app.SaveProbe(r.Context(), p, true)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"probe": p, "install": s.install(r, token)})
}

func (s *Server) updateProbe(w http.ResponseWriter, r *http.Request) {
	var p hub.ProbeConfig
	if err := decode(r, &p); err != nil {
		s.fail(w, err)
		return
	}
	p.ID = r.PathValue("id")
	token, err := s.app.SaveProbe(r.Context(), p, false)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"probe": p, "install": s.install(r, token)})
}

func (s *Server) deleteProbe(w http.ResponseWriter, r *http.Request) {
	if err := s.app.DeleteProbe(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) rotateToken(w http.ResponseWriter, r *http.Request) {
	token, err := s.app.RotateToken(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"install": s.install(r, token)})
}

func (s *Server) probeMetrics(w http.ResponseWriter, r *http.Request) {
	from, to, err := parseRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	pts, err := s.app.Store.SysSeries(r.Context(), r.PathValue("id"), from, to, 240)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from.Unix(), "to": to.Unix(), "points": pts})
}

// sparklines is the last hour of every server for the dashboard cards.
func (s *Server) sparklines(w http.ResponseWriter, r *http.Request) {
	to := time.Now()
	sp, err := s.app.Store.Sparks(r.Context(), to.Add(-time.Hour), to, 120)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sp)
}

func (s *Server) listIncidents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.IncidentFilter{Limit: 100, Open: q.Get("status") == "open"}
	if k := q.Get("key"); k != "" {
		f.Keys = []string{k}
	}
	if b := q.Get("before"); b != "" {
		if t, err := time.Parse(time.RFC3339, b); err == nil {
			f.To = t
		}
	}
	incs, err := s.app.Store.Incidents(r.Context(), f)
	if err != nil {
		s.fail(w, err)
		return
	}
	open := s.app.Hub.Snapshot().Incidents
	if isViewer(r) {
		redactIncidents(open)
		redactIncidents(incs)
	}
	writeJSON(w, http.StatusOK, map[string]any{"open": open, "history": incs})
}

func (s *Server) ackIncident(w http.ResponseWriter, r *http.Request) {
	if err := s.app.Hub.Ack(r.PathValue("id"), userFrom(r.Context()).Username); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// resolveIncident closes an incident by hand; it stays quiet until it recovers.
func (s *Server) resolveIncident(w http.ResponseWriter, r *http.Request) {
	if err := s.app.Hub.Resolve(r.PathValue("id"), userFrom(r.Context()).Username); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) muteIncident(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Minutes int `json:"minutes"`
	}
	if err := decode(r, &body); err != nil {
		s.fail(w, err)
		return
	}
	if body.Minutes <= 0 || body.Minutes > 7*24*60 {
		writeError(w, http.StatusBadRequest, errors.New("minutes must be between 1 and 10080"))
		return
	}
	if err := s.app.Hub.Mute(r.PathValue("id"), time.Duration(body.Minutes)*time.Minute, userFrom(r.Context()).Username); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
