#!/usr/bin/env bash
# Install the NCN CLIs (ncn-debug, ncn-login) to a bin directory.
#
#   ./install.sh                      # both CLIs → /usr/local/bin (sudo if needed)
#   ./install.sh ncn-debug            # just one
#   ./install.sh --build              # recompile from source first (reinstall latest)
#   PREFIX="$HOME/.local/bin" ./install.sh   # no-sudo install to your own dir
#
# install -m overwrites any existing binary, so re-running is a clean reinstall.
#
# Picks the matching prebuilt binary from dist/; if none exists it builds from
# source (needs Go). Installs under the clean name (no -os-arch suffix).
set -euo pipefail
cd "$(dirname "$0")"

# -b/--build forces a fresh compile from source (ignore any prebuilt dist/).
BUILD=0
ARGS=()
for a in "$@"; do
  case "$a" in
    -b | --build) BUILD=1 ;;
    *) ARGS+=("$a") ;;
  esac
done
CLIS=("${ARGS[@]}")
[ ${#CLIS[@]} -eq 0 ] && CLIS=(ncn-debug ncn-login)

# ── detect platform ──────────────────────────────────────────────────────────
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "install: unsupported arch $(uname -m)" >&2; exit 1 ;;
esac

# ── choose destination (PREFIX overrides; else /usr/local/bin, sudo if needed) ─
dest="${PREFIX:-/usr/local/bin}"
mkdir -p "$dest" 2>/dev/null || true
SUDO=""
if [ ! -w "$dest" ]; then
  if command -v sudo >/dev/null 2>&1; then
    SUDO=sudo
  else
    echo "install: no write access to $dest and no sudo." >&2
    echo "         set PREFIX to a writable dir, e.g.:  PREFIX=\"\$HOME/.local/bin\" $0" >&2
    exit 1
  fi
fi

for cli in "${CLIS[@]}"; do
  [ -d "$cli" ] || { echo "install: no such CLI '$cli'" >&2; continue; }
  src="dist/${cli}-${os}-${arch}"
  if [ "$BUILD" = 1 ] || [ ! -f "$src" ]; then
    if command -v go >/dev/null 2>&1; then
      echo "install: building $cli from source…"
      ( cd "$cli" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "../$src" . )
    elif [ ! -f "$src" ]; then
      echo "install: missing $src and no Go to build it — run ./build.sh first" >&2
      exit 1
    else
      echo "install: no Go to rebuild; installing the existing prebuilt $src" >&2
    fi
  fi
  $SUDO install -m 0755 "$src" "$dest/$cli"
  echo "installed  $cli → $dest/$cli"
done

# ── PATH hint ─────────────────────────────────────────────────────────────────
case ":$PATH:" in
  *":$dest:"*) ;;
  *) echo "note: $dest is not on your PATH — add this to your shell rc:"
     echo "      export PATH=\"$dest:\$PATH\"" ;;
esac
echo "done. try:  ncn-debug welcome"
