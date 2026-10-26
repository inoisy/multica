-- Gives the schema back without its data: the tables and columns are empty or
-- hold their defaults. Their contents (an agent's own rows, reserves, the 611
-- and 613 backups, the share keys of runtime copies, the cleared groups) come
-- back only from a database backup taken before 616. Until it is restored,
-- 611 down and 613 down find no backup and change nothing, and 610 down gives
-- the pools new share keys instead of the original ones.
CREATE TABLE IF NOT EXISTS agent_model_option (
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    position integer NOT NULL CHECK (position >= 1),
    runtime_id uuid NOT NULL,
    model text NOT NULL DEFAULT '',
    thinking_level text NOT NULL DEFAULT '',
    custom_args jsonb CHECK (custom_args IS NULL OR jsonb_typeof(custom_args) = 'array')
);
CREATE UNIQUE INDEX IF NOT EXISTS agent_model_option_position_uidx ON agent_model_option (agent_id, position);

CREATE TABLE IF NOT EXISTS subscription_limit_backup (
    id uuid NOT NULL,
    caps jsonb NOT NULL,
    reserve integer NOT NULL,
    saved_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE subscription ADD COLUMN IF NOT EXISTS reserve INT NOT NULL DEFAULT 0 CHECK (reserve BETWEEN 0 AND 100);

ALTER TABLE runtime_profile ADD COLUMN IF NOT EXISTS subscriptions JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE runtime_profile ADD COLUMN IF NOT EXISTS spend_order TEXT NOT NULL DEFAULT 'first'
    CHECK (spend_order IN ('first', 'balanced'));
ALTER TABLE runtime_profile ADD COLUMN IF NOT EXISTS share_key TEXT;
ALTER TABLE runtime_profile ADD COLUMN IF NOT EXISTS all_workspaces BOOLEAN NOT NULL DEFAULT false;
CREATE UNIQUE INDEX IF NOT EXISTS runtime_profile_workspace_share_key_uidx ON runtime_profile (workspace_id, share_key) WHERE share_key IS NOT NULL;

ALTER TABLE subscription_pool ADD COLUMN IF NOT EXISTS legacy_share_key text;

ALTER TABLE runtime_unavailable DROP CONSTRAINT IF EXISTS runtime_unavailable_reason_code_check;
ALTER TABLE runtime_unavailable ALTER COLUMN reason_code SET DEFAULT '';
