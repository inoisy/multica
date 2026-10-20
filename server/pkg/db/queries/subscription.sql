-- Instance-level subscriptions (migration 589). Integrity between
-- runtime_profile.subscriptions, subscription and subscription_usage is
-- enforced in the application layer — there are no DB FKs.

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
    windows, caps, reserve, settings, account_group, disabled_until, created_by
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
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
    reserve         = COALESCE(sqlc.narg('reserve'), reserve),
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

-- name: ListRuntimeProfilesUsingSubscription :many
-- Profiles that spend a subscription, across every workspace. Callers filter
-- the result down to the workspaces the reader belongs to.
SELECT * FROM runtime_profile
WHERE subscriptions ? @key::text
ORDER BY workspace_id, created_at;

-- name: ListRuntimeProfilesWithSubscriptions :many
SELECT * FROM runtime_profile
WHERE jsonb_array_length(subscriptions) > 0
ORDER BY workspace_id, created_at;

-- name: RemoveSubscriptionFromRuntimeProfiles :many
-- Drops one key from every profile's ordered list, keeping the order of the rest.
UPDATE runtime_profile
SET subscriptions = COALESCE((
        SELECT jsonb_agg(elem ORDER BY ord)
        FROM jsonb_array_elements_text(subscriptions) WITH ORDINALITY AS t(elem, ord)
        WHERE elem <> @key::text
    ), '[]'::jsonb),
    updated_at = now()
WHERE subscriptions ? @key::text
RETURNING *;

-- name: SetRuntimeProfileSubscriptions :many
-- Sets the subscriptions and spend order of a profile and of every copy that
-- shares its share_key, in every workspace.
UPDATE runtime_profile rp
SET subscriptions = @subscriptions::jsonb,
    spend_order   = @spend_order::text,
    updated_at    = now()
WHERE rp.id = @id
   OR (@share_key::text <> '' AND rp.share_key = @share_key::text)
RETURNING *;

-- name: SyncRuntimeProfileCopies :many
-- Copies the editable fields of one profile onto every other copy with the
-- same share_key.
UPDATE runtime_profile rp
SET display_name   = src.display_name,
    command_name   = src.command_name,
    description    = src.description,
    fixed_args     = src.fixed_args,
    enabled        = src.enabled,
    subscriptions  = src.subscriptions,
    spend_order    = src.spend_order,
    all_workspaces = src.all_workspaces,
    updated_at     = now()
FROM runtime_profile src
WHERE src.id = @id
  AND src.share_key IS NOT NULL
  AND rp.share_key = src.share_key
  AND rp.id <> src.id
RETURNING rp.*;

-- name: CreateSharedRuntimeProfile :one
INSERT INTO runtime_profile (
    workspace_id, display_name, protocol_family, command_name, description,
    fixed_args, visibility, created_by, enabled, runtime_type,
    subscriptions, spend_order, share_key, all_workspaces
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: ListAllWorkspaceRuntimeProfiles :many
-- One template per share_key among profiles marked for every workspace.
SELECT DISTINCT ON (share_key) * FROM runtime_profile
WHERE all_workspaces = true AND share_key IS NOT NULL
ORDER BY share_key, created_at ASC;

-- name: ListRuntimeProfilesByShareKey :many
SELECT * FROM runtime_profile
WHERE share_key = $1
ORDER BY created_at ASC;

-- name: ListAllWorkspaceIDs :many
SELECT id FROM workspace
ORDER BY created_at ASC;

-- name: SetRuntimeProfileSharing :one
-- Gives a profile a share_key (kept if it already has one) and sets whether
-- every workspace carries a copy.
UPDATE runtime_profile
SET share_key      = COALESCE(share_key, @share_key::text),
    all_workspaces = @all_workspaces::bool,
    updated_at     = now()
WHERE id = @id
RETURNING *;
