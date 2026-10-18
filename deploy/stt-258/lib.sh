#!/usr/bin/env bash
# Shared by preflight.sh, install.sh and rollback.sh. Sourced, never executed.
#
# Nothing here assumes where the stack lives: the project, its directory and its
# compose files are read from the labels of the running backend container, so a
# run changes exactly the stack that is serving traffic.

log() { printf '[stt-258] %s\n' "$*"; }
die() { printf '[stt-258] ОШИБКА: %s\n' "$*" >&2; exit 1; }

require_root() {
  # STT258_ALLOW_NON_ROOT exists for the test stand, whose docker is a stub.
  [[ ${STT258_ALLOW_NON_ROOT:-} == 1 || $(id -u) == 0 ]] || die 'нужен root (доступ к docker)'
}

json_get() { # json_get FILE 'a.b.c' -> value (empty for null/missing)
  python3 -I - "$1" "$2" <<'PY'
import json, sys
value = json.load(open(sys.argv[1]))
for key in sys.argv[2].split('.'):
    value = value.get(key) if isinstance(value, dict) else None
    if value is None:
        break
if isinstance(value, (list, dict)):
    print(json.dumps(value))
elif value is not None:
    print(value)
PY
}

json_field() { # json_field 'text' 'key' -> value, from JSON on stdin-free argument
  python3 -I -c 'import json,sys; v=json.loads(sys.argv[1]).get(sys.argv[2]); print("" if v is None else v)' "$1" "$2"
}

# discover_stack populates PROJECT, WORKDIR, SERVICE, CONFIG_FILES and
# BACKEND_CID from the running backend container.
discover_stack() {
  local service=${MANIFEST_SERVICE:-backend}
  local ids
  ids=$(docker ps -q --filter "label=com.docker.compose.service=$service" ${MANIFEST_PROJECT:+--filter "label=com.docker.compose.project=$MANIFEST_PROJECT"})
  [[ -n $ids ]] || die "не найден запущенный контейнер сервиса $service${MANIFEST_PROJECT:+ проекта $MANIFEST_PROJECT}"
  [[ $(wc -l <<<"$ids") == 1 ]] || die "несколько контейнеров сервиса $service; укажите проект в manifest.compose.project"
  BACKEND_CID=$ids
  SERVICE=$service
  PROJECT=$(docker inspect --format '{{index .Config.Labels "com.docker.compose.project"}}' "$BACKEND_CID")
  WORKDIR=$(docker inspect --format '{{index .Config.Labels "com.docker.compose.project.working_dir"}}' "$BACKEND_CID")
  CONFIG_FILES=$(docker inspect --format '{{index .Config.Labels "com.docker.compose.project.config_files"}}' "$BACKEND_CID")
  [[ -n $PROJECT && -n $WORKDIR && -n $CONFIG_FILES ]] || die 'у контейнера нет меток docker compose; стек запущен не через compose'
}

# compose runs docker compose against the discovered stack. EXTRA_COMPOSE_FILES
# (override files) are appended after the project's own.
compose() {
  local args=(compose --project-name "$PROJECT" --project-directory "$WORKDIR") f
  IFS=, read -r -a files <<<"$CONFIG_FILES"
  for f in "${files[@]}"; do args+=(-f "$f"); done
  for f in "${EXTRA_COMPOSE_FILES[@]:-}"; do [[ -n $f ]] && args+=(-f "$f"); done
  docker "${args[@]}" "$@"
}

health_url() {
  local hostport
  hostport=$(compose port "$SERVICE" 8080 2>/dev/null | head -n 1)
  [[ -n $hostport ]] || die 'не удалось определить опубликованный порт backend'
  printf 'http://%s/health' "${hostport/0.0.0.0/127.0.0.1}"
}

health_json() { curl -fsS --max-time 5 "$(health_url)"; }

wait_healthy() { # wait_healthy EXPECTED_COMMIT_PREFIX [SECONDS]
  local want=$1 deadline=$((SECONDS + ${2:-120})) body status commit
  while ((SECONDS < deadline)); do
    if body=$(health_json 2>/dev/null); then
      status=$(json_field "$body" status)
      commit=$(json_field "$body" commit)
      if [[ $status == ok && $commit == "$want"* ]]; then
        log "health: ok, commit $commit"
        return 0
      fi
    fi
    sleep "${STT258_POLL_SECONDS:-2}"
  done
  return 1
}

psql_scalar() { # psql_scalar 'SQL' -> single value from the stack's PostgreSQL
  compose exec -T postgres sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc "$1"' _ "$1"
}

schema_version() {
  psql_scalar "SELECT COALESCE(max(split_part(version,'_',1)::int),0) FROM schema_migrations"
}

service_container_ids() { # one line per service the release must not touch
  local svc
  for svc in "$@"; do
    printf '%s=%s\n' "$svc" "$(compose ps -q "$svc" 2>/dev/null | head -n 1)"
  done
}

pid_alive() { [[ -d ${STT258_PROC_ROOT:-/proc}/$1 ]]; } # a directory check: nothing is ever signalled

daemon_pids() { # PIDs of the host multica daemon(s); never signalled by these scripts
  pgrep -f 'multica( .*)? daemon' 2>/dev/null | sort -n | tr '\n' ' ' || true
}

load_manifest() { # load_manifest FILE
  MANIFEST=$1
  [[ -r $MANIFEST ]] || die "manifest не читается: $MANIFEST"
  RELEASE_DIR=$(cd -- "$(dirname -- "$MANIFEST")" && pwd)
  REVISION=$(json_get "$MANIFEST" source.revision)
  [[ $REVISION =~ ^[0-9a-f]{40}$ ]] || die 'manifest.source.revision должен быть полным SHA'
  SHORT=${REVISION:0:9}
  IMAGE_TAG=$(json_get "$MANIFEST" image.tag)
  IMAGE_REF=$(json_get "$MANIFEST" image.reference)
  PREVIOUS_COMMIT=$(json_get "$MANIFEST" previous.commit)
  MIGRATIONS_FROM=$(json_get "$MANIFEST" migrations.previous_max)
  MIGRATIONS_TO=$(json_get "$MANIFEST" migrations.target_max)
  MANIFEST_PROJECT=$(json_get "$MANIFEST" compose.project)
  MANIFEST_SERVICE=$(json_get "$MANIFEST" compose.service)
  PROTECTED=$(json_get "$MANIFEST" compose.protected_services | python3 -I -c 'import json,sys; print(" ".join(json.load(sys.stdin) or []))')
  [[ -n $IMAGE_TAG && -n $PREVIOUS_COMMIT && -n $MIGRATIONS_FROM && -n $MIGRATIONS_TO ]] || die 'manifest неполный'
  : "${PROTECTED:=postgres frontend}"
}
