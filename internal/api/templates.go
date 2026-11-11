package api

import (
	"context"
	"net/http"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/notify"
)

// templatesInfo is what the template editor needs: the defaults, the
// placeholders, the keys, kinds and samples.
func (s *Server) templatesInfo(w http.ResponseWriter, r *http.Request) {
	defaults := map[string]string{}
	for k, v := range notify.DefaultTemplates {
		defaults[string(k)] = v
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"defaults": defaults, "placeholders": notify.Placeholders, "keys": notify.TemplateKeys,
		"kinds":   []string{"down", "blocked", "partial", "probe_offline", "probe_no_net", "host", "users_drop", "node_down"},
		"samples": notify.SampleNames, "custom": s.app.Hub.Config().Templates,
	})
}

type templateReq struct {
	Lang   string `json:"lang"`
	Key    string `json:"key"`
	Text   string `json:"text"`
	Sample string `json:"sample"`
}

// previewTemplate renders a template on a sample; a template Telegram would
// refuse comes back with the reason in error, not as a failed request.
func (s *Server) previewTemplate(w http.ResponseWriter, r *http.Request) {
	var req templateReq
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	cfg := s.app.Hub.Config()
	loc, _ := time.LoadLocation(cfg.Timezone)
	if loc == nil {
		loc = time.UTC
	}
	rich, html, err := notify.Preview(req.Lang, notify.TemplateKey(req.Key), req.Text, req.Sample, loc, cfg.PublicURL)
	out := map[string]string{"rich": rich, "html": html}
	if err != nil {
		out["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, out)
}

// testTemplate sends a template rendered on a sample to the main chat.
func (s *Server) testTemplate(w http.ResponseWriter, r *http.Request) {
	var req templateReq
	if err := decode(r, &req); err != nil {
		s.fail(w, err)
		return
	}
	_, html, err := notify.Preview(req.Lang, notify.TemplateKey(req.Key), req.Text, req.Sample, time.UTC, s.app.Hub.Config().PublicURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := s.app.SendTelegramText(ctx, html); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// reportPreview is the weekly report as it would go out now.
func (s *Server) reportPreview(w http.ResponseWriter, r *http.Request) {
	rich, err := s.app.ReportPreview(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"rich": rich})
}

// reportSend posts the weekly report to Telegram now.
func (s *Server) reportSend(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.app.SendReport(ctx); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
