#!/usr/bin/env bash
# Acceptance test: install.sh verifies the downloaded archive against the
# release's checksums.txt before installing it.
#
# A fake curl on PATH serves a fake release from a temp directory, so the test
# needs no network. git and go are stubbed to fail, so a refused download can
# not fall back to a source build that reaches the network.
#
# checksums.txt follows the goreleaser convention: one `<sha256>  <asset>` line
# per release asset (the .tar.gz archives).
#
# Run: bash scripts/install_checksum_test.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TAG="v9.9.9"
ASSET="aiman-linux-amd64"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

FAILS=0
fail() {
	echo "FAIL: $*" >&2
	FAILS=$((FAILS + 1))
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	else
		shasum -a 256 "$1" | cut -d' ' -f1
	fi
}

write_fakes() {
	local bin="$1"
	mkdir -p "$bin"
	cat >"$bin/curl" <<'EOF'
#!/usr/bin/env bash
# Fake curl: maps GitHub release URLs onto $FAKE_RELEASE_DIR.
url="" out=""
while [ $# -gt 0 ]; do
	case "$1" in
	-o) out="$2"; shift 2 ;;
	http*) url="$1"; shift ;;
	*) shift ;;
	esac
done
echo "$url" >>"$FAKE_RELEASE_DIR/requests.log"
api="https://api.github.com/repos/bouwerp/aiman/releases"
dl="https://github.com/bouwerp/aiman/releases/download/$FAKE_TAG/"
case "$url" in
"$api/tags/$FAKE_TAG" | "$api/latest") src="$FAKE_RELEASE_DIR/release.json" ;;
"$dl"*) src="$FAKE_RELEASE_DIR/assets/${url#"$dl"}" ;;
*) exit 22 ;;
esac
[ -f "$src" ] || exit 22
if [ -n "$out" ]; then cp "$src" "$out"; else cat "$src"; fi
EOF
	printf '#!/bin/sh\necho "network disabled in test" >&2\nexit 1\n' >"$bin/git"
	cp "$bin/git" "$bin/go"
	cat >"$bin/uname" <<'EOF'
#!/bin/sh
case "$1" in -m) echo x86_64 ;; *) echo Linux ;; esac
EOF
	chmod +x "$bin/curl" "$bin/git" "$bin/go" "$bin/uname"
}

# make_release DIR BODY: archive holding a binary that prints BODY.
make_release() {
	local dir="$1" body="$2" stage
	stage="$(mktemp -d)"
	mkdir -p "$dir/assets"
	printf '#!/bin/sh\necho %s\n' "$body" >"$stage/$ASSET"
	chmod +x "$stage/$ASSET"
	tar -C "$stage" -czf "$dir/assets/$ASSET.tar.gz" "$ASSET"
	rm -rf "$stage"
	local dl="https://github.com/bouwerp/aiman/releases/download/$TAG"
	cat >"$dir/release.json" <<EOF
{
  "tag_name": "$TAG",
  "assets": [
    {"name": "$ASSET.tar.gz",
      "browser_download_url": "$dl/$ASSET.tar.gz"},
    {"name": "checksums.txt",
      "browser_download_url": "$dl/checksums.txt"}
  ]
}
EOF
}

# run_install CASE: runs install.sh against $WORK/CASE, prints its exit status.
run_install() {
	local c="$WORK/$1" status=0
	write_fakes "$c/fakebin"
	mkdir -p "$c/home"
	HOME="$c/home" PATH="$c/fakebin:$PATH" SHELL=/bin/bash \
		FAKE_RELEASE_DIR="$c/release" FAKE_TAG="$TAG" \
		INSTALL_DIR="$c/home/bin" VERSION="$TAG" \
		bash "$ROOT/install.sh" >"$c/out.log" 2>&1 || status=$?
	echo "$status"
}

case_good() {
	make_release "$WORK/good/release" good-binary
	echo "$(sha256 "$WORK/good/release/assets/$ASSET.tar.gz")  $ASSET.tar.gz" >"$WORK/good/release/assets/checksums.txt"
	local status
	status="$(run_install good)"
	[ "$status" = 0 ] || fail "good release: install.sh exited $status (see below)"
	if [ ! -x "$WORK/good/home/bin/aiman" ] || ! grep -q good-binary "$WORK/good/home/bin/aiman"; then
		fail "good release: binary not installed"
	fi
}

case_tampered() {
	make_release "$WORK/tampered/release" good-binary
	echo "$(sha256 "$WORK/tampered/release/assets/$ASSET.tar.gz")  $ASSET.tar.gz" >"$WORK/tampered/release/assets/checksums.txt"
	make_release "$WORK/tampered/release" evil-binary # replace the archive, keep the old sum
	local status
	status="$(run_install tampered)"
	[ "$status" != 0 ] || fail "tampered archive: install.sh exited 0"
	[ ! -e "$WORK/tampered/home/bin/aiman" ] || fail "tampered archive: binary was installed"
}

case_no_checksums() {
	make_release "$WORK/nosums/release" good-binary
	local status
	status="$(run_install nosums)"
	[ "$status" != 0 ] || fail "missing checksums.txt: install.sh exited 0"
	[ ! -e "$WORK/nosums/home/bin/aiman" ] || fail "missing checksums.txt: binary was installed"
}

case_good
case_tampered
case_no_checksums

if [ "$FAILS" -gt 0 ]; then
	for c in good tampered nosums; do
		echo "--- install.sh output ($c) ---" >&2
		tail -n 5 "$WORK/$c/out.log" >&2 || true
	done
	echo "$FAILS check(s) failed" >&2
	exit 1
fi
echo "PASS: install.sh checksum verification"
