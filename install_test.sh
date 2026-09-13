#!/usr/bin/env bash
# Test harness for install.sh (run: ./install_test.sh ./install.sh).
#
# install_stub.py stands in for GitHub's release endpoints, so every case runs
# offline and deterministically — including the redirect that resolves "latest".
set -uo pipefail

SCRIPT="${1:-./install.sh}"
STUB="$(dirname "$0")/install_stub.py"
WORK="$(mktemp -d)"
PORT=18211
pass=0; fail=0
stub_pid=""

cleanup() { [ -n "$stub_pid" ] && kill "$stub_pid" 2>/dev/null; rm -rf "$WORK"; }
trap cleanup EXIT

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$(uname -m)" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) arch=amd64 ;; esac
ASSET="deploy-senpai-${os}-${arch}.tar.gz"

start_stub() { # $1 = checksums mode, $2 = version the packaged binary reports, $3 = asset name
  [ -n "$stub_pid" ] && { kill "$stub_pid" 2>/dev/null; wait "$stub_pid" 2>/dev/null; }
  TAG="${TAG_OVERRIDE:-v0.9.9}" ASSET="${3:-$ASSET}" CHECKSUMS="$1" BODY_VERSION="$2" \
    python3 "$STUB" "$PORT" & stub_pid=$!
  for _ in $(seq 1 50); do curl -s "http://127.0.0.1:$PORT/releases/latest" >/dev/null 2>&1 && return; done
  echo "stub did not start" >&2; exit 1
}

existing() { # write a fake installed binary reporting $1
  printf '#!/bin/sh\n[ "$1" = version ] && echo %s\n' "$1" > "$WORK/bin/deploy-senpai"
  chmod +x "$WORK/bin/deploy-senpai"
}

run() { # run install.sh against the stub with a clean INSTALL_DIR
  env RELEASES_BASE="http://127.0.0.1:$PORT/releases" \
      API_BASE="http://127.0.0.1:$PORT/api" \
      INSTALL_DIR="$WORK/bin" SKIP_RESTART=1 bash "$SCRIPT" "$@"
}

check() { # $1 name, $2 want exit, $3 needle, rest: command
  local name="$1" want="$2" needle="$3"; shift 3
  # stdin from /dev/null so a stray sudo prompt fails instead of hanging.
  local out; out="$("$@" 2>&1 </dev/null)"; local got=$?
  if [ "$got" = "$want" ] && { [ -z "$needle" ] || grep -qi -- "$needle" <<<"$out"; }; then
    echo "PASS  $name"; pass=$((pass+1))
  else
    echo "FAIL  $name (exit $got, want $want; looking for '$needle')"
    sed 's/^/        /' <<<"$out"; fail=$((fail+1))
  fi
}

assert_reports() { # $1 name, $2 expected version
  local got; got="$("$WORK/bin/deploy-senpai" version 2>/dev/null)"
  if [ "$got" = "$2" ]; then echo "PASS  $1"; pass=$((pass+1))
  else echo "FAIL  $1 (binary reports '$got', want '$2')"; fail=$((fail+1)); fi
}

mkdir -p "$WORK/bin"

# --- a machine with nothing installed yet ------------------------------------
start_stub good 0.9.9
rm -f "$WORK/bin/deploy-senpai"
check "installs onto a machine with no existing binary" 0 "0.9.9" run
assert_reports "fresh install put the binary in INSTALL_DIR" "0.9.9"

# --- upgrading an existing install -------------------------------------------
existing 0.4.0
check "upgrade reports old -> new" 0 "0.4.0 -> 0.9.9" run
assert_reports "upgrade replaced the binary" "0.9.9"

# --- INSTALL_DIR is honoured --------------------------------------------------
start_stub good 0.9.9
mkdir -p "$WORK/elsewhere"
check "honours INSTALL_DIR" 0 "$WORK/elsewhere" \
  env RELEASES_BASE="http://127.0.0.1:$PORT/releases" API_BASE="http://127.0.0.1:$PORT/api" \
      INSTALL_DIR="$WORK/elsewhere" SKIP_RESTART=1 bash "$SCRIPT"
if [ -x "$WORK/elsewhere/deploy-senpai" ]; then echo "PASS  binary landed in INSTALL_DIR"; pass=$((pass+1))
else echo "FAIL  nothing installed into INSTALL_DIR"; fail=$((fail+1)); fi

# --- verification against checksums.txt --------------------------------------
start_stub bad 0.9.9
existing 0.4.0
check "refuses when checksums.txt disagrees" 1 "checksum" run
assert_reports "binary untouched after a checksum mismatch" "0.4.0"

# --- releases with no checksums.txt fall back to the API digest --------------
start_stub missing 0.9.9
existing 0.4.0
check "falls back to the API digest when checksums.txt is absent" 0 "0.9.9" run
assert_reports "fallback still installed the binary" "0.9.9"

# --- the packaged binary must be what the tag claims -------------------------
start_stub good 1.2.3
existing 0.4.0
check "rejects a binary reporting the wrong version" 1 "version" run
assert_reports "binary untouched after a version mismatch" "0.4.0"

# --- pre-0.4.0 releases report "dev" and must still install ------------------
TAG_OVERRIDE=v0.3.0 start_stub good dev
existing 0.4.0
check "installs an unstamped (pre-0.4.0) release with a warning" 0 "predates version stamping" \
  env RELEASES_BASE="http://127.0.0.1:$PORT/releases" API_BASE="http://127.0.0.1:$PORT/api" \
      INSTALL_DIR="$WORK/bin" SKIP_RESTART=1 bash "$SCRIPT" --version v0.3.0
unset TAG_OVERRIDE

# --- --check changes nothing --------------------------------------------------
start_stub good 0.9.9
existing 0.4.0
check "--check reports the available version" 0 "0.9.9" run --check
assert_reports "--check installed nothing" "0.4.0"

# --- no asset for this platform ----------------------------------------------
start_stub good 0.9.9 "deploy-senpai-plan9-sparc.tar.gz"
existing 0.4.0
check "clear error when no asset matches the platform" 1 "$os" run

echo "=== $pass passed, $fail failed ==="
[ "$fail" = 0 ]
