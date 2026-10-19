#!/usr/bin/env bash
# Runs preflight / install / rollback against fake docker, curl and pgrep.
#   deploy/stt-258/test/run-tests.sh
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
mutating_calls() { grep -E '^docker (build|tag|pull|compose .* (up|run) )' "$STUB_STATE/calls.log" || true; }

"$ROOT/build-package.sh" --revision HEAD --out "$WORK/pkg" --daemon-pid 2988 >/dev/null || { echo 'build-package.sh failed'; exit 1; }
SHORT=$(python3 -I -c 'import json,sys; print(json.load(open(sys.argv[1]))["source"]["revision"][:9])' "$WORK/pkg/manifest.json")
TARGET=$(python3 -I -c 'import json,sys; print(json.load(open(sys.argv[1]))["migrations"]["target_max"])' "$WORK/pkg/manifest.json")

new_stack() { # fresh fake stack + empty backup root
  rm -rf "$WORK/bin" "$WORK/state" "$WORK/backups" "$WORK/proc"
  mkdir -p "$WORK/backups" "$WORK/proc/2988"
  "$ROOT/test/stubs.sh" "$WORK/bin" "$WORK/state"
  echo "$SHORT" >"$WORK/state/new_commit"
  echo "$TARGET" >"$WORK/state/target_schema"
  export STUB_STATE=$WORK/state PATH=$WORK/bin:$PATH STT258_ALLOW_NON_ROOT=1 STT258_BACKUP_ROOT=$WORK/backups \
    STT258_PROC_ROOT=$WORK/proc STT258_POLL_SECONDS=0.05 STT258_HEALTH_SECONDS=2
}
run() { "$@" >"$WORK/out.log" 2>&1; echo $? >"$WORK/rc"; }
rc() { cat "$WORK/rc"; }
state_dir() { ls -d "$WORK"/backups/multica-stt-258.* | head -n1; }
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
expect 'the previous image got a tag of its own before anything changed' grep -q "tag sha256:old multica-backend:pre-stt-258-" "$STUB_STATE/calls.log"
expect 'the image is built from the shipped source' grep -q "docker build -t multica-backend:stt-258-$SHORT" "$STUB_STATE/calls.log"
expect 'only the backend is recreated' bash -c "grep -E 'compose .* up ' '$STUB_STATE/calls.log' | grep -qE -- '--no-deps .*backend\$' && ! grep -E 'compose .* (up|restart|down|stop|rm)' '$STUB_STATE/calls.log' | grep -qE 'postgres|frontend'"
expect 'migrations ran before the backend was replaced' bash -c "[[ \$(grep -n 'entrypoint ./migrate' '$STUB_STATE/calls.log' | head -1 | cut -d: -f1) -lt \$(grep -n 'compose .* up ' '$STUB_STATE/calls.log' | head -1 | cut -d: -f1) ]]"
expect 'state and rollback are kept' test -x "$(state_dir)/rollback.sh" -a -r "$(state_dir)/state.env"
expect 'the override pins the new image' grep -q "image: multica-backend:stt-258-$SHORT" "$(state_dir)/override.yml"
expect 'the daemon was never signalled' bash -c "! grep -q '^kill' '$STUB_STATE/calls.log'"

# --- rollback -------------------------------------------------------------------
echo 'fffffffff-0000 (active)' >"$STUB_STATE/active_routes"
run "$(state_dir)/rollback.sh"
expect 'rollback refuses while routes are active' test "$(rc)" != 0
expect '...and leaves the new backend running' test "$(running)" = "$SHORT"
run "$(state_dir)/rollback.sh" --force
expect 'rollback --force succeeds' test "$(rc)" = 0
expect 'the previous commit answers health again' test "$(running)" = 714994d36
expect 'rollback keeps the migrated schema' test "$(cat "$STUB_STATE/schema_version")" = "$TARGET"
expect 'rollback leaves a marker' test -e "$(state_dir)/ROLLED_BACK"
: >"$STUB_STATE/active_routes"

new_stack
run "$WORK/pkg/install.sh" --manifest "$WORK/pkg/manifest.json"
run "$(state_dir)/rollback.sh"
expect 'rollback with no active route needs no --force' test "$(rc)" = 0

# --- failed installs --------------------------------------------------------------
new_stack; touch "$STUB_STATE/broken_image"
run "$WORK/pkg/install.sh" --manifest "$WORK/pkg/manifest.json"
expect 'a new backend that never answers fails the install' test "$(rc)" = 2
expect '...and the previous backend is back' test "$(running)" = 714994d36

new_stack; touch "$STUB_STATE/migrate_fails"
run "$WORK/pkg/install.sh" --manifest "$WORK/pkg/manifest.json"
expect 'failed migrations stop before the backend is touched' test "$(rc)" != 0
expect '...so the old backend still serves' test "$(running)" = 714994d36
expect '...and was never recreated' bash -c "! grep -E 'compose .* up ' '$STUB_STATE/calls.log'"

new_stack; touch "$STUB_STATE/daemon_dies"
run "$WORK/pkg/install.sh" --manifest "$WORK/pkg/manifest.json"
expect 'a changed daemon is reported' test "$(rc)" = 3

printf '\n%d checks, %d failed\n' "$checks" "$failures"
((failures == 0))
