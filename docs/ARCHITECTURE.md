# Architecture

```
  agents (home, RU DCs, abroad, routers) ──wss──┐
                                                 ▼
┌──────────────────────────────── uptimeforge-hub ───────────────────────────────┐
│  api (REST + session auth) ── web UI (embedded)       /install.sh, /download/* │
│        │                                                                        │
│       app ── composes config: DB objects + optional config.yaml (read-only) │
│        │     notifiers (Telegram, webhook) · Remnawave poller · recorder         │
│        ▼                                                                        │
│       hub ── probes · assignments · ingest · verdicts · incidents · run-once    │
│        │      virtual probes: local / via proxy (runner + embedded xray-core)   │
│        ▼                                                                        │
│      store (PostgreSQL): settings, users, sessions, incidents,                 │
│             check samples + host metrics with 5m/1h rollups                     │
│                                                                                 │
│  /metrics (Prometheus) · /healthz · heartbeat → external dead man's switch      │
└─────────────────────────────────────────────────────────────────────────────────┘
```

## Packages

| Package | Role |
|---|---|
| `internal/xray` | Share-link and subscription parsing into Xray outbounds; in-process xray-core; the tunnel probe (light request, heavy download with stall detection, exit IP). |
| `internal/checks` | Check specs and results; checkers `xray`, `mtproto`, `http`, `tcp`, `tls`, `dns`. |
| `internal/runner` | Schedules assigned checks on a probe: jitter, concurrency limit, faster retry after a failure, heavy-run escalation, run-once. Shared by agents and virtual probes. |
| `internal/agent` | WebSocket client: hello, sysinfo, assignments, batched results and host metrics (buffered while the hub is unreachable; samples carry their age so agent clocks don't matter), run-once requests. |
| `internal/sysmon` | Host metrics: CPU, memory, disk, load, traffic on physical NICs, link speed, TCP sockets. |
| `internal/hub` | Probe registry, hot-reloadable config, assignments, ingest, verdicts, incident lifecycle, notification policy, run-once fan-out, Prometheus metrics. |
| `internal/app` | Owns configuration and wiring: merges UI-made objects with the provisioning file, validates changes before saving, rebuilds notifiers, polls Remnawave, enriches incidents, batches writes to the store. |
| `internal/store` | PostgreSQL: objects (probes, checks, status pages), settings, users/sessions (bcrypt), incident history, samples and rollups with retention. |
| `internal/api` | HTTP API for the UI, auth (cookies, roles, rate limit, same-origin checks), public status pages, agent installer and binaries. |
| `internal/notify` | Message rendering (ru/en), Telegram (one message per incident, edited in place; buttons), webhook. |
| `internal/remnawave` | Client for the Remnawave panel API, built from `@remnawave/backend-contract` 3.4.15. |
| `web/` | React + Mantine + ECharts UI, built by Vite and embedded into the hub binary. |
| `tools/demo` | A self-contained playground with seeded history for UI work and screenshots. |

## How a verdict is made

1. Each probe runs its checks; a failure is re-run sooner (≤ 20 s). A probe counts a check as **failing** after `retries` failures in a row and recovers after one success.
2. Probes that are offline, stale, or whose **baseline** targets don't answer (no internet) are left out. That keeps a probe that lost its uplink from looking like "everything is blocked".
3. The remaining probes decide: all fail → **down**; all probes of one region fail and only them → **blocked · region**; otherwise → **partial**.
4. **Partial** waits up to one interval before it is announced, because probes confirm at different moments; down/blocked are announced immediately.
5. An incident doesn't resolve just because its only failing probe went silent — it needs a success from that probe.

## Data retention

| Data | Kept |
|---|---|
| raw check results and host metrics | 48 h |
| 5-minute rollups | 10 days |
| hourly rollups | 120 days |
| incidents | forever (small) |

Daily uptime bars are computed from verdict-level incidents, so they match what was alerted.

## Security model

- Agents dial out with a per-probe token; only its SHA-256 is stored for UI-made probes, and it is shown once.
- UI sessions: random 256-bit tokens in HttpOnly SameSite=Lax cookies, stored hashed; bcrypt passwords; login rate limit; state-changing requests must be same-origin.
- Roles: admin (everything) and viewer (read, acknowledge, mute).
- Secrets (bot token, Remnawave token, webhook auth headers) are masked in the API; saving the mask keeps the stored value.
- Public status pages expose names and states only — no targets, probes or errors.
- Each probe receives only the checks selected for it. Config links are secrets: give a home router only what it needs.

## Roadmap

- Per-probe traffic budget for metered (mobile) probes; lite agent build without xray-core for small-flash routers.
- Remnawave node auto-import as checks.
- SSO / OIDC login.
