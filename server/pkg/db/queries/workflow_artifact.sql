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

-- Pull requests linked by the issue's workflow pin.

-- name: LinkIssueToPullRequestByPin :execrows
-- Automatic link from the issue's workflow pin: the PR's head branch is the
-- branch the issue pinned. Never overrides a person's removal (exclusion) or a
-- manual link; an automatic title/branch link becomes a pin link so a later
-- title edit does not drop it. Returns 1 when the row was inserted or converted.
INSERT INTO issue_pull_request (
    issue_id, pull_request_id, linked_by_type, linked_by_id
)
SELECT sqlc.arg('issue_id')::uuid, sqlc.arg('pull_request_id')::uuid, 'pin', NULL
WHERE NOT EXISTS (
    SELECT 1 FROM issue_pull_request_exclusion
    WHERE issue_id = sqlc.arg('issue_id')::uuid AND pull_request_id = sqlc.arg('pull_request_id')::uuid
)
ON CONFLICT (issue_id, pull_request_id) DO UPDATE SET
    linked_by_type = 'pin'
WHERE COALESCE(issue_pull_request.linked_by_type, 'system') = 'system';

-- name: ListIssuePinsByBranch :many
-- Issues of the workspace whose pin for their current workflow names this
-- branch. The repo is returned raw; the caller normalizes and compares it.
SELECT a.issue_id, COALESCE(a.artifact->>'repo', '')::text AS repo
FROM issue_workflow_artifact a
JOIN issue i ON i.id = a.issue_id AND i.project_id = a.project_id AND i.workspace_id = a.workspace_id
JOIN project p ON p.id = i.project_id
WHERE a.workspace_id = $1
  AND a.artifact->>'branch' = sqlc.arg('branch')::text
  AND a.workflow_id = COALESCE(p.workflow_id, (SELECT w.id FROM issue_workflow w
      WHERE w.workspace_id = i.workspace_id AND w.is_default));

-- name: ListGitHubPullRequestsByBranch :many
-- Mirrored PRs of the workspace with this repo and head branch, open ones
-- first, then the most recently updated.
SELECT * FROM github_pull_request
WHERE workspace_id = $1
  AND lower(repo_owner) = lower(sqlc.arg('repo_owner')::text)
  AND lower(repo_name) = lower(sqlc.arg('repo_name')::text)
  AND branch = sqlc.arg('branch')::text
ORDER BY (state IN ('open', 'draft')) DESC, pr_updated_at DESC;
