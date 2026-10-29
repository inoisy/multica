-- name: GetIssueStageDispatch :one
SELECT * FROM issue_stage_dispatch
WHERE issue_id = $1;

-- name: UpsertIssueStageDispatch :one
-- Turns stage dispatch on for a parent issue, or moves it to another squad.
INSERT INTO issue_stage_dispatch (issue_id, workspace_id, squad_id, dispatch_by)
VALUES ($1, $2, $3, $4)
ON CONFLICT (issue_id) DO UPDATE
SET squad_id = EXCLUDED.squad_id, dispatch_by = EXCLUDED.dispatch_by, updated_at = now()
RETURNING *;

-- name: DeleteIssueStageDispatch :exec
DELETE FROM issue_stage_dispatch
WHERE issue_id = $1;

-- name: ListStageDispatchParents :many
-- Parents with stage dispatch on, for the periodic sweep.
SELECT issue_id FROM issue_stage_dispatch
ORDER BY issue_id
LIMIT 500;
