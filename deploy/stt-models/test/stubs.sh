#!/usr/bin/env bash
# Writes fake docker / curl / pgrep into $1 and seeds the fake stack state in
# $2. The stack is three containers (backend, postgres, frontend) and a host
# daemon; the fakes keep their state in plain files so a test can read and bend
# it. Nothing here touches a real docker.
set -euo pipefail
BIN=$1 STATE=$2
mkdir -p "$BIN" "$STATE"

printf 'sha256:old\n' >"$STATE/running_image"
printf 'b74f84e2e\n' >"$STATE/running_commit"
printf 'cid-backend-1\n' >"$STATE/backend_cid"
printf '574\n' >"$STATE/schema_version"
printf '585\n' >"$STATE/target_schema"
printf 'enabled\n' >"$STATE/timer_state"
printf '3\n' >"$STATE/routed_runs"
printf '2988\n' >"$STATE/daemon_pids"
: >"$STATE/calls.log"

cat >"$BIN/docker" <<'DOCKER'
#!/usr/bin/env bash
STATE=${STUB_STATE:?}
echo "docker $*" >>"$STATE/calls.log"
cmd=$1; shift
case $cmd in
  ps) cat "$STATE/backend_cid" ;;
  inspect)
    fmt=$2
    case $fmt in
      *'{{.Image}}'*) cat "$STATE/running_image" ;;
      *compose.project.working_dir*) echo /opt/stack ;;
      *compose.project.config_files*) echo /opt/stack/docker-compose.yml,/opt/stack/docker-compose.extra.yml ;;
      *compose.project*) echo stack ;;
    esac ;;
  tag) echo "$2 $1" >>"$STATE/tags" ;;
  buildx)
    [[ $1 == inspect ]] && { [[ -f $STATE/no_builder ]] && exit 1; exit 0; }
    shift
    tag=
    while (($#)); do [[ $1 == -t ]] && tag=$2; shift; done
    echo "$tag sha256:new" >>"$STATE/images" ;;
  build)
    tag=
    while (($#)); do [[ $1 == -t ]] && tag=$2; shift; done
    echo "$tag sha256:new" >>"$STATE/images" ;;
  pull) ;;
  image)
    ref=${@: -1}
    grep -h "^$ref " "$STATE/images" 2>/dev/null | tail -n1 | cut -d' ' -f2 ;;
  compose)
    override=
    while (($#)); do
      case $1 in
        --project-name|--project-directory) shift 2 ;;
        -f) [[ $2 == */override*.yml ]] && override=$2; shift 2 ;;
        *) break ;;
      esac
    done
    sub=$1; shift
    case $sub in
      port) echo 0.0.0.0:18080 ;;
      ps)
        case ${@: -1} in
          postgres) echo cid-postgres-1 ;;
          frontend) echo cid-frontend-1 ;;
          *) cat "$STATE/backend_cid" ;;
        esac ;;
      exec)
        sql=${@: -1}
        case $sql in
          *schema_migrations*) cat "$STATE/schema_version" ;;
          *model_choice*) cat "$STATE/routed_runs"; echo 0 >"$STATE/routed_runs" ;;
        esac ;;
      run) [[ -f $STATE/migrate_fails ]] && exit 1; cp "$STATE/target_schema" "$STATE/schema_version" ;;
      up)
        if grep -q 'pre-stt-models' "$override" 2>/dev/null; then
          echo b74f84e2e >"$STATE/running_commit"; echo sha256:old >"$STATE/running_image"
        else
          if [[ -f $STATE/broken_image ]]; then echo down >"$STATE/running_commit"; else echo "$(cat "$STATE/new_commit")" >"$STATE/running_commit"; fi
          echo sha256:new >"$STATE/running_image"
        fi
        echo cid-backend-2 >"$STATE/backend_cid"
        [[ -f $STATE/daemon_dies ]] && echo 4001 >"$STATE/daemon_pids" || true ;;
    esac ;;
esac
DOCKER

cat >"$BIN/systemctl" <<'SYSTEMCTL'
#!/usr/bin/env bash
STATE=${STUB_STATE:?}
echo "systemctl $*" >>"$STATE/calls.log"
case $1 in
  cat) [[ -f $STATE/no_timer ]] && exit 1; exit 0 ;;
  is-enabled) cat "$STATE/timer_state" ;;
  disable) echo disabled >"$STATE/timer_state" ;;
  enable) echo enabled >"$STATE/timer_state" ;;
esac
SYSTEMCTL

cat >"$BIN/curl" <<'CURL'
#!/usr/bin/env bash
STATE=${STUB_STATE:?}
commit=$(cat "$STATE/running_commit")
[[ $commit == down ]] && exit 22
printf '{"status":"ok","pid":1,"commit":"%s","started_at":"2026-10-18T00:00:00Z"}\n' "$commit"
CURL
cat >"$BIN/pgrep" <<'PGREP'
#!/usr/bin/env bash
cat "${STUB_STATE:?}/daemon_pids"
PGREP
chmod +x "$BIN"/docker "$BIN"/curl "$BIN"/pgrep "$BIN"/systemctl
