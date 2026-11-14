package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/wh3r3ar3you/uptimeforge/internal/app"
)

// joinCommand is what a volunteer pastes to turn their machine into a
// checker: the hub's join script, given the invite code.
func joinCommand(hubURL, code string) string {
	if hubURL == "" {
		return ""
	}
	return fmt.Sprintf("curl -fsSL %s | sudo sh -s -- %s", shellWord(hubURL+"/join.sh"), shellWord(code))
}

func (s *Server) createInvite(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string `json:"name"`
		Trusted bool   `json:"trusted"`
	}
	if err := decode(r, &body); err != nil {
		s.fail(w, err)
		return
	}
	inv, code, err := s.app.CreateInvite(r.Context(), body.Name, body.Trusted)
	if err != nil {
		s.fail(w, err)
		return
	}
	hub := s.hubURL(r)
	out := map[string]any{"invite": inv, "code": code, "expires": inv.Expires, "command": joinCommand(hub, code)}
	if strings.HasPrefix(strings.ToLower(hub), "http://") {
		out["warning"] = plainHTTPWarning
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) listInvites(w http.ResponseWriter, r *http.Request) {
	invs, err := s.app.Invites(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"invites": invs})
}

func (s *Server) revokeInvite(w http.ResponseWriter, r *http.Request) {
	if err := s.app.RevokeInvite(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// join is where an agent exchanges an invite code for its probe and token.
// Nobody is signed in here, so it is rate limited per address, and the code
// is never logged: a refusal logs the address and why.
func (s *Server) join(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.joinLimit.allow(limitKey(ip)) {
		s.log.Warn("agent join refused", "ip", ip, "reason", "rate limited")
		writeError(w, http.StatusTooManyRequests, errors.New("too many attempts, try again in a minute"))
		return
	}
	var body struct {
		Code     string `json:"code"`
		Hostname string `json:"hostname"`
		OS       string `json:"os"`
		Arch     string `json:"arch"`
	}
	if err := decode(r, &body); err != nil {
		s.log.Warn("agent join refused", "ip", ip, "reason", "bad request")
		writeError(w, http.StatusBadRequest, errors.New("bad JSON"))
		return
	}
	id, token, err := s.app.Join(r.Context(), strings.TrimSpace(body.Code), body.Hostname, body.OS, body.Arch)
	if err != nil {
		s.log.Warn("agent join refused", "ip", ip, "reason", err.Error())
		switch {
		case errors.Is(err, app.ErrInviteUnknown):
			writeError(w, http.StatusNotFound, err)
		case errors.Is(err, app.ErrInviteUsed), errors.Is(err, app.ErrInviteExpired):
			writeError(w, http.StatusGone, err)
		default:
			s.fail(w, err)
		}
		return
	}
	s.log.Info("checker joined", "probe", id, "ip", ip, "host", app.CleanName(body.Hostname),
		"os", app.CleanName(body.OS)+"/"+app.CleanName(body.Arch))
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "token": token})
}
