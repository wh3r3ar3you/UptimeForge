package checks

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"
)

func init() {
	registerType(TypeInfo{
		Type: "push", Category: "passive", HubOnly: true,
		Fields: []Field{
			{Name: "token", Kind: "password", Required: true, Secret: true},
			{Name: "heartbeat_window", Kind: "number", Advanced: true},
		},
		target: targetNone,
		validate: func(p params) error {
			// The token is the only credential of an endpoint open to the
			// internet, and it goes into a URL path.
			if len(p["token"]) < MinPushToken || !pushTokenRe.MatchString(p["token"]) {
				return fmt.Errorf("parameter token must be at least %d letters, digits, '-', '_', '.' or '~'", MinPushToken)
			}
			// Beats can arrive before the first run, right after a restart.
			// A caller whose save then fails takes it back (ForgetPush).
			pushKnow(p["token"])
			return nil
		},
	}, checkPush)
	registerType(TypeInfo{
		Type: "manual", Category: "passive", HubOnly: true,
		Fields: []Field{
			{Name: "status", Kind: "select", Required: true, Default: "up", Options: []string{"up", "down"}},
			{Name: "message", Kind: "text"},
		},
		target: targetNone,
	}, checkManual)
}

type pushState struct {
	since   time.Time // when this token became known: the start of its grace
	last    time.Time // zero until the first beat
	up      bool
	msg     string
	latency float64
}

var (
	pushMu    sync.Mutex
	pushBeats = map[string]*pushState{}
)

const pushMsgLimit = 500

// MinPushToken is the shortest push token accepted: the UI makes 20 random
// bytes, a config file may carry its own.
const MinPushToken = 12

var pushTokenRe = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)

func pushKnow(token string) *pushState {
	pushMu.Lock()
	defer pushMu.Unlock()
	st, ok := pushBeats[token]
	if !ok {
		st = &pushState{since: time.Now()}
		pushBeats[token] = st
	}
	return st
}

// Beat records a heartbeat from an external job. It returns false for a token
// no push check uses, and stores nothing then: the endpoint is open to the
// internet, and made-up tokens must not grow this map.
func Beat(token string, up bool, msg string, pingMS float64) bool {
	pushMu.Lock()
	defer pushMu.Unlock()
	st, ok := pushBeats[token]
	if !ok {
		return false
	}
	st.last, st.up, st.msg, st.latency = time.Now(), up, clip(msg, pushMsgLimit), pingMS
	return true
}

// ForgetPush drops a deleted check's token, so its URL stops being accepted.
func ForgetPush(token string) {
	pushMu.Lock()
	defer pushMu.Unlock()
	delete(pushBeats, token)
}

// pushWindow is how long silence is fine. By default it is two check intervals:
// with exactly one, a job beating on the same period would flap on any jitter.
func pushWindow(s *Spec, p params) time.Duration {
	if sec := p.float("heartbeat_window"); sec > 0 {
		return time.Duration(sec * float64(time.Second))
	}
	interval := s.Interval.D()
	if interval <= 0 {
		interval = DefaultInterval
	}
	return 2 * interval
}

// checkPush judges the last beat. State lives in memory, so a restart forgets
// them all; the job then gets one full window to report again before the
// silence counts.
func checkPush(_ context.Context, s *Spec, _ *Env) Result {
	p := paramsOf(s)
	if p["token"] == "" {
		return fail("config", errors.New("push check without token"))
	}
	window := pushWindow(s, p)
	st := pushKnow(p["token"])
	pushMu.Lock()
	defer pushMu.Unlock()
	if st.last.IsZero() {
		if time.Since(st.since) < window {
			return Result{Info: map[string]string{"grace": "waiting for the first heartbeat"}}
		}
		return fail("no_heartbeat", fmt.Errorf("no heartbeat in %s", window))
	}
	if age := time.Since(st.last); age > window {
		return fail("no_heartbeat", fmt.Errorf("last heartbeat %s ago, window %s", age.Round(time.Second), window))
	}
	r := Result{LatencyMS: st.latency}
	if !st.up {
		r.Status, r.Reason, r.Error = Down, "push_down", st.msg
		if r.Error == "" {
			r.Error = "the job reported a failure"
		}
		return r
	}
	if st.msg != "" {
		r.Info = map[string]string{"message": st.msg}
	}
	return r
}

func checkManual(_ context.Context, s *Spec, _ *Env) Result {
	p := paramsOf(s)
	if p["status"] != "down" {
		if p["message"] != "" {
			return Result{Info: map[string]string{"message": p["message"]}}
		}
		return Result{}
	}
	msg := p["message"]
	if msg == "" {
		msg = "set to down by hand"
	}
	return fail("manual_down", errors.New(msg))
}
