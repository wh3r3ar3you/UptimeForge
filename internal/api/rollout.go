package api

import (
	"net/http"
	"slices"

	"github.com/wh3r3ar3you/uptimeforge/internal/hub"
	"github.com/wh3r3ar3you/uptimeforge/internal/update"
)

// rolloutControl is the hub's agent-update rollout as the API drives it.
type rolloutControl interface {
	Rollout() hub.RolloutStatus
	ResumeRollout(by string)
}

// RolloutView is the rollout as the settings page shows it.
type RolloutView struct {
	hub.RolloutStatus
	// Names are the display names of the probes the status lists, by ID;
	// a probe without a name is left out.
	Names map[string]string `json:"names"`
	// Release is false for a development build: it updates no agents.
	Release bool `json:"release"`
}

func (s *Server) rolloutView() RolloutView {
	st := s.rollout.Rollout()
	ids := map[string]bool{}
	for _, id := range st.InFlight {
		ids[id] = true
	}
	for _, id := range slices.Concat(st.NeedReinstall, st.Unsupported) {
		ids[id] = true
	}
	for _, f := range st.Failed {
		ids[f.Probe] = true
	}
	names := map[string]string{}
	for _, p := range s.app.Probes() {
		if ids[p.ID] && p.Name != "" {
			names[p.ID] = p.Name
		}
	}
	return RolloutView{RolloutStatus: st, Names: names, Release: update.IsRelease(st.Version)}
}

func (s *Server) getRollout(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.rolloutView())
}

// resumeRollout lifts a halted rollout in the admin's name and answers the
// status after it.
func (s *Server) resumeRollout(w http.ResponseWriter, r *http.Request) {
	s.rollout.ResumeRollout(userFrom(r.Context()).Username)
	writeJSON(w, http.StatusOK, s.rolloutView())
}
