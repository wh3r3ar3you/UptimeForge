#!/bin/sh
# UptimeForge agent installer, served by your hub at /install.sh:
#   curl -fsSL https://hub.example.com/install.sh | sudo UF_HUB=https://hub.example.com UF_TOKEN=xxxx sh
# Binaries come from the hub itself (/download), so GitHub is not needed; a
# hub without the binary for this machine sends it to the GitHub release of
# its own version. Either way the binary must match the published sha256.
# Works with systemd; on OpenWrt/Entware (no systemd) it installs an init script.
set -eu

default_hub=__HUB_URL__
default_version=__VERSION__
default_releases=__RELEASES_URL__
UF_HUB="${UF_HUB:-$default_hub}"
UF_VERSION="${UF_VERSION:-$default_version}"
RELEASES="${UF_RELEASES:-$default_releases}"
DOWNLOAD="${UF_DOWNLOAD:-$UF_HUB/download}"
BIN=/usr/local/bin/uptimeforge-agent

[ -n "${UF_TOKEN:-}" ] || { echo "set UF_TOKEN (shown in the hub when you add a probe)" >&2; exit 1; }
[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 1; }

case "$UF_HUB" in
  http://*)
    echo "" >&2
    echo "!!! WARNING: $UF_HUB is plain http. The agent token, everything the agent" >&2
    echo "!!! reports and this download cross the network unencrypted and can be read" >&2
    echo "!!! or swapped on the way. Put the hub behind https and set UF_PUBLIC_URL." >&2
    echo "" >&2
    ;;
esac

case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  armv7*|armv8l) arch=armv7 ;;
  mips) arch=mips ;;
  mipsel|mipsle) arch=mipsle ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

fetch() {
  if command -v curl >/dev/null 2>&1; then curl -fsSL "$1" -o "$2"; else wget -qO "$2" "$1"; fi
}

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1
  elif command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | cut -d' ' -f1
  elif command -v openssl >/dev/null 2>&1; then openssl dgst -sha256 "$1" | sed 's/.*= *//'
  else return 1
  fi
}

[ -d /opt/bin ] && [ ! -d /usr/local/bin ] && BIN=/opt/bin/uptimeforge-agent # Entware
mkdir -p "$(dirname "$BIN")"
name="uptimeforge-agent_linux_$arch"
tmp="$(mktemp)"
sums="$(mktemp)"
trap 'rm -f "$tmp" "$sums"' EXIT
echo "downloading agent for linux/$arch from $DOWNLOAD"
if ! { fetch "$DOWNLOAD/$name" "$tmp" && fetch "$DOWNLOAD/checksums.txt" "$sums"; }; then
  case "$UF_VERSION" in
    ""|dev) echo "the hub has no agent for linux/$arch, and a dev build has no release to fall back to" >&2; exit 1 ;;
  esac
  rel="$RELEASES/v${UF_VERSION#v}"
  echo "the hub has no agent for linux/$arch; downloading release v${UF_VERSION#v} from $rel"
  fetch "$rel/$name" "$tmp" || { echo "download from $rel failed" >&2; exit 1; }
  fetch "$rel/checksums.txt" "$sums" || { echo "no checksums.txt at $rel" >&2; exit 1; }
fi
want="$(awk -v n="$name" '$2 == n || $2 == "*" n { print $1; exit }' "$sums")"
[ -n "$want" ] || { echo "checksums.txt has no entry for $name: refusing to install" >&2; exit 1; }
got="$(sha256 "$tmp")" || { echo "need sha256sum, shasum or openssl to verify the download" >&2; exit 1; }
if [ "$got" != "$want" ]; then
  echo "checksum mismatch for $name: got $got, want $want. Refusing to install." >&2
  exit 1
fi
echo "sha256 OK: $got"
chmod 0755 "$tmp"
mv "$tmp" "$BIN"

if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
  id uptimeforge >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin uptimeforge
  umask 077
  mkdir -p /etc/uptimeforge
  printf 'UF_HUB=%s\nUF_TOKEN=%s\n' "$UF_HUB" "$UF_TOKEN" > /etc/uptimeforge/agent.env
  chown root:uptimeforge /etc/uptimeforge/agent.env
  chmod 0640 /etc/uptimeforge/agent.env
  cat > /etc/systemd/system/uptimeforge-agent.service <<UNIT
[Unit]
Description=UptimeForge agent
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=/etc/uptimeforge/agent.env
ExecStart=$BIN
User=uptimeforge
Restart=always
RestartSec=5
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
UNIT
  systemctl daemon-reload
  systemctl enable uptimeforge-agent >/dev/null
  systemctl restart uptimeforge-agent
  echo "uptimeforge-agent is running: journalctl -u uptimeforge-agent -f"
  exit 0
fi

# No systemd: Entware (/opt/etc/init.d) or plain init.d (OpenWrt).
initdir=/etc/init.d
[ -d /opt/etc/init.d ] && initdir=/opt/etc/init.d
cat > "$initdir/S99uptimeforge-agent" <<INIT
#!/bin/sh
case "\$1" in
  start) UF_HUB='$UF_HUB' UF_TOKEN='$UF_TOKEN' nohup $BIN >/tmp/uptimeforge-agent.log 2>&1 & ;;
  stop) killall uptimeforge-agent 2>/dev/null ;;
  restart) "\$0" stop; sleep 1; "\$0" start ;;
esac
INIT
chmod 0700 "$initdir/S99uptimeforge-agent"
"$initdir/S99uptimeforge-agent" restart
echo "uptimeforge-agent started via $initdir/S99uptimeforge-agent (log: /tmp/uptimeforge-agent.log)"
