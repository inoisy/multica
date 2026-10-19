#!/usr/bin/env bash
# Replaces the multica binary a running daemon was started from and restarts
# the daemon, so it registers the accept runtime. The old binary goes to a
# backup with a rollback script.
#   daemon-install.sh [--check] [--binary PATH] [--unit NAME] [--port 19514]
# The daemon is found by its process ("multica daemon"); its unit by the
# process's cgroup; --unit overrides. Exit: 0 done; 2 the restarted daemon did
# not offer accept — the old binary is back; 3 binary replaced, restart by hand.
# For tests: PROC (instead of /proc), BACKUPS (instead of /var/backups),
# SYSTEMCTL, CURL and PGREP commands.
set -euo pipefail
HERE=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
PROC=${PROC:-/proc} BACKUPS=${BACKUPS:-/var/backups}
SYSTEMCTL=${SYSTEMCTL:-systemctl} CURL=${CURL:-curl} PGREP=${PGREP:-pgrep}
MODE=install BINARY=$HERE/multica UNIT= PORT=19514 WAIT=${WAIT:-60}
while (($#)); do
  case $1 in
    --check) MODE=check; shift ;;
    --binary) BINARY=${2:?}; shift 2 ;;
    --unit) UNIT=${2:?}; shift 2 ;;
    --port) PORT=${2:?}; shift 2 ;;
    *) echo "неизвестный аргумент: $1" >&2; exit 1 ;;
  esac
done
[[ -x $BINARY ]] || { echo "нет нового multica: $BINARY" >&2; exit 1; }
if [[ -f $(dirname -- "$BINARY")/SHA256SUMS ]]; then
  (cd "$(dirname -- "$BINARY")" && sha256sum --quiet -c SHA256SUMS) || { echo 'SHA256SUMS не сходится' >&2; exit 1; }
fi

mapfile -t pids < <($PGREP -f 'multica daemon' || true)
((${#pids[@]})) || { echo 'демон multica не запущен: нечего обновлять' >&2; exit 1; }
TARGET=$(readlink -f "$PROC/${pids[0]}/exe")
TARGET=${TARGET% (deleted)}
for p in "${pids[@]}"; do
  t=$(readlink -f "$PROC/$p/exe"); t=${t% (deleted)}
  [[ $t == "$TARGET" ]] || { echo "демоны запущены из разных файлов ($TARGET, $t): обновите по одному с --unit" >&2; exit 1; }
done
if [[ -z $UNIT ]]; then
  UNIT=$(grep -oE '[^/]+\.service' "$PROC/${pids[0]}/cgroup" | tail -1 || true)
fi
SCOPE=system
[[ $(cat "$PROC/${pids[0]}/cgroup" 2>/dev/null) == *user@* ]] && SCOPE=user
OWNER=$(stat -c %U "$PROC/${pids[0]}" 2>/dev/null || echo agents)

echo "обновление multica на хосте демона ($MODE)"
echo "демон: ${pids[*]} → $TARGET"
echo "юнит: ${UNIT:-не найден} ($SCOPE, пользователь $OWNER)"
echo "новый: $BINARY ($("$BINARY" --version 2>/dev/null | head -1))"
[[ $MODE == check ]] && { echo 'режим --check: ничего не меняется'; exit 0; }

STAMP=$(date -u +%Y%m%dT%H%M%SZ)
BACKUP=$BACKUPS/stt-conveyor-cli.$STAMP
mkdir -p "$BACKUP"
cp -p -- "$TARGET" "$BACKUP/multica"
restart() {
  case $SCOPE in
    system) $SYSTEMCTL restart "$UNIT" ;;
    user) $SYSTEMCTL --user -M "$OWNER@" restart "$UNIT" ;;
  esac
}
{
  echo '#!/usr/bin/env bash'
  echo '# Return the previous multica and restart the daemon.'
  echo 'set -euo pipefail'
  printf 'TARGET=%q\nBACKUP=%q\nUNIT=%q\nSCOPE=%q\nOWNER=%q\nSYSTEMCTL=${SYSTEMCTL:-systemctl}\n' "$TARGET" "$BACKUP" "$UNIT" "$SCOPE" "$OWNER"
  cat <<'R'
install -m 0755 "$BACKUP/multica" "$TARGET.rollback-tmp" && mv -f "$TARGET.rollback-tmp" "$TARGET"
if [[ -n $UNIT ]]; then
  if [[ $SCOPE == user ]]; then $SYSTEMCTL --user -M "$OWNER@" restart "$UNIT"; else $SYSTEMCTL restart "$UNIT"; fi
fi
echo "multica возвращён: $TARGET"
R
} >"$BACKUP/rollback.sh"
chmod 0700 "$BACKUP/rollback.sh"

install -m 0755 "$BINARY" "$TARGET.new-tmp"
mv -f "$TARGET.new-tmp" "$TARGET"
if [[ -z $UNIT ]]; then
  echo "multica заменён, но юнит демона не найден: перезапустите демон вручную. Откат: $BACKUP/rollback.sh" >&2
  exit 3
fi
restart
for ((i = 0; i < WAIT; i++)); do
  health=$($CURL -fsS -m 3 "http://127.0.0.1:$PORT/health" 2>/dev/null || true)
  if [[ $health == *'"accept"'* ]]; then
    echo "готово: демон предлагает runtime accept. Откат: $BACKUP/rollback.sh"
    exit 0
  fi
  sleep 1
done
echo 'перезапущенный демон не предложил runtime accept — возвращаю прежний multica' >&2
SYSTEMCTL=$SYSTEMCTL bash "$BACKUP/rollback.sh" >&2
exit 2
