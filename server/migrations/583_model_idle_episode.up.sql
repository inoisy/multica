-- An agent stood still because every model of its run's list was on a limit
-- and no exhausted-profile applied. One open episode per agent; the owner of
-- the workspace is told once when it opens and once when it ends.
CREATE TABLE IF NOT EXISTS model_idle_episode (
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 workspace_id uuid NOT NULL,
 agent_id uuid NOT NULL,
 task_id uuid,
 until timestamptz,
 runtimes jsonb NOT NULL DEFAULT '[]'::jsonb,
 started_at timestamptz NOT NULL DEFAULT now(),
 ended_at timestamptz
);
