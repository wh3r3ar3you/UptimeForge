#!/bin/sh
# UptimeForge checker agent, one command, served by your hub at /join.sh:
#   curl -fsSL https://hub.example.com/join.sh | sudo sh -s -- INVITE_CODE
# Runs the agent in a locked-down Docker container. Docker is installed only
# when the machine has none at all; an existing engine is never touched (at
# most its compose plugin is added from the distro's own packages). Where
# Docker cannot be had (a router, an unsupported system) it installs the agent
# binary as a service instead.
# The invite code is single use: the agent swaps it for a token on first start.
set -eu

default_hub=__HUB_URL__
default_image=__IMAGE__
UF_HUB="${UF_HUB:-$default_hub}"
IMAGE="${UF_IMAGE:-$default_image}"
DIR="${UF_DIR:-/opt/uptimeforge-agent}"
INSECURE="${UF_INSECURE:-}"

CODE="${1:-${UF_JOIN:-}}"
[ -n "$CODE" ] || { echo "usage: curl -fsSL $UF_HUB/join.sh | sudo sh -s -- INVITE_CODE" >&2; exit 1; }
case "$CODE" in *[!A-Za-z0-9_-]*) echo "that is not an invite code" >&2; exit 1 ;; esac

# The agent refuses a plain http hub outside loopback unless told otherwise;
# better to say so now than to install an agent that never starts.
case "$UF_HUB" in
  http://localhost|http://localhost:*|http://127.0.0.1|http://127.0.0.1:*|"http://[::1]"|"http://[::1]:"*) ;;
  http://*)
    if [ "$INSECURE" != 1 ]; then
      echo "$UF_HUB is plain http: the invite code, the agent token and everything the agent" >&2
      echo "reports would cross the network in clear text, and the agent refuses to start." >&2
      echo "Put the hub behind https, or accept the risk and rerun with UF_INSECURE=1." >&2
      exit 1
    fi
    echo "WARNING: $UF_HUB is plain http (UF_INSECURE=1): the token and all reports travel in clear text." >&2
    ;;
esac

[ "$(id -u)" = 0 ] || { echo "run as root: curl -fsSL $UF_HUB/join.sh | sudo sh -s -- INVITE_CODE" >&2; exit 1; }

case "$(uname -m)" in
  x86_64|amd64|aarch64|arm64|armv7*|armv8l|mips|mipsel|mipsle) ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

have_docker() { command -v docker >/dev/null 2>&1; }
have_compose() { have_docker && docker compose version >/dev/null 2>&1; }

os_id=""
if [ -r /etc/os-release ]; then
  os_id="$(. /etc/os-release && echo "${ID:-}")"
fi

# Only for a machine with no docker binary at all.
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

# Docker is there without `docker compose`: add the plugin from the distro's
# own repositories and leave the engine alone.
install_compose_plugin() {
  case "$os_id" in
    debian|ubuntu)
      apt-get install -y docker-compose-v2 || apt-get install -y docker-compose-plugin || return 1 ;;
    fedora|centos|rhel|rocky|almalinux)
      { dnf install -y docker-compose-plugin || yum install -y docker-compose-plugin; } || return 1 ;;
    alpine)
      apk add --no-cache docker-cli-compose || return 1 ;;
    *) return 1 ;;
  esac
  have_compose
}

# The agent is up when its service says so a few seconds after the start;
# otherwise the last log lines are shown and the script fails.
verify_binary() {
  sleep 4
  if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    if systemctl is-active --quiet uptimeforge-agent; then return 0; fi
    echo "the agent service is not running:" >&2
    journalctl -u uptimeforge-agent -n 20 --no-pager >&2 || true
    return 1
  fi
  if pidof uptimeforge-agent >/dev/null 2>&1 || killall -0 uptimeforge-agent >/dev/null 2>&1; then return 0; fi
  echo "the agent is not running:" >&2
  tail -n 20 /tmp/uptimeforge-agent.log >&2 || true
  return 1
}

binary_fallback() {
  echo "Docker is not available here: installing the agent binary as a service instead" >&2
  tmp="$(mktemp)"
  trap 'rm -f "$tmp"' EXIT
  if command -v curl >/dev/null 2>&1; then curl -fsSL "$UF_HUB/install.sh" -o "$tmp"; else wget -qO "$tmp" "$UF_HUB/install.sh"; fi \
    || { echo "could not download $UF_HUB/install.sh" >&2; exit 1; }
  UF_HUB="$UF_HUB" UF_JOIN="$CODE" UF_INSECURE="$INSECURE" sh "$tmp" || exit 1
  verify_binary || exit 1
  echo "Done: the agent runs as a service and enrolls with $UF_HUB."
  exit 0
}

if ! have_docker; then
  echo "Docker not found: trying to install it"
  install_docker || binary_fallback
fi
if ! have_compose; then
  echo "docker compose not found: trying to add the compose plugin"
  install_compose_plugin || binary_fallback
fi

umask 077
mkdir -p "$DIR"
env_extra=""
[ "$INSECURE" != 1 ] || env_extra='      UF_INSECURE: "1"'
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
$env_extra
    volumes:
      - uptimeforge-agent-state:/state
volumes:
  uptimeforge-agent-state:
COMPOSE

cd "$DIR"
# An unreachable registry is no reason to give up when the image is already
# here (a re-run); only a failed start sends us to the binary.
docker compose pull || echo "could not pull $IMAGE: starting with what is on this machine" >&2
docker compose up -d || { echo "the agent container did not start" >&2; binary_fallback; }

sleep 6
state="$(docker inspect -f '{{.State.Running}} {{.State.Restarting}}' uptimeforge-agent 2>/dev/null || echo "missing")"
if [ "$state" != "true false" ]; then
  echo "the agent container is not staying up ($state); last log lines:" >&2
  docker logs --tail 20 uptimeforge-agent >&2 || true
  exit 1
fi
echo "Done: the agent runs in Docker (container uptimeforge-agent, $DIR/compose.yml) and enrolls with $UF_HUB; logs: docker logs -f uptimeforge-agent"
