-- Fork schema: model profiles and model routing of runs, squashed from the
-- fork's migrations 575-585 and 597-598 (2026-10). Idempotent: an existing
-- database that applied those migrations passes it as a no-op.
ALTER TABLE agent ADD COLUMN IF NOT EXISTS model_profile_id UUID;
ALTER TABLE agent ADD COLUMN IF NOT EXISTS disallowed_tools JSONB NOT NULL DEFAULT '[]'::jsonb;
DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'agent_disallowed_tools_check' AND conrelid = 'agent'::regclass) THEN
        ALTER TABLE agent ADD CONSTRAINT agent_disallowed_tools_check CHECK ((jsonb_typeof(disallowed_tools) = 'array'::text));
    END IF;
END $$;
ALTER TABLE issue ADD COLUMN IF NOT EXISTS model_profile_id UUID;
ALTER TABLE agent_task_queue ADD COLUMN IF NOT EXISTS model_position INTEGER;
ALTER TABLE agent_task_queue ADD COLUMN IF NOT EXISTS model_choice JSONB;
CREATE INDEX IF NOT EXISTS idx_issue_model_profile ON issue USING btree (model_profile_id) WHERE (model_profile_id IS NOT NULL);
CREATE INDEX IF NOT EXISTS idx_agent_task_queue_model_choice ON agent_task_queue USING btree (agent_id, created_at DESC) WHERE (model_position IS NOT NULL);
CREATE INDEX IF NOT EXISTS idx_agent_task_queue_model_routed_queued ON agent_task_queue USING btree (agent_id) WHERE ((status = 'queued'::text) AND (model_choice IS NOT NULL));

CREATE TABLE IF NOT EXISTS model_idle_episode (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    task_id uuid,
    until timestamp with time zone,
    runtimes jsonb DEFAULT '[]'::jsonb NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    ended_at timestamp with time zone
);

CREATE TABLE IF NOT EXISTS model_profile (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    when_exhausted_profile_id uuid,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    owner_agent_id uuid,
    based_on_profile_id uuid,
    CONSTRAINT model_profile_description_check CHECK ((char_length(description) <= 500)),
    CONSTRAINT model_profile_name_check CHECK (((char_length(btrim(name)) >= 1) AND (char_length(btrim(name)) <= 100)))
);

CREATE TABLE IF NOT EXISTS model_profile_option (
    workspace_id uuid NOT NULL,
    profile_id uuid NOT NULL,
    "position" integer NOT NULL,
    runtime_id uuid NOT NULL,
    model text DEFAULT ''::text NOT NULL,
    thinking_level text DEFAULT ''::text NOT NULL,
    custom_args jsonb,
    CONSTRAINT model_profile_option_custom_args_check CHECK (((custom_args IS NULL) OR (jsonb_typeof(custom_args) = 'array'::text))),
    CONSTRAINT model_profile_option_position_check CHECK (("position" >= 1))
);

CREATE UNIQUE INDEX IF NOT EXISTS model_idle_episode_open_uidx ON model_idle_episode USING btree (agent_id) WHERE (ended_at IS NULL);

CREATE UNIQUE INDEX IF NOT EXISTS model_profile_id_uidx ON model_profile USING btree (id);

CREATE UNIQUE INDEX IF NOT EXISTS model_profile_option_position_uidx ON model_profile_option USING btree (profile_id, "position");

CREATE UNIQUE INDEX IF NOT EXISTS model_profile_owner_agent_uidx ON model_profile USING btree (owner_agent_id) WHERE (owner_agent_id IS NOT NULL);

CREATE UNIQUE INDEX IF NOT EXISTS model_profile_workspace_name_uidx ON model_profile USING btree (workspace_id, lower(name));
