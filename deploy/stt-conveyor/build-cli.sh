#!/usr/bin/env bash
# Builds the multica CLI (it is also the daemon and the accept runtime) for one
# commit, into a package directory with its checksum and the host scripts.
# Needs git and go, not root.
#   build-cli.sh [--revision SHA] [--out DIR] [--goos linux] [--goarch amd64]
set -euo pipefail
HERE=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO=$(git -C "$HERE" rev-parse --show-toplevel)
REVISION=HEAD OUT= GOOS_=linux GOARCH_=amd64
while (($#)); do
  case $1 in
    --revision) REVISION=${2:?}; shift 2 ;;
    --out) OUT=${2:?}; shift 2 ;;
    --goos) GOOS_=${2:?}; shift 2 ;;
    --goarch) GOARCH_=${2:?}; shift 2 ;;
    *) echo "неизвестный аргумент: $1" >&2; exit 1 ;;
  esac
done
REVISION=$(git -C "$REPO" rev-parse --verify "$REVISION^{commit}")
SHORT=${REVISION:0:9}
OUT=${OUT:-/home/agents/deploy/stt-conveyor-$SHORT}
[[ ! -e $OUT ]] || { echo "уже существует: $OUT" >&2; exit 1; }
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
git -C "$REPO" archive "$REVISION" server | tar -x -C "$WORK"
mkdir -p "$OUT"
(cd "$WORK/server" && CGO_ENABLED=0 GOOS=$GOOS_ GOARCH=$GOARCH_ go build -trimpath \
  -ldflags "-s -w -X main.version=ru-0.6.1-conveyor -X main.commit=$SHORT -X main.date=$(git -C "$REPO" show -s --format=%cI "$REVISION")" \
  -o "$OUT/multica" ./cmd/multica)
for f in daemon-install.sh daemon-rollback.sh README.md; do
  git -C "$REPO" show "$REVISION:deploy/stt-conveyor/$f" >"$OUT/$f"
done
chmod 755 "$OUT"/*.sh "$OUT/multica"
(cd "$OUT" && sha256sum multica >SHA256SUMS)
echo "пакет: $OUT"
cat "$OUT/SHA256SUMS"
echo "на хосте демона (root): $OUT/daemon-install.sh --check, затем без --check"
