package update

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/version"
)

const (
	// HealthWait is how long a newly installed version has, from its first
	// start, to report a hub connection before it is rolled back.
	HealthWait = 2 * time.Minute
	// ExitRestart is the exit code by which the agent tells the launcher it
	// installed an update: read the choice again and start at once.
	ExitRestart = 75
	// stopWait is how long a child has to exit after SIGTERM.
	stopWait = 10 * time.Second
)

// Choose picks the binary to run: current, else previous, if it is not
// rolled back, is installed and is strictly newer than the builtin binary
// (the one the launcher itself is); otherwise the builtin. A dev builtin
// always runs itself, and the builtin is never rolled back.
func Choose(stateDir, builtin, builtinVersion string) (path, version string) {
	if !IsRelease(builtinVersion) {
		return builtin, builtinVersion
	}
	bad := readBad(stateDir)
	for _, name := range []string{"current", "previous"} {
		v := readVersion(stateDir, name)
		if v == "" || bad[v] || Compare(v, builtinVersion) <= 0 {
			continue
		}
		p := binPath(stateDir, v)
		if st, err := os.Stat(p); err != nil || !st.Mode().IsRegular() {
			continue
		}
		return p, v
	}
	return builtin, builtinVersion
}

// Launch runs the agent chosen by Choose as a child with args until ctx is
// cancelled, then stops the child and returns nil. builtin is the launcher's
// own binary. The child runs with UF_LAUNCHER=1 and the launcher's stdout
// and stderr. Exit 75 starts the new choice at once; any other exit restarts
// it after a backoff (1 s doubling to 30 s, reset after a minute of running).
// An installed version that has not written connected-<version> within
// HealthWait of its first start is killed, marked bad and replaced by the
// next choice; crashes in that window count toward the same deadline.
func Launch(ctx context.Context, stateDir, builtin string, args []string) error {
	l := &launcher{
		stateDir: stateDir, builtin: builtin, builtinVersion: version.Version, args: args,
		healthWait: HealthWait, minBackoff: time.Second, maxBackoff: 30 * time.Second, stable: time.Minute,
		log: slog.Default(),
	}
	return l.run(ctx)
}

type launcher struct {
	stateDir, builtin, builtinVersion string
	args                              []string
	healthWait                        time.Duration
	minBackoff, maxBackoff, stable    time.Duration
	log                               *slog.Logger
}

// errUnhealthy ends a child that did not connect in time.
var errUnhealthy = errors.New("no hub connection within the health window")

func (l *launcher) run(ctx context.Context) error {
	if l.builtin == "" {
		return errors.New("launcher: no builtin agent binary")
	}
	firstStart := map[string]time.Time{} // per watched version, in this run
	backoff := l.minBackoff
	for ctx.Err() == nil {
		path, v := Choose(l.stateDir, l.builtin, l.builtinVersion)
		var deadline time.Time // zero: not watched
		if path != l.builtin && !l.connected(v) {
			first, ok := firstStart[v]
			if !ok {
				first = time.Now()
				firstStart[v] = first
			}
			deadline = first.Add(l.healthWait)
			if !time.Now().Before(deadline) {
				l.rollBack(v, firstStart)
				continue
			}
		}

		started := time.Now()
		code, err := l.runChild(ctx, path, v, deadline)
		switch {
		case ctx.Err() != nil:
			return nil
		case errors.Is(err, errUnhealthy):
			l.rollBack(v, firstStart)
			backoff = l.minBackoff
			continue
		case code == ExitRestart:
			l.log.Info("agent restarts into a new choice", "version", v)
			backoff = l.minBackoff
			continue
		}
		if time.Since(started) >= l.stable {
			backoff = l.minBackoff
		}
		wait := backoff
		if !deadline.IsZero() {
			wait = max(0, min(wait, time.Until(deadline)))
		}
		l.log.Warn("agent exited", "version", v, "code", code, "err", err, "restart_in", wait)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		backoff = min(backoff*2, l.maxBackoff)
	}
	return nil
}

func (l *launcher) connected(v string) bool {
	_, err := os.Stat(filepath.Join(l.stateDir, ConnectedMarker(v)))
	return err == nil
}

// rollBack marks v bad; the next Choose falls back past it. Its health
// window is forgotten, so an explicit re-install gets a fresh one.
func (l *launcher) rollBack(v string, firstStart map[string]time.Time) {
	l.log.Error("agent version never connected to the hub: rolling back", "version", v, "within", l.healthWait)
	if err := setBad(l.stateDir, v, true); err != nil {
		l.log.Error("cannot mark the version bad", "version", v, "err", err)
	}
	delete(firstStart, v)
}

// runChild runs one child until it exits (its exit code), ctx is cancelled
// or, when deadline is set, the deadline passes without its connected
// marker (errUnhealthy). In the last two cases the child is stopped first.
func (l *launcher) runChild(ctx context.Context, path, v string, deadline time.Time) (int, error) {
	cmd := exec.Command(path, l.args...)
	cmd.Env = append(os.Environ(), "UF_LAUNCHER=1")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return -1, err
	}
	l.log.Info("agent started", "version", v, "path", path, "pid", cmd.Process.Pid)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var health <-chan time.Time
	if !deadline.IsZero() {
		t := time.NewTimer(time.Until(deadline))
		defer t.Stop()
		health = t.C
	}
	for {
		select {
		case err := <-done:
			var ee *exec.ExitError
			switch {
			case err == nil:
				return 0, nil
			case errors.As(err, &ee):
				return ee.ExitCode(), err
			}
			return -1, err
		case <-ctx.Done():
			stop(cmd.Process, done)
			return -1, ctx.Err()
		case <-health:
			if l.connected(v) {
				health = nil
				continue
			}
			stop(cmd.Process, done)
			return -1, errUnhealthy
		}
	}
}

// stop asks the child to exit (SIGTERM; Windows has only Kill) and kills it
// if it is still there after stopWait. done delivers its Wait.
func stop(p *os.Process, done <-chan error) {
	if runtime.GOOS == "windows" {
		p.Kill()
	} else {
		p.Signal(syscall.SIGTERM)
	}
	select {
	case <-done:
	case <-time.After(stopWait):
		p.Kill()
		<-done
	}
}
