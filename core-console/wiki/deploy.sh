#!/bin/bash
# Build both wiki sites and ship to tyo. Public → wiki.example.com (open),
# internal → admin.example.com/wiki (admin-gated). Needs: pip install mkdocs-material.
set -euo pipefail
cd "$(dirname "$0")"
TYO=${TYO:-deploy-host}
./build.sh
rsync -az --delete -e ssh public/site/   root@"$TYO":/opt/ncn-core-console/wiki/public/
ssh root@"$TYO" 'mkdir -p /opt/ncn-core-console/wikiroot/wiki'
rsync -az --delete -e ssh internal/site/ root@"$TYO":/opt/ncn-core-console/wikiroot/wiki/
echo "wiki deployed (public + internal)"
