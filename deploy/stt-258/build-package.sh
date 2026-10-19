#!/usr/bin/env bash
# Assembles the operator package for one commit: the source archive the image is
# built from, a manifest that pins it, and the install / rollback scripts.
# Needs git and python3, not docker or root.
#   build-package.sh [--revision SHA] [--base SHA] [--out DIR] [--project NAME] [--daemon-pid PID]...
#
# --revision  commit to ship (default HEAD)
# --base      commit the live backend was built from (default 714994d368, the
#             runtime in service when the release was written)
# --out       package directory (default /home/agents/deploy/stt-258-<short revision>)
set -euo pipefail
HERE=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO=$(git -C "$HERE" rev-parse --show-toplevel)

REVISION=HEAD BASE=714994d368 OUT= PROJECT= DAEMON_PIDS=()
while (($#)); do
  case $1 in
    --revision) REVISION=${2:?}; shift 2 ;;
    --base) BASE=${2:?}; shift 2 ;;
    --out) OUT=${2:?}; shift 2 ;;
    --project) PROJECT=${2:?}; shift 2 ;;
    --daemon-pid) DAEMON_PIDS+=("${2:?}"); shift 2 ;;
    *) echo "неизвестный аргумент: $1" >&2; exit 1 ;;
  esac
done

REVISION=$(git -C "$REPO" rev-parse --verify "$REVISION^{commit}")
BASE=$(git -C "$REPO" rev-parse --verify "$BASE^{commit}")
SHORT=${REVISION:0:9}
OUT=${OUT:-/home/agents/deploy/stt-258-$SHORT}
[[ ! -e $OUT ]] || { echo "уже существует: $OUT" >&2; exit 1; }
git -C "$REPO" cat-file -e "$REVISION:deploy/stt-258/install.sh" 2>/dev/null || { echo 'в этом коммите нет deploy/stt-258' >&2; exit 1; }

mkdir -p "$OUT"
git -C "$REPO" archive --format=tar.gz -o "$OUT/source.tar.gz" "$REVISION" Dockerfile docker server LICENSE NOTICE
for f in lib.sh preflight.sh install.sh rollback.sh README.md; do
  git -C "$REPO" show "$REVISION:deploy/stt-258/$f" >"$OUT/$f"
done
chmod 755 "$OUT"/*.sh

migrations() { git -C "$REPO" ls-tree --name-only "$1:server/migrations/" | grep -E '^[0-9]+_.*\.up\.sql$' | sort; }
migrations "$BASE" >"$OUT/.base-migrations"
migrations "$REVISION" >"$OUT/.rev-migrations"
python3 -I - "$OUT" "$REVISION" "$BASE" "$PROJECT" "${DAEMON_PIDS[@]:-}" <<'PY'
import json, hashlib, sys, os
out, revision, base, project, *pids = sys.argv[1:]
read = lambda name: [l.strip() for l in open(os.path.join(out, name)) if l.strip()]
base_files, rev_files = read('.base-migrations'), read('.rev-migrations')
number = lambda f: int(f.split('_', 1)[0])
sha = hashlib.sha256(open(os.path.join(out, 'source.tar.gz'), 'rb').read()).hexdigest()
manifest = {
    'release': 'stt-258',
    'source': {
        'repository': 'https://github.com/inoisy/multica',
        'revision': revision,
        'base_revision': base,
        'archive': 'source.tar.gz',
        'archive_sha256': sha,
    },
    'image': {
        'tag': 'multica-backend:stt-258-' + revision[:9],
        'version': 'ru-0.6.1-stt258',
        # A registry reference pinned by digest replaces the local build when set.
        'reference': None,
    },
    'previous': {'commit': base[:9]},
    'compose': {'project': project or None, 'service': 'backend', 'protected_services': ['postgres', 'frontend']},
    'migrations': {
        'previous_max': max(map(number, base_files)),
        'target_max': max(map(number, rev_files)),
        'files': sorted(set(rev_files) - set(base_files)),
    },
    'protected': {'daemon_pids': [int(p) for p in pids if p]},
    'rollback': {
        'restores': 'previous backend image',
        'keeps_additive_schema': True,
        'new_routes_after_rollback': 'impossible: the previous image has no route endpoints',
    },
}
json.dump(manifest, open(os.path.join(out, 'manifest.json'), 'w'), indent=2, ensure_ascii=False)
open(os.path.join(out, 'manifest.json'), 'a').write('\n')
PY
rm -f "$OUT/.base-migrations" "$OUT/.rev-migrations"
echo "пакет: $OUT"
echo "установка (root): sudo $OUT/install.sh --manifest $OUT/manifest.json"
echo "проверка без изменений: sudo $OUT/preflight.sh --manifest $OUT/manifest.json"
