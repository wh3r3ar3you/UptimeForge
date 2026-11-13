// Package agent connects a probe to the hub, runs the checks it is assigned
// and streams results back. Checks keep running while the hub is unreachable;
// results are buffered and delivered after reconnecting.
package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/wh3r3ar3you/uptimeforge/internal/checks"
	"github.com/wh3r3ar3you/uptimeforge/internal/proto"
	"github.com/wh3r3ar3you/uptimeforge/internal/runner"
	"github.com/wh3r3ar3you/uptimeforge/internal/sysmon"
	"github.com/wh3r3ar3you/uptimeforge/internal/update"
	"github.com/wh3r3ar3you/uptimeforge/internal/version"
)

const (
	bufferLimit = 5000
	batchEvery  = time.Second
	pingEvery   = 20 * time.Second
	// maxMessage keeps each results message well under the hub's WebSocket
	// read limit: a backlog after an outage goes out in several.
	maxMessage = 1 << 20
)

type Config struct {
	HubURL          string // https://hub.example.com or wss://hub.example.com/api/agent/ws
	Token           string
	MetricsInterval time.Duration // host metrics period; 0 = 15s, <0 = off
	NICs            []string      // interfaces counted as traffic; empty = auto
	// MinLive is the floor for a live command's interval; 0 = 500ms. A test
	// hook: production leaves it unset.
	MinLive time.Duration
	// SourceIP is the local address the hub connection leaves from. Empty
	// means auto: the kernel's choice first and, when the hub cannot be
	// reached, each public address of the host in turn (VPN servers often
	// find their main address filtered on the way to the hub). An IP pins
	// it; "off" keeps the kernel's choice. Checks are not affected.
	SourceIP string
	// Insecure allows a hub reached over plain http/ws beyond loopback.
	Insecure bool
	// DenyTypes are check types this probe refuses to run, whatever the hub
	// assigns (e.g. docker, real-browser, system-service).
	DenyTypes []string
	// StateDir keeps the token, installed updates and the connected marker.
	StateDir string
	// Launcher is set when the agent runs under `uptimeforge-agent launch`
	// (UF_LAUNCHER=1): only then does it install updates the hub pushes and
	// mark its version connected.
	Launcher bool
	// LauncherBuiltin is the launcher's own version (UF_LAUNCHER_BUILTIN).
	// The launcher selects only installed releases newer than it, so the
	// agent refuses any other update: installing it would restart into the
	// same binary over and over.
	LauncherBuiltin string
	// UpdateKeys verify updates; nil means update.ReleaseKeys. A test hook.
	UpdateKeys []ed25519.PublicKey
}

// ErrRestart is returned by Run after an update was installed: the process
// exits with update.ExitRestart and the launcher starts the new version.
var ErrRestart = errors.New("update installed, restart into it")

type Agent struct {
	cfg    Config
	log    *slog.Logger
	runner *runner.Runner
	sys    *sysmon.Collector
	// metricsOff is set when the hub's assignment says Metrics=false: no
	// metrics or sysinfo are sent (nor collected) until it says otherwise.
	metricsOff atomic.Bool

	mu      sync.Mutex
	pending []checks.Result
	metrics []sample
	// Live window (see setLive); liveSet wakes the metrics loop to re-arm.
	liveEvery time.Duration
	liveUntil time.Time
	liveSet   chan struct{}
	notify    chan struct{}
	out       chan []byte // immediate messages (run results)
	runs      chan struct{}

	// Auto source address: candidates ("" is the kernel's choice) and the
	// one in use. Only the Run goroutine touches them.
	srcs  []string
	srcAt int
	srcOK string // last address the hub was reached from, for the log
	local func() []string

	updating    atomic.Bool   // an update is being installed (or was, and the restart is due)
	marked      atomic.Bool   // connected marker written
	restart     chan struct{} // closed once an update is installed
	restartOnce sync.Once
}

type sample struct {
	m  proto.Metrics
	at time.Time
}

func New(ctx context.Context, cfg Config, log *slog.Logger) *Agent {
	if cfg.MetricsInterval == 0 {
		cfg.MetricsInterval = 15 * time.Second
	}
	a := &Agent{cfg: cfg, log: log, notify: make(chan struct{}, 1), liveSet: make(chan struct{}, 1), out: make(chan []byte, 16), runs: make(chan struct{}, maxRuns), restart: make(chan struct{})}
	a.runner = runner.New(ctx, log, a.enqueue)
	a.runner.Deny(cfg.DenyTypes)
	if cfg.MetricsInterval > 0 {
		a.sys = sysmon.New(cfg.NICs)
	}
	return a
}

// WSURL normalises a hub address into the agent WebSocket endpoint.
func WSURL(hub string) string {
	u := strings.TrimRight(hub, "/")
	switch {
	case strings.HasPrefix(u, "https://"):
		u = "wss://" + strings.TrimPrefix(u, "https://")
	case strings.HasPrefix(u, "http://"):
		u = "ws://" + strings.TrimPrefix(u, "http://")
	case !strings.HasPrefix(u, "ws://") && !strings.HasPrefix(u, "wss://"):
		u = "wss://" + u
	}
	if !strings.HasSuffix(u, "/api/agent/ws") {
		u += "/api/agent/ws"
	}
	return u
}

// checkHubURL refuses a hub reached in clear text: the token and every
// assignment (VPN links, passwords) cross that connection, and a hub
// impersonated on the way can make the agent run whatever it likes. Loopback
// is fine (tests, the demo, a hub on the same host).
func checkHubURL(hub string, insecure bool) error {
	u, err := url.Parse(WSURL(hub))
	if err != nil {
		return fmt.Errorf("bad hub address: %w", plainURLErr(err))
	}
	if u.Scheme == "wss" || insecure {
		return nil
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || ip != nil && (ip.IsLoopback() || ip.IsUnspecified()) {
		return nil
	}
	return fmt.Errorf("the hub address %s://%s is not encrypted: the probe token and every check with its links and passwords "+
		"would cross the network in clear text. Use https://, or set UF_INSECURE=1 (--insecure) to allow it", u.Scheme, u.Host)
}

// plainURLErr drops the URL that url.Parse quotes in its error.
func plainURLErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func (a *Agent) enqueue(r checks.Result) {
	a.mu.Lock()
	a.pending = append(a.pending, r)
	if over := len(a.pending) - bufferLimit; over > 0 {
		a.pending = a.pending[over:] // keep the newest
	}
	a.mu.Unlock()
	select {
	case a.notify <- struct{}{}:
	default:
	}
}

// collect samples host metrics on its own schedule; like results they are
// buffered while the hub is unreachable.
//
// One timer serves both rates: a live command re-arms it, and after each
// sample it is re-armed for the live interval while the live window lasts,
// for the normal one otherwise. Nothing is started per command, so switching
// leaks no goroutine or ticker.
func (a *Agent) collect(ctx context.Context) {
	t := time.NewTimer(a.cfg.MetricsInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.liveSet:
			// Go 1.23+ timers: Reset discards a pending tick.
			t.Reset(a.nextMetricsIn(time.Now()))
			continue
		case <-t.C:
		}
		if !a.metricsOff.Load() {
			m := a.sys.Collect(ctx)
			now := time.Now()
			a.mu.Lock()
			a.metrics = append(a.metrics, sample{m, now})
			if over := len(a.metrics) - 240; over > 0 {
				a.metrics = a.metrics[over:]
			}
			live := now.Before(a.liveUntil)
			a.mu.Unlock()
			if live { // flush now instead of at the next batch tick
				select {
				case a.notify <- struct{}{}:
				default:
				}
			}
		}
		t.Reset(a.nextMetricsIn(time.Now()))
	}
}

// nextMetricsIn is the wait before the next sample: the live interval when
// the next sample still falls inside the live window, else the normal one.
func (a *Agent) nextMetricsIn(now time.Time) time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.liveEvery > 0 && !a.metricsOff.Load() && !now.Add(a.liveEvery).After(a.liveUntil) {
		return a.liveEvery
	}
	return a.cfg.MetricsInterval
}

// minLive is the shortest live interval accepted in production.
const minLive = 500 * time.Millisecond

// setLive starts or extends a live window. Ignored when the hub switched
// metrics off or this agent collects none.
func (a *Agent) setLive(l proto.Live) {
	if a.sys == nil || a.metricsOff.Load() || l.IntervalMS <= 0 || l.TTLMS <= 0 {
		return
	}
	floor := minLive
	if a.cfg.MinLive > 0 {
		floor = a.cfg.MinLive
	}
	every := max(time.Duration(l.IntervalMS)*time.Millisecond, floor)
	a.mu.Lock()
	a.liveEvery = every
	a.liveUntil = time.Now().Add(time.Duration(l.TTLMS) * time.Millisecond)
	a.mu.Unlock()
	select {
	case a.liveSet <- struct{}{}:
	default:
	}
}

func (a *Agent) liveState() (every time.Duration, until time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.liveEvery, a.liveUntil
}

// Run keeps a connection to the hub until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	defer a.runner.Stop()
	if err := checkHubURL(a.cfg.HubURL, a.cfg.Insecure); err != nil {
		return err
	}
	if a.sys != nil {
		go a.collect(ctx)
	}
	backoff := time.Second
	for {
		start := time.Now()
		err := a.session(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, ErrRestart) {
			return ErrRestart
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		wait := backoff + time.Duration(rand.Int64N(int64(backoff/2)+1))
		if errors.Is(err, errDial) && a.nextSource() {
			wait = time.Second // an untried address: no reason to wait long
		}
		a.log.Warn("hub connection lost", "err", err, "retry_in", wait.Round(time.Second))
		select {
		case <-ctx.Done():
			return nil
		case <-a.restart: // installed while the hub was away
			return ErrRestart
		case <-time.After(wait):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (a *Agent) session(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	dialCtx, dialCancel := context.WithTimeout(ctx, 15*time.Second)
	defer dialCancel()
	client := a.hubClient() // also fetches updates: same source address
	conn, resp, err := websocket.Dial(dialCtx, WSURL(a.cfg.HubURL), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + a.cfg.Token}},
		HTTPClient: client,
	})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("hub rejected the token")
		}
		return fmt.Errorf("%w from %s: %w", errDial, sourceName(a.source()), err)
	}
	if src := a.source(); src != a.srcOK {
		a.srcOK = src
		a.log.Info("hub reached", "from", sourceName(src))
	}
	defer conn.CloseNow()
	conn.SetReadLimit(16 << 20)

	host, _ := os.Hostname()
	hello, _ := proto.Encode(proto.TypeHello, proto.Hello{
		Proto: proto.Version, Version: version.Version, Hostname: host, OS: runtime.GOOS, Arch: runtime.GOARCH,
		Launcher: a.cfg.Launcher,
	})
	if err := conn.Write(ctx, websocket.MessageText, hello); err != nil {
		return err
	}
	a.log.Info("connected to hub", "url", WSURL(a.cfg.HubURL))

	rerr, werr := make(chan error, 1), make(chan error, 1)
	go func() { rerr <- a.readLoop(ctx, conn, client) }()
	go func() { werr <- a.writeLoop(ctx, conn) }()
	select {
	case err = <-rerr:
	case err = <-werr:
	case <-a.restart:
		// writeLoop sends what is pending, then returns; a write stuck
		// behind a slow link does not hold the restart up for long.
		select {
		case <-werr:
		case <-time.After(restartFlush + time.Second):
		}
		err = ErrRestart
	}
	conn.Close(websocket.StatusNormalClosure, "")
	return err
}

// restartFlush bounds the last flush before an update restart.
const restartFlush = 2 * time.Second

// sendSysInfo describes the host to the hub. It waits for the first
// assignment of a session, which may say the hub does not want host data.
func (a *Agent) sendSysInfo(ctx context.Context, conn *websocket.Conn) error {
	info, _ := proto.Encode(proto.TypeSysInfo, a.sys.Info(ctx))
	return conn.Write(ctx, websocket.MessageText, info)
}

// readLoop handles the hub's messages; client (nil: Go's default) reaches the
// hub from the session's source address.
func (a *Agent) readLoop(ctx context.Context, conn *websocket.Conn, client *http.Client) error {
	sysSent := false
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var env proto.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			return fmt.Errorf("bad message from hub: %w", err)
		}
		switch env.Type {
		case proto.TypeAssign:
			var as proto.Assign
			if err := json.Unmarshal(env.Data, &as); err != nil {
				return fmt.Errorf("bad assignment: %w", err)
			}
			if err := a.runner.Apply(as); err != nil {
				a.log.Error("upstream proxy for this probe is broken", "err", err)
			}
			a.markConnected()
			off := as.Metrics != nil && !*as.Metrics
			if was := a.metricsOff.Swap(off); was != off {
				a.log.Info("host metrics switched by the hub", "off", off)
			}
			if off {
				a.mu.Lock()
				a.metrics = nil
				a.mu.Unlock()
			} else if a.sys != nil && !sysSent {
				sysSent = true
				if err := a.sendSysInfo(ctx, conn); err != nil {
					return err
				}
			}
			a.log.Info("assignment received", "probe", as.Probe, "checks", len(as.Checks))
		case proto.TypeLive:
			var l proto.Live
			if json.Unmarshal(env.Data, &l) == nil {
				a.setLive(l)
			}
		case proto.TypeRun:
			var run proto.Run
			if err := json.Unmarshal(env.Data, &run); err != nil {
				continue
			}
			a.startRun(ctx, run)
		case proto.TypeUpdate:
			var u proto.Update
			if json.Unmarshal(env.Data, &u) == nil {
				a.startUpdate(ctx, u.Version, client)
			}
		}
	}
}

// markConnected tells the launcher this version reached the hub and got
// work, which keeps it from being rolled back. Once per process.
func (a *Agent) markConnected() {
	if !a.cfg.Launcher || a.marked.Load() {
		return
	}
	if err := update.MarkConnected(a.cfg.StateDir, version.Version); err != nil {
		a.log.Warn("cannot mark this version connected: the launcher may roll it back", "err", err)
		return
	}
	a.marked.Store(true)
}

// startUpdate installs release v in the background. On success the agent
// restarts into it (Run returns ErrRestart) and tells the hub nothing; on
// failure it reports why and keeps running. Only one update runs at a time,
// and the version already running is not installed again.
func (a *Agent) startUpdate(ctx context.Context, v string, client *http.Client) {
	if v == version.Version {
		return
	}
	switch {
	case !a.cfg.Launcher:
		a.updateFailed(ctx, v, errors.New("not running under the launcher"))
		return
	case !update.IsRelease(a.cfg.LauncherBuiltin):
		a.updateFailed(ctx, v, errors.New("development build cannot update"))
		return
	case update.Compare(v, a.cfg.LauncherBuiltin) <= 0:
		a.updateFailed(ctx, v, fmt.Errorf("version not newer than the installed agent %s", a.cfg.LauncherBuiltin))
		return
	}
	if !a.updating.CompareAndSwap(false, true) {
		return
	}
	a.log.Info("installing an update", "version", v)
	go func() {
		keys := a.cfg.UpdateKeys
		if keys == nil {
			keys = update.ReleaseKeys
		}
		ictx, cancel := context.WithTimeout(ctx, 15*time.Minute)
		defer cancel()
		err := update.Install(ictx, a.cfg.StateDir, v, runtime.GOOS, update.ReleaseArch(), a.fetcher(ictx, client), keys)
		if err != nil {
			a.updating.Store(false)
			a.log.Warn("update failed", "version", v, "err", err)
			a.updateFailed(ctx, v, err)
			return
		}
		a.log.Info("update installed, restarting into it", "version", v)
		a.restartOnce.Do(func() { close(a.restart) })
	}()
}

func (a *Agent) updateFailed(ctx context.Context, v string, err error) {
	msg, _ := proto.Encode(proto.TypeUpdateResult, proto.UpdateResult{Version: v, Error: err.Error()})
	select {
	case a.out <- msg:
	case <-ctx.Done():
	}
}

// fetcher downloads release files from the hub's public /download/ (no
// credentials), capped in size. Errors carry no URL: they go to the hub UI.
func (a *Agent) fetcher(ctx context.Context, client *http.Client) func(string) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	return func(name string) ([]byte, error) {
		limit := int64(256 << 20)
		if strings.HasPrefix(name, "checksums.txt") {
			limit = 1 << 20
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, DownloadURL(a.cfg.HubURL, name), nil)
		if err != nil {
			return nil, plainURLErr(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, plainURLErr(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		if err != nil {
			return nil, plainURLErr(err)
		}
		if int64(len(b)) > limit {
			return nil, fmt.Errorf("larger than %d bytes", limit)
		}
		return b, nil
	}
}

// DownloadURL is a file under the hub's /download/, from the same hub
// address the WebSocket URL is derived from.
func DownloadURL(hub, name string) string {
	return strings.TrimSuffix(JoinURL(hub), "/api/agent/join") + "/download/" + name
}

// maxRuns caps checks run on demand at once: each can start an xray instance.
const maxRuns = 4

// startRun runs a check on demand from the UI and sends its result. Over
// maxRuns at once the run is refused at once instead of queued.
func (a *Agent) startRun(ctx context.Context, run proto.Run) {
	select {
	case a.runs <- struct{}{}:
	default:
		a.reply(ctx, run.ID, checks.Result{CheckID: run.Spec.ID, Status: checks.Down, Reason: "unavailable",
			Error: fmt.Sprintf("this probe is already running %d checks on demand, try again shortly", maxRuns), At: time.Now()})
		return
	}
	go func() {
		defer func() { <-a.runs }()
		a.reply(ctx, run.ID, a.runner.RunOnce(ctx, run.Spec))
	}()
}

func (a *Agent) reply(ctx context.Context, id string, res checks.Result) {
	if msg, err := proto.Encode(proto.TypeRunResult, proto.RunResult{ID: id, Result: res}); err == nil {
		select {
		case a.out <- msg:
		case <-ctx.Done():
		}
	}
}

func (a *Agent) writeLoop(ctx context.Context, conn *websocket.Conn) error {
	ping := time.NewTicker(pingEvery)
	defer ping.Stop()
	flush := time.NewTicker(batchEvery)
	defer flush.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				return fmt.Errorf("ping: %w", err)
			}
		case msg := <-a.out:
			wctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := conn.Write(wctx, websocket.MessageText, msg)
			cancel()
			if err != nil {
				return err
			}
			continue
		case <-flush.C:
		case <-a.notify:
		case <-a.restart:
			// Results waiting for the next tick go out before the restart
			// closes the connection; what does not make it stays lost
			// with the process, as it would on any stop.
			fctx, cancel := context.WithTimeout(ctx, restartFlush)
			defer cancel()
			a.flush(func(msg []byte) error { return conn.Write(fctx, websocket.MessageText, msg) })
			return ErrRestart
		}
		if err := a.flush(func(msg []byte) error {
			wctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			return conn.Write(wctx, websocket.MessageText, msg)
		}); err != nil {
			return err
		}
	}
}

func (a *Agent) flush(write func([]byte) error) error {
	a.mu.Lock()
	batch, metrics := a.pending, a.metrics
	a.pending, a.metrics = nil, nil
	a.mu.Unlock()
	if a.metricsOff.Load() {
		metrics = nil
	}
	for i, sm := range metrics {
		sm.m.Age = time.Since(sm.at).Seconds()
		msg, _ := proto.Encode(proto.TypeMetrics, sm.m)
		err := write(msg)
		if err != nil {
			a.mu.Lock()
			a.metrics = append(metrics[i:], a.metrics...)
			a.pending = append(batch, a.pending...)
			a.mu.Unlock()
			return err
		}
	}
	if len(batch) == 0 {
		return nil
	}
	now := time.Now()
	for i := range batch {
		batch[i].Age = now.Sub(batch[i].At).Seconds()
	}
	for len(batch) > 0 {
		msg, n := encodeResults(batch)
		if msg == nil {
			batch = batch[n:]
			continue
		}
		if err := write(msg); err != nil {
			a.mu.Lock()
			a.pending = append(batch, a.pending...) // put back for the next session
			a.mu.Unlock()
			return err
		}
		batch = batch[n:]
	}
	return nil
}

// encodeResults encodes as many results from the front of batch as fit in
// maxMessage (at least one) and says how many it took.
func encodeResults(batch []checks.Result) ([]byte, int) {
	n, size := 0, 64 // the envelope
	for n < len(batch) {
		b, _ := json.Marshal(batch[n])
		if n > 0 && size+len(b)+1 > maxMessage {
			break
		}
		size += len(b) + 1
		n++
	}
	for {
		msg, err := proto.Encode(proto.TypeResults, proto.Results{Results: batch[:n]})
		switch {
		case err == nil && (len(msg) <= maxMessage || n == 1):
			return msg, n
		case err != nil && n == 1:
			return nil, 1 // cannot be encoded (NaN): drop it rather than block the rest
		}
		n = max(1, n/2) // the estimate was off, or a result in there cannot be encoded
	}
}

// errDial marks a hub connection that never got established, as opposed to
// one that was up and dropped: only the first says the address may be filtered.
var errDial = errors.New("cannot reach the hub")

// source is the local address the next hub connection leaves from; "" is
// the kernel's choice.
func (a *Agent) source() string {
	switch a.cfg.SourceIP {
	case "", "auto":
		if a.srcAt < len(a.srcs) {
			return a.srcs[a.srcAt]
		}
		return ""
	case "off":
		return ""
	}
	return a.cfg.SourceIP
}

func sourceName(src string) string {
	if src == "" {
		return "default address"
	}
	return src
}

// nextSource moves an auto agent to the next local address after a failed
// dial. It reports whether that address has not been tried in this round;
// after the last one it starts over from the kernel's choice.
func (a *Agent) nextSource() bool {
	if a.cfg.SourceIP != "" && a.cfg.SourceIP != "auto" {
		return false
	}
	if a.srcAt+1 < len(a.srcs) {
		a.srcAt++
		return true
	}
	first := len(a.srcs) == 0
	local := localAddrs
	if a.local != nil {
		local = a.local
	}
	a.srcs = append([]string{""}, local()...) // addresses may have changed since the last round
	a.srcAt = 0
	if first && len(a.srcs) > 1 {
		a.srcAt = 1 // the default just failed: go on to the next one
		return true
	}
	return false // a full round failed: back to the default, at the normal pace
}

// localAddrs lists the host's public addresses: private, loopback and
// link-local ones cannot reach a hub on the internet.
func localAddrs() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, ad := range addrs {
		n, ok := ad.(*net.IPNet)
		if !ok || !n.IP.IsGlobalUnicast() || n.IP.IsPrivate() {
			continue
		}
		out = append(out, n.IP.String())
	}
	return out
}

// hubClient dials the hub from the chosen source address; nil keeps Go's
// default client (which honours HTTPS_PROXY).
func (a *Agent) hubClient() *http.Client {
	src := a.source()
	if src == "" {
		return nil
	}
	d := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(src)}, Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Client{Transport: &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         d.DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		ForceAttemptHTTP2:   false,
	}}
}
