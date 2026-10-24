# Upstream v0.7.0 integration

The integration branch merges `v0.7.0` into `runtime/0.6.1-live`, whose
starting commit is `23eb8b5c9b4a7fcbd737926b8bf79a598d89a906`. Both histories
remain ancestors of the branch. Production deployment is a separate decision.

## Route ownership

The fork's `issue_conveyor` remains responsible for routed issues. Upstream
Project Workflows coexist in the schema; `project_workflows_v1` defaults to
false. Do not enable that flag during this upgrade or attach workflows to
existing projects without a separate migration plan.

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

Notably absent from live and this integration are their `/profiles`,
`/subscriptions`, and `/conveyor` web pages, model selectors, subscription
wizards, and related core queries/types. The conveyor branch additionally
contains custom pages and newer subscription hooks. Their backend equivalents
are already in live. Matching missing Russian translation keys were restored
from those branches; new upstream messages were translated to keep the Russian
bundle aligned with the English schema and interpolation parameters.

These features have not been silently imported into an upstream merge. Decide
whether to port them separately; the integration preserves the live baseline,
not every change ever made on another fork branch.

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

The root override pins undici 7 to 7.30.1, addressing newly introduced audit
advisories in Electron's download dependency. Existing audit findings in the
baseline remain; compare advisory identifiers, not only the total count.

## Review environment

`docker-compose.upstream-sync.yml` builds both production images and starts an
isolated PostgreSQL database. It is intended for the stand service, which
supplies `STAND_URL`, generates `DB_PASSWORD` and `JWT_SECRET`, and publishes
only the web service. Keep this test environment private: it uses development
authentication and the fixed verification code `411411`. No production
credentials or data belong in it.

## Deployment and rollback gates

Do not deploy until production-copy migration checks and the final acceptance
checks pass and the owner approves the rollout. No new application secret or
production environment variable is required by the merge fixes.

The rollback baseline is image tag
`stt-agent-terminal-fixes-23eb8b5c9`, commit `23eb8b5c9`. Before deploying,
record immutable digests for both current images and register that baseline as
the previous healthy release in harness-control. Retain a database backup.

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
