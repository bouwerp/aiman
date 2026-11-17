#!/usr/bin/env bash
# Builds bun-dap with the pinned Bun (linux x64) and checks the result
# against the pins in internal/harness/tools/pins.go: the Bun zip against
# bunSHA256, the output against BunDAPSHA256. CI runs it so a change to
# main.ts or build.sh without a new pin fails; the release workflow runs it
# to make the release asset.
#
#   build/bun-dap/check.sh [out dir]
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
pins=$here/../../internal/harness/tools/pins.go
out=${1:-$here/dist}
mkdir -p "$out"
out=$(cd "$out" && pwd)

pin() { sed -n "s/^[[:space:]]*$1[[:space:]]*=[[:space:]]*\"\\([^\"]*\\)\".*/\\1/p" "$pins"; }
version=$(pin BunVersion)
dap_sum=$(pin BunDAPSHA256)
bun_sum=$(awk '/^var bunSHA256 = /{found=1} found && /"amd64"/{gsub(/[",]/,"",$2); print $2; exit}' "$pins")
if [ -z "$version" ] || [ ${#dap_sum} -ne 64 ] || [ ${#bun_sum} -ne 64 ]; then
    echo "check.sh: cannot read the Bun pins from $pins" >&2
    exit 1
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
curl -fsSL -o "$work/bun.zip" "https://github.com/oven-sh/bun/releases/download/bun-$version/bun-linux-x64.zip"
echo "$bun_sum  $work/bun.zip" | sha256sum -c --quiet -
unzip -q "$work/bun.zip" bun-linux-x64/bun -d "$work"

BUN="$work/bun-linux-x64/bun" "$here/build.sh" "$out" >/dev/null
asset="bun-dap-$version.js"
if ! echo "$dap_sum  $out/$asset" | sha256sum -c --quiet -; then
    echo "check.sh: $asset is $(sha256sum "$out/$asset" | cut -d' ' -f1), the pin BunDAPSHA256 is $dap_sum" >&2
    exit 1
fi
echo "$out/$asset"
