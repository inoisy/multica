#!/bin/sh
# Verify the unpacked bundle and install its pinned backend/web.
set -eu

bundle=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
if [ "$(id -u)" -ne 0 ]; then
  echo "Run with sudo: sudo sh $0" >&2
  exit 1
fi
if [ ! -f "$bundle/acceptance.json" ]; then
  echo "Missing acceptance.json; use the release report for this bundle's SHA." >&2
  exit 1
fi
cd "$bundle"
sha256sum --check --strict SHA256SUMS
python3 "$bundle/deploy.py" check "$@"
exec python3 "$bundle/deploy.py" install --acceptance "$bundle/acceptance.json" "$@"
