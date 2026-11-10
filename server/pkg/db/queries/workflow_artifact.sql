-- name: PutIssueWorkflowArtifact :exec
INSERT INTO issue_workflow_artifact (issue_id, workspace_id, project_id, workflow_id, artifact)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (issue_id) DO UPDATE SET
    workspace_id = EXCLUDED.workspace_id, project_id = EXCLUDED.project_id,
    workflow_id = EXCLUDED.workflow_id, artifact = EXCLUDED.artifact, updated_at = now();

-- name: GetIssueWorkflowArtifact :one
SELECT * FROM issue_workflow_artifact WHERE issue_id = $1 AND workspace_id = $2;

-- name: DeleteIssueWorkflowArtifact :exec
DELETE FROM issue_workflow_artifact WHERE issue_id = $1;

-- name: SetTaskWorkflowArtifact :exec
-- The run that pins a commit has seen it: its snapshot follows its own pin,
-- so the run's next handoff is not refused as workflow_artifact_changed.
UPDATE agent_task_queue SET context = jsonb_set(
    COALESCE(context, '{}'::jsonb), '{workflow_artifact}', sqlc.arg('artifact')::jsonb
) WHERE id = sqlc.arg('task_id');
