#!/usr/bin/env bash
# Builds bun-dap-v<bun version>.js: Bun's debug adapter packages
# (packages/bun-debug-adapter-protocol and packages/bun-inspector-protocol)
# at one pinned Bun commit, with main.ts as the DAP-over-TCP entry, bundled
# into one file by the same Bun release. The output is reproducible: the
# release workflow builds it, and the sha256 printed last must equal
# tools.BunDAPSHA256 in internal/harness/tools/pins.go.
#
#   BUN=/path/to/bun build/bun-dap/build.sh [out dir]
#
# BUN must be Bun BUN_VERSION (the pinned bun-linux-<arch>.zip). The packages
# install from their own bun.lock with --frozen-lockfile.
set -euo pipefail

BUN_VERSION=1.3.14
BUN_COMMIT=0d9b296af33f2b851fcbf4df3e9ec89751734ba4 # tag bun-v1.3.14

here=$(cd "$(dirname "$0")" && pwd)
out=${1:-$here/dist}
mkdir -p "$out"
out=$(cd "$out" && pwd)
bun=${BUN:-bun}

got=$("$bun" --version)
if [ "$got" != "$BUN_VERSION" ]; then
    echo "build.sh: $bun is Bun $got; build with Bun $BUN_VERSION so the output matches the pin" >&2
    exit 1
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
src=$work/bun
git init -q "$src"
git -C "$src" remote add origin https://github.com/oven-sh/bun.git
git -C "$src" sparse-checkout set --no-cone packages/bun-debug-adapter-protocol packages/bun-inspector-protocol
git -C "$src" fetch -q --depth 1 origin "$BUN_COMMIT"
git -C "$src" checkout -q FETCH_HEAD
if [ "$(git -C "$src" rev-parse HEAD)" != "$BUN_COMMIT" ]; then
    echo "build.sh: fetched commit is not $BUN_COMMIT" >&2
    exit 1
fi

for pkg in bun-inspector-protocol bun-debug-adapter-protocol; do
    (cd "$src/packages/$pkg" && "$bun" install --frozen-lockfile --ignore-scripts --no-progress >/dev/null)
done
mkdir -p "$src/packages/aiman-bun-dap"
cp "$here/main.ts" "$src/packages/aiman-bun-dap/main.ts"

mkdir -p "$out"
name="bun-dap-v$BUN_VERSION.js"
# NODE_ENV production turns off the adapter's message trace on stdout.
(cd "$src/packages" && "$bun" build aiman-bun-dap/main.ts --target=bun \
    --define 'process.env.NODE_ENV="production"' --outfile "$out/$name" >/dev/null)
(cd "$out" && sha256sum "$name")
