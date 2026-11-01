-- Fork schema: stage dispatch moves from columns on issue (9002) to the table
-- of upstream PR multica-ai/multica#9669, so the fork runs the code under
-- review. Idempotent: the upstream migration, whatever number it lands under,
-- creates the same table with IF NOT EXISTS and finds it in place.
-- The columns of 9002 stay until the generated issue queries are regenerated
-- without them; nothing reads them after this migration.
CREATE TABLE IF NOT EXISTS issue_stage_dispatch (
    issue_id     UUID PRIMARY KEY,
    workspace_id UUID NOT NULL,
    squad_id     UUID NOT NULL,
    dispatch_by  UUID NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO issue_stage_dispatch (issue_id, workspace_id, squad_id, dispatch_by)
SELECT id, workspace_id, stage_squad_id, stage_dispatch_by
FROM issue
WHERE stage_squad_id IS NOT NULL AND stage_dispatch_by IS NOT NULL
ON CONFLICT (issue_id) DO NOTHING;
