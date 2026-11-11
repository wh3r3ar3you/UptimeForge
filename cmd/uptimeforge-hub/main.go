// Command uptimeforge-hub runs the UptimeForge hub: probes connect to it,
// it evaluates checks, alerts, stores history and serves the web UI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/wh3r3ar3you/uptimeforge/internal/api"
	"github.com/wh3r3ar3you/uptimeforge/internal/app"
	"github.com/wh3r3ar3you/uptimeforge/internal/hub"
	"github.com/wh3r3ar3you/uptimeforge/internal/store"
	"github.com/wh3r3ar3you/uptimeforge/internal/version"
	"github.com/wh3r3ar3you/uptimeforge/web"
)

func main() {
	fs := flag.NewFlagSet("uptimeforge-hub", flag.ExitOnError)
	configPath := fs.String("config", env("UF_CONFIG", ""), "optional config.yaml that provisions probes, checks and settings (env UF_CONFIG)")
	dataDir := fs.String("data", env("UF_DATA_DIR", "./data"), "directory for small caches (env UF_DATA_DIR)")
	dbURL := fs.String("database-url", env("UF_DATABASE_URL", ""), "PostgreSQL URL, postgres://user:password@host:5432/db (env UF_DATABASE_URL)")
	listen := fs.String("listen", env("UF_LISTEN", ":8080"), "listen address (env UF_LISTEN)")
	publicURL := fs.String("public-url", env("UF_PUBLIC_URL", ""), "external https URL of the hub (env UF_PUBLIC_URL)")
	adminToken := fs.String("admin-token", env("UF_ADMIN_TOKEN", ""), "bearer token for scripts using the API (env UF_ADMIN_TOKEN)")
	agentsDir := fs.String("agents-dir", env("UF_AGENTS_DIR", "/usr/share/uptimeforge/agents"), "agent binaries served at /download (env UF_AGENTS_DIR)")
	setupToken := fs.String("setup-token", env("UF_SETUP_TOKEN", ""), "token that creates the first admin in the web UI; empty: one is printed to the log (env UF_SETUP_TOKEN)")
	trustedProxies := fs.String("trusted-proxies", env("UF_TRUSTED_PROXIES", ""), "comma-separated addresses/CIDRs of reverse proxies whose X-Forwarded-* headers count; empty: loopback and private networks, none: nobody (env UF_TRUSTED_PROXIES)")
	metricsToken := fs.String("metrics-token", env("UF_METRICS_TOKEN", ""), "bearer token for /metrics; empty: /metrics answers only loopback and private addresses, never through a proxy (env UF_METRICS_TOKEN)")

	cmd := "serve"
	args := os.Args[1:]
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "version":
		fmt.Println(version.Version)
		return
	case "check-config":
		fs.Parse(args)
		c, err := hub.LoadConfig(*configPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("config OK: %d probes, %d checks\n", len(c.Probes), len(c.Checks))
		return
	case "admin":
		user := fs.String("username", "admin", "username")
		pass := fs.String("password", os.Getenv("UF_ADMIN_PASSWORD"), "password (env UF_ADMIN_PASSWORD)")
		fs.Parse(args)
		if err := setAdmin(*dbURL, *user, *pass); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("admin %q is ready\n", *user)
		return
	case "serve":
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q (serve, admin, check-config, version)\n", cmd)
		os.Exit(2)
	}
	fs.Parse(args)

	log := newLogger()
	proxies, err := api.ParseTrustedProxies(*trustedProxies)
	if err == nil {
		err = api.CheckAdminToken(*adminToken)
	}
	if err != nil {
		log.Error("startup failed", "err", err)
		os.Exit(1)
	}
	a, err := app.New(app.Options{ConfigPath: *configPath, DataDir: *dataDir, DatabaseURL: *dbURL, Listen: *listen,
		PublicURL: *publicURL, AdminToken: *adminToken}, log)
	if err != nil {
		log.Error("startup failed", "err", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := newServer(*listen, api.New(a, api.Options{UI: web.FS(), AgentDir: *agentsDir, SetupToken: *setupToken,
		TrustedProxies: proxies, MetricsToken: *metricsToken}).Handler())
	go func() {
		cfg := a.Hub.Config()
		log.Info("UptimeForge hub listening", "addr", *listen, "version", version.Version,
			"probes", len(cfg.Probes), "checks", len(cfg.Checks), "ui", web.FS() != nil)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server", "err", err)
			stop()
		}
	}()

	if err := a.Run(ctx); err != nil {
		log.Error("hub stopped", "err", err)
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shutdown)
}

// setAdmin creates the user or resets its password: the way back in after
// losing the password.
func setAdmin(dbURL, username, password string) error {
	if password == "" {
		return errors.New("set --password or UF_ADMIN_PASSWORD")
	}
	st, err := store.Open(dbURL)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	users, err := st.Users(ctx)
	if err != nil {
		return err
	}
	for _, u := range users {
		if strings.EqualFold(u.Username, username) {
			return st.SetPassword(ctx, u.ID, password)
		}
	}
	_, err = st.CreateUser(ctx, username, password, store.RoleAdmin)
	return err
}

// newServer bounds how long a client may take to send a request and keep
// an idle connection. There is no write timeout: agent downloads over slow
// links and long tool runs take minutes. The agent WebSocket is hijacked
// (Go clears the deadlines then), so the read timeout does not cut it.
func newServer(addr string, h http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func newLogger() *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if os.Getenv("UF_DEBUG") != "" {
		opts.Level = slog.LevelDebug
	}
	if os.Getenv("UF_LOG_FORMAT") == "json" {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}
