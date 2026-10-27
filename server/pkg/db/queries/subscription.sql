-- Instance-level subscriptions (migration 589). Integrity between the pools,
-- subscription and subscription_usage is enforced in the application layer —
-- there are no DB FKs.

-- name: ListSubscriptions :many
SELECT * FROM subscription
ORDER BY provider ASC, created_at ASC;

-- name: GetSubscriptionByKey :one
SELECT * FROM subscription
WHERE key = $1;

-- name: ListSubscriptionsByKeys :many
SELECT * FROM subscription
WHERE key = ANY(@keys::text[]);

-- name: CreateSubscription :one
INSERT INTO subscription (
    key, provider, title, owner_note, auth, credential_path,
    windows, caps, settings, account_group, disabled_until, created_by
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING *;

-- name: UpdateSubscription :one
-- Partial update via COALESCE: NULL args leave the column unchanged. key and
-- provider are immutable. disabled_until is cleared with clear_disabled.
UPDATE subscription
SET title           = COALESCE(sqlc.narg('title'), title),
    owner_note      = COALESCE(sqlc.narg('owner_note'), owner_note),
    auth            = COALESCE(sqlc.narg('auth'), auth),
    credential_path = COALESCE(sqlc.narg('credential_path'), credential_path),
    windows         = COALESCE(sqlc.narg('windows'), windows),
    caps            = COALESCE(sqlc.narg('caps'), caps),
    settings        = COALESCE(sqlc.narg('settings'), settings),
    account_group   = COALESCE(sqlc.narg('account_group'), account_group),
    disabled_until  = CASE WHEN @clear_disabled::bool THEN NULL
                           ELSE COALESCE(sqlc.narg('disabled_until'), disabled_until) END,
    updated_at      = now()
WHERE key = @key
RETURNING *;

-- name: DeleteSubscription :exec
DELETE FROM subscription
WHERE id = $1;

-- name: DeleteSubscriptionUsage :exec
DELETE FROM subscription_usage
WHERE subscription_id = $1;

-- name: CreateSubscriptionUsage :one
INSERT INTO subscription_usage (
    subscription_id, window_key, used_percent, resets_at, limit_reached, source, observed_at
) VALUES ($1, $2, $3, $4, $5, $6, COALESCE(sqlc.narg('observed_at'), now()))
RETURNING *;

-- name: ListLatestSubscriptionUsage :many
-- The newest snapshot of every window of every subscription.
SELECT DISTINCT ON (subscription_id, window_key) *
FROM subscription_usage
ORDER BY subscription_id, window_key, observed_at DESC;

-- name: ListSubscriptionUsageSince :many
SELECT * FROM subscription_usage
WHERE subscription_id = $1 AND observed_at >= @since::timestamptz
ORDER BY observed_at DESC
LIMIT @max_rows::int;

-- name: ListAllWorkspaceIDs :many
SELECT id FROM workspace
ORDER BY created_at ASC;

-- name: UpsertSubscriptionHostState :one
INSERT INTO subscription_host_state (subscription_id, daemon_id, device_name, credential_state, error, checked_at)
VALUES ($1, $2, $3, $4, $5, now())
ON CONFLICT (subscription_id, daemon_id) DO UPDATE
SET device_name = EXCLUDED.device_name,
    credential_state = EXCLUDED.credential_state,
    error = EXCLUDED.error,
    checked_at = now()
RETURNING *;

-- name: ListSubscriptionHostStates :many
SELECT * FROM subscription_host_state
ORDER BY subscription_id, daemon_id;

-- name: DeleteSubscriptionHostStates :exec
DELETE FROM subscription_host_state
WHERE subscription_id = $1;

-- name: ListAgentRuntimesByProfiles :many
-- Registered runtime instances of the given profiles, for the "every
-- subscription is over its cap" decision.
SELECT id, workspace_id, profile_id FROM agent_runtime
WHERE profile_id = ANY(@profile_ids::uuid[]);

-- name: ListAgentsOnRuntimeProfiles :many
-- Active agents bound to runtimes of the given profiles, for discrepancies.
SELECT a.id, a.name, a.workspace_id, ar.profile_id
FROM agent a
JOIN agent_runtime ar ON ar.id = a.runtime_id
WHERE ar.profile_id = ANY(@profile_ids::uuid[]) AND a.archived_at IS NULL;

-- name: CreateSubscriptionManualRun :one
INSERT INTO subscription_manual_run (subscription_id, user_id, daemon_id, command, agent_cap_exceeded)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetSubscriptionManualRun :one
SELECT * FROM subscription_manual_run
WHERE id = $1 AND subscription_id = $2;

-- name: CloseSubscriptionManualRun :one
-- Closes a record once; a record that is already closed is left as it is.
UPDATE subscription_manual_run
SET finished_at = now(), exit_code = @exit_code
WHERE id = @id AND subscription_id = @subscription_id AND finished_at IS NULL
RETURNING *;

-- name: ListSubscriptionManualRuns :many
SELECT r.*, COALESCE(u.name, '')::text AS user_name
FROM subscription_manual_run r
LEFT JOIN "user" u ON u.id = r.user_id
WHERE r.subscription_id = @subscription_id
  AND (sqlc.narg('before')::timestamptz IS NULL OR r.started_at < sqlc.narg('before')::timestamptz)
ORDER BY r.started_at DESC, r.id DESC
LIMIT @row_limit;

-- name: DeleteSubscriptionManualRuns :exec
DELETE FROM subscription_manual_run
WHERE subscription_id = $1;

-- name: DeleteExpiredSubscriptionManualRuns :execrows
DELETE FROM subscription_manual_run
WHERE id IN (
    SELECT id FROM subscription_manual_run
    WHERE started_at < @before::timestamptz
    ORDER BY started_at
    LIMIT @batch_size
);
