# Upstream v0.7.0 integration

The integration branch merges `v0.7.0` into `runtime/0.6.1-live`, whose
starting commit is `23eb8b5c9b4a7fcbd737926b8bf79a598d89a906`. Both histories
remain ancestors of the branch. The owner requested this release in STT-411;
the operational gates below still apply.

## Route ownership

The owner approved trying upstream Project Workflows as shipped. No additional
workflow execution engine is introduced. `FF_PROJECT_WORKFLOWS_V1=true` exposes
the editor and project workflow picker; the self-host Compose file forwards this
variable into the backend container and keeps it off when unset. The isolated
review environment enables it explicitly.

The fork's `issue_conveyor` still owns issues with active routes. Leave existing
projects on Default without handoffs while those routes finish; test native
handoffs on a separate project without a route. A project workflow change does
not itself start agents or migrate the fork's active route state. Switching off
the editor flag does not disable workflows already attached to projects: revert
the project's workflow to Default as a separate, reviewed action.

The isolated server smoke test uses simulated executors and the real HTTP
register, claim, start, result, and completion endpoints. It exercises
dev → qa → review → accept → completed. It does not launch installed agent
CLIs, contact model providers, or merge a repository.

## Migration numbering and order

Keep the already deployed fork migration filenames 564–594 unchanged.
`schema_migrations.version` stores the complete filename stem. Renaming a
deployed file would make it pending again. The migration linter permits only
the enumerated existing fork stems to overlap upstream numeric prefixes.
It still rejects duplicate prefixes within either group and unknown future
duplicates.

The overlapping fork changes add conveyor, model profiles, stage dispatch,
subscription tables, and their indexes. Upstream changes add workflows,
channel typing state, cloud connections, delegation counters, and runtime
sharing. On shared tables, their added columns and index names differ.

Verification on isolated PostgreSQL:

- All migrations applied to a new empty database.
- Down/up of `594_subscription_host_state_index`,
  `593_subscription_host_state`, and `592_runtime_shared_with` succeeded.
- Schema-only dumps, without owner/privileges and random dump restriction
  tokens, match between a fresh database and a synthetic database first
  migrated with the live revision and then with this branch.

The synthetic upgrade is **not a production database copy**. Before deployment,
restore an authorized production dump into an isolated database, record the
source revision and dump timestamp, apply `server/bin/migrate up`, verify
indexes are valid, and compare conveyor/model/subscription records before and
after. Never run rollback experiments against production.

## Other fork branches

`ru-v0.6.1-fallback` has 45 non-equivalent patches relative to live;
`ru-v0.6.1-conveyor` has 53 (`git cherry`). These are not all duplicates.
The inventory in [upstream-sync-branch-inventory.json](upstream-sync-branch-inventory.json)
lists paths present in those branches and absent from live.

The integration now ports the deployed web additions from
`ru-v0.6.1-conveyor` at `560703762`: `/profiles`, `/subscriptions`, `/conveyor`,
custom pages, the agent model editor, issue profile and stage controls, squad
route editing, runtime subscription management, and the hidden acceptance-agent
name lookup. Web and desktop share these views. Upstream workflow properties,
handoff history, Qoder restrictions and conversation-starter controls remain.

This is a three-way port of the UI changes after `714994d36`, preserving the
newer upstream content. UI controls use shared Select, RadioGroup, Checkbox,
Button and Table components. Locale keys are merged structurally; obsolete
translation keys are omitted and Russian plural categories are normalized by
the locale parity guard.

The native editor header wraps its actions to fit narrow content areas.
The compact settings selector keeps its chevron inside the padded control.

Custom pages still use the separate `multica-pages` service at `/pages-api/`.
Keep the current reverse-proxy mapping when replacing the web image. The
integration validates that service's responses; malformed permissions remain
read-only and an unknown embed mode opens a tab. An isolated stand without that
service can show a load error for pages until a synthetic fixture is supplied.

## Reproducible checks

```sh
git merge-base --is-ancestor v0.7.0 HEAD
git merge-base --is-ancestor 23eb8b5c9 HEAD
make sqlc
(cd server && CGO_ENABLED=0 go build ./...)
# DATABASE_URL must point to an isolated, migrated test database.
bash scripts/test-go.sh --race
pnpm typecheck
pnpm lint
pnpm exec turbo test --filter='!@multica/mobile' --concurrency=1 -- --maxWorkers=2
```

On a busy shared host, limit Go package parallelism with `GOFLAGS=-p=2` and
`GOMAXPROCS=2`. Keep the agent CLI guard in `scripts/test-go.sh` enabled.
CLI tests isolate both their home and working directory, so a real task marker
cannot lend authority to a test. Local Git fixtures use `test/base` or
`test/shallow-cache`; production branch push guards remain enabled.

Cursor background-process lifecycle tests require Linux 6.9 or newer: they
exercise `pidfd_send_signal` with `PIDFD_SIGNAL_PROCESS_GROUP`. On the current
Linux 6.8 host the kernel returns `EINVAL`; production code deliberately fails
closed rather than falling back to an unsafe process-group signal. Those tests
must be verified on a supported kernel. Do not remove the ownership check or
skip its failures to label the full backend suite green.

The root override pins undici 7 to 7.30.1, addressing newly introduced audit
advisories in Electron's download dependency. Existing audit findings in the
baseline remain; compare advisory identifiers, not only the total count.

## Review environment

`docker-compose.upstream-sync.yml` builds both production images and starts an
isolated PostgreSQL database. It is intended for the stand service, which
supplies `STAND_URL`, generates `DB_PASSWORD` and `JWT_SECRET`, and publishes
only the web service. Keep this test environment private: it uses development
authentication and the fixed verification code `411411`. The synthetic
`release-check-411@example.test` user is an instance admin only on this stand,
so subscription forms can be verified without provider credentials. No production
credentials or data belong in it.

## Deployment and rollback gates

The owner has approved the rollout. Production-copy migration checks and final
acceptance checks are still required before deployment. Set
`FF_PROJECT_WORKFLOWS_V1=true` on the server to expose the native editor;
the integration requires no new application secret.

The backend rollback baseline is image tag
`stt-agent-terminal-fixes-23eb8b5c9`, commit `23eb8b5c9`. The deployed web was
built separately from `ru-v0.6.1-conveyor`; do not assume its tag or digest is
the same as the backend. Before deploying, record the actual immutable digests
for both current images and register the pair as the previous healthy release
in harness-control. Retain a database backup.

The required operational sequence is:

```sh
harness-control inspect multica
harness-control validate multica <full-integration-sha> --reason 'upstream v0.7.0'
harness-control op <validation-id>
harness-control deploy multica --validation <validation-id> --digest sha256:<validated-digest> --reason 'upstream v0.7.0'
harness-control op <deploy-id>
# After verifying the saved previous release is the baseline above:
harness-control rollback multica
```

At verification time, `harness-control inspect multica` returns
`forbidden_component`: Multica is not managed by the current harness policy.
The commands above are a gated procedure, not a ready-to-run deployment.
An operator must add the component and baseline through the authorized policy
workflow before validate/deploy/rollback can be used. Do not bypass the policy
with direct Docker or systemd commands.

After deployment, verify readiness, sign-in, route progression, quota retry,
wakeup delivery, model routing, subscriptions, and chat. Watch backend migration
and claim/completion errors. If health fails, use the registered rollback;
leave additive schema changes in place unless a database restore/rollback has
been separately rehearsed on the production copy.
