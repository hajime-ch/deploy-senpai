#!/usr/bin/env bash
# Test harness for update.sh (run: ./update_test.sh ./update.sh). Serves a fabricated GitHub release API + asset
# from a local HTTP server, so every case is offline and deterministic.
set -uo pipefail

SCRIPT="$1"            # path to update.sh under test
WORK="$(mktemp -d)"
PORT=18211
pass=0; fail=0
trap 'rm -rf "$WORK"; pkill -f "http.server $PORT" 2>/dev/null' EXIT

# A stand-in for the released binary: prints whatever version we bake in.
make_asset() {   # $1 = version the fake binary reports, $2 = output tarball
  local d="$WORK/pkg"; rm -rf "$d"; mkdir -p "$d"
  printf '#!/bin/sh\n[ "$1" = version ] && echo "%s"\n' "$1" > "$d/deploy-senpai"
  chmod +x "$d/deploy-senpai"
  tar czf "$2" -C "$d" deploy-senpai
}

# Fabricate the release JSON the script will parse.
make_release() { # $1 = tag, $2 = asset file, $3 = digest to advertise
  local name; name="$(basename "$2")"
  cat > "$WORK/srv/release.json" <<JSON
{"tag_name": "$1", "assets": [
  {"name": "$name",
   "browser_download_url": "http://127.0.0.1:$PORT/$name",
   "digest": "sha256:$3"}
]}
JSON
}

sha_of() { shasum -a 256 "$1" 2>/dev/null | cut -d' ' -f1 || sha256sum "$1" | cut -d' ' -f1; }

check() { # $1 = name, $2 = expected exit, $3 = expected substring, rest = env+cmd
  local name="$1" want="$2" needle="$3"; shift 3
  local out; out="$("$@" 2>&1)"; local got=$?
  if [ "$got" = "$want" ] && grep -qi -- "$needle" <<<"$out"; then
    echo "PASS  $name"; pass=$((pass+1))
  else
    echo "FAIL  $name (exit $got, want $want; looking for '$needle')"
    sed 's/^/        /' <<<"$out"; fail=$((fail+1))
  fi
}

mkdir -p "$WORK/srv"
( cd "$WORK/srv" && exec python3 -m http.server $PORT >/dev/null 2>&1 ) &
for _ in $(seq 1 40); do curl -s "http://127.0.0.1:$PORT/" >/dev/null 2>&1 && break; done

OS=$(uname -s | tr 'A-Z' 'a-z')
ARCH=$(uname -m); [ "$ARCH" = "x86_64" ] && ARCH=amd64; [ "$ARCH" = "aarch64" ] && ARCH=arm64
[ "$ARCH" = "arm64" ] || ARCH=amd64
ASSET="$WORK/srv/deploy-senpai-$OS-$ARCH.tar.gz"

# --- case 1: happy path installs and reports old -> new ---
make_asset "0.9.9" "$ASSET"; make_release "v0.9.9" "$ASSET" "$(sha_of "$ASSET")"
printf '#!/bin/sh\n[ "$1" = version ] && echo 0.4.0\n' > "$WORK/bin"; chmod +x "$WORK/bin"
check "installs and reports old -> new" 0 "0.4.0 -> 0.9.9" \
  env RELEASE_URL="http://127.0.0.1:$PORT/release.json" BIN="$WORK/bin" SKIP_RESTART=1 bash "$SCRIPT"
if [ "$("$WORK/bin" version)" = "0.9.9" ]; then echo "PASS  installed binary is the new one"; pass=$((pass+1))
else echo "FAIL  installed binary still reports $("$WORK/bin" version)"; fail=$((fail+1)); fi

# --- case 2: a wrong digest must abort before touching BIN ---
make_asset "0.9.9" "$ASSET"; make_release "v0.9.9" "$ASSET" "$(printf 'd%.0s' {1..64})"
printf '#!/bin/sh\n[ "$1" = version ] && echo 0.4.0\n' > "$WORK/bin"; chmod +x "$WORK/bin"
check "refuses on digest mismatch" 1 "checksum" \
  env RELEASE_URL="http://127.0.0.1:$PORT/release.json" BIN="$WORK/bin" SKIP_RESTART=1 bash "$SCRIPT"
if [ "$("$WORK/bin" version)" = "0.4.0" ]; then echo "PASS  BIN untouched after mismatch"; pass=$((pass+1))
else echo "FAIL  BIN was modified despite a bad digest"; fail=$((fail+1)); fi

# --- case 3: binary that reports the wrong version is rejected ---
make_asset "1.2.3" "$ASSET"; make_release "v0.9.9" "$ASSET" "$(sha_of "$ASSET")"
printf '#!/bin/sh\n[ "$1" = version ] && echo 0.4.0\n' > "$WORK/bin"; chmod +x "$WORK/bin"
check "rejects a binary reporting the wrong version" 1 "version" \
  env RELEASE_URL="http://127.0.0.1:$PORT/release.json" BIN="$WORK/bin" SKIP_RESTART=1 bash "$SCRIPT"

# --- case 4: --check reports without installing ---
make_asset "0.9.9" "$ASSET"; make_release "v0.9.9" "$ASSET" "$(sha_of "$ASSET")"
printf '#!/bin/sh\n[ "$1" = version ] && echo 0.4.0\n' > "$WORK/bin"; chmod +x "$WORK/bin"
check "--check reports the available version" 0 "0.9.9" \
  env RELEASE_URL="http://127.0.0.1:$PORT/release.json" BIN="$WORK/bin" bash "$SCRIPT" --check
if [ "$("$WORK/bin" version)" = "0.4.0" ]; then echo "PASS  --check installed nothing"; pass=$((pass+1))
else echo "FAIL  --check modified BIN"; fail=$((fail+1)); fi

# --- case 5: no asset for this platform is a clear error ---
rm -f "$ASSET"; make_asset "0.9.9" "$WORK/srv/deploy-senpai-plan9-sparc.tar.gz"
make_release "v0.9.9" "$WORK/srv/deploy-senpai-plan9-sparc.tar.gz" "$(sha_of "$WORK/srv/deploy-senpai-plan9-sparc.tar.gz")"
check "clear error when no asset matches the platform" 1 "$OS" \
  env RELEASE_URL="http://127.0.0.1:$PORT/release.json" BIN="$WORK/bin" SKIP_RESTART=1 bash "$SCRIPT"

# --- case 6: releases older than v0.4.0 report "dev", and must still install ---
make_asset "dev" "$ASSET"; make_release "v0.3.0" "$ASSET" "$(sha_of "$ASSET")"
printf '#!/bin/sh\n[ "$1" = version ] && echo 0.4.0\n' > "$WORK/bin"; chmod +x "$WORK/bin"
check "installs an unstamped (pre-0.4.0) release with a warning" 0 "predates version stamping" \
  env RELEASE_URL="http://127.0.0.1:$PORT/release.json" BIN="$WORK/bin" SKIP_RESTART=1 bash "$SCRIPT"
if [ "$("$WORK/bin" version)" = "dev" ]; then echo "PASS  rollback to an unstamped release works"; pass=$((pass+1))
else echo "FAIL  unstamped release was not installed"; fail=$((fail+1)); fi

echo "=== $pass passed, $fail failed ==="
[ "$fail" = 0 ]
