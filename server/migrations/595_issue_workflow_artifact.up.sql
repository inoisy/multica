CREATE TABLE IF NOT EXISTS issue_workflow_artifact (
    issue_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    project_id uuid NOT NULL,
    workflow_id uuid NOT NULL,
    artifact jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
