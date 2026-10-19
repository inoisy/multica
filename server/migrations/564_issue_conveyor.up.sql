-- Opt-in linear route for one issue: the executor hands in a structured
-- result and the server decides, in the transaction that ends the run, which
-- run starts next. One row per issue; route is a snapshot taken when the
-- route was enabled ([{idx, step, agent_id}]), the last node is the
-- coordinator. No foreign keys; issue and workspace deletion remove these
-- rows in the application deletion graph.
--
-- status: active | paused | awaiting_coordinator | waiting_provider |
-- completed | disabled. revision changes with every operator action and is
-- the compare-and-swap token of the API; cur_idx and round advance with the
-- route itself.
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

-- Append-only journal of what the route saw and decided. kind:
--   input       a run of the current assignment reached a terminal status
--   result      the structured result handed in by that run (immutable)
--   decision    what the server decided about an input (action column)
--   action      a run the route owes (state pending|delivered|cancelled;
--               task_id is set once, when the run exists)
--   state       an operator action (enable, pause, resume, disable)
--   superseded  a wakeup or mention the route already covers (covered_by)
--   rejected    a result handed in by a run that may not hand one in (reason)
--   rejected_stale  a run withdrawn at claim or start because its action is
--               no longer current (reason)
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
