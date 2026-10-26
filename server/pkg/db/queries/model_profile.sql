-- Model profiles, agent model lists and runtime unavailability (STT-258,
-- model tiers). No foreign keys: callers validate references and clean up
-- dependents in application code.

-- name: ListModelProfiles :many
-- Every profile of the workspace, personal ones included: the choice reads them all.
SELECT * FROM model_profile
WHERE workspace_id = $1
ORDER BY lower(name), id;

-- name: ListSharedModelProfiles :many
SELECT * FROM model_profile
WHERE workspace_id = $1 AND owner_agent_id IS NULL
ORDER BY lower(name), id;

-- name: ListPersonalModelProfiles :many
SELECT * FROM model_profile
WHERE workspace_id = $1 AND owner_agent_id IS NOT NULL
ORDER BY lower(name), id;

-- name: GetSharedModelProfileInWorkspace :one
SELECT * FROM model_profile
WHERE id = $1 AND workspace_id = $2 AND owner_agent_id IS NULL;

-- name: GetPersonalModelProfileOfAgent :one
SELECT * FROM model_profile
WHERE owner_agent_id = $1;

-- name: CreatePersonalModelProfile :one
INSERT INTO model_profile (id, workspace_id, name, description, when_exhausted_profile_id, created_by, owner_agent_id, based_on_profile_id)
VALUES ($1, $2, $3, '', $4, $5, $6, $7)
RETURNING *;

-- name: SetPersonalModelProfileBase :exec
UPDATE model_profile SET based_on_profile_id = sqlc.narg(based_on_profile_id), updated_at = now()
WHERE id = $1;

-- name: ClearPersonalModelProfileBase :exec
-- A shared profile is deleted: the personal profiles made from it keep their
-- rows and their agents, and forget where they came from.
UPDATE model_profile SET based_on_profile_id = NULL, updated_at = now()
WHERE workspace_id = $1 AND based_on_profile_id = $2;

-- name: GetModelProfileInWorkspace :one
SELECT * FROM model_profile
WHERE id = $1 AND workspace_id = $2;

-- name: GetModelProfileByNameInWorkspace :one
SELECT * FROM model_profile
WHERE workspace_id = $1 AND lower(name) = lower(sqlc.arg(name)::text);

-- name: LockModelProfilesInWorkspace :many
-- Serializes profile writes of one workspace so the cycle check of
-- when_exhausted_profile_id sees a stable graph.
SELECT id, when_exhausted_profile_id, owner_agent_id FROM model_profile
WHERE workspace_id = $1
ORDER BY id
FOR UPDATE;

-- name: CreateModelProfile :one
INSERT INTO model_profile (id, workspace_id, name, description, when_exhausted_profile_id, created_by)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: UpdateModelProfile :one
UPDATE model_profile
SET name = $3, description = $4, when_exhausted_profile_id = $5, updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: DeleteModelProfile :execrows
DELETE FROM model_profile WHERE id = $1 AND workspace_id = $2;

-- name: ListModelProfileOptions :many
SELECT * FROM model_profile_option
WHERE profile_id = $1
ORDER BY position;

-- name: ListModelProfileOptionsByWorkspace :many
SELECT * FROM model_profile_option
WHERE workspace_id = $1
ORDER BY profile_id, position;

-- name: DeleteModelProfileOptions :exec
DELETE FROM model_profile_option WHERE profile_id = $1;

-- name: InsertModelProfileOption :exec
INSERT INTO model_profile_option (workspace_id, profile_id, position, runtime_id, model, thinking_level, custom_args)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: SetAgentModelProfile :one
UPDATE agent SET model_profile_id = sqlc.narg(model_profile_id), updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: SetAgentDisallowedTools :one
UPDATE agent SET disallowed_tools = $3, updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: SetIssueModelProfile :one
UPDATE issue SET model_profile_id = sqlc.narg(model_profile_id), updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: CountModelProfileReferences :one
-- Who references a profile: agents (not archived), open issues, and other
-- profiles that fall back to it. Deleting a profile is refused while any of
-- these is non-zero.
SELECT
 (SELECT count(*) FROM agent a WHERE a.workspace_id = sqlc.arg(workspace_id) AND a.model_profile_id = sqlc.arg(profile_id) AND a.archived_at IS NULL)::bigint AS agents,
 (SELECT count(*) FROM issue i WHERE i.workspace_id = sqlc.arg(workspace_id) AND i.model_profile_id = sqlc.arg(profile_id)
   AND NOT (i.status IN ('done', 'cancelled') OR EXISTS(SELECT 1 FROM issue_status s WHERE s.workspace_id=i.workspace_id AND s.key=i.status AND s.category IN ('done','closed'))))::bigint AS open_issues,
 (SELECT count(*) FROM model_profile p WHERE p.workspace_id = sqlc.arg(workspace_id) AND p.when_exhausted_profile_id = sqlc.arg(profile_id) AND p.id <> sqlc.arg(profile_id))::bigint AS fallback_of;

-- name: ListAgentsByModelProfile :many
SELECT * FROM agent
WHERE workspace_id = $1 AND model_profile_id = $2 AND archived_at IS NULL
ORDER BY name;

-- name: ListOpenIssuesWithModelProfile :many
-- Open issues of the workspace that carry a profile, optionally only those
-- assigned to one agent.
SELECT i.id, i.number, i.title, i.status, i.assignee_type, i.assignee_id, i.model_profile_id FROM issue i
WHERE i.workspace_id = sqlc.arg(workspace_id) AND i.model_profile_id IS NOT NULL
  AND NOT (i.status IN ('done', 'cancelled') OR EXISTS(SELECT 1 FROM issue_status s WHERE s.workspace_id=i.workspace_id AND s.key=i.status AND s.category IN ('done','closed')))
  AND (sqlc.narg(agent_id)::uuid IS NULL OR (i.assignee_type = 'agent' AND i.assignee_id = sqlc.narg(agent_id)::uuid))
ORDER BY i.number;

-- name: ClearClosedIssueModelProfiles :exec
-- Detach a deleted profile from closed issues; open issues block the delete.
UPDATE issue SET model_profile_id = NULL
WHERE workspace_id = $1 AND model_profile_id = $2;

-- name: ListActiveRuntimeUnavailable :many
SELECT * FROM runtime_unavailable
WHERE workspace_id = sqlc.arg(workspace_id) AND until > sqlc.arg(now)::timestamptz
ORDER BY runtime_id, until DESC;

-- name: ListActiveRuntimeUnavailableForRuntimes :many
SELECT * FROM runtime_unavailable
WHERE runtime_id = ANY(sqlc.arg(runtime_ids)::uuid[]) AND until > now()
ORDER BY runtime_id, until DESC;

-- name: InsertRuntimeUnavailable :one
-- reason_code is the kind of refusal (provider_limit, agent_cap_refusal,
-- disabled_refusal); subscription_id pins a provider refusal to the account
-- the run was confirmed to spend.
INSERT INTO runtime_unavailable (workspace_id, runtime_id, until, reason, task_id, reason_code, subscription_id)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: ListActiveLocalRefusals :many
-- The daemon's own refusals that still hold: on the agent limit or on disabled
-- accounts. Unknown refusals are never lifted by an operational refresh.
SELECT * FROM runtime_unavailable
WHERE until > @now::timestamptz
  AND reason_code IN ('agent_cap_refusal', 'disabled_refusal')
ORDER BY runtime_id, until DESC;

-- name: DeleteRuntimeUnavailableByIDs :many
-- Lifts refusals already chosen by their code.
DELETE FROM runtime_unavailable WHERE id = ANY(@ids::uuid[])
RETURNING *;

-- name: DeleteExpiredRuntimeUnavailable :many
-- The scheduler's sweep: rows whose reset has passed. Returns what it removed
-- so the caller can wake the runtimes' queues.
DELETE FROM runtime_unavailable
WHERE until <= now()
RETURNING *;

-- name: DeleteRuntimeModelReferences :exec
-- A deleted runtime leaves no model rows or unavailability behind.
WITH deleted_profile_rows AS (
 DELETE FROM model_profile_option WHERE model_profile_option.runtime_id = $1
), deleted_agent_rows AS (
 DELETE FROM agent_model_option WHERE agent_model_option.runtime_id = $1
)
DELETE FROM runtime_unavailable WHERE runtime_unavailable.runtime_id = $1;

-- name: ListAgentModelChoices :many
-- The agent's latest model choices, newest first: the journal of the Models
-- screen.
SELECT t.id, t.issue_id, t.status, t.runtime_id, t.model_position, t.model_choice, t.account_fact, t.created_at, t.dispatched_at,
       i.number AS issue_number, i.title AS issue_title
FROM agent_task_queue t
LEFT JOIN issue i ON i.id = t.issue_id
WHERE t.agent_id = $1 AND t.model_position IS NOT NULL
ORDER BY t.created_at DESC
LIMIT $2;

-- name: ReassignRuntimeModelReferences :exec
-- A legacy runtime merged into its successor: model rows and unavailability
-- follow the runtime instead of disappearing with it.
WITH moved_profile_rows AS (
 UPDATE model_profile_option SET runtime_id = sqlc.arg(new_runtime_id) WHERE model_profile_option.runtime_id = sqlc.arg(old_runtime_id)
), moved_agent_rows AS (
 UPDATE agent_model_option SET runtime_id = sqlc.arg(new_runtime_id) WHERE agent_model_option.runtime_id = sqlc.arg(old_runtime_id)
)
UPDATE runtime_unavailable SET runtime_id = sqlc.arg(new_runtime_id) WHERE runtime_unavailable.runtime_id = sqlc.arg(old_runtime_id);

-- name: SetTaskModelRoute :one
-- Places a run that has not been claimed yet on the runtime its model list
-- chooses now, with the choice that put it there. A run already claimed or
-- finished is left alone.
UPDATE agent_task_queue
SET runtime_id = sqlc.arg(runtime_id), model_choice = sqlc.arg(model_choice)
WHERE id = sqlc.arg(id) AND status IN ('queued', 'deferred')
RETURNING *;

-- name: SetClaimedTaskModelChoice :exec
-- What the claim chose for the run it just dispatched.
UPDATE agent_task_queue
SET model_position = sqlc.arg(model_position), model_choice = sqlc.arg(model_choice)
WHERE id = sqlc.arg(id) AND status = 'dispatched';

-- name: ListQueuedTasksForModelRouting :many
-- Unclaimed runs whose model comes from a list: already routed, or of an agent
-- on a profile (shared or personal), or of an issue with a profile.
SELECT t.* FROM agent_task_queue t
WHERE t.status = 'queued'
  AND (
    t.model_choice IS NOT NULL
    OR EXISTS (SELECT 1 FROM agent a WHERE a.id = t.agent_id AND a.model_profile_id IS NOT NULL)
    OR EXISTS (SELECT 1 FROM issue i WHERE i.id = t.issue_id AND i.model_profile_id IS NOT NULL)
  )
ORDER BY t.created_at
LIMIT sqlc.arg(page_size);

-- name: OpenModelIdleEpisode :one
-- Opens the agent's idle episode unless one is open: zero rows means the
-- owner was already told.
INSERT INTO model_idle_episode (workspace_id, agent_id, task_id, until, runtimes)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (agent_id) WHERE ended_at IS NULL DO NOTHING
RETURNING *;

-- name: CloseModelIdleEpisode :one
UPDATE model_idle_episode SET ended_at = now()
WHERE agent_id = $1 AND ended_at IS NULL
RETURNING *;

-- name: GetOpenModelIdleEpisode :one
SELECT * FROM model_idle_episode WHERE agent_id = $1 AND ended_at IS NULL;

-- name: ListWorkspaceOwnerUserIDs :many
SELECT user_id FROM member WHERE workspace_id = $1 AND role = 'owner' ORDER BY created_at, user_id;

-- name: ListAgentWaitingModelTasks :many
-- Unclaimed runs of the agent whose every model is unavailable now.
SELECT t.id, t.issue_id, t.model_choice, t.created_at, i.number AS issue_number, i.title AS issue_title
FROM agent_task_queue t
LEFT JOIN issue i ON i.id = t.issue_id
WHERE t.agent_id = $1 AND t.status = 'queued' AND t.model_choice->>'reason' = 'all_unavailable'
ORDER BY t.created_at;
