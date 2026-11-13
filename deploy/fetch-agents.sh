#!/bin/sh
# Fetches the release agent binaries for the hub image, with the release
# checksums.txt and its signature, and checks them. Any failure fails the
# image build: a hub must not serve agents an update cannot verify.
#
#   fetch-agents.sh <release download URL> <output dir>
#
# VERIFY is the command that checks <file>.sig of the file it is given
# (default: go run ./tools/sign verify, which uses the release keys built
# into the agent, or UF_RELEASE_PUBKEYS). checksums.txt and its .sig are
# stored as downloaded.
set -eu

[ $# -eq 2 ] && [ -n "$1" ] || { echo "usage: fetch-agents.sh <release URL> <output dir>" >&2; exit 2; }
base=${1%/}
out=$2
verify=${VERIFY:-go run ./tools/sign verify}
names="uptimeforge-agent_linux_amd64 uptimeforge-agent_linux_arm64 uptimeforge-agent_linux_armv7 uptimeforge-agent_linux_mips uptimeforge-agent_linux_mipsle"

get() { # url dest
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --retry 3 -o "$2" "$1"
	else
		wget -q -O "$2" "$1"
	fi
}

mkdir -p "$out"
for f in checksums.txt checksums.txt.sig $names; do
	get "$base/$f" "$out/$f" || { echo "fetch-agents: cannot download $base/$f" >&2; exit 1; }
	[ -s "$out/$f" ] || { echo "fetch-agents: $f is empty" >&2; exit 1; }
done

# The signature first: the sha256 lines are only worth trusting once signed.
$verify "$out/checksums.txt" || { echo "fetch-agents: checksums.txt signature check failed" >&2; exit 1; }

for f in $names; do
	want=$(awk -v n="$f" '{ name = $2; sub(/^[*]/, "", name) } name == n { print $1; exit }' "$out/checksums.txt")
	[ -n "$want" ] || { echo "fetch-agents: $f is not in checksums.txt" >&2; exit 1; }
	got=$(sha256sum "$out/$f" | awk '{ print $1 }')
	[ "$got" = "$want" ] || { echo "fetch-agents: sha256 mismatch for $f" >&2; exit 1; }
	chmod 0755 "$out/$f"
done
echo "fetch-agents: 5 agents verified in $out"
