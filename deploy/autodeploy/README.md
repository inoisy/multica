# multica-autodeploy

Pull-based deploy of `main` onto the self-hosted server. GitHub never gets access to the server.

```
merge into main ─▶ GitHub Actions: CI + STT (images ghcr.io/inoisy/multica-{backend,web}:<sha>)
                          │ webhook workflow_run (HMAC), or the 30-minute timer
                          ▼
server: hook.py touches /run/multica-autodeploy/trigger ─▶ multica-autodeploy.service (root)
        1. newest commit on main with green CI and STT, ahead of /health commit
        2. migrations in the range: only newly added files without DROP/RENAME/TRUNCATE/DELETE/type change
           otherwise stop and wait for `--allow-destructive <sha>`
        3. pg_dump to /var/backups/multica-autodeploy (last 5 kept)
        4. docker-compose.autodeploy.yml pins backend/web to the sha; pull; up -d
           (the backend runs `migrate up` itself on start)
        5. /health must report the sha within 4 minutes, else the previous images come back
        6. daemon binary from the backend image, swapped only when the daemon is idle
        7. result posted to the log issue
```

Why migrations must be additive: rollback is "start the previous images". That only works when the old
code runs on the new schema. Removals (like STT-433 migration 615) ship separately and are applied by an
operator with `sudo multica-autodeploy run --allow-destructive <sha>`.

## Install (root, once)

```sh
sudo NOTIFY_ISSUE=<issue-uuid> bash deploy/autodeploy/install.sh
```

Then add `handle /hooks/deploy { reverse_proxy 127.0.0.1:8977 }` to the server's Caddy site block and
reload Caddy. The webhook secret is shared with the GitHub repo webhook; the installer copies it from
`/home/agents/ops/autodeploy/webhook-secret` when present.

It starts in dry-run mode: runs only report the plan to the log issue. Set `"dry_run": false` in
`/etc/multica-autodeploy/config.json` to go live.

## Operate

| What | Command |
| --- | --- |
| Plan without changes | `sudo multica-autodeploy run --dry-run` |
| Deploy now | `sudo systemctl start multica-autodeploy` |
| Approve a blocked migration | `sudo multica-autodeploy run --allow-destructive <sha>` |
| Roll back the last deploy | `sudo multica-autodeploy rollback` |
| History | `/var/log/multica-autodeploy.jsonl`, `journalctl -u multica-autodeploy` |

Manual `docker compose` commands must include `docker-compose.autodeploy.yml`, otherwise compose falls
back to the image from `.env`. The script reads the compose files from the running container labels, so
after the first deploy they already include it.

## Tests

```sh
python3 -I -m unittest discover -s deploy/autodeploy -p 'test_*.py'
```
