DROP INDEX IF EXISTS idx_issue_stage_dispatch;
ALTER TABLE issue DROP COLUMN IF EXISTS stage_dispatch_by;
ALTER TABLE issue DROP COLUMN IF EXISTS stage_squad_id;
