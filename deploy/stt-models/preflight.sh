#!/usr/bin/env bash
# Read-only check before install.sh: nothing is built, stopped or changed.
#   preflight.sh --manifest PATH/manifest.json
set -euo pipefail
HERE=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=lib.sh
. "$HERE/lib.sh"

MANIFEST_PATH=
while (($#)); do
  case $1 in
    --manifest) MANIFEST_PATH=${2:?}; shift 2 ;;
    *) die "неизвестный аргумент: $1" ;;
  esac
done
[[ -n $MANIFEST_PATH ]] || die 'нужен --manifest'

require_root
load_manifest "$MANIFEST_PATH"
discover_stack

fail=0
check() { # check 'what' command...
  local what=$1; shift
  if "$@"; then log "ок: $what"; else printf '[stt-models] НЕТ: %s\n' "$what" >&2; fail=1; fi
}

archive=$(json_get "$MANIFEST" source.archive)
if [[ -n $archive ]]; then
  check "архив исходников совпадает с manifest" \
    bash -c '[[ $(sha256sum "$1" | cut -d" " -f1) == "$2" ]]' _ "$RELEASE_DIR/$archive" "$(json_get "$MANIFEST" source.archive_sha256)"
fi

running_image=$(docker inspect --format '{{.Image}}' "$BACKEND_CID")
log "backend: контейнер ${BACKEND_CID:0:12}, образ $running_image"
body=$(health_json) || die 'health не отвечает: сначала приведите текущий backend в порядок'
current=$(json_field "$body" commit)
check "работает ожидаемый коммит ($PREVIOUS_COMMIT), сейчас $current" \
  bash -c '[[ $2 == "$1"* || $1 == "$2"* ]]' _ "$PREVIOUS_COMMIT" "$current"

version=$(schema_version)
check "миграции применимы: схема $version, ожидается от $MIGRATIONS_FROM до $MIGRATIONS_TO" \
  bash -c '(($1 >= $2 && $1 <= $3))' _ "$version" "$MIGRATIONS_FROM" "$MIGRATIONS_TO"

for svc in $PROTECTED; do
  check "сервис $svc запущен и этим релизом не затрагивается" \
    bash -c '[[ -n $1 ]]' _ "$(compose ps -q "$svc" 2>/dev/null | head -n 1)"
done

pids=$(daemon_pids)
log "daemon (хост): PID ${pids:-не найден}; скрипты его не трогают"
expected=$(json_get "$MANIFEST" protected.daemon_pids | python3 -I -c 'import json,sys; t=sys.stdin.read().strip(); print(" ".join(str(p) for p in (json.loads(t) if t else [])))')
for pid in $expected; do
  check "daemon PID $pid жив" pid_alive "$pid"
done

free_kb=$(df -Pk /var/lib/docker 2>/dev/null | awk 'NR==2{print $4}' || true)
check "на диске для сборки есть место (${free_kb:-?} КБ)" bash -c '[[ -z $1 ]] || (($1 > 2000000))' _ "${free_kb:-}"

if [[ -z $IMAGE_REF ]]; then
  check "в пакете есть Dockerfile для сборки" bash -c 'tar -tzf "$1" Dockerfile >/dev/null' _ "$RELEASE_DIR/$archive"
  builder=$(json_get "$MANIFEST" build.builder)
  check "есть buildx-сборщик $builder" bash -c 'docker buildx inspect "$1" >/dev/null 2>&1' _ "$builder"
fi
((fail == 0)) || die 'preflight не пройден; ничего не изменено'
log 'preflight пройден; ничего не изменено'
