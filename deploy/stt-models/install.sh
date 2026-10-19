#!/usr/bin/env bash
# Replaces only the backend container of the running multica stack with the
# image built from this release's source, after applying its (additive)
# migrations with the new image while the old backend still serves.
#   sudo install.sh --manifest PATH/manifest.json [--stop-provider-timer]
#
# --stop-provider-timer  once the new backend answers, disable and stop the
#                        watchdog's provider lane (multica-provider.timer): the
#                        backend now switches models itself, two writers must
#                        not run. Rollback re-enables it.
#
# It does not stop, restart or recreate PostgreSQL, the frontend or the host
# daemon, and it never signals a process: it records their container ids / PIDs
# and fails loudly if any of them changed.
set -euo pipefail
HERE=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=lib.sh
. "$HERE/lib.sh"

MANIFEST_PATH= STOP_TIMER=0
while (($#)); do
  case $1 in
    --manifest) MANIFEST_PATH=${2:?}; shift 2 ;;
    --stop-provider-timer) STOP_TIMER=1; shift ;;
    *) die "неизвестный аргумент: $1" ;;
  esac
done
[[ -n $MANIFEST_PATH ]] || die 'нужен --manifest'

require_root
"$HERE/preflight.sh" --manifest "$MANIFEST_PATH"
load_manifest "$MANIFEST_PATH"
discover_stack

BACKUP_ROOT=${STTMODELS_BACKUP_ROOT:-/var/backups}
STATE_DIR=$(mktemp -d "$BACKUP_ROOT/multica-stt-models.XXXXXXXX")
chmod 700 "$STATE_DIR"
STAMP=${STATE_DIR##*.}

PREV_IMAGE_ID=$(docker inspect --format '{{.Image}}' "$BACKEND_CID")
PREV_IMAGE_TAG="multica-backend:pre-stt-models-$STAMP"
# Keep the running image reachable by a tag of our own, so pruning cannot take
# the only way back.
docker tag "$PREV_IMAGE_ID" "$PREV_IMAGE_TAG"
DAEMON_PIDS_BEFORE=$(daemon_pids)
# shellcheck disable=SC2086
CONTAINERS_BEFORE=$(service_container_ids $PROTECTED)

{
  printf 'PROJECT=%q\nWORKDIR=%q\nCONFIG_FILES=%q\nSERVICE=%q\n' "$PROJECT" "$WORKDIR" "$CONFIG_FILES" "$SERVICE"
  printf 'PROTECTED=%q\nPREV_IMAGE_ID=%q\nPREV_IMAGE_TAG=%q\nPREV_COMMIT=%q\n' "$PROTECTED" "$PREV_IMAGE_ID" "$PREV_IMAGE_TAG" "$PREVIOUS_COMMIT"
  printf 'NEW_IMAGE_TAG=%q\nNEW_SHORT=%q\nDAEMON_PIDS_BEFORE=%q\n' "$IMAGE_TAG" "$SHORT" "$DAEMON_PIDS_BEFORE"
} >"$STATE_DIR/state.env"
install -m 600 "$MANIFEST_PATH" "$STATE_DIR/manifest.json"
install -m 700 "$HERE/lib.sh" "$STATE_DIR/lib.sh"
install -m 700 "$HERE/rollback.sh" "$STATE_DIR/rollback.sh"
log "резервные данные и откат: $STATE_DIR/rollback.sh"

if [[ -n $IMAGE_REF ]]; then
  docker pull "$IMAGE_REF"
  docker tag "$IMAGE_REF" "$IMAGE_TAG"
else
  archive=$(json_get "$MANIFEST" source.archive)
  mkdir "$STATE_DIR/src"
  tar -xzf "$RELEASE_DIR/$archive" -C "$STATE_DIR/src"
  # The stand's buildx builder, loaded into the local image store: a bare
  # `docker build` would use another builder and cache.
  docker buildx build --builder "$(json_get "$MANIFEST" build.builder)" --load -t "$IMAGE_TAG" \
    --build-arg "VERSION=$(json_get "$MANIFEST" image.version)" \
    --build-arg "COMMIT=$SHORT" \
    --build-arg "DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -f "$STATE_DIR/src/Dockerfile" "$STATE_DIR/src"
fi
NEW_IMAGE_ID=$(docker image inspect --format '{{.Id}}' "$IMAGE_TAG")
printf 'NEW_IMAGE_ID=%q\n' "$NEW_IMAGE_ID" >>"$STATE_DIR/state.env"
log "новый образ: $IMAGE_TAG ($NEW_IMAGE_ID), исходники $REVISION"

cat >"$STATE_DIR/override.yml" <<YAML
services:
  $SERVICE:
    image: $IMAGE_TAG
YAML
EXTRA_COMPOSE_FILES=("$STATE_DIR/override.yml")

# Additive migrations first, with the new image, while the old backend still
# serves: the old code ignores the new tables and column.
log 'миграции (новый образ, старый backend продолжает работать)'
compose run --rm --no-deps --entrypoint ./migrate "$SERVICE" up
version=$(schema_version)
((version >= MIGRATIONS_TO)) || die "после миграций схема $version, ожидалось не меньше $MIGRATIONS_TO; backend не заменён (откат не нужен)"

log 'замена только backend'
compose up -d --no-deps --pull never "$SERVICE"

if ! wait_healthy "$SHORT" "${STTMODELS_HEALTH_SECONDS:-120}"; then
  log "новый backend не ответил на health с коммитом $SHORT; откатываю"
  "$STATE_DIR/rollback.sh" --force || true
  exit 2
fi

status=0
if [[ $(daemon_pids) != "$DAEMON_PIDS_BEFORE" ]]; then
  printf '[stt-models] ВНИМАНИЕ: набор PID daemon изменился: было [%s], стало [%s]\n' "$DAEMON_PIDS_BEFORE" "$(daemon_pids)" >&2
  status=3
fi
# shellcheck disable=SC2086
if [[ $(service_container_ids $PROTECTED) != "$CONTAINERS_BEFORE" ]]; then
  printf '[stt-models] ВНИМАНИЕ: контейнер защищённого сервиса (%s) пересоздан\n' "$PROTECTED" >&2
  status=3
fi

TIMER=$(json_get "$MANIFEST" provider_timer)
if ((STOP_TIMER)) && [[ -n $TIMER ]]; then
  if systemctl cat "$TIMER" >/dev/null 2>&1; then
    printf 'PROVIDER_TIMER=%q\nPROVIDER_TIMER_WAS_ENABLED=%q\n' "$TIMER" "$(systemctl is-enabled "$TIMER" 2>/dev/null || true)" >>"$STATE_DIR/state.env"
    systemctl disable --now "$TIMER"
    log "полоса провайдеров сторожа выключена: $TIMER"
  else
    log "таймера $TIMER нет; пропускаю"
  fi
elif [[ -n $TIMER ]]; then
  log "ВНИМАНИЕ: $TIMER не тронут; выключите его в этом же окне (--stop-provider-timer), иначе модели меняют двое"
fi

log "готово: backend $SHORT, схема $(schema_version)"
log "откат: $STATE_DIR/rollback.sh"
log "чтобы замена пережила ручной 'docker compose up' без override, укажите образ $IMAGE_TAG в MULTICA_BACKEND_IMAGE / MULTICA_IMAGE_TAG окружения стека"
exit "$status"
