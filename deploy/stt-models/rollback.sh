#!/usr/bin/env bash
# Puts the previous backend image back. Run from the directory install.sh
# printed: it carries state.env with the stack and the image to return to.
#   sudo rollback.sh [--force]
#
# Only the backend container is replaced. The additive tables and columns stay:
# the previous code does not read them. Unclaimed model-routed runs go back to
# their agent's runtime, which is the only runtime the previous code claims a
# run on; a provider timer the install stopped is enabled again.
set -euo pipefail
HERE=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
[[ -r $HERE/state.env ]] || { echo '[stt-models] ОШИБКА: state.env не найден; запускайте rollback.sh из каталога, который напечатал install.sh' >&2; exit 1; }
# shellcheck source=lib.sh
. "$HERE/lib.sh"
# shellcheck disable=SC1091
. "$HERE/state.env"

while (($#)); do
  case $1 in
    --force) shift ;; # accepted for symmetry with stt-258; nothing to override
    *) die "неизвестный аргумент: $1" ;;
  esac
done

require_root
: "${PROJECT:?}" "${WORKDIR:?}" "${CONFIG_FILES:?}" "${SERVICE:?}"

cat >"$HERE/override-previous.yml" <<YAML
services:
  $SERVICE:
    image: $PREV_IMAGE_TAG
YAML
EXTRA_COMPOSE_FILES=("$HERE/override-previous.yml")
# shellcheck disable=SC2086
containers_before=$(service_container_ids $PROTECTED)

log "возвращаю backend: образ $PREV_IMAGE_TAG ($PREV_IMAGE_ID)"
compose up -d --no-deps --pull never "$SERVICE"
wait_healthy "$PREV_COMMIT" "${STTMODELS_HEALTH_SECONDS:-120}" || die "прежний backend не ответил на health с коммитом $PREV_COMMIT; проверьте 'docker compose logs $SERVICE'"

# shellcheck disable=SC2086
[[ $(service_container_ids $PROTECTED) == "$containers_before" ]] || die "контейнер защищённого сервиса ($PROTECTED) пересоздан"
# The previous code claims a run only on its agent's runtime and knows nothing
# of model_choice: put every unclaimed routed run back there.
moved=$(psql_scalar "WITH moved AS (UPDATE agent_task_queue t SET runtime_id = a.runtime_id, model_choice = NULL FROM agent a WHERE a.id = t.agent_id AND t.status IN ('queued','deferred') AND t.model_choice IS NOT NULL AND a.runtime_id IS NOT NULL RETURNING 1) SELECT count(*) FROM moved")
log "раны, возвращённые на runtime агента: ${moved:-0}"

if [[ -n ${PROVIDER_TIMER:-} && ${PROVIDER_TIMER_WAS_ENABLED:-} == enabled ]]; then
  systemctl enable --now "$PROVIDER_TIMER"
  log "полоса провайдеров сторожа снова включена: $PROVIDER_TIMER"
fi
: >"$HERE/ROLLED_BACK"
log 'откат выполнен; таблицы профилей и колонки остаются, прежний код их не читает'
