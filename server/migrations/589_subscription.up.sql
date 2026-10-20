-- Instance-level provider subscriptions (Claude / Codex accounts, API keys).
--
-- A subscription is NOT workspace-scoped: one account is shared by every
-- workspace, and each workspace decides which runtime profiles spend it.
-- Only instance admins (MULTICA_INSTANCE_ADMINS) may change these rows;
-- members of any workspace may read them.
--
-- Secrets never live here: credential_path points at a file owned by the
-- daemon's OS user on the daemon host. settings carries non-secret launch
-- settings (base URL, models, context window) for api_key subscriptions.
--
-- No foreign keys (house rule): runtime_profile.subscriptions references
-- subscription.key, subscription_usage.subscription_id references
-- subscription.id; cleanup is done in the application layer.
CREATE TABLE IF NOT EXISTS subscription (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Stable human-readable identifier (e.g. claude-3). Never changes.
    key TEXT NOT NULL CHECK (key ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    provider TEXT NOT NULL CHECK (provider IN ('claude', 'codex', 'zai', 'minimax', 'loodsen-ai', 'openrouter')),
    title TEXT NOT NULL DEFAULT '',
    owner_note TEXT NOT NULL DEFAULT '',
    auth TEXT NOT NULL CHECK (auth IN ('claude_login', 'claude_oauth', 'codex_auth', 'api_key')),
    credential_path TEXT NOT NULL DEFAULT '',
    -- Quota window keys tracked for this subscription, e.g. ["5h","week"].
    windows JSONB NOT NULL DEFAULT '[]'::jsonb,
    -- Per-window agent caps in percent, e.g. {"5h":70,"week":90}.
    caps JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- Percent of every window kept for people; agents stop at 100 - reserve.
    reserve INT NOT NULL DEFAULT 0 CHECK (reserve BETWEEN 0 AND 100),
    settings JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- Subscriptions of one account share one usage row (two keys of one
    -- plan). Empty = the subscription is its own account.
    account_group TEXT NOT NULL DEFAULT '',
    disabled_until TIMESTAMPTZ,
    created_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Usage snapshots of a subscription's quota windows.
CREATE TABLE IF NOT EXISTS subscription_usage (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subscription_id UUID NOT NULL,
    window_key TEXT NOT NULL,
    used_percent DOUBLE PRECISION NOT NULL,
    resets_at TIMESTAMPTZ,
    limit_reached BOOLEAN NOT NULL DEFAULT false,
    -- Who measured it: daemon probe, usage-dash, import, manual.
    source TEXT NOT NULL DEFAULT '',
    observed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A runtime profile spends its subscriptions in this order.
--   subscriptions  ordered subscription keys of one provider family
--   spend_order    first: first usable; balanced: least-used week
--   share_key      the same runtime across workspaces; editing one copy
--                  edits every copy
--   all_workspaces new workspaces receive a copy of this profile
ALTER TABLE runtime_profile ADD COLUMN IF NOT EXISTS subscriptions JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE runtime_profile ADD COLUMN IF NOT EXISTS spend_order TEXT NOT NULL DEFAULT 'first'
    CHECK (spend_order IN ('first', 'balanced'));
ALTER TABLE runtime_profile ADD COLUMN IF NOT EXISTS share_key TEXT;
ALTER TABLE runtime_profile ADD COLUMN IF NOT EXISTS all_workspaces BOOLEAN NOT NULL DEFAULT false;
