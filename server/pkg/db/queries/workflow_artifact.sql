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

-- name: ProjectHasLegacyRoutes :one
SELECT EXISTS (
    SELECT 1 FROM issue_conveyor c JOIN issue i ON i.id = c.issue_id
    WHERE i.project_id = $1 AND i.workspace_id = $2 AND c.status NOT IN ('disabled', 'completed')
);

-- name: WorkspaceHasLegacyRoutes :one
SELECT EXISTS (
    SELECT 1 FROM issue_conveyor c JOIN issue i ON i.id = c.issue_id
    WHERE i.workspace_id = $1 AND c.status NOT IN ('disabled', 'completed')
);
