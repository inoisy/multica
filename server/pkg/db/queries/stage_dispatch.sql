-- name: SetIssueStageDispatch :one
-- Turns an epic's stage dispatch on (a squad) or off (NULL).
UPDATE issue SET stage_squad_id = sqlc.narg('squad_id'), stage_dispatch_by = sqlc.narg('dispatch_by'), updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: ListStageDispatchParents :many
-- One page of epics whose stages the server dispatches, for the periodic
-- sweep. The caller pages with the last id it saw.
SELECT id FROM issue
WHERE stage_squad_id IS NOT NULL AND id > sqlc.arg('after')::uuid
ORDER BY id
LIMIT sqlc.arg('page_size')::int;

-- name: ListWorkspaceStageDispatch :many
-- Epics of a workspace whose stages the server dispatches, oldest first.
SELECT * FROM issue
WHERE workspace_id = $1 AND stage_squad_id IS NOT NULL
ORDER BY number
LIMIT 200;
