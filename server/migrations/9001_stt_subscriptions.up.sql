-- Fork schema: subscriptions, account pools and runtime limits, squashed from
-- the fork's migrations 580, 589-594, 599-608, 614-616 (2026-10). Idempotent:
-- an existing database that applied those migrations passes it as a no-op.
ALTER TABLE agent_task_queue ADD COLUMN IF NOT EXISTS account_fact JSONB;
ALTER TABLE agent_task_queue ADD COLUMN IF NOT EXISTS account_offer JSONB;
ALTER TABLE runtime_profile ADD COLUMN IF NOT EXISTS pool_id UUID;
DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'runtime_profile_pool_command_check' AND conrelid = 'runtime_profile'::regclass) THEN
        ALTER TABLE runtime_profile ADD CONSTRAINT runtime_profile_pool_command_check CHECK (((pool_id IS NULL) OR (command_name = runtime_type))) NOT VALID;
    END IF;
END $$;
CREATE UNIQUE INDEX IF NOT EXISTS runtime_profile_workspace_pool_uidx ON runtime_profile USING btree (workspace_id, pool_id) WHERE (pool_id IS NOT NULL);

CREATE TABLE IF NOT EXISTS runtime_unavailable (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    runtime_id uuid NOT NULL,
    until timestamp with time zone NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    task_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    reason_code text DEFAULT 'unknown'::text NOT NULL,
    subscription_id uuid,
    CONSTRAINT runtime_unavailable_reason_code_check CHECK ((reason_code = ANY (ARRAY['provider_limit'::text, 'agent_cap_refusal'::text, 'disabled_refusal'::text, 'unknown'::text])))
);

CREATE TABLE IF NOT EXISTS subscription (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    key text NOT NULL,
    provider text NOT NULL,
    title text DEFAULT ''::text NOT NULL,
    owner_note text DEFAULT ''::text NOT NULL,
    auth text NOT NULL,
    credential_path text DEFAULT ''::text NOT NULL,
    windows jsonb DEFAULT '[]'::jsonb NOT NULL,
    caps jsonb DEFAULT '{}'::jsonb NOT NULL,
    settings jsonb DEFAULT '{}'::jsonb NOT NULL,
    account_group text DEFAULT ''::text NOT NULL,
    disabled_until timestamp with time zone,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT subscription_auth_check CHECK ((auth = ANY (ARRAY['claude_login'::text, 'claude_oauth'::text, 'codex_auth'::text, 'api_key'::text]))),
    CONSTRAINT subscription_key_check CHECK ((key ~ '^[a-z0-9][a-z0-9-]{0,62}$'::text)),
    CONSTRAINT subscription_provider_check CHECK ((provider = ANY (ARRAY['claude'::text, 'codex'::text, 'zai'::text, 'minimax'::text, 'loodsen-ai'::text, 'openrouter'::text])))
);

CREATE TABLE IF NOT EXISTS subscription_host_state (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    subscription_id uuid NOT NULL,
    daemon_id text NOT NULL,
    device_name text DEFAULT ''::text NOT NULL,
    credential_state text NOT NULL,
    error text DEFAULT ''::text NOT NULL,
    checked_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT subscription_host_state_credential_state_check CHECK ((credential_state = ANY (ARRAY['present'::text, 'missing'::text, 'revoked'::text, 'unreadable'::text, 'unknown'::text])))
);

CREATE TABLE IF NOT EXISTS subscription_manual_run (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    subscription_id uuid NOT NULL,
    user_id uuid NOT NULL,
    daemon_id text NOT NULL,
    command text NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    finished_at timestamp with time zone,
    exit_code integer,
    agent_cap_exceeded boolean DEFAULT false NOT NULL,
    CONSTRAINT subscription_manual_run_check CHECK (((finished_at IS NULL) = (exit_code IS NULL))),
    CONSTRAINT subscription_manual_run_command_check CHECK ((command = ANY (ARRAY['claude'::text, 'codex'::text]))),
    CONSTRAINT subscription_manual_run_daemon_id_check CHECK (((char_length(daemon_id) >= 1) AND (char_length(daemon_id) <= 200)))
);

CREATE TABLE IF NOT EXISTS subscription_pool (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    runtime_type text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    spend_order text DEFAULT 'first'::text NOT NULL,
    all_workspaces boolean DEFAULT false NOT NULL,
    revision integer DEFAULT 1 NOT NULL,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT subscription_pool_name_check CHECK (((char_length(btrim(name)) >= 1) AND (char_length(btrim(name)) <= 100))),
    CONSTRAINT subscription_pool_runtime_type_check CHECK ((runtime_type = ANY (ARRAY['claude'::text, 'codex'::text]))),
    CONSTRAINT subscription_pool_spend_order_check CHECK ((spend_order = ANY (ARRAY['first'::text, 'balanced'::text])))
);

CREATE TABLE IF NOT EXISTS subscription_pool_grant (
    pool_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE IF NOT EXISTS subscription_pool_member (
    pool_id uuid NOT NULL,
    subscription_id uuid NOT NULL,
    "position" integer NOT NULL,
    CONSTRAINT subscription_pool_member_position_check CHECK (("position" >= 1))
);

CREATE TABLE IF NOT EXISTS subscription_usage (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    subscription_id uuid NOT NULL,
    window_key text NOT NULL,
    used_percent double precision NOT NULL,
    resets_at timestamp with time zone,
    limit_reached boolean DEFAULT false NOT NULL,
    source text DEFAULT ''::text NOT NULL,
    observed_at timestamp with time zone DEFAULT now() NOT NULL
);

DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'subscription_host_state_pkey' AND conrelid = 'subscription_host_state'::regclass) THEN
        ALTER TABLE subscription_host_state ADD CONSTRAINT subscription_host_state_pkey PRIMARY KEY (id);
    END IF;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'subscription_manual_run_pkey' AND conrelid = 'subscription_manual_run'::regclass) THEN
        ALTER TABLE subscription_manual_run ADD CONSTRAINT subscription_manual_run_pkey PRIMARY KEY (id);
    END IF;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'subscription_pkey' AND conrelid = 'subscription'::regclass) THEN
        ALTER TABLE subscription ADD CONSTRAINT subscription_pkey PRIMARY KEY (id);
    END IF;
END $$;

DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'subscription_usage_pkey' AND conrelid = 'subscription_usage'::regclass) THEN
        ALTER TABLE subscription_usage ADD CONSTRAINT subscription_usage_pkey PRIMARY KEY (id);
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS runtime_unavailable_runtime_idx ON runtime_unavailable USING btree (runtime_id, until);

CREATE UNIQUE INDEX IF NOT EXISTS subscription_host_state_uidx ON subscription_host_state USING btree (subscription_id, daemon_id);

CREATE UNIQUE INDEX IF NOT EXISTS subscription_key_uidx ON subscription USING btree (key);

CREATE INDEX IF NOT EXISTS subscription_manual_run_account_started_idx ON subscription_manual_run USING btree (subscription_id, started_at DESC);

CREATE UNIQUE INDEX IF NOT EXISTS subscription_pool_grant_uidx ON subscription_pool_grant USING btree (pool_id, workspace_id);

CREATE INDEX IF NOT EXISTS subscription_pool_grant_workspace_idx ON subscription_pool_grant USING btree (workspace_id);

CREATE UNIQUE INDEX IF NOT EXISTS subscription_pool_id_uidx ON subscription_pool USING btree (id);

CREATE INDEX IF NOT EXISTS subscription_pool_member_account_idx ON subscription_pool_member USING btree (subscription_id);

CREATE UNIQUE INDEX IF NOT EXISTS subscription_pool_member_position_uidx ON subscription_pool_member USING btree (pool_id, "position");

CREATE UNIQUE INDEX IF NOT EXISTS subscription_pool_member_subscription_uidx ON subscription_pool_member USING btree (pool_id, subscription_id);

CREATE INDEX IF NOT EXISTS subscription_usage_latest_idx ON subscription_usage USING btree (subscription_id, window_key, observed_at DESC);
