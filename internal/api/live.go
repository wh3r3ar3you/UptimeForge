package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/hub"
	"github.com/wh3r3ar3you/uptimeforge/internal/proto"
)

// liveKeepAlive is a var so a test can shorten it.
var liveKeepAlive = 15 * time.Second

// liveEvent is one point of the stream: the agent's metrics plus At, the
// moment they were taken in unix milliseconds (received now minus the age
// the agent reports).
type liveEvent struct {
	proto.Metrics
	At int64 `json:"at"`
}

// liveStream is GET /probes/{id}/live: a Server-Sent Events stream of the
// server's host metrics, a point a second while it is watched. Checkers,
// via probes and unknown ids are a 404.
func (s *Server) liveStream(w http.ResponseWriter, r *http.Request) {
	ch, stop, err := s.app.Hub.Watch(r.PathValue("id"))
	if errors.Is(err, hub.ErrNoLive) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	defer stop()

	// A stream outlives any server-wide ReadTimeout/WriteTimeout. Tested:
	// net/http does not cut an in-flight response with the hub's
	// ReadTimeout today; clearing both deadlines keeps it so if that
	// changes or a WriteTimeout is added.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if rc.Flush() != nil {
		return
	}

	keep := time.NewTicker(liveKeepAlive)
	defer keep.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keep.C:
			if _, err := w.Write([]byte(": keep-alive\n\n")); err != nil {
				return
			}
		case m, ok := <-ch:
			if !ok {
				return // the probe was removed or the hub stopped
			}
			at := time.Now().Add(-time.Duration(m.Age * float64(time.Second)))
			b, err := json.Marshal(liveEvent{Metrics: m, At: at.UnixMilli()})
			if err != nil {
				continue
			}
			if _, err := w.Write(append(append([]byte("data: "), b...), '\n', '\n')); err != nil {
				return
			}
		}
		if rc.Flush() != nil {
			return
		}
	}
}
