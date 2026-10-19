#!/usr/bin/env bash
# Puts the previous backend image back. Run from the directory install.sh
# printed: it carries state.env with the stack and the image to return to.
#   sudo rollback.sh [--force]
#
# Only the backend container is replaced. The additive tables and the column
# stay: the previous code does not read them, and dropping them would lose the
# journal. The previous image has no route endpoints, so no new route can be
# enabled until a new install.
set -euo pipefail
HERE=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
[[ -r $HERE/state.env ]] || { echo '[stt-258] ОШИБКА: state.env не найден; запускайте rollback.sh из каталога, который напечатал install.sh' >&2; exit 1; }
# shellcheck source=lib.sh
. "$HERE/lib.sh"
# shellcheck disable=SC1091
. "$HERE/state.env"

FORCE=0
while (($#)); do
  case $1 in
    --force) FORCE=1; shift ;;
    *) die "неизвестный аргумент: $1" ;;
  esac
done

require_root
: "${PROJECT:?}" "${WORKDIR:?}" "${CONFIG_FILES:?}" "${SERVICE:?}"

active=$(psql_scalar "SELECT COALESCE(string_agg(issue_id::text||' ('||status||')', ', '),'') FROM issue_conveyor WHERE status NOT IN ('disabled','completed')" 2>/dev/null || true)
if [[ -n $active && $FORCE != 1 ]]; then
  die "у этих задач включён маршрут, после отката его некому вести: $active. Отключите их (multica issue conveyor disable) или повторите с --force"
fi
[[ -z $active ]] || log "маршруты остаются без обработки: $active"

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
wait_healthy "$PREV_COMMIT" "${STT258_HEALTH_SECONDS:-120}" || die "прежний backend не ответил на health с коммитом $PREV_COMMIT; проверьте 'docker compose logs $SERVICE'"

# shellcheck disable=SC2086
[[ $(service_container_ids $PROTECTED) == "$containers_before" ]] || die "контейнер защищённого сервиса ($PROTECTED) пересоздан"
: >"$HERE/ROLLED_BACK"
log 'откат выполнен; таблицы issue_conveyor* остаются, новые маршруты не включать до новой установки'
