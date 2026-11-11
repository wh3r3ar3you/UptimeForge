# Contributing to UptimeForge

UptimeForge is a hub (UI, API, alerts, storage) plus agents that run checks from many networks and report back. This page is what you need to change it safely.

## Layout

- `cmd/uptimeforge-hub`, `cmd/uptimeforge-agent`: the binaries. `tools/demo`: a playground hub with fake nodes, a DPI proxy, a fake Remnawave panel and a fake Telegram.
- `internal/hub`: the engine. `engine.go` holds consensus, incidents and the settle window; `verdict.go` classifies results into down / blocked / partial; `subs.go` expands subscriptions; `server.go` handles the agent WebSocket and snapshots; `config.go` loads and normalizes the config; `deps.go` folds a service's failure into its node's alert; `hostalerts.go` turns host metrics into threshold incidents; `usersdrop.go` watches users online per Remnawave node.
- `internal/app`: wraps the hub with PostgreSQL settings and objects, YAML provisioning (items from the file are read-only in the UI), the notifier lifecycle, the Remnawave poller and the weekly report.
- `internal/api`: REST and auth (cookie sessions, admin and viewer roles), the agent install script and downloads, public status pages.
- `internal/checks` (every Uptime Kuma check type plus Xray and MTProto; `catalog.go` describes the types and fields that the form, validation and secret masking read), `internal/xray` (share links, subscriptions, embedded xray-core), `internal/notify` (Telegram, webhook, formatter, templates), `internal/store` (PostgreSQL and rollups), `internal/remnawave`, `internal/agent`, `internal/sysmon`.
- `web/`: Vite + React + Mantine + ECharts, embedded into the hub binary. Each page has an `L = {ru, en}` dictionary used through `useT(L)`.

## Commands

```sh
docker compose -f deploy/compose.test.yml up -d   # PostgreSQL for the tests and the demo
make demo                      # http://127.0.0.1:8080, admin / admin12345
cd web && npm run dev          # UI with hot reload, proxies /api to :8080
make test                      # go test -race ./... and tsc
make lint                      # go vet and gofmt
cd web && npx vite build       # must pass before committing UI changes
go test -tags review ./internal/...   # proof tests of open bugs and the ParseLink fuzz test
```

## Rules

- **Alerts must not lie in either direction:** no false alarms, no swallowed incidents. Any change in `internal/hub` comes with a test in `hub_test.go` or `verdict_test.go`.
- **Charts:** a series' colour follows its probe (fixed categorical order); status colours are reserved for status; one y-axis; 2 px lines; a legend from two series up.
- **UI text** goes through the page's `ru`/`en` dictionary, never hard-coded. The Telegram formatter (`internal/notify/format.go`) and `web/src/reason.ts` word failure reasons the same way; keep them in sync.
- **Secrets** (tokens, VPN links, proxy credentials, database passwords) never reach logs, alerts or viewers. Go puts URLs into its errors, and error text reaches alerts: strip them. A new secret field needs `Secret: true` in the check catalog.
- **Tests** that touch the database use a real PostgreSQL through `UF_TEST_DATABASE_URL`, each test in its own schema (`internal/store/pgtest`).
- **Proof tests for open bugs** go behind the `review` build tag in `internal/**/review*_test.go`; once a bug is fixed, its test moves into the normal suite (no tag, polling instead of sleeps).

## Reporting security issues

Please do not open a public issue for a vulnerability: see [SECURITY.md](SECURITY.md).
