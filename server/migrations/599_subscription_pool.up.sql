-- Subscription pools (STT-420). A pool is an ordered set of accounts of one CLI
-- family and the order they are spent in, shared by the whole instance. A
-- runtime profile of a workspace points at a pool with pool_id and keeps being
-- the launch setting of the CLI in that workspace.
--
-- No foreign keys: members, grants and runtime_profile.pool_id are checked and
-- cleaned up in application code. Indexes are built by the next migrations.
--   revision        +1 on every change of the member set or its order
--   legacy_share_key the runtime_profile.share_key the pool was moved from
CREATE TABLE IF NOT EXISTS subscription_pool (
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    name text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 100),
    runtime_type text NOT NULL CHECK (runtime_type IN ('claude', 'codex')),
    description text NOT NULL DEFAULT '',
    spend_order text NOT NULL DEFAULT 'first' CHECK (spend_order IN ('first', 'balanced')),
    all_workspaces boolean NOT NULL DEFAULT false,
    revision integer NOT NULL DEFAULT 1,
    legacy_share_key text,
    created_by uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS subscription_pool_member (
    pool_id uuid NOT NULL,
    subscription_id uuid NOT NULL,
    position integer NOT NULL CHECK (position >= 1)
);

CREATE TABLE IF NOT EXISTS subscription_pool_grant (
    pool_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE runtime_profile ADD COLUMN IF NOT EXISTS pool_id uuid;
