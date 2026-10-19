#!/usr/bin/env bash
# Runs preflight / install / rollback against fake docker, curl and pgrep.
#   deploy/stt-models/test/run-tests.sh
# Needs git (a checkout with this directory committed), python3 and bash only.
set -uo pipefail
HERE=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd -- "$HERE/.." && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
failures=0 checks=0

ok()   { checks=$((checks + 1)); printf 'ok   %s\n' "$1"; }
fail() { checks=$((checks + 1)); failures=$((failures + 1)); printf 'FAIL %s\n' "$1"; }
expect() { # expect 'what' command...
  local what=$1; shift
  if "$@"; then ok "$what"; else fail "$what"; fi
}
calls() { cat "$STUB_STATE/calls.log"; }
mutating_calls() { grep -E '^(docker (build |buildx build |tag |pull |compose .* (up|run) )|systemctl (enable|disable) )' "$STUB_STATE/calls.log" || true; }

"$ROOT/build-package.sh" --revision HEAD --out "$WORK/pkg" --daemon-pid 2988 >/dev/null || { echo 'build-package.sh failed'; exit 1; }
SHORT=$(python3 -I -c 'import json,sys; print(json.load(open(sys.argv[1]))["source"]["revision"][:9])' "$WORK/pkg/manifest.json")
TARGET=$(python3 -I -c 'import json,sys; print(json.load(open(sys.argv[1]))["migrations"]["target_max"])' "$WORK/pkg/manifest.json")

new_stack() { # fresh fake stack + empty backup root
  rm -rf "$WORK/bin" "$WORK/state" "$WORK/backups" "$WORK/proc"
  mkdir -p "$WORK/backups" "$WORK/proc/2988"
  "$ROOT/test/stubs.sh" "$WORK/bin" "$WORK/state"
  echo "$SHORT" >"$WORK/state/new_commit"
  echo "$TARGET" >"$WORK/state/target_schema"
  export STUB_STATE=$WORK/state PATH=$WORK/bin:$PATH STTMODELS_ALLOW_NON_ROOT=1 STTMODELS_BACKUP_ROOT=$WORK/backups \
    STTMODELS_PROC_ROOT=$WORK/proc STTMODELS_POLL_SECONDS=0.05 STTMODELS_HEALTH_SECONDS=2
}
run() { "$@" >"$WORK/out.log" 2>&1; echo $? >"$WORK/rc"; }
rc() { cat "$WORK/rc"; }
state_dir() { ls -d "$WORK"/backups/multica-stt-models.* | head -n1; }
running() { cat "$STUB_STATE/running_commit"; }

# --- preflight changes nothing -------------------------------------------------
new_stack
run "$WORK/pkg/preflight.sh" --manifest "$WORK/pkg/manifest.json"
expect 'preflight passes on a healthy stack' test "$(rc)" = 0
expect 'preflight builds, tags, pulls and recreates nothing' test -z "$(mutating_calls)"

new_stack; echo 999999999 >"$STUB_STATE/running_commit"
run "$WORK/pkg/preflight.sh" --manifest "$WORK/pkg/manifest.json"
expect 'preflight refuses a backend that is not the expected commit' test "$(rc)" != 0

new_stack; echo $((TARGET + 7)) >"$STUB_STATE/schema_version"
run "$WORK/pkg/preflight.sh" --manifest "$WORK/pkg/manifest.json"
expect 'preflight refuses a schema newer than the release knows' test "$(rc)" != 0

new_stack; rmdir "$WORK/proc/2988"
run "$WORK/pkg/preflight.sh" --manifest "$WORK/pkg/manifest.json"
expect 'preflight refuses when the daemon the manifest names is gone' test "$(rc)" != 0

new_stack; touch "$STUB_STATE/no_builder"
run "$WORK/pkg/preflight.sh" --manifest "$WORK/pkg/manifest.json"
expect 'preflight refuses when the stand-build builder is missing' test "$(rc)" != 0

new_stack; cp -r "$WORK/pkg" "$WORK/pkg-bad"; echo x >>"$WORK/pkg-bad/source.tar.gz"
run "$WORK/pkg-bad/preflight.sh" --manifest "$WORK/pkg-bad/manifest.json"
expect 'preflight refuses a source archive that does not match the manifest' test "$(rc)" != 0
rm -rf "$WORK/pkg-bad"

# --- install --------------------------------------------------------------------
new_stack
run "$WORK/pkg/install.sh" --manifest "$WORK/pkg/manifest.json"
expect 'install succeeds' test "$(rc)" = 0
expect 'the new commit answers health' test "$(running)" = "$SHORT"
expect 'migrations were applied with the new image' test "$(cat "$STUB_STATE/schema_version")" = "$TARGET"
expect 'the previous image got a tag of its own before anything changed' grep -q "tag sha256:old multica-backend:pre-stt-models-" "$STUB_STATE/calls.log"
expect 'the image is built with buildx on stand-build and loaded' grep -q "docker buildx build --builder stand-build --load -t multica-backend:stt-models-$SHORT" "$STUB_STATE/calls.log"
expect 'no bare docker build' bash -c "! grep -q '^docker build ' '$STUB_STATE/calls.log'"
expect 'without --stop-provider-timer the timer is left alone' bash -c "! grep -qE '^systemctl (disable|enable)' '$STUB_STATE/calls.log'"
expect 'only the backend is recreated' bash -c "grep -E 'compose .* up ' '$STUB_STATE/calls.log' | grep -qE -- '--no-deps .*backend\$' && ! grep -E 'compose .* (up|restart|down|stop|rm)' '$STUB_STATE/calls.log' | grep -qE 'postgres|frontend'"
expect 'migrations ran before the backend was replaced' bash -c "[[ \$(grep -n 'entrypoint ./migrate' '$STUB_STATE/calls.log' | head -1 | cut -d: -f1) -lt \$(grep -n 'compose .* up ' '$STUB_STATE/calls.log' | head -1 | cut -d: -f1) ]]"
expect 'state and rollback are kept' test -x "$(state_dir)/rollback.sh" -a -r "$(state_dir)/state.env"
expect 'the override pins the new image' grep -q "image: multica-backend:stt-models-$SHORT" "$(state_dir)/override.yml"
expect 'the daemon was never signalled' bash -c "! grep -q '^kill' '$STUB_STATE/calls.log'"

# --- rollback ---------------------------------------------------------------------
run "$(state_dir)/rollback.sh"
expect 'rollback succeeds' test "$(rc)" = 0
expect 'the previous commit answers health again' test "$(running)" = b74f84e2e
expect 'rollback keeps the migrated schema' test "$(cat "$STUB_STATE/schema_version")" = "$TARGET"
expect 'rollback puts routed runs back on their agent runtime' grep -q 'раны, возвращённые на runtime агента: 3' "$WORK/out.log"
expect 'rollback leaves a marker' test -e "$(state_dir)/ROLLED_BACK"
expect 'rollback does not touch a timer the install left alone' bash -c "! grep -qE '^systemctl enable' '$STUB_STATE/calls.log'"

# --- provider timer ------------------------------------------------------------------
new_stack
run "$WORK/pkg/install.sh" --manifest "$WORK/pkg/manifest.json" --stop-provider-timer
expect 'install with --stop-provider-timer succeeds' test "$(rc)" = 0
expect '...and stops the provider lane' test "$(cat "$STUB_STATE/timer_state")" = disabled
expect '...only after the new backend answered' bash -c "[[ \$(grep -n 'compose .* up ' '$STUB_STATE/calls.log' | head -1 | cut -d: -f1) -lt \$(grep -n '^systemctl disable' '$STUB_STATE/calls.log' | head -1 | cut -d: -f1) ]]"
run "$(state_dir)/rollback.sh"
expect 'rollback enables the provider lane again' test "$(cat "$STUB_STATE/timer_state")" = enabled

new_stack; touch "$STUB_STATE/no_timer"
run "$WORK/pkg/install.sh" --manifest "$WORK/pkg/manifest.json" --stop-provider-timer
expect 'a host without the timer installs fine' test "$(rc)" = 0

new_stack; echo disabled >"$STUB_STATE/timer_state"
run "$WORK/pkg/install.sh" --manifest "$WORK/pkg/manifest.json" --stop-provider-timer
run "$(state_dir)/rollback.sh"
expect 'rollback does not enable a timer that was already off' test "$(cat "$STUB_STATE/timer_state")" = disabled

# --- failed installs --------------------------------------------------------------
new_stack; touch "$STUB_STATE/broken_image"
run "$WORK/pkg/install.sh" --manifest "$WORK/pkg/manifest.json"
expect 'a new backend that never answers fails the install' test "$(rc)" = 2
expect '...and the previous backend is back' test "$(running)" = b74f84e2e

new_stack; touch "$STUB_STATE/migrate_fails"
run "$WORK/pkg/install.sh" --manifest "$WORK/pkg/manifest.json"
expect 'failed migrations stop before the backend is touched' test "$(rc)" != 0
expect '...so the old backend still serves' test "$(running)" = b74f84e2e
expect '...and was never recreated' bash -c "! grep -E 'compose .* up ' '$STUB_STATE/calls.log'"

new_stack; touch "$STUB_STATE/daemon_dies"
run "$WORK/pkg/install.sh" --manifest "$WORK/pkg/manifest.json"
expect 'a changed daemon is reported' test "$(rc)" = 3

printf '\n%d checks, %d failed\n' "$checks" "$failures"
((failures == 0))
