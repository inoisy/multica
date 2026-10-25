-- Subscription pools : the ordered accounts an environment spends, the
-- workspaces they are granted to, and the runtime profile each workspace keeps
-- for them. No foreign keys; the pool service keeps the three in step in one
-- transaction.

-- name: ListSubscriptionPools :many
SELECT * FROM subscription_pool
ORDER BY lower(name), created_at, id;

-- name: GetSubscriptionPool :one
SELECT * FROM subscription_pool WHERE id = $1;

-- name: LockSubscriptionPool :one
-- Serializes writes of one pool: members, order, grants and revision.
SELECT * FROM subscription_pool WHERE id = $1 FOR UPDATE;

-- name: CreateSubscriptionPool :one
INSERT INTO subscription_pool (id, name, runtime_type, description, spend_order, all_workspaces, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: UpdateSubscriptionPool :one
-- revision_step is 1 when the member set, their order or the spend order
-- changes, 0 otherwise.
UPDATE subscription_pool
SET name = @name,
    description = @description,
    spend_order = @spend_order,
    all_workspaces = @all_workspaces,
    revision = revision + @revision_step::int,
    updated_at = now()
WHERE id = @id
RETURNING *;

-- name: DeleteSubscriptionPool :exec
DELETE FROM subscription_pool WHERE id = $1;

-- name: ListSubscriptionPoolMembers :many
SELECT * FROM subscription_pool_member
ORDER BY pool_id, position;

-- name: ListSubscriptionPoolMembersByPools :many
SELECT * FROM subscription_pool_member
WHERE pool_id = ANY(@pool_ids::uuid[])
ORDER BY pool_id, position;

-- name: DeleteSubscriptionPoolMembers :exec
DELETE FROM subscription_pool_member WHERE pool_id = $1;

-- name: InsertSubscriptionPoolMember :exec
INSERT INTO subscription_pool_member (pool_id, subscription_id, position) VALUES ($1, $2, $3);

-- name: DeleteSubscriptionFromPools :many
-- An account is deleted: it leaves every pool it was in. Returns the pools
-- that changed, so their revision can be raised.
DELETE FROM subscription_pool_member WHERE subscription_id = $1
RETURNING pool_id;

-- name: ListSubscriptionPoolGrants :many
SELECT * FROM subscription_pool_grant ORDER BY pool_id, created_at, workspace_id;

-- name: ListSubscriptionPoolGrantsByPool :many
SELECT * FROM subscription_pool_grant WHERE pool_id = $1 ORDER BY created_at, workspace_id;

-- name: InsertSubscriptionPoolGrant :exec
INSERT INTO subscription_pool_grant (pool_id, workspace_id) VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: DeleteSubscriptionPoolGrant :exec
DELETE FROM subscription_pool_grant WHERE pool_id = $1 AND workspace_id = $2;

-- name: ListAllWorkspacePools :many
-- Pools every workspace gets an environment for.
SELECT * FROM subscription_pool WHERE all_workspaces ORDER BY created_at, id;

-- name: ListPoolRuntimeProfiles :many
-- The environments of a pool, one per workspace it reaches.
SELECT * FROM runtime_profile WHERE pool_id = $1 ORDER BY created_at, id;

-- name: ListRuntimeProfilesByPools :many
SELECT * FROM runtime_profile WHERE pool_id = ANY(@pool_ids::uuid[]) ORDER BY created_at, id;

-- name: GetPoolRuntimeProfileInWorkspace :one
SELECT * FROM runtime_profile WHERE pool_id = $1 AND workspace_id = $2;

-- name: CreatePoolRuntimeProfile :one
-- The environment a workspace keeps for a pool. command_name is the CLI family.
INSERT INTO runtime_profile (
    workspace_id, display_name, protocol_family, command_name, description,
    fixed_args, visibility, created_by, enabled, runtime_type, pool_id
) VALUES ($1, $2, $3, $3, $4, '[]'::jsonb, 'workspace', $5, true, $3, $6)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: SyncPoolRuntimeProfiles :many
-- Name and description of a pool go to its environments in one statement.
UPDATE runtime_profile
SET display_name = @display_name,
    description = @description,
    updated_at = now()
WHERE pool_id = @pool_id
  AND (display_name <> @display_name OR description IS DISTINCT FROM @description)
RETURNING *;

-- name: SetPoolRuntimeProfilesEnabled :many
-- Switching an environment of a pool on or off applies to the environments the
-- pool is granted to; a revoked one stays off.
UPDATE runtime_profile rp
SET enabled = @enabled, updated_at = now()
WHERE rp.pool_id = @pool_id
  AND rp.enabled <> @enabled
  AND (@enabled::bool = false OR rp.workspace_id IN (
        SELECT g.workspace_id FROM subscription_pool_grant g WHERE g.pool_id = @pool_id
        UNION SELECT w.id FROM workspace w WHERE EXISTS (SELECT 1 FROM subscription_pool p WHERE p.id = @pool_id AND p.all_workspaces)))
RETURNING *;

-- name: DisablePoolRuntimeProfileInWorkspace :many
-- A grant is taken back: the environment stays, it is switched off.
UPDATE runtime_profile
SET enabled = false, updated_at = now()
WHERE pool_id = $1 AND workspace_id = $2 AND enabled
RETURNING *;

-- name: ListEnabledPoolRuntimeProfilesForWorkspace :many
-- Daemon-facing: the enabled environments of a workspace that spend a pool.
SELECT * FROM runtime_profile
WHERE workspace_id = $1 AND enabled AND pool_id IS NOT NULL
ORDER BY created_at;

-- name: ListLatestSubscriptionUsageBySubscriptions :many
SELECT DISTINCT ON (subscription_id, window_key) *
FROM subscription_usage
WHERE subscription_id = ANY(@subscription_ids::uuid[])
ORDER BY subscription_id, window_key, observed_at DESC;

-- name: ListSubscriptionsByIDs :many
SELECT * FROM subscription WHERE id = ANY(@ids::uuid[]);

-- name: ListSubscriptionHostStatesBySubscriptions :many
SELECT * FROM subscription_host_state
WHERE subscription_id = ANY(@subscription_ids::uuid[])
ORDER BY subscription_id, daemon_id;

-- name: ListActiveAccountRefusals :many
-- Provider refusals pinned to one account that still hold at now.
SELECT * FROM runtime_unavailable
WHERE subscription_id = ANY(@subscription_ids::uuid[]) AND until > @now::timestamptz
ORDER BY subscription_id, until DESC;

-- name: LockSubscriptionPoolShared :one
-- A claim reads the pool and its members under a shared lock so a concurrent
-- edit (which takes the exclusive lock) cannot slip between the two reads.
SELECT * FROM subscription_pool WHERE id = $1 FOR SHARE;

-- name: SetClaimedTaskAccountOffer :exec
-- The accounts handed to the daemon with the claim. The fact of an earlier
-- assignment is not carried over.
UPDATE agent_task_queue
SET account_offer = sqlc.narg(account_offer), account_fact = NULL
WHERE id = sqlc.arg(id) AND status = 'dispatched';

-- name: LockAgentTaskForAccount :one
SELECT * FROM agent_task_queue WHERE id = $1 FOR UPDATE;

-- name: SetTaskAccountFact :exec
UPDATE agent_task_queue SET account_fact = $2 WHERE id = $1;

-- name: ListPoolRuntimeProfilesAll :many
-- Every environment that spends a pool, in every workspace.
SELECT * FROM runtime_profile WHERE pool_id IS NOT NULL ORDER BY workspace_id, created_at, id;

-- name: ListSubscriptionPoolMembersBySubscription :many
SELECT * FROM subscription_pool_member WHERE subscription_id = $1 ORDER BY pool_id;
