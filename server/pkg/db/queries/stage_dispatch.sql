-- name: SetIssueStageDispatch :one
-- Turns an epic's stage dispatch on (a squad) or off (NULL).
UPDATE issue SET stage_squad_id = sqlc.narg('squad_id'), stage_dispatch_by = sqlc.narg('dispatch_by'), updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: ListStageDispatchParents :many
-- Epics whose stages the server dispatches, for the periodic sweep.
SELECT id FROM issue
WHERE stage_squad_id IS NOT NULL
ORDER BY id
LIMIT 500;
