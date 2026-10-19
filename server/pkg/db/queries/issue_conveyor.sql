-- name: GetIssueConveyor :one
SELECT * FROM issue_conveyor WHERE issue_id = @issue_id;

-- name: LockIssueConveyor :one
-- Terminal writes take task -> route without locking the issue. Delivery takes
-- catalog -> issue -> route and only inserts a new task, never locks an old one.
SELECT * FROM issue_conveyor WHERE issue_id = @issue_id FOR UPDATE;

-- name: LockIssueForConveyorDelivery :one
-- Keep closure and status/metadata projection atomic with delivery of a run.
SELECT * FROM issue WHERE id = @id AND workspace_id = @workspace_id FOR UPDATE;

-- name: InsertIssueConveyor :one
INSERT INTO issue_conveyor (
    id, workspace_id, issue_id, route, revision, cur_idx, round, status,
    coordinator_agent_id, enabled_by, accepted_commit, waiting_reason
) VALUES (
    @id, @workspace_id, @issue_id, @route, 1, @cur_idx, 1, 'active',
    @coordinator_agent_id, @enabled_by, NULL, NULL
)
RETURNING *;

-- name: SaveIssueConveyor :one
-- Writes the whole mutable state of a locked row.
UPDATE issue_conveyor SET
    route = @route,
    revision = @revision,
    cur_idx = @cur_idx,
    round = @round,
    cur_task_id = sqlc.narg(cur_task_id),
    status = @status,
    coordinator_agent_id = @coordinator_agent_id,
    enabled_by = @enabled_by,
    accepted_commit = sqlc.narg(accepted_commit),
    waiting_reason = sqlc.narg(waiting_reason),
    updated_at = clock_timestamp()
WHERE id = @id
RETURNING *;

-- name: InsertIssueConveyorEvent :one
-- DO NOTHING on any unique key: the partial unique indexes make a replayed
-- input, a repeated result or a second action for the same position return no
-- row instead of a second one.
INSERT INTO issue_conveyor_event (
    id, conveyor_id, workspace_id, issue_id, kind, source_task_id, revision, idx, round,
    action, state, task_id, covered_by, reason, details
) VALUES (
    @id, @conveyor_id, @workspace_id, @issue_id, @kind, sqlc.narg(source_task_id), sqlc.narg(revision), sqlc.narg(idx), sqlc.narg(round),
    sqlc.narg(action), sqlc.narg(state), sqlc.narg(task_id), sqlc.narg(covered_by), sqlc.narg(reason), COALESCE(sqlc.narg(details)::jsonb, '{}'::jsonb)
)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetIssueConveyorEvent :one
SELECT * FROM issue_conveyor_event WHERE id = @id;

-- name: LockIssueConveyorEvent :one
SELECT * FROM issue_conveyor_event WHERE id = @id FOR UPDATE;

-- name: GetIssueConveyorRunEvent :one
-- The input or the result a run left on its route.
SELECT * FROM issue_conveyor_event
WHERE conveyor_id = @conveyor_id AND source_task_id = @source_task_id AND kind = @kind
LIMIT 1;

-- name: GetIssueConveyorDecision :one
SELECT * FROM issue_conveyor_event
WHERE conveyor_id = @conveyor_id AND source_task_id = @source_task_id AND kind = 'decision'
LIMIT 1;

-- name: ListIssueConveyorDecisionsForTasks :many
-- Decisions already recorded for these runs; a decision that moved the route
-- (not one that refused a stale run) means the route covers the run's ending.
SELECT * FROM issue_conveyor_event
WHERE issue_id = @issue_id AND kind = 'decision' AND source_task_id = ANY(@task_ids::uuid[])
  AND action IN ('advance','complete','escalate','wait_provider');

-- name: ListIssueConveyorEvents :many
SELECT * FROM issue_conveyor_event
WHERE conveyor_id = @conveyor_id AND seq > @after_seq
ORDER BY seq
LIMIT @page_size;

-- name: ListPendingIssueConveyorActions :many
SELECT * FROM issue_conveyor_event
WHERE kind = 'action' AND state = 'pending'
  AND (sqlc.narg(issue_id)::uuid IS NULL OR issue_id = sqlc.narg(issue_id))
ORDER BY created_at
LIMIT @page_size;

-- name: SetIssueConveyorActionDelivered :exec
UPDATE issue_conveyor_event SET state = 'delivered', task_id = @task_id, reason = NULL
WHERE id = @id AND state = 'pending';

-- name: SetIssueConveyorActionReason :exec
UPDATE issue_conveyor_event SET reason = @reason WHERE id = @id AND state = 'pending';

-- name: CancelPendingIssueConveyorActions :many
UPDATE issue_conveyor_event SET state = 'cancelled', reason = @reason
WHERE conveyor_id = @conveyor_id AND kind = 'action' AND state = 'pending'
RETURNING *;

-- name: CreateConveyorTask :one
-- Fenced against workspace teardown like CreateWakeupTask: lock_task_owner_rows
-- locks the owners' workspace rows in the writer's transaction and returns
-- false once they are gone, so no row is written for a deleted workspace.
INSERT INTO agent_task_queue (
    id, agent_id, runtime_id, issue_id, status, priority, trigger_summary, is_leader_task,
    handoff_note, squad_id, context, originator_user_id, accountable_user_id,
    runtime_mcp_overlay, runtime_connected_apps, originator_source,
    trigger_evidence_kind, trigger_evidence_ref_id, conveyor_action_id, rerun_of_task_id
)
SELECT
    @id, @agent_id, @runtime_id, @issue_id, 'queued', @priority, @trigger_summary, @is_leader_task,
    @handoff_note, sqlc.narg(squad_id), @task_context::jsonb, @originator_user_id, @originator_user_id,
    sqlc.narg(runtime_mcp_overlay), sqlc.narg(runtime_connected_apps), 'direct_human',
    'conveyor_action', @action_id, @action_id, sqlc.narg(rerun_of_task_id)
WHERE lock_task_owner_rows(@agent_id, @issue_id, @runtime_id)
RETURNING *;

-- name: SetTaskConveyorAction :exec
-- Binds a rerun successor to the action its failed predecessor served and
-- gives it the route's instructions, which a plain rerun does not carry.
UPDATE agent_task_queue SET
    conveyor_action_id = @action_id,
    handoff_note = @handoff_note,
    context = COALESCE(context, '{}'::jsonb) || @task_context::jsonb
WHERE id = @id AND conveyor_action_id IS NULL;

-- name: GetLiveRerunSuccessor :one
-- The unfinished rerun of a run, if one exists.
SELECT * FROM agent_task_queue
WHERE rerun_of_task_id = @source_task_id
  AND status IN ('queued','deferred','dispatched','running','waiting_local_directory')
ORDER BY created_at DESC
LIMIT 1;

-- name: ListUnstartedConveyorTasks :many
-- Runs of this route that no executor has claimed yet; the caller withdraws the
-- ones whose action is no longer current.
SELECT t.* FROM agent_task_queue t
JOIN issue_conveyor_event e ON e.id = t.conveyor_action_id
WHERE e.conveyor_id = @conveyor_id AND t.status IN ('queued','deferred');

-- name: CountLiveIssueRunsOfAgent :one
-- Unfinished runs of the agent on the issue; a route step does not start next
-- to one.
SELECT count(*) FROM agent_task_queue
WHERE issue_id = @issue_id AND agent_id = @agent_id
  AND status IN ('queued','deferred','dispatched','running','waiting_local_directory');

-- name: ListStalledIssueConveyors :many
-- Routes that say "a run is working" while no run exists and none is owed:
-- the terminal event of the current run was lost (a sweeper failed it, a
-- person cancelled it, the server died between the status write and the
-- decision). The caller hands such a route to its coordinator.
SELECT c.* FROM issue_conveyor c
WHERE c.status = 'active' AND c.updated_at < @before
  AND NOT EXISTS (
    SELECT 1 FROM issue_conveyor_event a
    WHERE a.conveyor_id = c.id AND a.kind = 'action' AND a.state = 'pending')
  AND NOT EXISTS (
    SELECT 1 FROM agent_task_queue t
    JOIN issue_conveyor_event a ON a.id = t.conveyor_action_id
    WHERE a.conveyor_id = c.id
      AND t.status IN ('queued','deferred','dispatched','running','waiting_local_directory'))
ORDER BY c.updated_at
LIMIT @page_size;

-- name: IsIssueConveyorStalled :one
SELECT EXISTS (
  SELECT 1 FROM issue_conveyor c
  WHERE c.id = @id AND c.status = 'active' AND c.updated_at < @before
    AND NOT EXISTS (
      SELECT 1 FROM issue_conveyor_event a
      WHERE a.conveyor_id = c.id AND a.kind = 'action' AND a.state = 'pending')
    AND NOT EXISTS (
      SELECT 1 FROM agent_task_queue t
      JOIN issue_conveyor_event a ON a.id = t.conveyor_action_id
      WHERE a.conveyor_id = c.id
        AND t.status IN ('queued','deferred','dispatched','running','waiting_local_directory'))
);

-- name: CancelIssueConveyorAction :exec
UPDATE issue_conveyor_event SET state = 'cancelled', reason = @reason
WHERE id = @id AND state = 'pending';

-- name: GetLatestConveyorStepTask :one
-- The newest run that serves the step at (idx, round): the original, a system
-- retry or a rerun.
SELECT t.* FROM agent_task_queue t
JOIN issue_conveyor_event a ON a.id = t.conveyor_action_id
WHERE a.conveyor_id = @conveyor_id AND a.kind = 'action' AND a.action = 'run_step'
  AND a.idx = @idx AND a.round = @round
ORDER BY t.created_at DESC
LIMIT 1;

-- name: ListIssueConveyorsByIssues :many
-- The routes of a page of issues (an epic's sub-issues), one row per routed issue.
SELECT * FROM issue_conveyor WHERE issue_id = ANY(@issue_ids::uuid[]);
