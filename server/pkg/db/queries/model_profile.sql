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
WITH settings AS (
    INSERT INTO agent_model_settings (agent_id, workspace_id, model_profile_id)
    SELECT a.id, a.workspace_id, sqlc.narg(model_profile_id)::uuid FROM agent a
    WHERE a.id = sqlc.arg(id) AND a.workspace_id = sqlc.arg(workspace_id)
    ON CONFLICT (agent_id) DO UPDATE SET model_profile_id = EXCLUDED.model_profile_id
)
UPDATE agent SET updated_at = now()
WHERE agent.id = sqlc.arg(id) AND agent.workspace_id = sqlc.arg(workspace_id)
RETURNING *;

-- name: ProjectModelProfileRow1OnAgents :execrows
-- Row 1 of a profile is the projection every agent on it carries in
-- runtime_id/model/thinking_level (old clients read those fields). The raw row
-- is copied: the model an API-key account sets is decided at launch, not here.
-- A profile without rows leaves the fields alone; such an agent does not run.
UPDATE agent a
SET runtime_id = o.runtime_id,
    runtime_mode = r.runtime_mode,
    model = NULLIF(o.model, ''),
    thinking_level = NULLIF(o.thinking_level, ''),
    updated_at = now()
FROM model_profile_option o
JOIN agent_runtime r ON r.id = o.runtime_id
WHERE EXISTS (SELECT 1 FROM agent_model_settings s WHERE s.agent_id = a.id AND s.model_profile_id = sqlc.arg(profile_id))
  AND a.workspace_id = sqlc.arg(workspace_id)
  AND o.profile_id = sqlc.arg(profile_id)
  AND o.position = (SELECT min(m.position) FROM model_profile_option m WHERE m.profile_id = sqlc.arg(profile_id))
  AND (a.runtime_id IS DISTINCT FROM o.runtime_id
    OR a.runtime_mode IS DISTINCT FROM r.runtime_mode
    OR a.model IS DISTINCT FROM NULLIF(o.model, '')
    OR a.thinking_level IS DISTINCT FROM NULLIF(o.thinking_level, ''));

-- name: ListModelProfileOptionsOfRuntime :many
SELECT * FROM model_profile_option WHERE runtime_id = $1 ORDER BY profile_id, position;

-- name: ShiftModelProfileOptionPositions :exec
-- First half of closing the gaps a deleted row leaves: the unique index on
-- (profile_id, position) forbids renumbering in place.
UPDATE model_profile_option SET position = position + 1000 WHERE profile_id = $1;

-- name: CompactModelProfileOptionPositions :exec
UPDATE model_profile_option o
SET position = ranked.n
FROM (
    SELECT m.position, row_number() OVER (ORDER BY m.position)::int AS n
    FROM model_profile_option m WHERE m.profile_id = $1
) ranked
WHERE o.profile_id = $1 AND o.position = ranked.position;

-- name: ListActiveAgentsByModelProfiles :many
SELECT * FROM agent
WHERE agent.workspace_id = sqlc.arg(workspace_id) AND agent.archived_at IS NULL
  AND id IN (SELECT s.agent_id FROM agent_model_settings s WHERE s.model_profile_id = ANY(sqlc.arg(profile_ids)::uuid[]))
ORDER BY name;

-- name: SetAgentDisallowedTools :one
WITH settings AS (
    INSERT INTO agent_model_settings (agent_id, workspace_id, disallowed_tools)
    SELECT a.id, a.workspace_id, sqlc.arg(disallowed_tools)::jsonb FROM agent a
    WHERE a.id = sqlc.arg(id) AND a.workspace_id = sqlc.arg(workspace_id)
    ON CONFLICT (agent_id) DO UPDATE SET disallowed_tools = EXCLUDED.disallowed_tools
)
UPDATE agent SET updated_at = now()
WHERE agent.id = sqlc.arg(id) AND agent.workspace_id = sqlc.arg(workspace_id)
RETURNING *;

-- name: SetIssueModelProfile :one
WITH cleared AS (
    DELETE FROM issue_model_profile p
    WHERE p.issue_id = sqlc.arg(id) AND sqlc.narg(model_profile_id)::uuid IS NULL
), set_profile AS (
    INSERT INTO issue_model_profile (issue_id, workspace_id, model_profile_id)
    SELECT i.id, i.workspace_id, sqlc.narg(model_profile_id)::uuid FROM issue i
    WHERE i.id = sqlc.arg(id) AND i.workspace_id = sqlc.arg(workspace_id) AND sqlc.narg(model_profile_id)::uuid IS NOT NULL
    ON CONFLICT (issue_id) DO UPDATE SET model_profile_id = EXCLUDED.model_profile_id
)
UPDATE issue SET updated_at = now()
WHERE issue.id = sqlc.arg(id) AND issue.workspace_id = sqlc.arg(workspace_id)
RETURNING *;

-- name: CountModelProfileReferences :one
-- Who references a profile: agents (not archived), open issues, and other
-- profiles that fall back to it. Deleting a profile is refused while any of
-- these is non-zero.
SELECT
 (SELECT count(*) FROM agent a JOIN agent_model_settings ams ON ams.agent_id = a.id
   WHERE a.workspace_id = sqlc.arg(workspace_id) AND ams.model_profile_id = sqlc.arg(profile_id) AND a.archived_at IS NULL)::bigint AS agents,
 (SELECT count(*) FROM issue i JOIN issue_model_profile imp ON imp.issue_id = i.id
   WHERE i.workspace_id = sqlc.arg(workspace_id) AND imp.model_profile_id = sqlc.arg(profile_id)
   AND NOT (i.status IN ('done', 'cancelled') OR EXISTS(SELECT 1 FROM issue_status s WHERE s.workspace_id=i.workspace_id AND s.key=i.status AND s.category IN ('done','closed'))))::bigint AS open_issues,
 (SELECT count(*) FROM model_profile p WHERE p.workspace_id = sqlc.arg(workspace_id) AND p.when_exhausted_profile_id = sqlc.arg(profile_id) AND p.id <> sqlc.arg(profile_id))::bigint AS fallback_of;

-- name: ListAgentsByModelProfile :many
SELECT * FROM agent
WHERE agent.workspace_id = $1 AND agent.archived_at IS NULL
  AND id IN (SELECT s.agent_id FROM agent_model_settings s WHERE s.model_profile_id = $2)
ORDER BY name;

-- name: ListOpenIssuesWithModelProfile :many
-- Open issues of the workspace that carry a profile, optionally only those
-- assigned to one agent.
SELECT i.id, i.number, i.title, i.status, i.assignee_type, i.assignee_id, p.model_profile_id, i.project_id FROM issue i
JOIN issue_model_profile p ON p.issue_id = i.id
WHERE i.workspace_id = sqlc.arg(workspace_id)
  AND NOT (i.status IN ('done', 'cancelled') OR EXISTS(SELECT 1 FROM issue_status s WHERE s.workspace_id=i.workspace_id AND s.key=i.status AND s.category IN ('done','closed')))
  AND (sqlc.narg(agent_id)::uuid IS NULL OR (i.assignee_type = 'agent' AND i.assignee_id = sqlc.narg(agent_id)::uuid))
ORDER BY i.number;

-- name: ClearClosedIssueModelProfiles :exec
-- Detach a deleted profile from closed issues; open issues block the delete.
DELETE FROM issue_model_profile
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
)
DELETE FROM runtime_unavailable WHERE runtime_unavailable.runtime_id = $1;

-- name: ListAgentModelChoices :many
-- The agent's latest model choices, newest first: the journal of the Models
-- screen.
SELECT t.id, t.issue_id, t.status, t.runtime_id, c.model_position, c.model_choice, c.account_fact, t.created_at, t.dispatched_at,
       i.number AS issue_number, i.title AS issue_title, i.project_id AS issue_project_id
FROM agent_task_queue t
JOIN task_model_choice c ON c.task_id = t.id
LEFT JOIN issue i ON i.id = t.issue_id
WHERE t.agent_id = $1 AND c.model_position IS NOT NULL
ORDER BY t.created_at DESC
LIMIT $2;

-- name: ReassignRuntimeModelReferences :exec
-- A legacy runtime merged into its successor: model rows and unavailability
-- follow the runtime instead of disappearing with it.
WITH moved_profile_rows AS (
 UPDATE model_profile_option SET runtime_id = sqlc.arg(new_runtime_id) WHERE model_profile_option.runtime_id = sqlc.arg(old_runtime_id)
)
UPDATE runtime_unavailable SET runtime_id = sqlc.arg(new_runtime_id) WHERE runtime_unavailable.runtime_id = sqlc.arg(old_runtime_id);

-- name: SetTaskModelRoute :one
-- Places a run that has not been claimed yet on the runtime its model list
-- chooses now, with the choice that put it there. A run already claimed or
-- finished is left alone.
WITH choice AS (
    INSERT INTO task_model_choice (task_id, model_choice)
    SELECT t.id, sqlc.arg(model_choice)::jsonb FROM agent_task_queue t
    WHERE t.id = sqlc.arg(id) AND t.status IN ('queued', 'deferred')
    ON CONFLICT (task_id) DO UPDATE SET model_choice = EXCLUDED.model_choice
)
UPDATE agent_task_queue
SET runtime_id = sqlc.arg(runtime_id)
WHERE agent_task_queue.id = sqlc.arg(id) AND agent_task_queue.status IN ('queued', 'deferred')
RETURNING *;

-- name: SetClaimedTaskModelChoice :exec
-- What the claim chose for the run it just dispatched.
INSERT INTO task_model_choice (task_id, model_position, model_choice)
SELECT t.id, sqlc.arg(model_position)::int, sqlc.arg(model_choice)::jsonb FROM agent_task_queue t
WHERE t.id = sqlc.arg(id) AND t.status = 'dispatched'
ON CONFLICT (task_id) DO UPDATE SET model_position = EXCLUDED.model_position, model_choice = EXCLUDED.model_choice;

-- name: ListQueuedTasksForModelRouting :many
-- Unclaimed runs whose model comes from a list: already routed, or of an agent
-- on a profile (shared or personal), or of an issue with a profile.
SELECT t.* FROM agent_task_queue t
WHERE t.status = 'queued'
  AND (
    EXISTS (SELECT 1 FROM task_model_choice c WHERE c.task_id = t.id AND c.model_choice IS NOT NULL)
    OR EXISTS (SELECT 1 FROM agent_model_settings s WHERE s.agent_id = t.agent_id AND s.model_profile_id IS NOT NULL)
    OR EXISTS (SELECT 1 FROM issue_model_profile p WHERE p.issue_id = t.issue_id)
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
SELECT t.id, t.issue_id, c.model_choice, t.created_at, i.number AS issue_number, i.title AS issue_title,
       i.project_id AS issue_project_id
FROM agent_task_queue t
JOIN task_model_choice c ON c.task_id = t.id
LEFT JOIN issue i ON i.id = t.issue_id
WHERE t.agent_id = $1 AND t.status = 'queued' AND c.model_choice->>'reason' = 'all_unavailable'
ORDER BY t.created_at;

-- name: DeleteProviderLimitRefusalsBySubscription :many
-- A probe the provider answered without a limit frees the refusals pinned to
-- that account; other accounts' refusals stay.
DELETE FROM runtime_unavailable
WHERE subscription_id = @subscription_id AND reason_code = 'provider_limit'
RETURNING *;

-- name: GetAgentModelSettings :one
SELECT * FROM agent_model_settings WHERE agent_id = $1;

-- name: ListAgentModelSettings :many
SELECT * FROM agent_model_settings WHERE workspace_id = $1;

-- name: ListAgentModelSettingsByAgents :many
SELECT * FROM agent_model_settings WHERE agent_id = ANY(sqlc.arg(agent_ids)::uuid[]);

-- name: GetIssueModelProfileID :one
SELECT model_profile_id FROM issue_model_profile WHERE issue_id = $1;

-- name: GetTaskModelChoice :one
SELECT * FROM task_model_choice WHERE task_id = $1;

-- name: ListTaskModelChoices :many
SELECT * FROM task_model_choice WHERE task_id = ANY(sqlc.arg(task_ids)::uuid[]);

-- name: DeleteOrphanModelSideRows :exec
-- Upstream deletes agents, issues, runs and runtime profiles in many places;
-- their side rows are swept here instead of in each of them.
WITH a AS (DELETE FROM agent_model_settings s WHERE NOT EXISTS (SELECT 1 FROM agent x WHERE x.id = s.agent_id)),
     i AS (DELETE FROM issue_model_profile p WHERE NOT EXISTS (SELECT 1 FROM issue x WHERE x.id = p.issue_id)),
     r AS (DELETE FROM runtime_profile_pool p WHERE NOT EXISTS (SELECT 1 FROM runtime_profile x WHERE x.id = p.runtime_profile_id))
DELETE FROM task_model_choice c WHERE NOT EXISTS (SELECT 1 FROM agent_task_queue x WHERE x.id = c.task_id);

-- name: ListRunFacts24Hours :many
-- Count attempts once, by the runtime the claim recorded and the account the
-- daemon confirmed, never from quota observations or today's mutable
-- agent/profile configuration.
SELECT COALESCE(NULLIF(c.account_fact ->> 'subscription_id', ''), '')::text AS subscription_id,
       count(*)::bigint AS runs
FROM agent_task_queue t
LEFT JOIN task_model_choice c ON c.task_id = t.id
WHERE COALESCE(t.started_at, t.dispatched_at) >= @since::timestamptz
  AND COALESCE(t.started_at, t.dispatched_at) < @until::timestamptz
  AND COALESCE(NULLIF(c.model_choice #>> '{chosen,runtime_id}', ''), t.runtime_id::text) = @runtime_id::text
GROUP BY 1;
