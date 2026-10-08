#!/usr/bin/env bash
#
# Install or update the deploy-senpai binary from a GitHub release.
#
#   ./install.sh                    install the latest release
#   ./install.sh --check            report what is available, change nothing
#   ./install.sh --version v0.4.0   install a specific release
#
# Needs only curl, tar, grep and sha256sum/shasum. If a systemd unit for the
# service exists, it is restarted — and rolled back if it does not come back up.
#
# Environment: INSTALL_DIR (default /usr/local/bin), SERVICE, SKIP_RESTART=1,
# and RELEASES_BASE / API_BASE for testing.
set -euo pipefail

REPO="hajime-ch/deploy-senpai"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
BIN="${BIN:-$INSTALL_DIR/deploy-senpai}"
SERVICE="${SERVICE:-deploy-senpai}"
RELEASES_BASE="${RELEASES_BASE:-https://github.com/${REPO}/releases}"
API_BASE="${API_BASE:-https://api.github.com}"

die() { echo "Error: $*" >&2; exit 1; }

# --- arguments ---------------------------------------------------------------
want_tag=""
check_only=0
while [ $# -gt 0 ]; do
  case "$1" in
    --check)   check_only=1; shift ;;
    --version) want_tag="${2:-}"; [ -n "$want_tag" ] || die "--version needs a tag, e.g. --version v0.4.0"; shift 2 ;;
    -h|--help) sed -n '2,13p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *)         die "unknown argument: $1" ;;
  esac
done

# --- platform ----------------------------------------------------------------
os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$(uname -m)" in
  x86_64|amd64)  arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *)             die "unsupported architecture: $(uname -m)" ;;
esac
asset="deploy-senpai-${os}-${arch}.tar.gz"

# --- helpers -----------------------------------------------------------------
sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1
  else shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

normalize() { printf '%s' "${1#v}"; }   # release tags are vX.Y.Z, the binary prints X.Y.Z

# Escalate only when the target actually needs it, so running as root or
# installing into a user-owned prefix never prompts for a password.
as_root() {
  if [ "$(id -u)" = 0 ] || [ -w "$INSTALL_DIR" ]; then "$@"
  else sudo "$@"
  fi
}

# Only used when a release predates checksums.txt. Needs a JSON parser, which
# is exactly the dependency checksums.txt exists to avoid.
digest_from_api() {
  local url="${API_BASE}/repos/${REPO}/releases/tags/${tag}" body
  body="$(curl -sfL -H "Accept: application/vnd.github+json" "$url" 2>/dev/null)" || return 0
  if command -v jq >/dev/null 2>&1; then
    jq -r --arg a "$asset" '.assets[] | select(.name == $a) | .digest // ""' <<<"$body" 2>/dev/null | sed 's/^sha256://'
  elif command -v python3 >/dev/null 2>&1; then
    python3 -c "
import json,sys
d = json.load(sys.stdin)
print(next((a.get('digest','') for a in d.get('assets',[]) if a['name'] == '$asset'), '').removeprefix('sha256:'))
" <<<"$body" 2>/dev/null
  fi
}

# --- resolve the release ------------------------------------------------------
# The /releases/latest redirect names the tag, so resolving it needs no JSON.
if [ -n "$want_tag" ]; then
  tag="$want_tag"
else
  effective="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "${RELEASES_BASE}/latest")" \
    || die "could not reach ${RELEASES_BASE}/latest"
  tag="${effective##*/}"
  [ -n "$tag" ] && [ "$tag" != "latest" ] || die "could not resolve the latest release tag"
fi

current="none"
if [ -x "$BIN" ]; then current="$("$BIN" version 2>/dev/null || echo unknown)"; fi

if [ "$check_only" = 1 ]; then
  echo "installed: $current"
  echo "available: $(normalize "$tag") ($asset)"
  exit 0
fi

if [ "$(normalize "$tag")" = "$current" ]; then
  echo "Already on $current — nothing to do."
  exit 0
fi

# --- download -----------------------------------------------------------------
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "Downloading $tag ($asset)..."
code="$(curl -sSL -o "$tmp/release.tar.gz" -w '%{http_code}' "${RELEASES_BASE}/download/${tag}/${asset}" || echo 000)"
case "$code" in
  200) ;;
  404) die "release $tag has no asset for ${os}/${arch} (expected $asset)" ;;
  *)   die "downloading $asset failed (HTTP $code)" ;;
esac

# --- verify -------------------------------------------------------------------
# checksums.txt keeps this dependency-free; the API digest covers releases
# published before it existed. Both come from the same place as the file, so
# this proves the download arrived intact — not that the release is trustworthy.
want=""
sums="$(curl -fsSL "${RELEASES_BASE}/download/${tag}/checksums.txt" 2>/dev/null || true)"
if [ -n "$sums" ]; then
  want="$(awk -v a="$asset" '$2 == a || $2 == "*" a { print $1; exit }' <<<"$sums")"
fi
[ -n "$want" ] || want="$(digest_from_api)"
[ -n "$want" ] || die "release $tag publishes no checksum for $asset, and no JSON parser is available to read one from the API — refusing to install unverified"

got="$(sha256_of "$tmp/release.tar.gz")"
[ "$want" = "$got" ] || die "checksum mismatch for $asset: expected $want, got $got"
echo "Checksum OK ($got)."

tar -xzf "$tmp/release.tar.gz" -C "$tmp" || die "could not extract $asset"
[ -x "$tmp/deploy-senpai" ] || die "archive did not contain a deploy-senpai binary"

# Run it before trusting it: catches a wrong-arch or mismatched build while it
# is still in the temp directory.
reported="$("$tmp/deploy-senpai" version 2>/dev/null || true)"
if [ "$reported" = "dev" ]; then
  echo "Warning: $tag predates version stamping and reports 'dev'; cannot confirm the build." >&2
elif [ "$reported" != "$(normalize "$tag")" ]; then
  die "downloaded binary reports version '${reported:-nothing}', expected '$(normalize "$tag")'"
fi

# --- install ------------------------------------------------------------------
[ -d "$INSTALL_DIR" ] || as_root install -d -m 755 "$INSTALL_DIR"

backup=""
if [ -e "$BIN" ]; then
  backup="${BIN}.old"
  as_root cp -p "$BIN" "$backup"
fi

as_root install -m 755 "$tmp/deploy-senpai" "$BIN"

# --- restart the service, if there is one -------------------------------------
restarted=0
if [ "${SKIP_RESTART:-0}" != 1 ] && command -v systemctl >/dev/null 2>&1 \
   && systemctl cat "$SERVICE" >/dev/null 2>&1; then
  echo "Restarting $SERVICE..."
  as_root systemctl restart "$SERVICE" || true
  sleep 2
  if as_root systemctl is-active --quiet "$SERVICE"; then
    restarted=1
  else
    echo "Error: $SERVICE did not come back up — rolling back." >&2
    if [ -n "$backup" ]; then
      as_root install -m 755 "$backup" "$BIN"
      as_root systemctl restart "$SERVICE" || true
    fi
    exit 1
  fi
fi

# --- report exactly what landed where -----------------------------------------
if [ "$current" = "none" ]; then
  echo "Installed $(normalize "$tag") -> $BIN"
else
  echo "Updated: $current -> $("$BIN" version) ($BIN)"
  echo "Previous binary kept at $backup"
fi
echo "sha256: $got"

if [ "$restarted" = 0 ] && [ "${SKIP_RESTART:-0}" != 1 ]; then
  echo
  echo "No $SERVICE systemd unit found, so nothing was restarted."
  echo "To run the server: $BIN serve --config /etc/deployer/config.yaml"
fi
