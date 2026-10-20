#!/usr/bin/env bash
# daemon-install.sh against a fake /proc, systemctl, curl and pgrep. No root, no daemon.
set -euo pipefail
HERE=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
fail() { echo "FAIL: $*" >&2; exit 1; }
pass=0

setup() {
  T=$(mktemp -d)
  mkdir -p "$T/bin" "$T/proc/4242" "$T/pkg" "$T/backups"
  printf '#!/bin/sh\necho "multica old"\n' >"$T/bin/multica"; chmod 755 "$T/bin/multica"
  printf '#!/bin/sh\necho "multica new"\n' >"$T/pkg/multica"; chmod 755 "$T/pkg/multica"
  ln -s "$T/bin/multica" "$T/proc/4242/exe"
  echo "0::/system.slice/${1:-multica-agents.service}" >"$T/proc/4242/cgroup"
  printf '#!/bin/sh\necho 4242\n' >"$T/pgrep"
  printf '#!/bin/sh\necho "$*" >>%s/systemctl.log\n' "$T" >"$T/systemctl"
  # The fake daemon offers accept only when it runs the new binary.
  printf '#!/bin/sh\nif grep -q new %s/bin/multica; then echo "{\\"agents\\":[\\"codex\\",\\"%s\\"]}"; else echo "{\\"agents\\":[\\"codex\\"]}"; fi\n' "$T" "${2:-accept}" >"$T/curl"
  chmod 755 "$T/pgrep" "$T/systemctl" "$T/curl"
}
run() {
  PROC=$T/proc BACKUPS=$T/backups SYSTEMCTL=$T/systemctl CURL=$T/curl PGREP=$T/pgrep WAIT=2 \
    bash "$HERE/daemon-install.sh" --binary "$T/pkg/multica" "$@"
}

# --check changes nothing.
setup
out=$(run --check)
[[ $out == *'multica-agents.service'* && $out == *'ничего не меняется'* ]] || fail "check: $out"
grep -q old "$T/bin/multica" || fail 'check replaced the binary'
[[ ! -e $T/systemctl.log ]] || fail 'check restarted something'
pass=$((pass + 1))

# Install: binary replaced, unit restarted, accept offered; rollback returns it.
setup
run >/dev/null || fail "install exit $?"
grep -q new "$T/bin/multica" || fail 'binary not replaced'
grep -qx 'restart multica-agents.service' "$T/systemctl.log" || fail 'unit not restarted'
SYSTEMCTL=$T/systemctl BACKUPS=$T/backups bash "$HERE/daemon-rollback.sh" >/dev/null
grep -q old "$T/bin/multica" || fail 'rollback did not return the old binary'
[[ $(grep -c restart "$T/systemctl.log") == 2 ]] || fail 'rollback did not restart'
pass=$((pass + 1))

# The restarted daemon does not offer accept: the old binary is back, exit 2.
setup multica-agents.service none
set +e; run >/dev/null 2>&1; code=$?; set -e
[[ $code == 2 ]] || fail "no accept: exit $code"
grep -q old "$T/bin/multica" || fail 'no accept: old binary not returned'
pass=$((pass + 1))

# No unit in the cgroup: binary replaced, exit 3, nothing restarted.
setup
echo '0::/user.slice/session-1.scope' >"$T/proc/4242/cgroup"
set +e; run >/dev/null 2>&1; code=$?; set -e
[[ $code == 3 ]] || fail "no unit: exit $code"
[[ ! -e $T/systemctl.log ]] || fail 'no unit: something was restarted'
pass=$((pass + 1))

# A tampered package is refused before anything changes.
setup
(cd "$T/pkg" && sha256sum multica >SHA256SUMS)
echo tamper >>"$T/pkg/multica"
set +e; run >/dev/null 2>&1; code=$?; set -e
[[ $code == 1 ]] && grep -q old "$T/bin/multica" || fail 'tampered package was installed'
pass=$((pass + 1))

echo "ok: $pass/5"
