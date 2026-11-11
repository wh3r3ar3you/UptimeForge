package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/checks"
	"github.com/wh3r3ar3you/uptimeforge/internal/hub"
	"github.com/wh3r3ar3you/uptimeforge/internal/xray"
)

type runRequest struct {
	Target    string   `json:"target"`
	Probes    []string `json:"probes"`
	Mode      string   `json:"mode"` // "auto" or "subscription"
	UserAgent string   `json:"user_agent"`
}

type runRow struct {
	Name     string           `json:"name"`
	Target   string           `json:"target"`
	Type     string           `json:"type"`
	Outcomes []hub.RunOutcome `json:"outcomes"`
	Error    string           `json:"error,omitempty"`
}

const maxSubscriptionRows = 100

// SpecFor turns whatever the user pasted into a check: a share link, an
// http(s) URL, a tg:// MTProxy link or host:port.
func SpecFor(target string) (checks.Spec, error) {
	target = strings.TrimSpace(target)
	spec := checks.Spec{ID: "_run", Timeout: checks.Duration(25 * time.Second)}
	scheme, _, hasScheme := strings.Cut(target, "://")
	scheme = strings.ToLower(scheme)
	switch {
	case hasScheme && (scheme == "http" || scheme == "https"):
		if mt := mtprotoSpec(target); mt != nil {
			return *mt, nil
		}
		spec.Type, spec.HTTP = "http", &checks.HTTPParams{URL: target}
	case hasScheme && scheme == "tg":
		if mt := mtprotoSpec(target); mt != nil {
			return *mt, nil
		}
		return spec, errors.New("this tg:// link is not an MTProxy link")
	case hasScheme:
		if _, err := xray.ParseLink(target); err != nil {
			return spec, err
		}
		spec.Type, spec.Xray = "xray", &checks.XrayParams{Link: target}
	default:
		if _, _, err := net.SplitHostPort(target); err != nil {
			return spec, errors.New("paste a share link (vless://, vmess://, trojan://, ss://, hy2://), an http(s) URL, a tg://proxy link or host:port")
		}
		spec.Type, spec.TCP = "tcp", &checks.TCPParams{Address: target}
	}
	return spec, nil
}

func (s *Server) runTool(w http.ResponseWriter, r *http.Request) {
	var req runRequest
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()

	if req.Mode == "subscription" {
		fctx, fcancel := context.WithTimeout(ctx, 30*time.Second)
		entries, err := xray.FetchSubscription(fctx, &http.Client{}, strings.TrimSpace(req.Target), req.UserAgent)
		fcancel()
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if len(entries) > maxSubscriptionRows {
			entries = entries[:maxSubscriptionRows]
		}
		rows := make([]runRow, len(entries))
		var wg sync.WaitGroup
		sem := make(chan struct{}, 8)
		for i, e := range entries {
			proto, _ := e.Outbound["protocol"].(string)
			rows[i] = runRow{Name: e.Name, Target: strings.ToUpper(proto) + " · " + e.Endpoint, Type: "xray"}
			wg.Add(1)
			go func(i int, ob xray.Outbound) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				spec := checks.Spec{ID: "_run", Type: "xray", Timeout: checks.Duration(25 * time.Second), Xray: &checks.XrayParams{Outbound: ob}}
				rows[i].Outcomes = s.app.Hub.RunOnce(ctx, spec, req.Probes)
			}(i, e.Outbound)
		}
		wg.Wait()
		writeJSON(w, http.StatusOK, map[string]any{"rows": rows})
		return
	}

	spec, err := SpecFor(req.Target)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	row := runRow{Name: req.Target, Target: spec.Target(), Type: spec.Type}
	if spec.Xray != nil {
		if l, err := xray.ParseLink(req.Target); err == nil {
			row.Name, row.Target = pick(l.Name, l.Endpoint()), strings.ToUpper(l.Protocol)+" · "+l.Endpoint()
		}
	}
	row.Outcomes = s.app.Hub.RunOnce(ctx, spec, req.Probes)
	writeJSON(w, http.StatusOK, map[string]any{"rows": []runRow{row}})
}
