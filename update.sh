#!/usr/bin/env bash
#
# Update the deploy-senpai binary from a GitHub release.
#
#   ./update.sh                 install the latest release
#   ./update.sh --check         report what is available, change nothing
#   ./update.sh --version v0.4.0  install a specific release
#
# Overrides, mostly for testing: BIN (install path), SERVICE (systemd unit),
# SKIP_RESTART=1 (don't touch systemd), RELEASE_URL (release document to read).
set -euo pipefail

REPO="hajime-ch/deploy-senpai"
BIN="${BIN:-/usr/local/bin/deploy-senpai}"
SERVICE="${SERVICE:-deploy-senpai}"

die() { echo "Error: $*" >&2; exit 1; }

# --- arguments ---------------------------------------------------------------
want_tag=""
check_only=0
while [ $# -gt 0 ]; do
  case "$1" in
    --check)   check_only=1; shift ;;
    --version) want_tag="${2:-}"; [ -n "$want_tag" ] || die "--version needs a tag, e.g. --version v0.4.0"; shift 2 ;;
    -h|--help) sed -n '2,9p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
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
# Parse JSON with jq when present, python3 otherwise. Hand-rolled grep parsing
# is how you end up installing the wrong asset.
json() { # $1 = jq filter, $2 = equivalent python expression over `d`
  if command -v jq >/dev/null 2>&1; then
    jq -r "$1" <<<"$release"
  elif command -v python3 >/dev/null 2>&1; then
    python3 -c "import json,sys; d=json.load(sys.stdin); print($2)" <<<"$release"
  else
    die "need jq or python3 to read the release metadata"
  fi
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1
  else shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

normalize() { printf '%s' "${1#v}"; }   # release tags are vX.Y.Z, the binary prints X.Y.Z

# Only escalate when the target actually needs it, so running as root or
# installing into a user-owned prefix never prompts for a password.
as_root() {
  if [ "$(id -u)" = 0 ] || [ -w "$(dirname "$BIN")" ]; then "$@"
  else sudo "$@"
  fi
}

# --- find the release --------------------------------------------------------
if [ -n "$want_tag" ]; then
  url="${RELEASE_URL:-https://api.github.com/repos/${REPO}/releases/tags/${want_tag}}"
else
  url="${RELEASE_URL:-https://api.github.com/repos/${REPO}/releases/latest}"
fi

release="$(curl -sfL -H "Accept: application/vnd.github+json" "$url")" \
  || die "could not fetch release metadata from $url"

tag="$(json '.tag_name' 'd["tag_name"]')"
[ -n "$tag" ] && [ "$tag" != "null" ] || die "release metadata has no tag_name"

download_url="$(json ".assets[] | select(.name == \"$asset\") | .browser_download_url" \
  "next((a['browser_download_url'] for a in d['assets'] if a['name'] == '$asset'), ''))")"
[ -n "$download_url" ] && [ "$download_url" != "null" ] \
  || die "release $tag has no asset for ${os}/${arch} (expected $asset)"

# GitHub publishes a SHA-256 per asset. This catches a truncated or corrupted
# download; it does not prove the release itself is trustworthy, since the
# digest and the file come from the same place.
digest="$(json ".assets[] | select(.name == \"$asset\") | .digest" \
  "next((a.get('digest') or '' for a in d['assets'] if a['name'] == '$asset'), ''))")"

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

# --- download and verify -----------------------------------------------------
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "Downloading $tag ($asset)..."
curl -sfL "$download_url" -o "$tmp/release.tar.gz" || die "download failed: $download_url"

if [ -n "$digest" ] && [ "$digest" != "null" ]; then
  want="${digest#sha256:}"
  got="$(sha256_of "$tmp/release.tar.gz")"
  [ "$want" = "$got" ] || die "checksum mismatch for $asset: expected $want, got $got"
  echo "Checksum OK."
else
  echo "Warning: the release advertises no checksum for $asset — installing unverified." >&2
fi

tar -xzf "$tmp/release.tar.gz" -C "$tmp" || die "could not extract $asset"
[ -x "$tmp/deploy-senpai" ] || die "archive did not contain a deploy-senpai binary"

# Run it before trusting it: catches a wrong-arch or mismatched build while it
# is still in the temp directory.
reported="$("$tmp/deploy-senpai" version 2>/dev/null || true)"
if [ "$reported" = "dev" ]; then
  # Releases before v0.4.0 were built without -X main.version, so they cannot
  # identify themselves. Still installable — rolling back to one is legitimate.
  echo "Warning: $tag predates version stamping and reports 'dev'; cannot confirm the build." >&2
elif [ "$reported" != "$(normalize "$tag")" ]; then
  die "downloaded binary reports version '${reported:-nothing}', expected '$(normalize "$tag")'"
fi

# --- install -----------------------------------------------------------------
backup=""
if [ -e "$BIN" ]; then
  backup="${BIN}.old"
  as_root cp -p "$BIN" "$backup"
fi

echo "Installing to $BIN..."
as_root install -m 755 "$tmp/deploy-senpai" "$BIN"

# --- restart, and undo the update if the service does not come back ----------
if [ "${SKIP_RESTART:-0}" != 1 ] && command -v systemctl >/dev/null 2>&1; then
  echo "Restarting $SERVICE..."
  as_root systemctl restart "$SERVICE" || true
  sleep 2
  if ! as_root systemctl is-active --quiet "$SERVICE"; then
    echo "Error: $SERVICE did not come back up — rolling back." >&2
    if [ -n "$backup" ]; then
      as_root install -m 755 "$backup" "$BIN"
      as_root systemctl restart "$SERVICE" || true
    fi
    exit 1
  fi
fi

echo "Updated: $current -> $("$BIN" version)"
