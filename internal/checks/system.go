package checks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// Names go to a tool as a single argument and without a shell, but a tool can
// still read a leading dash as an option, hence the first character.
var (
	serviceName = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9._@-]*$`)
	pm2Name     = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9._@:/-]*$`)
	nodeName    = regexp.MustCompile(`^[a-zA-Z0-9_:\[][a-zA-Z0-9._:\[\]-]*$`)
	dockerName  = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
)

func matches(re *regexp.Regexp, field, what string) func(params) error {
	return func(p params) error {
		if !re.MatchString(p[field]) {
			return fmt.Errorf("parameter %s is not a valid %s", field, what)
		}
		return nil
	}
}

func init() {
	registerType(TypeInfo{
		Type: "docker", Category: "system", Needs: "docker",
		Fields: []Field{
			{Name: "container", Kind: "text", Required: true},
			{Name: "docker_host", Kind: "text", Default: "unix:///var/run/docker.sock"},
			fInsecure,
		},
		target: targetParam("container"),
		validate: func(p params) error {
			if err := matches(dockerName, "container", "container name")(p); err != nil {
				return err
			}
			u, err := url.Parse(p["docker_host"])
			if err != nil || u.User != nil {
				return errors.New("parameter docker_host must look like unix:///var/run/docker.sock or tcp://host:2375")
			}
			switch u.Scheme {
			case "unix", "tcp", "http", "https":
				return nil
			}
			return errors.New("parameter docker_host must look like unix:///var/run/docker.sock or tcp://host:2375")
		},
	}, checkDocker)
	registerType(TypeInfo{
		Type: "system-service", Category: "system", Needs: "systemd",
		Fields:   []Field{{Name: "service", Kind: "text", Required: true}},
		target:   targetParam("service"),
		validate: matches(serviceName, "service", "service name"),
	}, checkSystemService)
	registerType(TypeInfo{
		Type: "pm2", Category: "system", Needs: "pm2",
		Fields:   []Field{{Name: "process", Kind: "text", Required: true}},
		target:   targetParam("process"),
		validate: matches(pm2Name, "process", "process name"),
	}, checkPM2)
	registerType(TypeInfo{
		Type: "tailscale-ping", Category: "network", Needs: "tailscale",
		Fields:   []Field{fHost},
		target:   targetParam("host"),
		validate: matches(nodeName, "host", "tailnet host"),
	}, checkTailscale)
}

// checkDocker reads the container's state from the Engine API. Its timeout and
// socket are local matters, so only a tcp daemon goes through the probe's dialer.
func checkDocker(ctx context.Context, s *Spec, env *Env) Result {
	p := paramsOf(s)
	u, err := url.Parse(p["docker_host"])
	if err != nil {
		return fail("config", errors.New("bad docker_host"))
	}
	base, dial := "", env.Dial
	switch u.Scheme {
	case "unix":
		base = "http://docker"
		socket := u.Path
		dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}
	case "tcp":
		base = "http://" + u.Host
	default:
		base = u.Scheme + "://" + u.Host
	}
	client := newHTTPClient(&Env{Dial: dial}, p.bool("insecure"))
	defer client.CloseIdleConnections()
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/containers/"+url.PathEscape(p["container"])+"/json", nil)
	if err != nil {
		return fail("config", plainErr(err))
	}
	resp, err := client.Do(req)
	if err != nil {
		err = plainErr(err)
		// No socket or no right to it is this probe's setup, not the container's state.
		if u.Scheme == "unix" && ctx.Err() == nil {
			return fail("unavailable", fmt.Errorf("cannot reach Docker at %s, mount the socket and allow the agent to read it (%v)", u.Path, err))
		}
		return fail(reasonOf(ctx, err), err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fail(reasonOf(ctx, err), err)
	}
	r := Result{LatencyMS: ms(time.Since(start))}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		r.Status, r.Reason, r.Error = Down, "not_found", "no container named "+p["container"]
		return r
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		r.Status, r.Reason, r.Error = Down, "auth", fmt.Sprintf("Docker refused access (HTTP %d)", resp.StatusCode)
		return r
	case resp.StatusCode != http.StatusOK:
		r.Status, r.Reason, r.Error = Down, "http_status", fmt.Sprintf("HTTP %d", resp.StatusCode)
		return r
	}
	var c struct {
		State struct {
			Status  string `json:"Status"`
			Running bool   `json:"Running"`
			Health  *struct {
				Status string `json:"Status"`
			} `json:"Health"`
		} `json:"State"`
	}
	if err := json.Unmarshal(body, &c); err != nil {
		r.Status, r.Reason, r.Error = Down, "bad_response", "Docker answered with something that is not a container"
		return r
	}
	r.Info = map[string]string{"state": c.State.Status}
	if !c.State.Running {
		r.Status, r.Reason, r.Error = Down, "not_running", "container is "+c.State.Status
		return r
	}
	if c.State.Health != nil {
		r.Info["health"] = c.State.Health.Status
		// "starting" stays up: it resolves by itself into healthy or unhealthy,
		// and calling every restart an outage would be a false alarm.
		if c.State.Health.Status == "unhealthy" {
			r.Status, r.Reason, r.Error = Down, "unhealthy", "container health check fails"
		}
	}
	return r
}

func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

func checkSystemService(ctx context.Context, s *Spec, env *Env) Result {
	name := paramsOf(s)["service"]
	if !serviceName.MatchString(name) {
		return fail("config", errors.New("bad service name"))
	}
	if runtime.GOOS == "windows" {
		return checkWindowsService(ctx, name)
	}
	return checkSystemd(ctx, name)
}

func checkSystemd(ctx context.Context, name string) Result {
	stdout, stderr, err := runCmd(ctx, "systemctl", "is-active", name)
	if err == nil {
		return Result{Info: map[string]string{"state": "active"}}
	}
	if errors.Is(err, exec.ErrNotFound) || ctx.Err() != nil {
		return failCmd(ctx, "systemctl", stdout, stderr, err)
	}
	out := cmdOutput(stdout, stderr, err)
	switch {
	case strings.Contains(out, "Failed to connect to bus") || strings.Contains(out, "not been booted"):
		return fail("unavailable", errors.New("systemd is not reachable from this probe: "+out))
	case exitCode(err) == 4 || unitMissing(ctx, name):
		return fail("not_found", errors.New("no service named "+name))
	}
	return fail("not_running", fmt.Errorf("service %s is %s", name, out))
}

// unitMissing asks systemd whether a unit exists: is-active answers
// "inactive" with exit code 3 for a missing unit too on most versions.
func unitMissing(ctx context.Context, name string) bool {
	stdout, _, err := runCmd(ctx, "systemctl", "show", "-p", "LoadState", "--value", name)
	return err == nil && strings.TrimSpace(string(stdout)) == "not-found"
}

// STATE is translated on some Windows versions, its value is not.
var scRunning = regexp.MustCompile(`:\s*4\s+RUNNING`)

func checkWindowsService(ctx context.Context, name string) Result {
	stdout, stderr, err := runCmd(ctx, "sc", "query", name)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || ctx.Err() != nil {
			return failCmd(ctx, "sc", stdout, stderr, err)
		}
		if exitCode(err) == 1060 {
			return fail("not_found", errors.New("no service named "+name))
		}
		return fail("error", fmt.Errorf("sc: exit code %d", exitCode(err)))
	}
	if !scRunning.Match(stdout) {
		return fail("not_running", fmt.Errorf("service %s is not running", name))
	}
	return Result{Info: map[string]string{"state": "running"}}
}

func checkPM2(ctx context.Context, s *Spec, env *Env) Result {
	name := paramsOf(s)["process"]
	stdout, stderr, err := runCmd(ctx, "pm2", "jlist")
	if err != nil {
		return failCmd(ctx, "pm2", stdout, stderr, err)
	}
	// pm2 can print its banner to stdout before the list on the first run.
	if i := bytes.IndexByte(stdout, '['); i > 0 {
		stdout = stdout[i:]
	}
	var list []struct {
		Name string `json:"name"`
		Env  struct {
			Status string `json:"status"`
		} `json:"pm2_env"`
	}
	if err := json.Unmarshal(stdout, &list); err != nil {
		return fail("bad_response", errors.New("pm2 jlist did not print a process list"))
	}
	found := false
	bad := ""
	for _, proc := range list {
		if proc.Name != name {
			continue
		}
		found = true
		if proc.Env.Status != "online" {
			bad = proc.Env.Status // cluster mode has several; one down instance counts
		}
	}
	switch {
	case !found:
		return fail("not_found", errors.New("no pm2 process named "+name))
	case bad != "":
		return fail("not_running", fmt.Errorf("process %s is %s", name, bad))
	}
	return Result{Info: map[string]string{"state": "online"}}
}

var tailscaleLatency = regexp.MustCompile(` in ([0-9.]+)(µs|us|ms|s)\b`)

func checkTailscale(ctx context.Context, s *Spec, env *Env) Result {
	host := paramsOf(s)["host"]
	if !nodeName.MatchString(host) {
		return fail("config", errors.New("bad tailnet host"))
	}
	stdout, stderr, err := runCmd(ctx, "tailscale", "ping", "--c", "1", host)
	if errors.Is(err, exec.ErrNotFound) {
		return failCmd(ctx, "tailscale", stdout, stderr, err)
	}
	if ctx.Err() != nil {
		return fail("timeout", errors.New("tailscale ping did not finish in time"))
	}
	return tailscaleResult(string(stdout) + "\n" + string(stderr))
}

func tailscaleResult(output string) Result {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.Contains(line, "pong from"):
			r := Result{Info: map[string]string{"pong": clip(line, 120)}}
			if m := tailscaleLatency.FindStringSubmatch(line); m != nil {
				if d, err := time.ParseDuration(m[1] + strings.Replace(m[2], "µs", "us", 1)); err == nil {
					r.LatencyMS = ms(d)
				}
			}
			return r
		case strings.Contains(line, "timed out"):
			return fail("timeout", errors.New(clip(line, 200)))
		case strings.Contains(line, "no matching peer"):
			return fail("not_found", errors.New("no such peer, or ACLs hide it: "+clip(line, 160)))
		case strings.Contains(line, "is local Tailscale IP"):
			return fail("config", errors.New("this is the probe's own address: "+clip(line, 160)))
		case strings.Contains(line, "tailscaled") || strings.Contains(line, "Tailscale is stopped") || strings.Contains(line, "doesn't appear to be running"):
			return fail("unavailable", errors.New(clip(line, 200)))
		}
	}
	return fail("bad_response", errors.New("unexpected tailscale output: "+clip(strings.TrimSpace(output), 200)))
}
