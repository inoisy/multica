# multica-autodeploy

Pull-based deploy of `main` onto the self-hosted server. GitHub never gets access to the server.

```
merge into main ─▶ GitHub Actions: CI + STT (images ghcr.io/inoisy/multica-{backend,web}:<sha>)
                          │ webhook workflow_run (HMAC), or the 30-minute timer
                          ▼
server: hook.py touches /run/multica-autodeploy/trigger ─▶ multica-autodeploy.service (root)
        1. newest commit on main with green CI and STT, ahead of /health commit
        2. migrations in the range: only newly added files without DROP/RENAME/TRUNCATE/DELETE/type change
           otherwise stop, unless each destructive one came from a PR merged with the label
           `allow-destructive`; else wait for the label or `--allow-destructive <sha>`
        3. pg_dump to /var/backups/multica-autodeploy (last 5 kept)
        4. docker-compose.autodeploy.yml pins backend/web to the sha; pull; up -d
           (the backend runs `migrate up` itself on start)
        5. /health must report the sha within 4 minutes, else the previous images come back
        6. daemon binary from the backend image, swapped only when the daemon is idle
        7. result posted to the log issue
```

## multica-tasks MCP

With `"mcp_service": "multica-tasks-mcp"` in `config.json` the release also carries the MCP container
(compose file `mcp_addon_file`, default `/opt/multica/docker-compose.mcp.yml`, owned by root). Its image is
pinned in the released commit itself, `deploy/autodeploy/mcp-image`: one line
`ghcr.io/inoisy/multica-tasks-mcp:<40-hex sha>` or `@sha256:<digest>`; floating tags abort the run before
anything is touched. With MCP enabled, a missing pin also aborts before any changes.
A pin-only commit is under `deploy/**`, ignored by the STT push trigger. The release picks green CI from push runs
and green STT from push or `workflow_dispatch` runs on that sha (`dispatch_workflows`, default `["STT"]`). After merging that pin PR,
run the existing image workflow (`gh workflow run stt.yml --repo inoisy/multica --ref main`), verify its `headSha`
equals the accepted pin release SHA, and require green CI and STT before enabling MCP. If main moved,
review the new SHA before switching. Subsequent releases retain the pin file.

- Switch: pull and `up --no-deps` of backend, frontend and MCP.
- Health: `/health` reports the sha, and the MCP container runs the pinned image, is healthy and answers
  `mcp_health_url`. A failing MCP rolls back all images, the same as a failing platform.
- Rollback (automatic and `multica-autodeploy rollback`) restores `previous_images`; if the MCP was not part
  of the previous release its container is stopped. A partial compose start also triggers rollback.
- First activation also works when the platform already runs the target commit: a missing or outdated MCP
  is reconciled. An already healthy release does not overwrite rollback state.
- Pause the deploy timer/path before configuring MCP and stop the old `multica-tasks-mcp.service` before
  the first live run: both bind `127.0.0.1:8101`. The operator restarts the old unit if first activation fails.

Why migrations must be additive: rollback is "start the previous images". That only works when the old
code runs on the new schema. Removals (like STT-433 migration 617) ship separately and need an approval:

- **Label.** Put `allow-destructive` (`approve_label` in `config.json`, empty turns it off) on the PR that adds
  the migration, before or after merging it into `main`. For each migration with destructive statements the run
  finds the last commit touching that file up to the target and requires a merged PR into `main` with the label
  among that commit's PRs. A commit pushed straight to `main` has no such PR and stays blocked. A blocked
  commit is rechecked every run, so a label added later deploys on the next run without a new notice.
- **Operator.** `sudo multica-autodeploy run --allow-destructive <sha>`.

The label never approves an edited existing migration or a 300+ file change that cannot be verified.
Only accounts that can label PRs in the repo can approve; agents push with a deploy key and cannot.

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
| Approve a destructive migration | label `allow-destructive` on its PR, or `sudo multica-autodeploy run --allow-destructive <sha>` |
| Roll back the last deploy | `sudo multica-autodeploy rollback` |
| History | `/var/log/multica-autodeploy.jsonl`, `journalctl -u multica-autodeploy` |

Manual `docker compose` commands must include `docker-compose.autodeploy.yml`, otherwise compose falls
back to the image from `.env`. The script reads the compose files from the running container labels, so
after the first deploy they already include it.

## Tests

```sh
python3 -I -m unittest discover -s deploy/autodeploy -p 'test_*.py'
```
