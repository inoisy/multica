-- Fork schema: the workflow artifact of an issue, squashed from the fork's
-- migrations 595-596 (2026-10). Idempotent.

CREATE TABLE IF NOT EXISTS issue_workflow_artifact (
    issue_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    project_id uuid NOT NULL,
    workflow_id uuid NOT NULL,
    artifact jsonb NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS issue_workflow_artifact_issue_index ON issue_workflow_artifact USING btree (issue_id);
