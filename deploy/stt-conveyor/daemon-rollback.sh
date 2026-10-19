#!/usr/bin/env bash
# Runs the rollback script of the latest daemon-install.sh on this host.
set -euo pipefail
BACKUPS=${BACKUPS:-/var/backups}
latest=$(ls -1d "$BACKUPS"/stt-conveyor-cli.* 2>/dev/null | sort | tail -1 || true)
[[ -n $latest ]] || { echo 'бэкапа daemon-install.sh на этом хосте нет' >&2; exit 1; }
exec bash "$latest/rollback.sh"
