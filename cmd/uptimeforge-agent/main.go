// Command uptimeforge-agent is a probe: it runs the checks the hub assigns
// from the network it is installed in.
//
//	uptimeforge-agent --hub https://hub.example.com --token <token>
//	uptimeforge-agent check 'vless://...'        # one-off check, prints the result
//	uptimeforge-agent launch --hub ... --token ...  # the agent as an updatable child (signed updates, rollback)
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/agent"
	"github.com/wh3r3ar3you/uptimeforge/internal/checks"
	"github.com/wh3r3ar3you/uptimeforge/internal/update"
	"github.com/wh3r3ar3you/uptimeforge/internal/version"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "check":
			os.Exit(oneOff(os.Args[2:]))
		case "version":
			fmt.Println(version.Version)
			return
		case "launch":
			os.Exit(launch(os.Args[2:]))
		}
	}

	f := agentFlags(flag.CommandLine)
	flag.Parse()
	hubURL, token, metrics, nics, src, insecure, join, stateDir, deny := f.hub, f.token, f.metrics, f.nics, f.src, f.insecure, f.join, f.stateDir, f.deny

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *hubURL == "" || (*token == "" && *join == "" && !hasSavedToken(*stateDir)) {
		fmt.Fprintln(os.Stderr, "usage: uptimeforge-agent --hub https://hub.example.com --token TOKEN")
		fmt.Fprintln(os.Stderr, "       uptimeforge-agent --hub https://hub.example.com --join INVITE-CODE [--state-dir DIR]")
		fmt.Fprintln(os.Stderr, "       uptimeforge-agent check <vless://…|https://…|host:port> [--via socks5://…]")
		fmt.Fprintln(os.Stderr, "       uptimeforge-agent launch [the flags above]   # runs the agent updatable by the hub")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Info("UptimeForge agent", "version", version.Version)
	if *src != "" && *src != "auto" && *src != "off" && net.ParseIP(*src) == nil {
		fmt.Fprintln(os.Stderr, "--source-ip must be an IP address, auto or off")
		os.Exit(2)
	}
	if *token == "" { // an explicit token wins over the invite code
		if err := agent.CheckHubURL(*hubURL, *insecure); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		tok, err := agent.Enroll(ctx, *hubURL, *join, *stateDir)
		if err != nil {
			log.Error("cannot join the hub", "err", err)
			os.Exit(1)
		}
		*token = tok
	}
	cfg := agent.Config{HubURL: *hubURL, Token: *token, MetricsInterval: *metrics, SourceIP: *src, Insecure: *insecure,
		StateDir: *stateDir, Launcher: os.Getenv("UF_LAUNCHER") == "1"}
	if *metrics == 0 {
		cfg.MetricsInterval = -1
	}
	if *nics != "" {
		cfg.NICs = strings.Split(*nics, ",")
	}
	if *deny != "" {
		cfg.DenyTypes = strings.Split(*deny, ",")
		log.Info("check types disabled on this probe", "types", *deny)
	}
	if err := agent.New(ctx, cfg, log).Run(ctx); err != nil {
		if errors.Is(err, agent.ErrRestart) {
			log.Info("restarting into the installed update")
			os.Exit(update.ExitRestart)
		}
		log.Error("agent stopped", "err", err)
		os.Exit(1)
	}
}

// flags are the agent's command line, shared by the agent and the launcher
// (which reads --state-dir from it).
type flags struct {
	hub, token, nics, src, join, stateDir, deny *string
	metrics                                     *time.Duration
	insecure                                    *bool
}

func agentFlags(fs *flag.FlagSet) flags {
	return flags{
		hub:      fs.String("hub", os.Getenv("UF_HUB"), "hub address, e.g. https://hub.example.com (env UF_HUB)"),
		token:    fs.String("token", os.Getenv("UF_TOKEN"), "probe token shown by the hub (env UF_TOKEN)"),
		metrics:  fs.Duration("metrics", envDuration("UF_METRICS_INTERVAL", 15*time.Second), "host metrics interval, 0 to disable (env UF_METRICS_INTERVAL)"),
		nics:     fs.String("nic", os.Getenv("UF_NIC"), "comma-separated interfaces counted as traffic; default: all physical (env UF_NIC)"),
		src:      fs.String("source-ip", os.Getenv("UF_SOURCE_IP"), "local address to reach the hub from; empty tries the host's public addresses in turn when the hub is unreachable, \"off\" never does (env UF_SOURCE_IP)"),
		insecure: fs.Bool("insecure", os.Getenv("UF_INSECURE") == "1", "allow a hub over plain http/ws beyond loopback: the token and checks then cross the network in clear text (env UF_INSECURE=1)"),
		join:     fs.String("join", os.Getenv("UF_JOIN"), "one-time invite code from the hub: exchanged for a token that is saved in --state-dir (env UF_JOIN)"),
		stateDir: fs.String("state-dir", envOr("UF_STATE_DIR", agent.DefaultStateDir), "where the token obtained with --join, installed updates and their state are kept (env UF_STATE_DIR)"),
		deny:     fs.String("deny-types", os.Getenv("UF_DENY_TYPES"), "comma-separated check types this probe refuses to run whatever the hub assigns, e.g. docker,real-browser,system-service (env UF_DENY_TYPES)"),
	}
}

// launch runs the agent as a child that can be updated and rolled back:
//
//	uptimeforge-agent launch [agent flags...]
//
// The child gets the same flags; the state dir is read from them as the
// agent reads it. Flags the agent would refuse are refused here at once
// rather than in a restart loop.
func launch(args []string) int {
	fs := flag.NewFlagSet("launch", flag.ContinueOnError)
	f := agentFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2 // the flag package printed why
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(log)
	self, err := os.Executable()
	if err != nil {
		log.Error("cannot find the agent binary", "err", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Info("UptimeForge agent launcher", "version", version.Version, "state_dir", *f.stateDir)
	if err := update.Launch(ctx, *f.stateDir, self, args); err != nil {
		log.Error("launcher stopped", "err", err)
		return 1
	}
	return 0
}

// oneOff runs a single check from the command line: handy to test a config
// or a fresh probe location before wiring it into the hub.
func oneOff(args []string) int {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	via := fs.String("via", "", "run through an upstream proxy link (socks5://, vless://, ...)")
	timeout := fs.Duration("timeout", 25*time.Second, "check timeout")
	asJSON := fs.Bool("json", false, "print the raw result as JSON")
	var target string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		target, args = args[0], args[1:]
	}
	fs.Parse(args)
	if target == "" && fs.NArg() > 0 {
		target = fs.Arg(0)
	}
	if target == "" {
		fmt.Fprintln(os.Stderr, "usage: uptimeforge-agent check <vless://…|https://…|host:port> [--via link] [--json]")
		return 2
	}

	spec := checks.Spec{ID: "cli", Timeout: checks.Duration(*timeout)}
	scheme, _, hasScheme := strings.Cut(target, "://")
	switch {
	case checks.IsMTProxyLink(target):
		spec.Type, spec.MTProto = "mtproto", &checks.MTProtoParams{Link: target}
	case hasScheme && (scheme == "http" || scheme == "https"):
		spec.Type, spec.HTTP = "http", &checks.HTTPParams{URL: target}
	case hasScheme:
		spec.Type, spec.Xray = "xray", &checks.XrayParams{Link: target}
	default:
		if _, _, err := net.SplitHostPort(target); err != nil {
			fmt.Fprintln(os.Stderr, "target must be a share link, an http(s) URL or host:port")
			return 2
		}
		spec.Type, spec.TCP = "tcp", &checks.TCPParams{Address: target}
	}

	env := &checks.Env{Seq: 0} // run 0 is always heavy for xray
	if *via != "" {
		v, err := viaOutbound(*via)
		if err != nil {
			fmt.Fprintln(os.Stderr, "via:", err)
			return 2
		}
		env.Via = v
		inst, err := startVia(v)
		if err != nil {
			fmt.Fprintln(os.Stderr, "via:", err)
			return 2
		}
		defer inst.Close()
		env.Dial = inst.DialContext
	}
	r := checks.Run(context.Background(), &spec, env)
	if *asJSON {
		b, _ := json.MarshalIndent(r, "", "  ")
		fmt.Println(string(b))
	} else {
		printResult(&spec, r)
	}
	if r.Status != checks.Up {
		return 1
	}
	return 0
}

func printResult(s *checks.Spec, r checks.Result) {
	mark := "✅ UP"
	if r.Status != checks.Up {
		mark = "❌ DOWN"
	}
	fmt.Printf("%s  %s  %.0f ms\n", mark, s.Type, r.LatencyMS)
	if r.Status != checks.Up {
		fmt.Printf("   reason: %s\n   error:  %s\n", r.Reason, r.Error)
	}
	for _, k := range []string{"connect", "tls", "ttfb", "rtt", "download"} {
		if v, ok := r.Phases[k]; ok {
			fmt.Printf("   %-9s %.0f ms\n", k+":", v)
		}
	}
	if r.Throughput > 0 {
		fmt.Printf("   speed:    %.2f Mbit/s (%d bytes)\n", r.Throughput*8/1e6, r.Bytes)
	}
	for k, v := range r.Info {
		fmt.Printf("   %-9s %s\n", k+":", v)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func hasSavedToken(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "token"))
	return err == nil && strings.TrimSpace(string(b)) != ""
}

func envDuration(k string, def time.Duration) time.Duration {
	if v, err := time.ParseDuration(os.Getenv(k)); err == nil {
		return v
	}
	return def
}
