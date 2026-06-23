#!/bin/bash
# Build both wiki sites to their site/ dirs. Needs: pip install mkdocs-material.
set -euo pipefail
cd "$(dirname "$0")"
for s in public internal; do
  ( cd "$s" && mkdocs build -d site )
  echo "built wiki/$s/site"
done
