-- Gives the schema back without its data: the tables are empty, the columns
-- are NULL or ''. Routes, their events and squad templates come back only
-- from a database backup taken before 618.
CREATE TABLE IF NOT EXISTS issue_conveyor (
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 workspace_id uuid NOT NULL,
 issue_id uuid NOT NULL,
 route jsonb NOT NULL,
 revision bigint NOT NULL DEFAULT 1,
 cur_idx integer NOT NULL,
 round integer NOT NULL DEFAULT 1,
 cur_task_id uuid,
 status text NOT NULL CHECK (status IN ('active','paused','awaiting_coordinator','waiting_provider','completed','disabled')),
 coordinator_agent_id uuid NOT NULL,
 enabled_by uuid NOT NULL,
 accepted_commit text,
 waiting_reason text,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE IF NOT EXISTS issue_conveyor_event (
 id uuid NOT NULL DEFAULT gen_random_uuid(),
 seq bigint GENERATED ALWAYS AS IDENTITY,
 conveyor_id uuid NOT NULL,
 workspace_id uuid NOT NULL,
 issue_id uuid NOT NULL,
 kind text NOT NULL CHECK (kind IN ('input','result','decision','action','state','superseded','rejected','rejected_stale')),
 source_task_id uuid,
 revision bigint,
 idx integer,
 round integer,
 action text,
 state text CHECK (state IN ('pending','delivered','cancelled')),
 task_id uuid,
 covered_by text,
 reason text,
 details jsonb NOT NULL DEFAULT '{}'::jsonb,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

ALTER TABLE agent_task_queue ADD COLUMN IF NOT EXISTS conveyor_action_id uuid;

CREATE UNIQUE INDEX IF NOT EXISTS issue_conveyor_id_uidx ON issue_conveyor(id);
CREATE UNIQUE INDEX IF NOT EXISTS issue_conveyor_issue_uidx ON issue_conveyor(issue_id);
CREATE UNIQUE INDEX IF NOT EXISTS issue_conveyor_event_id_uidx ON issue_conveyor_event(id);
CREATE UNIQUE INDEX IF NOT EXISTS issue_conveyor_event_input_uidx ON issue_conveyor_event(conveyor_id, source_task_id, kind) WHERE kind IN ('input','result','decision','rejected');
CREATE UNIQUE INDEX IF NOT EXISTS issue_conveyor_event_action_uidx ON issue_conveyor_event(conveyor_id, revision, idx, round, action) WHERE kind = 'action';
CREATE INDEX IF NOT EXISTS issue_conveyor_event_seq_idx ON issue_conveyor_event(conveyor_id, seq);
CREATE INDEX IF NOT EXISTS issue_conveyor_event_pending_idx ON issue_conveyor_event(created_at) WHERE kind = 'action' AND state = 'pending';
CREATE INDEX IF NOT EXISTS issue_conveyor_event_source_task_idx ON issue_conveyor_event(source_task_id) WHERE source_task_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_agent_task_queue_conveyor_action ON agent_task_queue(conveyor_action_id) WHERE conveyor_action_id IS NOT NULL;

ALTER TABLE squad ADD COLUMN IF NOT EXISTS route_template TEXT NOT NULL DEFAULT '';
