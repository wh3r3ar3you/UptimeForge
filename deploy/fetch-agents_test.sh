#!/bin/sh
# Exercises deploy/fetch-agents.sh against a local directory (file:// URLs)
# holding fake agents and a checksums.txt signed with a throwaway key.
# Run from the repo root: sh deploy/fetch-agents_test.sh
set -eu
root=$(pwd)
tmp=$(mktemp -d)
urlroot=$(cd "$tmp" && { pwd -W 2>/dev/null || pwd; })
trap 'rm -rf "$tmp"' EXIT
names="uptimeforge-agent_linux_amd64 uptimeforge-agent_linux_arm64 uptimeforge-agent_linux_armv7 uptimeforge-agent_linux_mips uptimeforge-agent_linux_mipsle"

go build -o "$tmp/sign" ./tools/sign
"$tmp/sign" keygen >"$tmp/priv" 2>"$tmp/pub"
UF_RELEASE_PUBKEYS=$(cat "$tmp/pub")
export UF_RELEASE_PUBKEYS
export VERIFY="$tmp/sign verify"

rel="$tmp/rel"
mkdir "$rel"
for n in $names; do echo "fake $n" >"$rel/$n"; done
echo "unrelated" >"$rel/uptimeforge-hub_linux_amd64"
(cd "$rel" && sha256sum uptimeforge-* >checksums.txt)
UF_RELEASE_KEY=$(cat "$tmp/priv") "$tmp/sign" "$rel/checksums.txt"

fail=0
expect_fail() { # name, expected message, then command
	label=$1; want=$2; shift 2
	if "$@" >"$tmp/log" 2>&1; then echo "FAIL: $label succeeded"; cat "$tmp/log"; fail=1; elif ! grep -q "$want" "$tmp/log"; then echo "FAIL: $label failed for another reason"; cat "$tmp/log"; fail=1; else echo "ok: $label failed as it must: $(tail -1 "$tmp/log")"; fi
}

# 1. Everything right.
sh "$root/deploy/fetch-agents.sh" "file://$urlroot/rel" "$tmp/out1" >"$tmp/log" 2>&1 || { echo "FAIL: good release rejected"; cat "$tmp/log"; fail=1; }
for n in $names checksums.txt checksums.txt.sig; do [ -s "$tmp/out1/$n" ] || { echo "FAIL: $n missing from output"; fail=1; }; done
cmp -s "$rel/checksums.txt" "$tmp/out1/checksums.txt" && cmp -s "$rel/checksums.txt.sig" "$tmp/out1/checksums.txt.sig" || { echo "FAIL: checksums not stored as downloaded"; fail=1; }
echo "ok: good release fetched"

# 2. A binary changed after signing: sha256 mismatch.
cp -r "$rel" "$tmp/rel2"; echo tampered >>"$tmp/rel2/uptimeforge-agent_linux_mips"
expect_fail "sha256 mismatch" "sha256 mismatch" sh "$root/deploy/fetch-agents.sh" "file://$urlroot/rel2" "$tmp/out2"

# 3. A missing binary.
cp -r "$rel" "$tmp/rel3"; rm "$tmp/rel3/uptimeforge-agent_linux_armv7"
expect_fail "missing binary" "cannot download" sh "$root/deploy/fetch-agents.sh" "file://$urlroot/rel3" "$tmp/out3"

# 4. Missing signature.
cp -r "$rel" "$tmp/rel4"; rm "$tmp/rel4/checksums.txt.sig"
expect_fail "missing signature" "cannot download" sh "$root/deploy/fetch-agents.sh" "file://$urlroot/rel4" "$tmp/out4"

# 5. checksums.txt rewritten to match a tampered binary: the signature no longer fits.
cp -r "$rel" "$tmp/rel5"; echo tampered >>"$tmp/rel5/uptimeforge-agent_linux_amd64"
(cd "$tmp/rel5" && sha256sum uptimeforge-* >checksums.txt)
expect_fail "forged checksums.txt" "signature check failed" sh "$root/deploy/fetch-agents.sh" "file://$urlroot/rel5" "$tmp/out5"

# 6. Signed by another key.
"$tmp/sign" keygen >"$tmp/priv2" 2>/dev/null
cp -r "$rel" "$tmp/rel6"; UF_RELEASE_KEY=$(cat "$tmp/priv2") "$tmp/sign" "$tmp/rel6/checksums.txt"
expect_fail "foreign signature" "signature check failed" sh "$root/deploy/fetch-agents.sh" "file://$urlroot/rel6" "$tmp/out6"

# 7. A binary absent from checksums.txt.
cp -r "$rel" "$tmp/rel7"; grep -v mipsle "$rel/checksums.txt" >"$tmp/rel7/checksums.txt"
UF_RELEASE_KEY=$(cat "$tmp/priv") "$tmp/sign" "$tmp/rel7/checksums.txt"
expect_fail "binary not in checksums.txt" "not in checksums.txt" sh "$root/deploy/fetch-agents.sh" "file://$urlroot/rel7" "$tmp/out7"

[ "$fail" -eq 0 ] && echo "all fetch-agents checks passed"
exit "$fail"
