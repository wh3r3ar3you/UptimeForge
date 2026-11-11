<div align="center">

<img src="web/public/favicon.svg" width="88" alt="UptimeForge" />

# UptimeForge

**Monitoring for VPN and proxy infrastructure — see your nodes the way your users reach them.**

Down, blocked in a region, or failing at one ISP? UptimeForge checks from many networks at once and tells you which.

[![Stars](https://img.shields.io/github/stars/wh3r3ar3you/UptimeForge?style=for-the-badge&logo=github&label=Star&color=f5c542)](https://github.com/wh3r3ar3you/UptimeForge/stargazers)
[![Release](https://img.shields.io/github/v/release/wh3r3ar3you/UptimeForge?include_prereleases&style=for-the-badge&color=2ee6c4)](https://github.com/wh3r3ar3you/UptimeForge/releases)
[![Docker pulls](https://img.shields.io/docker/pulls/intoxxx/uptimeforge-hub?style=for-the-badge&logo=docker&logoColor=white&color=2a8cf0)](https://hub.docker.com/r/intoxxx/uptimeforge-hub)
[![License](https://img.shields.io/github/license/wh3r3ar3you/UptimeForge?style=for-the-badge&color=8b5cf6)](LICENSE)
[![CI](https://img.shields.io/github/actions/workflow/status/wh3r3ar3you/UptimeForge/ci.yml?branch=main&style=for-the-badge&label=tests)](https://github.com/wh3r3ar3you/UptimeForge/actions/workflows/ci.yml)

**English** · [Русский](README.ru.md)

[Features](#-features) · [Screenshots](#-screenshots) · [Quick start](#-quick-start) · [Deploy on a VPS](#-deploy-on-a-vps) · [Configuration](#%EF%B8%8F-configuration) · [How verdicts work](#-how-verdicts-work)

<img src="docs/screenshots/dashboard.png" alt="Dashboard" width="100%" />

</div>

## 💡 Why

Server monitoring (Beszel, Zabbix) shows that a node is alive. Uptime monitors (Uptime Kuma) ping it from one place. Neither answers the question a VPN operator wakes up with:

> *Can users in a filtered country connect right now — and if not, is the node dead or is it being filtered?*

UptimeForge runs real checks **from many networks at once** — your home ISP, datacenters inside the filtered country, nodes abroad, routers, residential proxies — and turns them into one honest verdict: 🔴 **down** everywhere · 🟠 **blocked** in a region · 🟡 failing at **some ISPs**.

## ✨ Features

<table>
<tr>
<td width="50%" valign="top">

### 🔍 Checks that see what users see
- **Real Xray checks** — VLESS (Reality, XHTTP, gRPC, WS), VMess, Trojan, Shadowsocks, Hysteria2 through an embedded xray-core, with a real request through the tunnel. A 128 KB download catches the "freezes after ~16 KB" DPI that a ping never sees. The exit IP can be verified.
- **Subscriptions as checks** — point it at a canary user's subscription (Remnawave, Marzban, 3x-ui…); every config becomes a check.
- **MTProto proxies end to end** — a real `req_pq` → `resPQ` round trip to a Telegram DC through the proxy.
- **Every Uptime Kuma check type** — HTTP(S), TCP, ping, DNS, TLS, databases, brokers, push, Docker, systemd and more.
- **Config checker** — paste a link and run it from any probes right now.

</td>
<td width="50%" valign="top">

### ⚖️ Verdicts that don't lie
- **Many probes, one verdict** — probes without internet are excluded automatically, so a dead home router never paints everything red.
- **Dependencies** — when a node is down, its configs join the node's alert as "affected" instead of alerting one by one. Found by address, or set by hand.
- **Restart-safe** — after a restart the hub waits for its probes before calling anything down.
- **Probes anywhere** — agents dial out (no open ports) on Linux, Keenetic/OpenWrt routers, Windows, macOS; or the hub checks **through a proxy** to see an ISP's network without installing anything.

</td>
</tr>
<tr>
<td valign="top">

### 🔔 Alerts you can live with
- **Telegram cards** — one message per incident, edited as it evolves, with Acknowledge / Mute / Resolved buttons, quiet hours and topics per severity.
- **Tags and routing rules** — send what matches a rule to its own chat or topic.
- **Notification templates** — rewrite any message with `{placeholders}`; a template never costs an alert.
- **Weekly report** — uptime, worst offenders, lost nodes, bans, traffic.
- **Signed webhooks** and a **dead man's switch** for the monitor itself.

</td>
<td valign="top">

### 🛰 Remnawave and servers
- **Panel at a glance** — users online, nodes up and traffic on top of the dashboard.
- **Users-gone alert** — a node that drops from a thousand users to two is alive but unreachable: that's how a DPI ban looks.
- **Node lost by the panel** — its own alert, folded with the node's agent.
- **Server metrics** — CPU with steal and iowait, memory, disks, I/O, traffic, TCP; Zabbix-style thresholds that don't flap.
- **Public status pages** — by check groups or by **VPN locations**: a flag, your name for it and a status, nothing internal.

</td>
</tr>
</table>

## 📸 Screenshots

<table>
<tr>
<td width="50%"><img src="docs/screenshots/checks.png" alt="Checks" /><br/><sub><b>Checks</b> — grouped, with 24-hour pulse bars and bulk actions</sub></td>
<td width="50%"><img src="docs/screenshots/check.png" alt="A check" /><br/><sub><b>A check</b> — which probes fail and why: here, blocked in Russia</sub></td>
</tr>
<tr>
<td><img src="docs/screenshots/servers.png" alt="Servers" /><br/><sub><b>Servers</b> — metrics from every agent, columns you pick</sub></td>
<td><img src="docs/screenshots/remnawave.png" alt="Remnawave" /><br/><sub><b>Remnawave</b> — panel load, traffic and users online</sub></td>
</tr>
<tr>
<td><img src="docs/screenshots/incidents.png" alt="Incidents" /><br/><sub><b>Incidents</b> — history with causes and what each one affected</sub></td>
<td><img src="docs/screenshots/templates.png" alt="Templates" /><br/><sub><b>Templates</b> — your own words for every alert, with a live preview</sub></td>
</tr>
<tr>
<td><img src="docs/screenshots/tools.png" alt="Config checker" /><br/><sub><b>Config checker</b> — try a link from every probe before adding it</sub></td>
<td><img src="docs/screenshots/status.png" alt="Status page" /><br/><sub><b>Public status page</b> — by location, for your customers</sub></td>
</tr>
</table>

<p align="center"><img src="docs/screenshots/status-mobile.png" alt="Status page on a phone" width="300" /><br/><sub>The status page on a phone</sub></p>

## 🚀 Quick start

You need Docker with Compose and a domain pointing at the server.

```sh
mkdir uptimeforge && cd uptimeforge
curl -fsSLO https://raw.githubusercontent.com/wh3r3ar3you/UptimeForge/main/docker-compose.yml
echo "UF_DB_PASSWORD=$(openssl rand -hex 24)" > .env
echo "UF_SETUP_TOKEN=$(openssl rand -hex 16)" >> .env
echo "UF_PUBLIC_URL=https://status.example.com" >> .env
docker compose up -d
```

1. **Put HTTPS in front** — see [Deploy on a VPS](#-deploy-on-a-vps) for ready-made Caddy, Traefik and nginx setups. Agents connect over `wss://`.
2. **Create the admin** — open the UI and enter the setup token from `.env`. Without `UF_SETUP_TOKEN` the hub makes one up and prints it to its log (`docker compose logs hub`); until an admin exists nobody can use the hub without it.
3. **Add probes** — *Servers & probes → Add probe*. You get a one-line installer served by your own hub; it verifies the binary's checksum:

   ```sh
   curl -fsSL https://status.example.com/install.sh | sudo UF_HUB=https://status.example.com UF_TOKEN=… sh
   ```

   The hub is a probe itself: keep it **outside** the filtered region, as the control point.
4. **Add checks** — a VPN link, a subscription, an MTProxy link, an HTTP endpoint, a port, a certificate, a DNS name.
5. **Alerts** — *Settings → Telegram*: a bot token (a dedicated bot) and a chat, then *Send test*. Add your Telegram user id to *admins* to resolve and mute from the chat.

Try a config from the command line before adding it:

```sh
uptimeforge-agent check 'vless://…'                    # handshake, 128 KB download, exit IP
uptimeforge-agent check 'vless://…' --via socks5://…   # as seen through another network
```

## 🌐 Deploy on a VPS

Pick what suits your server — each is one folder in [`deploy/`](deploy):

| Setup | When | How |
|---|---|---|
| 🟢 **Caddy** | a clean VPS, nothing else on ports 80/443 | [`deploy/caddy`](deploy/caddy/docker-compose.yml): HTTPS from Let's Encrypt with no extra config |
| 🔷 **Traefik** | Traefik already fronts your services, or you like labels | [`deploy/traefik`](deploy/traefik/docker-compose.yml): Let's Encrypt via the HTTP challenge |
| 🟩 **nginx** | nginx is already on the server | the main `docker-compose.yml` + [`deploy/nginx/uptimeforge.conf`](deploy/nginx/uptimeforge.conf) + `certbot --nginx` |
| 🐳 **Plain** | you have your own proxy | the main [`docker-compose.yml`](docker-compose.yml) listens on `127.0.0.1:8080` |

With Caddy, for example:

```sh
mkdir uptimeforge && cd uptimeforge
curl -fsSLO https://raw.githubusercontent.com/wh3r3ar3you/UptimeForge/main/deploy/caddy/docker-compose.yml
echo "UF_DOMAIN=status.example.com" > .env
echo "UF_DB_PASSWORD=$(openssl rand -hex 24)" >> .env
echo "UF_SETUP_TOKEN=$(openssl rand -hex 16)" >> .env
docker compose up -d     # then open https://status.example.com
```

**Tips for VPN operators**
- **Host the hub outside the filtered country** and on its own address: if the hub's address gets filtered, agents inside the country go blind. Agents try the host's other public addresses by themselves (`UF_SOURCE_IP` pins one).
- **1 vCPU / 1 GB RAM** is enough for dozens of nodes; PostgreSQL holds the history.
- Behind a CDN or another proxy, list it in `UF_TRUSTED_PROXIES` so the login limit and secure cookies see real addresses.

## ⬆️ Upgrading and backup

```sh
docker compose pull && docker compose up -d        # upgrade: the schema migrates forward on start
docker compose exec postgres pg_dump -U uptimeforge -Fc uptimeforge > uptimeforge.dump   # backup
```

Back up before upgrading: going back to an older version is not supported. The `hub-data` volume only holds caches.

## ⚙️ Configuration

Everything is configurable in the UI. Prefer git? Start the hub with `UF_CONFIG=/path/config.yaml`: probes, checks and settings from the file appear in the UI read-only. See [`config.example.yaml`](config.example.yaml).

<details>
<summary><b>Hub variables</b></summary>

| Variable | Default | |
|---|---|---|
| `UF_DATABASE_URL` | required | PostgreSQL 14+: `postgres://user:password@host:5432/db` |
| `UF_LISTEN` | `:8080` | listen address |
| `UF_PUBLIC_URL` | | the hub's external URL, used in alert links and install commands |
| `UF_SETUP_TOKEN` | printed to the log | needed to create the first admin |
| `UF_ADMIN_TOKEN` | | bearer token for scripts calling the API; at least 32 characters |
| `UF_METRICS_TOKEN` | | bearer token for `/metrics`; without it `/metrics` answers only loopback and private addresses with no proxy in between |
| `UF_TRUSTED_PROXIES` | loopback and private networks | IPs/CIDRs whose `X-Forwarded-*` headers are believed; `none` to believe nobody |
| `UF_CONFIG` | | optional provisioning file |
| `UF_DATA_DIR` | `./data` (`/data` in Docker) | caches |
| `UF_AGENTS_DIR` | `/usr/share/uptimeforge/agents` | agent binaries served to installers (the image has them; without them the installer downloads the hub's version from GitHub Releases) |
| `UF_LOG_FORMAT` | text | `json` for JSON logs |
| `UF_DEBUG` | | set to log at debug level |
| `TZ` | `UTC` | time zone of alerts, quiet hours and the weekly report |

Forgot the password: `docker compose exec hub uptimeforge-hub admin --username admin --password '…'`.

</details>

<details>
<summary><b>Agent variables</b></summary>

| Variable | |
|---|---|
| `UF_HUB` | the hub's URL (`https://…`) |
| `UF_TOKEN` | the agent's token from the UI |
| `UF_SOURCE_IP` | pin the source address used to reach the hub; `off` to disable the search |
| `UF_METRICS_INTERVAL` | host metrics interval (default `15s`, `0` to disable) |
| `UF_NIC` | interfaces counted as traffic (default: physical ones) |
| `UF_DENY_TYPES` | check types this agent refuses to run, e.g. `docker,real-browser` |
| `UF_INSECURE` | `1` allows an `http://` hub outside loopback (the token then travels in clear text) |
| `UF_BROWSER_NO_SANDBOX` | `1` runs Chromium for `real-browser` checks without its sandbox |

</details>

<details>
<summary><b>Metrics for Grafana</b></summary>

```sh
curl -fsSL --create-dirs -o deploy/scrape.yml https://raw.githubusercontent.com/wh3r3ar3you/UptimeForge/main/deploy/scrape.yml
docker compose --profile metrics up -d
```

VictoriaMetrics scrapes the hub inside the compose network and listens on `127.0.0.1:8428`; add it to Grafana as a Prometheus data source.

</details>

## 🧠 How verdicts work

A probe counts a check as failing after N failures in a row. Probes without internet are excluded. All failing → **down**; all of one region failing and only them → **blocked**; otherwise **partial**, which settles for one interval before alerting. An incident never "recovers" just because its only witness went silent, and after a restart the hub waits for its probes before calling anything down. More in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## 🛠 Development

```sh
docker compose -f deploy/compose.test.yml up -d   # PostgreSQL
make demo        # hub on :8080 with a week of seeded history, fake Remnawave and Telegram — admin / admin12345
cd web && npm run dev
make test
```

See [CONTRIBUTING.md](CONTRIBUTING.md). Security issues: [SECURITY.md](SECURITY.md).

## ⭐ Star history

If UptimeForge saves you a sleepless night, give it a star — that's how other operators find it.

<a href="https://star-history.com/#wh3r3ar3you/UptimeForge&Date">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/svg?repos=wh3r3ar3you/UptimeForge&type=Date&theme=dark" />
    <img alt="Star history" src="https://api.star-history.com/svg?repos=wh3r3ar3you/UptimeForge&type=Date" width="100%" />
  </picture>
</a>

## 📄 License

[AGPL-3.0](LICENSE). Release archives and images list the licenses of the code built in (`THIRD_PARTY_LICENSES.csv`). Country lookups use the DB-IP IP-to-Country Lite database, [IP Geolocation by DB-IP](https://db-ip.com) (CC BY 4.0), downloaded by the hub on first use.
