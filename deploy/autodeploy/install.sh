#!/bin/bash
# Install or update multica-autodeploy. Run as root from a checkout of inoisy/multica:
#   sudo bash deploy/autodeploy/install.sh
# Starts in dry-run mode: the first runs only report what they would deploy.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
lib=/usr/local/lib/multica-autodeploy
etc=/etc/multica-autodeploy
[[ $EUID -eq 0 ]] || { echo "run as root" >&2; exit 1; }

install -d -m 0755 "$lib" "$etc"
install -m 0755 "$here/autodeploy.py" "$lib/autodeploy.py"
install -m 0755 "$here/hook.py" "$lib/hook.py"
ln -sf "$lib/autodeploy.py" /usr/local/bin/multica-autodeploy

if [[ ! -f $etc/config.json ]]; then
  cat > "$etc/config.json" <<JSON
{"dry_run": true, "notify_issue": "${NOTIFY_ISSUE:-}"}
JSON
fi
if [[ ! -f $etc/webhook-secret ]]; then
  src=${WEBHOOK_SECRET_FILE:-/home/agents/ops/autodeploy/webhook-secret}
  if [[ -s $src ]]; then install -m 0640 -g agents "$src" "$etc/webhook-secret"
  else umask 027; head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$etc/webhook-secret"; chgrp agents "$etc/webhook-secret"
  fi
fi

install -m 0644 "$here"/systemd/multica-autodeploy{.service,.timer,.path,-hook.service} /etc/systemd/system/
install -m 0644 "$here/systemd/multica-autodeploy-tmpfiles.conf" /etc/tmpfiles.d/multica-autodeploy.conf
systemd-tmpfiles --create /etc/tmpfiles.d/multica-autodeploy.conf
systemctl daemon-reload
systemctl enable --now multica-autodeploy.timer multica-autodeploy.path multica-autodeploy-hook.service

cat <<TXT
Installed. Mode: $(python3 -c "import json;print('dry-run' if json.load(open('$etc/config.json')).get('dry_run',True) else 'live')")
Caddy: add inside the multica.yakutov.com block, then reload Caddy:
    handle /hooks/deploy { reverse_proxy 127.0.0.1:8977 }
Try a plan now:      sudo multica-autodeploy run --dry-run
Go live:             set "dry_run": false in $etc/config.json
Log:                 /var/log/multica-autodeploy.jsonl, journalctl -u multica-autodeploy
Rollback last run:   sudo multica-autodeploy rollback
TXT
