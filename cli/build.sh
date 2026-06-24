#!/usr/bin/env bash
# Build the NCN CLIs (ncn-debug, ncn-login) for the common platforms.
#
#   ./build.sh            # build all CLIs × all platforms → ./dist
#   ./build.sh ncn-debug  # just one CLI, all platforms
#
# Output: dist/<cli>-<os>-<arch>[.exe], plus SHA256SUMS.
set -euo pipefail
cd "$(dirname "$0")"

CLIS=("${@:-ncn-debug ncn-login}")
# shellcheck disable=SC2206
CLIS=(${CLIS[*]})

PLATFORMS=(
  "linux/amd64" "linux/arm64"
  "darwin/amd64" "darwin/arm64"
  "windows/amd64"
)

OUT="dist"
mkdir -p "$OUT"

for cli in "${CLIS[@]}"; do
  [ -d "$cli" ] || { echo "skip: no such CLI dir '$cli'"; continue; }
  rm -f "$OUT/${cli}-"*   # clear only THIS cli's stale artifacts, keep the others
  for p in "${PLATFORMS[@]}"; do
    os="${p%/*}"; arch="${p#*/}"
    ext=""; [ "$os" = "windows" ] && ext=".exe"
    bin="../$OUT/${cli}-${os}-${arch}${ext}"
    echo "build: ${cli}  ${os}/${arch}"
    ( cd "$cli" && CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
        go build -trimpath -ldflags "-s -w" -o "$bin" . )
  done
done

( cd "$OUT" && command -v sha256sum >/dev/null && sha256sum ./* > SHA256SUMS || true )
echo "done → $OUT/"
ls -1 "$OUT"
