# Model accounts: server API and verification

The data contract and rollback policy are in [model-selection-and-accounts.md](model-selection-and-accounts.md).
This document describes contract `model-accounts/v1` for daemon and client implementations.
No real provider CLI is needed to exercise these endpoints.

## Pools

All pool writes require an instance administrator authenticated in person;
task tokens and machine tokens cannot mutate them. Reads expose only granted
pools and intersect grants and environments with the reader's workspaces.
An instance administrator can see all grants. Revoking a grant disables its
environment and removes that workspace's pool access; existing runs retain their offer.

| Method and path | Body / behavior |
| --- | --- |
| `GET /api/pools` | `{pools: Pool[], can_manage: boolean}` |
| `GET /api/pools/{id}` | One pool; inaccessible pools return 404 |
| `POST /api/pools` | `name`, `runtime_type` (`claude` or `codex`), optional `description`, `spend_order` (`first` or `balanced`), ordered subscription keys in `subscriptions`, `workspace_ids`, `all_workspaces` |
| `PATCH /api/pools/{id}` | Required expected `revision`; optional `name`, `description`, `spend_order`, `subscriptions`, `all_workspaces` |
| `PUT /api/pools/{id}/grants` | Required expected `revision`, exact `workspace_ids` list |
| `DELETE /api/pools/{id}` | 204; 409 while any environment still references the pool |

A pool has `id`, `name`, `runtime_type`, `description`, `spend_order`,
`all_workspaces`, `revision`, ordered `subscriptions`, `workspace_ids`,
`environments`, `availability`, `created_at`, and `updated_at`.
Each environment has `runtime_profile_id`, `workspace_id`, `display_name`, and `enabled`.
An account entry has `id`, `key`, `title`, `provider`, `account_group`, `position`,
`blocked`, `usable`, `reason_code`, `until`, and `until_estimated`.
Availability carries the same verdict fields without account identity.
`until_estimated=true` means that the time is a retry estimate, not a reported reset.

Composition and spend-order changes increment the revision in the same transaction.
Other edits and grants validate the supplied composition revision without changing it.
A stale revision returns 409 with `code=pool_revision_conflict` and `current_revision`.
Account keys in mutation bodies resolve to ids; joins and grants store ids.

## Agent profiles

Reads use the same agent visibility rules as the agent list and detail view.
Personal profiles of inaccessible agents are absent from `kind=personal|all`,
shared-profile references, and impact previews. Direct profile reads/previews
return 404; `GET /api/agents/{id}/models` returns 403 for a private agent the
member cannot view. Agent owners, workspace admins, and allowed members retain
their existing access.

Personal-profile writes have the same agent-owner / workspace-admin permission
check as the existing agent model editor.

| Method and path | Body / behavior |
| --- | --- |
| `GET /api/agents/{id}/models` | Existing response plus `mode`, `personal_profile_id`, `base_profile_id`, `base_profile_name`, `matches_base`, `diff_rows`, `personal_rows` |
| `POST /api/agents/{id}/models/personal` | Optional `from_profile_id`; copies that shared profile, or the agent's current shared profile |
| `PUT /api/agents/{id}/models/personal` | `options[]`, optional `from_profile_id`, optional `when_exhausted_profile_id` |
| `PUT /api/agents/{id}/models/single` | One row: `runtime_id`, `model`, optional `thinking_level`, optional `custom_args` |
| `DELETE /api/agents/{id}/models/personal` | Returns to the base and deletes the personal profile and its rows atomically |

Mutation responses are the updated agent models response. `mode` is `fields`,
`profile`, `personal`, or `single`. Row differences use one-based positions.
`matches_base=null` means no base exists. Deleting the base preserves the
personal profile and clears its base reference.
Only the owning agent can reference its personal profile; issues, route steps,
and fallback references require shared profiles.

The old `PUT /api/agents/{id}/models` with `options[]` now writes a personal
profile; an empty list resets it. Compatible `own_list`, `options`, and
`own_list_agents` fields are computed from personal profiles. Runtime profile
responses retain computed `subscriptions`, `spend_order`, `share_key`, and
`all_workspaces`, with the new `pool_id`.
Legacy subscription mutations are routed through the pool service.
Legacy `reserve` input folds into `caps`; returned `reserve` is zero and
`effective_caps` is the cap map without entries equal to 100.

## Account read privacy and caps

`GET /api/subscriptions` and `GET /api/subscriptions/{key}` return credential
paths, host details, and active login sessions only to an instance administrator
authenticated in person. For other readers (including task tokens), the existing
response fields remain `credential_path: ""`, `hosts: []`, and `login: null`.
Aggregate `login_state`, usage, and workspace-filtered consumers remain visible.
Daemon provisioning keeps its separate authenticated contract.

On account creation and update, `caps` must be an object of integer percentages
from 0 through 100. Null values, fractional/string values, and keys absent from
the account's resulting `windows` are rejected with 400 without changing data.
An omitted `caps` leaves caps unchanged; updates merge by window.

## Claim and account confirmation

A claim transaction saves `account_offer` and clears any prior `account_fact`.
The offer pins a unique `offer_id`, daemon id, pool id and composition revision,
spend order, and ordered account ids/keys/groups. It contains no credential paths.
The daemon receives the existing `subscription_pool` payload with additional
`offer_id`, `pool_id`, and `revision` fields. Accounts with active provider
refusals project as `disabled=true`; the stored administrator setting is unchanged.

Before starting the CLI, the daemon calls:

```http
POST /api/daemon/tasks/{taskId}/account
Content-Type: application/json

{"offer_id":"<uuid>","subscription_id":"<uuid>","pool_id":"<uuid>","pool_revision":1}
```

The response is `{account_fact: {subscription_id, subscription_key, account_group,
pool_id, pool_revision, daemon_id, confirmed_at}}`. The server takes key/group
from the saved offer and supplies daemon/time itself. The current pool's
composition is irrelevant to confirmation. UUID inputs are validated.

| Status | Meaning |
| --- | --- |
| 200 | Confirmed, or an identical confirmation repeated for the same assignment |
| 400 | Invalid body, UUID, or nonpositive revision |
| 404 | Task/runtime/daemon/workspace access does not match |
| 409 `no_offer` | This assignment was given no pool |
| 409 `stale_offer` | A newer claim replaced the assignment |
| 409 `not_offered` | Account, pool, or revision does not match the saved offer |
| 409 `account_conflict` | Another account was already confirmed |
| 409 `task_finished` | A first confirmation arrived after termination |

Confirmation locks the task row. Concurrent different accounts cannot replace
one another. Identical repeats after termination return the original fact and
timestamp; a first write after termination fails. A reissued task gets a new
offer and no prior fact. Task responses and model-choice journals expose the
confirmed fact, never the offer. Old daemons leave the fact absent (unknown).

## Structured failure reports

`POST /api/daemon/tasks/{taskId}/fail` accepts optional `refusal_code`:
`agent_cap_refusal`, `disabled_refusal`, or `provider_limit`. A supplied code
wins over the human-readable `error` and classifies the failure as a quota
refusal. Unknown request codes return 400 without changing the task.
`quota_reset_at` remains optional. Both fields survive the daemon terminal
report outbox and its retries. Older daemons omit the code; only that
compatibility path classifies their local refusal text. No extra quota
registry is kept in the CLI or daemon.

The new daemon's account confirmation retries transient HTTP/transport errors
using the existing bounded request retry helper, always with the same offer,
account id, pool id and revision. A malformed/mismatched success response,
409, or an exhausted retry fails preparation before CLI launch. Chi's plain
`404 page not found` response identifies an old server without this route;
its account remains unknown. A structured 404 from the supported endpoint
means access/assignment failure and stops launch. Payloads without all three
new pool fields use the old protocol only when all three are absent; partial
new metadata and an empty new offer are rejected.

`AccountFact` and `AccountConfirmation` are shared Go wire types in
`server/pkg/protocol/account.go`; the service re-exports aliases. CLI commands
are documented in [the CLI guide](../../apps/docs/content/docs/cli.mdx#model-profiles-and-account-pools).

## Availability and verification

The account availability service supplies pool, runtime, forecast and claim
verdicts. Each usage window is checked for freshness and reset independently.
An account stays blocked until every blocking window, disable and provider
refusal clears; any usable account opens the pool. Host sign-in applies to the
runtime's daemon. Shared accounts carry their refusals across pools.
Stored refusal rows are interpreted and lifted by code; historical pool verdicts
are ignored. Provider failure classification accepts the old daemon's messages.

Run `make sqlc` twice and check the generated tree is unchanged on the second
run. Run `make test` with the checkout's configured PostgreSQL. Migration tests
under `server/cmd/migrate` cover round-trip, repeated data migration, collision
guards, and preserving edits on rollback; these synthetic tests supplement the
required production-copy checks.

Build the original comparison command from its dedicated baseline commit and
the new command from the server commit. On an authorized isolated database copy:

```sh
original-model-choice-compare snapshot --workspace STT --now "$SNAPSHOT_TIME" --out before.json
# Apply migrations using the new server's migrate command.
new-model-choice-compare snapshot --workspace STT --now "$SNAPSHOT_TIME" --out after.json
new-model-choice-compare diff --before before.json --after after.json
```

Keep the original refusal rows for the first snapshot. The optional original
`--recompute-pool-verdicts` pass must use a separate snapshot with an explicit diff.
The comparator checks archived agents, open agent-assigned issues, legacy route
steps, workflow catalog steps and active issue handoffs, and queued/deferred runs.
Only personal-profile identity/source normalization is allowed; shared fallback
changes, null versus empty arguments, missing contexts and preview errors fail.
The production-copy comparison and rollback-data comparison remain required
even when all synthetic tests pass. Never attach private snapshots or dumps to an issue.
