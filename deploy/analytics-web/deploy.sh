#!/usr/bin/env bash
# Operator script: update only the web container of the existing Multica stack.
set -Eeuo pipefail
umask 077

REF=${REF:-0d866396f891601a3096f41bc11f2b940fbb3e9e}
BASE_REVISION=aebbec7876b9f187bd3f5d78ef357e6770c33b63
FORK=${FORK:-/root/multica-ru}
SOURCE_URL=https://github.com/inoisy/multica.git
COMPOSE=${COMPOSE:-/opt/multica/docker-compose.selfhost.yml}
BUILDER=${BUILDER:-stand-build}
API=${API:-http://127.0.0.1:8080}
WEB=${WEB:-http://127.0.0.1:3000}
DAEMON_UNIT=${DAEMON_UNIT:-multica-agents}
BACKUP_ROOT=${BACKUP_ROOT:-/var/backups}
HEALTH_WAIT_SECONDS=${HEALTH_WAIT_SECONDS:-90}
CHECK_ONLY=0
STATE_DIR=
SWAPPED=0
TEMP_SOURCE=

log() { printf '[analytics-web] %s\n' "$*"; }
die() { log "ОШИБКА: $*" >&2; exit 1; }
[[ $(id -u) == 0 ]] || die 'запустите скрипт от root на сервере Multica'
for cmd in docker git curl python3 flock systemctl; do command -v "$cmd" >/dev/null || die "нет команды $cmd"; done
exec 9>"${LOCK_FILE:-/run/lock/multica-web-deploy.lock}"
flock -n 9 || die 'уже выполняется другая выкатка веба'

compose() { docker compose "${COMPOSE_ARGS[@]}" "$@"; }
one_cid() {
  local cid
  cid=$(compose ps -q "$1")
  [[ -n $cid && $cid != *$'\n'* ]] || die "нужен один запущенный сервис $1 в заданном Compose-проекте"
  printf '%s\n' "$cid"
}
protected_snapshot() {
  local service cid ids
  for service in "${SERVICES[@]}"; do
    [[ $service == "$WEB_SERVICE" ]] && continue
    ids=$(compose ps -a -q "$service") || return 1
    while IFS= read -r cid; do
      [[ -n $cid ]] || continue
      printf '%s:%s:%s\n' "$service" "$cid" "$(docker inspect -f '{{.State.StartedAt}}' "$cid")"
    done <<<"$ids"
  done
  printf 'daemon:%s\n' "$(systemctl show -p MainPID --value "$DAEMON_UNIT")"
}
wait_web() {
  local deadline=$((SECONDS + HEALTH_WAIT_SECONDS)) code
  while (( SECONDS < deadline )); do
    code=$(curl -sS --max-time 5 -o /dev/null -w '%{http_code}' "$WEB/login" 2>/dev/null) || code=000
    if [[ $code == 200 ]]; then return 0; fi
    sleep 2
  done
  return 1
}
rollback() {
  log "возвращаю прежний образ $OLD_IMAGE_ID"
  docker tag "$OLD_IMAGE_ID" "$LIVE_IMAGE_TAG" || return 1
  compose up -d --no-deps --no-build --pull never "$WEB_SERVICE" || return 1
  wait_web || return 1
  [[ $(docker inspect -f '{{.Image}}' "$(one_cid "$WEB_SERVICE")") == "$OLD_IMAGE_ID" ]] || return 1
  [[ $(protected_snapshot) == "$PROTECTED_BEFORE" ]] || return 1
  touch "$STATE_DIR/ROLLED_BACK"
  log 'откат выполнен, веб отвечает'
}
on_failure() {
  local rc=$?
  trap - ERR INT TERM
  (( rc != 0 )) || rc=1
  if (( SWAPPED )); then
    log 'выкатка прервана; выполняю откат'
    rollback || log "автооткат не завершён; выполните bash $STATE_DIR/rollback.sh и проверьте журнал контейнера"
  fi
  exit "$rc"
}
cleanup() { [[ -z $TEMP_SOURCE ]] || rm -rf -- "$TEMP_SOURCE"; }
trap cleanup EXIT

if [[ ${1:-} == --rollback ]]; then
  [[ $# == 2 ]] || die 'использование: deploy.sh --rollback КАТАЛОГ_БЭКАПА'
  STATE_DIR=$2
  [[ -r $STATE_DIR/state.env ]] || die 'нет state.env в каталоге бэкапа'
  # state.env is generated with printf %q in a root-only backup directory.
  # shellcheck disable=SC1091
  source "$STATE_DIR/state.env"
  current=$(docker inspect -f '{{.Image}}' "$(one_cid "$WEB_SERVICE")")
  [[ $current == "$NEW_IMAGE_ID" || $current == "$OLD_IMAGE_ID" ]] || die 'после этой выкатки уже установлен другой образ; откат остановлен'
  rollback || die 'откат не прошёл проверку здоровья'
  exit 0
fi
[[ $# == 0 || ( $# == 1 && $1 == --check ) ]] || die 'использование: deploy.sh [--check]'
[[ ${1:-} != --check ]] || CHECK_ONLY=1
[[ $REF =~ ^[a-f0-9]{40}$ ]] || die 'REF должен быть полным SHA проверенного коммита'
[[ -r $COMPOSE ]] || die "нет $COMPOSE"
[[ -d $FORK/.git ]] || die "нет клона inoisy/multica: $FORK (задайте FORK)"

# Anchor discovery to the production Compose file, never to global docker ps.
COMPOSE_ARGS=(-f "$COMPOSE")
BACKEND_CID=$(one_cid backend)
PROJECT=$(docker inspect -f '{{index .Config.Labels "com.docker.compose.project"}}' "$BACKEND_CID")
WORKDIR=$(docker inspect -f '{{index .Config.Labels "com.docker.compose.project.working_dir"}}' "$BACKEND_CID")
CONFIG_FILES=$(docker inspect -f '{{index .Config.Labels "com.docker.compose.project.config_files"}}' "$BACKEND_CID")
[[ -n $PROJECT && $PROJECT != '<no value>' && -d $WORKDIR ]] || die 'неполные метки Compose у продового backend'
COMPOSE_ARGS=(-p "$PROJECT" --project-directory "$WORKDIR")
IFS=, read -r -a files <<<"$CONFIG_FILES"
for file in "${files[@]}"; do
  [[ -r $file ]] || die "нет действующего Compose-файла $file"
  COMPOSE_ARGS+=(-f "$file")
done
[[ ${#files[@]} -gt 0 && $(one_cid backend) == "$BACKEND_CID" ]] || die 'метки Compose указывают на другой backend'
mapfile -t SERVICES < <(compose config --services)
WEB_SERVICE=${WEB_SERVICE:-}
if [[ -z $WEB_SERVICE ]]; then
  for candidate in frontend web; do
    if printf '%s\n' "${SERVICES[@]}" | grep -qxF "$candidate"; then
      [[ -z $WEB_SERVICE ]] || die 'есть frontend и web; задайте WEB_SERVICE явно'
      WEB_SERVICE=$candidate
    fi
  done
fi
[[ $WEB_SERVICE == frontend || $WEB_SERVICE == web ]] || die 'допустим только веб-сервис frontend или web'
printf '%s\n' "${SERVICES[@]}" | grep -qxF "$WEB_SERVICE" || die "нет сервиса $WEB_SERVICE"
WEB_CID=$(one_cid "$WEB_SERVICE")
one_cid postgres >/dev/null
PROTECTED_BEFORE=$(protected_snapshot)
backend_commit=$(curl -fsS --max-time 5 "$API/health" | python3 -I -c 'import json,sys; d=json.load(sys.stdin); assert d.get("status")=="ok"; print(d.get("commit",""))')
expected_backend=${EXPECTED_BACKEND_COMMIT:-${BASE_REVISION:0:9}}
[[ -n $backend_commit && $backend_commit == "$expected_backend"* ]] || die "backend $backend_commit, ожидался $expected_backend; сначала сверьте совместимость"
wait_web || die 'текущий веб не отвечает на /login'
docker buildx inspect "$BUILDER" >/dev/null

# A fixed public commit is fetched; the operator's working tree is never switched.
git -C "$FORK" fetch --no-tags "$SOURCE_URL" "$REF"
[[ $(git -C "$FORK" rev-parse "$REF^{commit}") == "$REF" ]] || die 'не совпадает коммит исходников'
git -C "$FORK" merge-base --is-ancestor "$BASE_REVISION" "$REF" || die 'коммит не основан на выкаченной v0.7.0'
changed=$(git -C "$FORK" diff --name-only "$BASE_REVISION" "$REF")
while IFS= read -r path; do
  case $path in packages/views/*|apps/web/*|'') ;; *) die "в коммите есть изменение вне веба: $path" ;; esac
done <<<"$changed"
log "проверки пройдены: проект $PROJECT, сервис $WEB_SERVICE, коммит $REF"
(( CHECK_ONLY == 0 )) || { log '--check: продовые сервисы и образы не изменены'; exit 0; }

STATE_DIR=$(mktemp -d "$BACKUP_ROOT/multica-web-analytics.XXXXXXXX")
OLD_IMAGE_ID=$(docker inspect -f '{{.Image}}' "$WEB_CID")
LIVE_IMAGE_TAG=$(docker inspect -f '{{.Config.Image}}' "$WEB_CID")
[[ $LIVE_IMAGE_TAG != sha256:* && $LIVE_IMAGE_TAG != *@sha256:* ]] || die 'в Compose указан digest вместо тега; нужна отдельная выкатка через override'
NEW_IMAGE_TAG="multica-web:analytics-${REF:0:12}"
docker tag "$OLD_IMAGE_ID" "multica-web:before-analytics-${STATE_DIR##*.}"
install -m 700 "${BASH_SOURCE[0]}" "$STATE_DIR/deploy.sh"
cat >"$STATE_DIR/rollback.sh" <<'ROLLBACK'
#!/usr/bin/env bash
set -euo pipefail
HERE=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
exec bash "$HERE/deploy.sh" --rollback "$HERE"
ROLLBACK
chmod 700 "$STATE_DIR/rollback.sh"
TEMP_SOURCE=$(mktemp -d "${TMPDIR:-/var/tmp}/multica-web-source.XXXXXXXX")
git -C "$FORK" archive "$REF" | tar -x -C "$TEMP_SOURCE"
log "сборка только веба; журнал: $STATE_DIR/build.log"
docker buildx build --builder "$BUILDER" --load -t "$NEW_IMAGE_TAG" \
  --label "org.opencontainers.image.revision=$REF" \
  --build-arg "NEXT_PUBLIC_APP_VERSION=analytics-${REF:0:12}" \
  -f "$TEMP_SOURCE/Dockerfile.web" "$TEMP_SOURCE" >"$STATE_DIR/build.log" 2>&1 || {
    tail -n 40 "$STATE_DIR/build.log"; die 'сборка не прошла, продовый веб не заменён';
  }
NEW_IMAGE_ID=$(docker image inspect -f '{{.Id}}' "$NEW_IMAGE_TAG")
{
  declare -p COMPOSE_ARGS SERVICES
  for var in WEB_SERVICE OLD_IMAGE_ID LIVE_IMAGE_TAG NEW_IMAGE_ID PROTECTED_BEFORE DAEMON_UNIT WEB HEALTH_WAIT_SECONDS REF; do
    printf '%s=%q\n' "$var" "${!var}"
  done
} >"$STATE_DIR/state.env"
log "откат: bash $STATE_DIR/rollback.sh"
[[ $(protected_snapshot) == "$PROTECTED_BEFORE" && $(one_cid "$WEB_SERVICE") == "$WEB_CID" ]] || die 'стек изменился во время сборки; выкатка остановлена'
trap on_failure ERR INT TERM
SWAPPED=1
docker tag "$NEW_IMAGE_ID" "$LIVE_IMAGE_TAG"
compose up -d --no-deps --no-build --pull never "$WEB_SERVICE"
wait_web || { log 'новый веб не ответил на /login'; false; }
[[ $(docker inspect -f '{{.Image}}' "$(one_cid "$WEB_SERVICE")") == "$NEW_IMAGE_ID" ]]
[[ $(protected_snapshot) == "$PROTECTED_BEFORE" ]]
curl -fsS --max-time 5 "$API/health" >"$STATE_DIR/backend-health.json"
SWAPPED=0
trap - ERR INT TERM
touch "$STATE_DIR/DEPLOYED"
log "готово: $REF; веб отвечает, остальные контейнеры и PID демона сохранены"
log "откат: bash $STATE_DIR/rollback.sh"
