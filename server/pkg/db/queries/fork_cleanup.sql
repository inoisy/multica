-- name: DeleteForkOrphans :exec
-- Fork rows whose owner is gone. Upstream deletes agents, issues, runs,
-- projects, runtime profiles and workspaces in many places without knowing
-- the fork tables; they are swept here instead of in each of those paths.
WITH side_agent AS (
    DELETE FROM agent_model_settings s WHERE NOT EXISTS (SELECT 1 FROM agent x WHERE x.id = s.agent_id)
), side_issue AS (
    DELETE FROM issue_model_profile s WHERE NOT EXISTS (SELECT 1 FROM issue x WHERE x.id = s.issue_id)
), side_task AS (
    DELETE FROM task_model_choice s WHERE NOT EXISTS (SELECT 1 FROM agent_task_queue x WHERE x.id = s.task_id)
), side_profile AS (
    DELETE FROM runtime_profile_pool s WHERE NOT EXISTS (SELECT 1 FROM runtime_profile x WHERE x.id = s.runtime_profile_id)
), artifacts AS (
    DELETE FROM issue_workflow_artifact s
    WHERE NOT EXISTS (SELECT 1 FROM issue x WHERE x.id = s.issue_id)
       OR NOT EXISTS (SELECT 1 FROM project x WHERE x.id = s.project_id)
), personal_profiles AS (
    DELETE FROM model_profile s
    WHERE (s.owner_agent_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM agent x WHERE x.id = s.owner_agent_id))
       OR NOT EXISTS (SELECT 1 FROM workspace x WHERE x.id = s.workspace_id)
), profile_rows AS (
    DELETE FROM model_profile_option s
    WHERE NOT EXISTS (SELECT 1 FROM model_profile x WHERE x.id = s.profile_id)
       OR NOT EXISTS (SELECT 1 FROM workspace x WHERE x.id = s.workspace_id)
), unavailable AS (
    DELETE FROM runtime_unavailable s WHERE NOT EXISTS (SELECT 1 FROM workspace x WHERE x.id = s.workspace_id)
), idle AS (
    DELETE FROM model_idle_episode s WHERE NOT EXISTS (SELECT 1 FROM workspace x WHERE x.id = s.workspace_id)
), grants AS (
    DELETE FROM subscription_pool_grant s WHERE NOT EXISTS (SELECT 1 FROM workspace x WHERE x.id = s.workspace_id)
)
SELECT 1;
