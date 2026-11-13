#!/bin/sh
# UptimeForge checker agent, one command, served by your hub at /join.sh:
#   curl -fsSL https://hub.example.com/join.sh | sudo sh -s -- INVITE_CODE
# Installs Docker when it is missing (on a system that supports it) and runs
# the agent in a locked-down container; where Docker cannot be had (a router,
# an unsupported system) it installs the agent binary as a service instead.
# The invite code is single use: the agent swaps it for a token on first start.
set -eu

default_hub=__HUB_URL__
default_image=__IMAGE__
UF_HUB="${UF_HUB:-$default_hub}"
IMAGE="${UF_IMAGE:-$default_image}"
DIR=/opt/uptimeforge-agent

CODE="${1:-${UF_JOIN:-}}"
[ -n "$CODE" ] || { echo "usage: curl -fsSL $UF_HUB/join.sh | sudo sh -s -- INVITE_CODE" >&2; exit 1; }
case "$CODE" in *[!A-Za-z0-9_-]*) echo "that is not an invite code" >&2; exit 1 ;; esac
[ "$(id -u)" = 0 ] || { echo "run as root: curl -fsSL $UF_HUB/join.sh | sudo sh -s -- INVITE_CODE" >&2; exit 1; }

case "$UF_HUB" in
  http://*)
    echo "" >&2
    echo "!!! WARNING: $UF_HUB is plain http. The invite code, the agent token and" >&2
    echo "!!! everything the agent reports cross the network unencrypted." >&2
    echo "" >&2
    ;;
esac

case "$(uname -m)" in
  x86_64|amd64|aarch64|arm64|armv7*|armv8l|mips|mipsel|mipsle) ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

have_compose() { command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; }

os_id=""
if [ -r /etc/os-release ]; then
  os_id="$(. /etc/os-release && echo "${ID:-}")"
fi

# Docker is installed only on systems where that is routine; anything else
# (or any failure) falls through to the binary.
install_docker() {
  case "$os_id" in
    debian|ubuntu|fedora|centos|rhel|rocky|almalinux)
      command -v curl >/dev/null 2>&1 || return 1
      curl -fsSL https://get.docker.com | sh || return 1
      systemctl enable --now docker || return 1
      ;;
    alpine)
      apk add --no-cache docker docker-cli-compose || return 1
      rc-update add docker default || true
      service docker start || rc-service docker start || return 1
      ;;
    *) return 1 ;;
  esac
  have_compose
}

binary_fallback() {
  echo "Docker is not available here: installing the agent binary as a service instead" >&2
  tmp="$(mktemp)"
  trap 'rm -f "$tmp"' EXIT
  if command -v curl >/dev/null 2>&1; then curl -fsSL "$UF_HUB/install.sh" -o "$tmp"; else wget -qO "$tmp" "$UF_HUB/install.sh"; fi \
    || { echo "could not download $UF_HUB/install.sh" >&2; exit 1; }
  UF_HUB="$UF_HUB" UF_JOIN="$CODE" sh "$tmp"
  echo "Done: the agent runs as a service and enrolls with $UF_HUB on first start."
  exit 0
}

if ! have_compose; then
  echo "docker compose not found: trying to install Docker"
  install_docker || binary_fallback
fi

umask 077
mkdir -p "$DIR"
cat > "$DIR/compose.yml" <<COMPOSE
services:
  uptimeforge-agent:
    image: $IMAGE
    container_name: uptimeforge-agent
    restart: unless-stopped
    network_mode: host
    read_only: true
    tmpfs: [/tmp]
    cap_drop: [ALL]
    security_opt: ["no-new-privileges:true"]
    mem_limit: 256m
    cpus: 0.5
    environment:
      UF_HUB: "$UF_HUB"
      UF_JOIN: "$CODE"
      UF_STATE_DIR: /state
    volumes:
      - uptimeforge-agent-state:/state
volumes:
  uptimeforge-agent-state:
COMPOSE

cd "$DIR"
if ! { docker compose pull && docker compose up -d; }; then
  echo "the agent container did not start" >&2
  binary_fallback
fi
echo "Done: the agent runs in Docker (container uptimeforge-agent, $DIR/compose.yml) and enrolls with $UF_HUB; logs: docker logs -f uptimeforge-agent"
