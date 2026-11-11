package checks

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRabbitMQ(t *testing.T) {
	var body string
	var code int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health/checks/alarms/" {
			t.Errorf("path %s", r.URL.Path)
		}
		u, p, _ := r.BasicAuth()
		if u != "guest" || p != "pw" {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(code)
		fmt.Fprint(w, body)
	}))
	defer srv.Close()
	spec := func(pass string) *Spec {
		return &Spec{Type: "rabbitmq", Params: map[string]string{"nodes": srv.URL + "/", "username": "guest", "password": pass}}
	}
	code, body = 200, `{"status":"ok"}`
	wantUp(t, runSpec(t, spec("pw")))
	code, body = 503, `{"status":"failed","reason":"resource alarm: memory"}`
	r := runSpec(t, spec("pw"))
	wantDown(t, r, "unhealthy")
	if !strings.Contains(r.Error, "memory") {
		t.Fatalf("%+v", r)
	}
	wantDown(t, runSpec(t, spec("wrong")), "auth")
	code = 500
	wantDown(t, runSpec(t, spec("pw")), "http_status")

	// One live node is enough; the dead one is tried first.
	code = 200
	h, p := closedPort(t)
	s := &Spec{Type: "rabbitmq", Params: map[string]string{"nodes": "http://" + h + ":" + p + "\n" + srv.URL, "username": "guest", "password": "pw"}}
	wantUp(t, runSpec(t, s))
	s.Params["nodes"] = "http://" + h + ":" + p
	wantDown(t, runSpec(t, s), "refused")
}

func TestSteam(t *testing.T) {
	var servers string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/IGameServersService/GetServerList/v1/" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.URL.Query().Get("filter") != `addr\192.0.2.1:27015` {
			t.Errorf("filter %s", r.URL.Query().Get("filter"))
		}
		if r.URL.Query().Get("key") != "good" {
			w.WriteHeader(403)
			return
		}
		fmt.Fprintf(w, `{"response":{%s}}`, servers)
	}))
	defer srv.Close()
	old := steamBase
	steamBase = srv.URL
	defer func() { steamBase = old }()
	spec := func(key string) *Spec {
		return &Spec{Type: "steam", Params: map[string]string{"host": "192.0.2.1", "port": "27015", "api_key": key}}
	}
	servers = `"servers":[{"name":"My Server"}]`
	r := runSpec(t, spec("good"))
	wantUp(t, r)
	if r.Info["name"] != "My Server" {
		t.Fatalf("%+v", r)
	}
	servers = ""
	wantDown(t, runSpec(t, spec("good")), "not_found")
	r = runSpec(t, spec("wrong-key-value"))
	wantDown(t, r, "auth")
	if strings.Contains(r.Error, "wrong-key-value") {
		t.Fatalf("leak: %+v", r)
	}
	steamBase = "http://127.0.0.1:1"
	r = runSpec(t, spec("wrong-key-value"))
	if r.Status != Down || strings.Contains(r.Error, "wrong-key-value") {
		t.Fatalf("key leaked through a url.Error: %+v", r)
	}
}

func dockerServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(dockerHandler())
	t.Cleanup(srv.Close)
	return srv
}

func dockerHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/containers/ok/json":
			fmt.Fprint(w, `{"State":{"Status":"running","Running":true}}`)
		case "/containers/healthy/json":
			fmt.Fprint(w, `{"State":{"Status":"running","Running":true,"Health":{"Status":"healthy"}}}`)
		case "/containers/starting/json":
			fmt.Fprint(w, `{"State":{"Status":"running","Running":true,"Health":{"Status":"starting"}}}`)
		case "/containers/sick/json":
			fmt.Fprint(w, `{"State":{"Status":"running","Running":true,"Health":{"Status":"unhealthy"}}}`)
		case "/containers/stopped/json":
			fmt.Fprint(w, `{"State":{"Status":"exited","Running":false}}`)
		default:
			w.WriteHeader(404)
			fmt.Fprint(w, `{"message":"No such container"}`)
		}
	})
}

func TestDockerTCP(t *testing.T) {
	srv := dockerServer(t)
	host := "tcp://" + strings.TrimPrefix(srv.URL, "http://")
	spec := func(name string) *Spec {
		return &Spec{Type: "docker", Params: map[string]string{"container": name, "docker_host": host}}
	}
	wantUp(t, runSpec(t, spec("ok")))
	wantUp(t, runSpec(t, spec("healthy")))
	r := runSpec(t, spec("starting"))
	wantUp(t, r)
	if r.Info["health"] != "starting" {
		t.Fatalf("%+v", r)
	}
	wantDown(t, runSpec(t, spec("sick")), "unhealthy")
	r = runSpec(t, spec("stopped"))
	wantDown(t, r, "not_running")
	if !strings.Contains(r.Error, "exited") {
		t.Fatalf("%+v", r)
	}
	wantDown(t, runSpec(t, spec("nope")), "not_found")
}

func TestDockerUnixSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skip("no unix sockets here: ", err)
	}
	srv := &http.Server{Handler: dockerHandler()}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	// A drive letter does not fit a unix:// URL; the path still resolves without it.
	path := filepath.ToSlash(strings.TrimPrefix(sock, filepath.VolumeName(sock)))
	wantUp(t, runSpec(t, &Spec{Type: "docker", Params: map[string]string{"container": "ok", "docker_host": "unix://" + path}}))
}

func TestDockerMissingSocketIsUnavailable(t *testing.T) {
	r := runSpec(t, &Spec{Type: "docker", Params: map[string]string{"container": "ok", "docker_host": "unix:///no/such/docker.sock"}})
	wantDown(t, r, "unavailable")
}

// fakeCmd replaces runCmd for one test.
func fakeCmd(t *testing.T, f func(name string, args []string) ([]byte, []byte, error)) {
	t.Helper()
	old := runCmd
	runCmd = func(_ context.Context, name string, args ...string) ([]byte, []byte, error) { return f(name, args) }
	t.Cleanup(func() { runCmd = old })
}

// exitErr makes a real *exec.ExitError with the given code, from this test binary.
func exitErr(t *testing.T, code int) error {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperExit")
	cmd.Env = append(os.Environ(), fmt.Sprintf("UF_HELPER_EXIT=%d", code))
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != code {
		t.Fatalf("helper: %v", err)
	}
	return err
}

func TestHelperExit(*testing.T) {
	if v := os.Getenv("UF_HELPER_EXIT"); v != "" {
		var code int
		fmt.Sscan(v, &code)
		os.Exit(code)
	}
}

func TestSystemServiceSystemd(t *testing.T) {
	run := func() Result { return checkSystemd(context.Background(), "nginx.service") }
	fakeCmd(t, func(name string, args []string) ([]byte, []byte, error) {
		if name != "systemctl" || args[0] != "is-active" || args[1] != "nginx.service" {
			t.Errorf("%s %v", name, args)
		}
		return []byte("active\n"), nil, nil
	})
	if r := run(); r.Status != "" && r.Status != Up {
		t.Fatalf("%+v", r)
	}
	fakeCmd(t, func(string, []string) ([]byte, []byte, error) { return []byte("inactive\n"), nil, exitErr(t, 3) })
	wantDown(t, run(), "not_running")
	fakeCmd(t, func(string, []string) ([]byte, []byte, error) { return nil, nil, exitErr(t, 4) })
	wantDown(t, run(), "not_found")
	fakeCmd(t, func(string, []string) ([]byte, []byte, error) {
		return nil, []byte("System has not been booted with systemd"), exitErr(t, 1)
	})
	wantDown(t, run(), "unavailable")
	fakeCmd(t, func(string, []string) ([]byte, []byte, error) { return nil, nil, exec.ErrNotFound })
	wantDown(t, run(), "unavailable")
}

// systemctl is-active says "inactive" with exit code 3 for a unit that does
// not exist as well (exit 4 only on some versions): LoadState tells them apart.
func TestSystemServiceMissingUnit(t *testing.T) {
	run := func() Result { return checkSystemd(context.Background(), "nope.service") }
	for load, want := range map[string]string{"not-found": "not_found", "loaded": "not_running"} {
		fakeCmd(t, func(name string, args []string) ([]byte, []byte, error) {
			if args[0] == "show" {
				if strings.Join(args, " ") != "show -p LoadState --value nope.service" {
					t.Errorf("%s %v", name, args)
				}
				return []byte(load + "\n"), nil, nil
			}
			return []byte("inactive\n"), nil, exitErr(t, 3)
		})
		wantDown(t, run(), want)
	}
}

func TestSystemServiceWindowsParsing(t *testing.T) {
	if !scRunning.MatchString("        STATE              : 4  RUNNING\r\n") {
		t.Error("english output")
	}
	if !scRunning.MatchString("        Состояние          : 4  RUNNING\r\n") {
		t.Error("translated label")
	}
	if scRunning.MatchString("        STATE              : 1  STOPPED\r\n") {
		t.Error("stopped")
	}
}

// The real sc.exe, on the OS where it exists.
func TestSystemServiceWindowsReal(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows only")
	}
	wantUp(t, runSpec(t, &Spec{Type: "system-service", Params: map[string]string{"service": "EventLog"}}))
	wantDown(t, runSpec(t, &Spec{Type: "system-service", Params: map[string]string{"service": "no-such-service-uf"}}), "not_found")
}

func TestPM2(t *testing.T) {
	spec := func(name string) *Spec { return &Spec{Type: "pm2", Params: map[string]string{"process": name}} }
	fakeCmd(t, func(name string, args []string) ([]byte, []byte, error) {
		if name != "pm2" || args[0] != "jlist" {
			t.Errorf("%s %v", name, args)
		}
		return []byte(`banner line
[{"name":"api","pm_id":0,"pm2_env":{"status":"online"}},
 {"name":"worker","pm_id":1,"pm2_env":{"status":"online"}},
 {"name":"worker","pm_id":2,"pm2_env":{"status":"errored"}},
 {"name":"cron","pm_id":3,"pm2_env":{"status":"stopped"}}]`), nil, nil
	})
	wantUp(t, runSpec(t, spec("api")))
	wantDown(t, runSpec(t, spec("worker")), "not_running")
	wantDown(t, runSpec(t, spec("cron")), "not_running")
	wantDown(t, runSpec(t, spec("nope")), "not_found")
	fakeCmd(t, func(string, []string) ([]byte, []byte, error) { return []byte("nonsense"), nil, nil })
	wantDown(t, runSpec(t, spec("api")), "bad_response")
	fakeCmd(t, func(string, []string) ([]byte, []byte, error) { return nil, nil, exec.ErrNotFound })
	wantDown(t, runSpec(t, spec("api")), "unavailable")
}

func TestTailscalePing(t *testing.T) {
	spec := &Spec{Type: "tailscale-ping", Params: map[string]string{"host": "node-1"}}
	fakeCmd(t, func(name string, args []string) ([]byte, []byte, error) {
		if name != "tailscale" || args[len(args)-1] != "node-1" {
			t.Errorf("%s %v", name, args)
		}
		return []byte("pong from node-1 (100.64.0.1) via DERP(fra) in 42ms\n"), nil, nil
	})
	r := runSpec(t, spec)
	wantUp(t, r)
	if r.LatencyMS != 42 {
		t.Fatalf("%+v", r)
	}
	cases := []struct{ out, reason string }{
		{"ping \"100.64.0.1\" timed out\n", "timeout"},
		{"no matching peer\n", "not_found"},
		{"100.64.0.1 is local Tailscale IP\n", "config"},
		{"failed to connect to local tailscaled; it doesn't appear to be running\n", "unavailable"},
		{"what is this\n", "bad_response"},
	}
	for _, c := range cases {
		fakeCmd(t, func(string, []string) ([]byte, []byte, error) { return nil, []byte(c.out), exitErr(t, 1) })
		wantDown(t, runSpec(t, spec), c.reason)
	}
	fakeCmd(t, func(string, []string) ([]byte, []byte, error) { return nil, nil, exec.ErrNotFound })
	wantDown(t, runSpec(t, spec), "unavailable")
}

func TestPushAndManual(t *testing.T) {
	const token = "tok-push-0001"
	defer ForgetPush(token)
	if Beat(token, true, "", 0) {
		t.Fatal("a token no check uses was accepted")
	}
	spec := func(window string) *Spec {
		p := map[string]string{"token": token}
		if window != "" {
			p["heartbeat_window"] = window
		}
		return &Spec{Type: "push", Params: p}
	}
	// Validate makes the token known: beats can arrive before the first run.
	if err := (&Spec{ID: "c", Type: "push", Params: map[string]string{"token": token}}).Validate(); err != nil {
		t.Fatal(err)
	}
	if !Beat(token, true, "ok", 12) {
		t.Fatal("known token refused")
	}
	r := runSpec(t, spec("60"))
	wantUp(t, r)
	if r.LatencyMS != 12 || r.Info["message"] != "ok" {
		t.Fatalf("%+v", r)
	}
	Beat(token, false, "disk full", 0)
	r = runSpec(t, spec("60"))
	wantDown(t, r, "push_down")
	if r.Error != "disk full" {
		t.Fatalf("%+v", r)
	}
	Beat(token, false, "", 0)
	if r = runSpec(t, spec("60")); r.Error == "" {
		t.Fatalf("%+v", r)
	}

	// Window ran out after a beat.
	Beat(token, true, "", 0)
	pushMu.Lock()
	pushBeats[token].last = time.Now().Add(-time.Hour)
	pushMu.Unlock()
	wantDown(t, runSpec(t, spec("60")), "no_heartbeat")

	// After a start: one window of grace, then no_heartbeat.
	ForgetPush(token)
	r = runSpec(t, spec("60"))
	wantUp(t, r)
	if r.Info["grace"] == "" {
		t.Fatalf("%+v", r)
	}
	pushMu.Lock()
	pushBeats[token].since = time.Now().Add(-time.Hour)
	pushMu.Unlock()
	wantDown(t, runSpec(t, spec("60")), "no_heartbeat")

	// The default window follows the interval.
	s := spec("")
	s.Interval = Duration(10 * time.Minute)
	if got := pushWindow(s, paramsOf(s)); got != 20*time.Minute {
		t.Fatalf("%s", got)
	}

	wantUp(t, runSpec(t, &Spec{Type: "manual", Params: map[string]string{"status": "up"}}))
	wantUp(t, runSpec(t, &Spec{Type: "manual"}))
	r = runSpec(t, &Spec{Type: "manual", Params: map[string]string{"status": "down", "message": "maintenance"}})
	wantDown(t, r, "manual_down")
	if r.Error != "maintenance" {
		t.Fatalf("%+v", r)
	}
	if err := (&Spec{ID: "c", Type: "manual", Params: map[string]string{"status": "maybe"}}).Validate(); err == nil {
		t.Fatal("bad status passed")
	}
}
