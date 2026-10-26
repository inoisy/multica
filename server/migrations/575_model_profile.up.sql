-- Model profiles of a workspace: an ordered list of (runtime, model, thinking)
-- rows that agents and issues reference. Editing a profile changes the next run
-- of everyone who references it. No foreign keys; workspace, agent, issue and
-- runtime deletion remove or detach these rows in application code.
--
-- when_exhausted_profile_id: every row of the profile is unavailable -> the
-- list of that profile is taken instead; NULL -> the run waits. Cycles are
-- refused when the profile is saved.
CREATE TABLE IF NOT EXISTS model_profile (
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 workspace_id uuid NOT NULL,
 name text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 100),
 description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 500),
 when_exhausted_profile_id uuid,
 created_by uuid,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);

-- One row of a profile. position 1 is the primary model. thinking_level ''
-- means the runtime default; custom_args NULL means derived from the agent's
-- disallowed tools for the runtime's family.
CREATE TABLE IF NOT EXISTS model_profile_option (
 workspace_id uuid NOT NULL,
 profile_id uuid NOT NULL,
 position integer NOT NULL CHECK (position >= 1),
 runtime_id uuid NOT NULL,
 model text NOT NULL DEFAULT '',
 thinking_level text NOT NULL DEFAULT '',
 custom_args jsonb CHECK (custom_args IS NULL OR jsonb_typeof(custom_args) = 'array')
);

-- An agent's own list: an explicit exception to its profile. Any row present
-- means the agent's profile is not used; a single row pins the model.
CREATE TABLE IF NOT EXISTS agent_model_option (
 workspace_id uuid NOT NULL,
 agent_id uuid NOT NULL,
 position integer NOT NULL CHECK (position >= 1),
 runtime_id uuid NOT NULL,
 model text NOT NULL DEFAULT '',
 thinking_level text NOT NULL DEFAULT '',
 custom_args jsonb CHECK (custom_args IS NULL OR jsonb_typeof(custom_args) = 'array')
);

-- A runtime that hit a provider quota or capacity limit, until the reset the
-- error named (60 minutes when it named none). Written only by the backend.
CREATE TABLE IF NOT EXISTS runtime_unavailable (
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 workspace_id uuid NOT NULL,
 runtime_id uuid NOT NULL,
 until timestamptz NOT NULL,
 reason text NOT NULL DEFAULT '',
 task_id uuid,
 created_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE agent ADD COLUMN IF NOT EXISTS model_profile_id uuid;
-- server:tool pairs the agent may not use; rendered into custom_args per
-- runtime family when a model row carries none.
ALTER TABLE agent ADD COLUMN IF NOT EXISTS disallowed_tools jsonb NOT NULL DEFAULT '[]'::jsonb
 CHECK (jsonb_typeof(disallowed_tools) = 'array');
ALTER TABLE issue ADD COLUMN IF NOT EXISTS model_profile_id uuid;

-- What the claim actually chose for a run: the row, and where it came from.
ALTER TABLE agent_task_queue ADD COLUMN IF NOT EXISTS model_position integer;
ALTER TABLE agent_task_queue ADD COLUMN IF NOT EXISTS model_choice jsonb;
