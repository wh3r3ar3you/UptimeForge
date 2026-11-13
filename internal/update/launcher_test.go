package update

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeAgent is the testdata/fakeagent binary, built once into fakeDir for
// the package's tests.
var fakeAgent, fakeDir string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "uf-fakeagent-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fakeDir = dir
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func buildFake(t *testing.T) string {
	t.Helper()
	if fakeAgent != "" {
		return fakeAgent
	}
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go toolchain to build the fake agent")
	}
	out := filepath.Join(fakeDir, binName())
	cmd := exec.Command(gobin, "build", "-o", out, "./testdata/fakeagent")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake agent: %v\n%s", err, b)
	}
	fakeAgent = out
	return out
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestChoose(t *testing.T) {
	const builtin = "/usr/bin/uptimeforge-agent"
	for _, tc := range []struct {
		name              string
		current, previous string
		bad               string
		have              []string // versions with a binary under bin/
		builtinVersion    string
		want              string
	}{
		{name: "newer current", current: "0.3.0", have: []string{"0.3.0"}, builtinVersion: "0.2.0", want: "0.3.0"},
		{name: "current bad", current: "0.3.0", bad: "0.3.0", have: []string{"0.3.0"}, builtinVersion: "0.2.0", want: "builtin"},
		{name: "current bad, previous newer", current: "0.3.0", previous: "0.2.1", bad: "0.1.0\n0.3.0", have: []string{"0.3.0", "0.2.1"}, builtinVersion: "0.2.0", want: "0.2.1"},
		{name: "current older", current: "0.1.0", have: []string{"0.1.0"}, builtinVersion: "0.2.0", want: "builtin"},
		{name: "current same", current: "0.2.0", have: []string{"0.2.0"}, builtinVersion: "0.2.0", want: "builtin"},
		{name: "rc older than release", current: "0.2.0-rc1", have: []string{"0.2.0-rc1"}, builtinVersion: "0.2.0", want: "builtin"},
		{name: "binary missing", current: "0.3.0", builtinVersion: "0.2.0", want: "builtin"},
		{name: "dev build", current: "0.3.0", have: []string{"0.3.0"}, builtinVersion: "dev", want: "builtin"},
		{name: "not a version", current: "../../tmp", builtinVersion: "0.2.0", want: "builtin"},
		{name: "nothing installed", builtinVersion: "0.2.0", want: "builtin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.current != "" {
				writeState(t, dir, "current", tc.current)
			}
			if tc.previous != "" {
				writeState(t, dir, "previous", tc.previous)
			}
			if tc.bad != "" {
				writeState(t, dir, "bad", tc.bad)
			}
			for _, v := range tc.have {
				p := binPath(dir, v)
				os.MkdirAll(filepath.Dir(p), 0o755)
				os.WriteFile(p, []byte(v), 0o755)
			}
			path, v := Choose(dir, builtin, tc.builtinVersion)
			if tc.want == "builtin" {
				if path != builtin || v != tc.builtinVersion {
					t.Fatalf("got %s %s, want the builtin", path, v)
				}
				return
			}
			if path != binPath(dir, tc.want) || v != tc.want {
				t.Fatalf("got %s %s, want %s", path, v, tc.want)
			}
		})
	}
}

// fixture lays out a builtin fake agent of version 0.2.0 and fake agents
// installed under the state dir, and runs a launcher over them.
type fixture struct {
	t     *testing.T
	state string
	log   string
	l     *launcher
}

func newFixture(t *testing.T, spec string, installed ...string) *fixture {
	fake := buildFake(t)
	dir := t.TempDir()
	f := &fixture{t: t, state: filepath.Join(dir, "state"), log: filepath.Join(dir, "log")}
	builtin := filepath.Join(dir, "builtin", binName())
	copyFile(t, fake, builtin)
	for _, v := range installed {
		copyFile(t, fake, binPath(f.state, v))
	}
	f.l = &launcher{
		stateDir: f.state, builtin: builtin, builtinVersion: "0.2.0",
		args:       []string{f.log, f.state, spec},
		healthWait: 200 * time.Millisecond,
		minBackoff: 50 * time.Millisecond, maxBackoff: 100 * time.Millisecond, stable: time.Minute,
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return f
}

// start runs the launcher until the test ends; stopping it must end the
// child and return nil.
func (f *fixture) start() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.l.run(ctx) }()
	f.t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				f.t.Errorf("launcher returned %v", err)
			}
		case <-time.After(10 * time.Second):
			f.t.Error("launcher did not stop")
		}
	})
}

// started lists the children started so far, failing the test if one ran
// without UF_LAUNCHER=1 or without the builtin version in its environment.
func (f *fixture) started() []string {
	f.t.Helper()
	b, _ := os.ReadFile(f.log)
	var ids []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		fs := strings.Fields(line)
		if len(fs) != 3 || fs[1] != "launcher=1" || fs[2] != "builtin="+f.l.builtinVersion {
			f.t.Fatalf("a child ran without UF_LAUNCHER=1 or UF_LAUNCHER_BUILTIN: %q", line)
		}
		ids = append(ids, fs[0])
	}
	return ids
}

// starts waits until the children started match want, in order.
func (f *fixture) starts(want ...string) {
	f.t.Helper()
	var got []string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if got = f.started(); len(got) >= len(want) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		f.t.Fatalf("children started: %v, want %v", got, want)
	}
}

// waitBad waits until v is in bad.
func (f *fixture) waitBad(v string) {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !readBad(f.state)[v] && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !readBad(f.state)[v] {
		f.t.Fatalf("%s never marked bad", v)
	}
}

// A new version that never reports a hub connection is rolled back to the
// previous one within healthWait.
func TestLaunchRollsBack(t *testing.T) {
	for _, mode := range []string{"hang", "exit1"} { // a crash loop counts toward the same deadline
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t, "0.3.0="+mode+",0.2.1=connect", "0.3.0", "0.2.1")
			writeState(t, f.state, "current", "0.3.0")
			writeState(t, f.state, "previous", "0.2.1")
			began := time.Now()
			f.start()
			f.waitBad("0.3.0")
			if took := time.Since(began); took < 200*time.Millisecond {
				t.Fatalf("marked bad after %v, before healthWait", took)
			}
			time.Sleep(300 * time.Millisecond) // 0.2.1 is up: no more starts
			ids := f.started()
			if ids[0] != "0.3.0" || ids[len(ids)-1] != "0.2.1" || strings.Count(strings.Join(ids, " "), "0.2.1") != 1 {
				t.Fatalf("children started: %v", ids)
			}
		})
	}
}

// An installed version that cannot even be started (not an executable for
// this host, say) is rolled back at once, without waiting out healthWait.
func TestLaunchRollsBackUnstartable(t *testing.T) {
	f := newFixture(t, "0.2.1=connect", "0.2.1")
	f.l.healthWait = time.Hour
	broken := binPath(f.state, "0.3.0")
	os.MkdirAll(filepath.Dir(broken), 0o755)
	if err := os.WriteFile(broken, []byte("not a program"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeState(t, f.state, "current", "0.3.0")
	writeState(t, f.state, "previous", "0.2.1")
	f.start()
	f.waitBad("0.3.0")
	f.starts("0.2.1")
}

// A version that connected keeps running past healthWait.
func TestLaunchKeepsHealthy(t *testing.T) {
	f := newFixture(t, "0.3.0=connect", "0.3.0")
	writeState(t, f.state, "current", "0.3.0")
	f.start()
	f.starts("0.3.0")
	time.Sleep(500 * time.Millisecond)
	if len(readBad(f.state)) != 0 {
		t.Fatalf("bad: %v", readBad(f.state))
	}
	f.starts("0.3.0")
}

// Exit 75 means "an update is installed": the choice is read again and the
// new child starts at once, without the crash backoff.
func TestLaunchRestartsOn75(t *testing.T) {
	f := newFixture(t, "0.3.0=upgrade:0.4.0,0.4.0=connect", "0.3.0", "0.4.0")
	f.l.minBackoff, f.l.maxBackoff = time.Hour, time.Hour
	writeState(t, f.state, "current", "0.3.0")
	writeState(t, f.state, ConnectedMarker("0.3.0"), "")
	f.start()
	f.starts("0.3.0", "0.4.0")
}

// Exit 75 that leaves the choice as it was (the update is not selectable)
// is no reason to spin: it takes the crash backoff.
func TestLaunch75WithoutNewChoiceBacksOff(t *testing.T) {
	f := newFixture(t, "builtin=exit75")
	f.l.minBackoff, f.l.maxBackoff = time.Hour, time.Hour
	f.start()
	f.starts("builtin")
	time.Sleep(400 * time.Millisecond)
	f.starts("builtin")
}

// Any other exit restarts the same choice after a backoff.
func TestLaunchRestartsOnCrash(t *testing.T) {
	f := newFixture(t, "builtin=exit1")
	f.start()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(f.started()) >= 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, id := range f.started() {
		if id != "builtin" {
			t.Fatalf("children started: %v", f.started())
		}
	}
	if n := len(f.started()); n < 3 {
		t.Fatalf("%d restarts", n)
	}
}
