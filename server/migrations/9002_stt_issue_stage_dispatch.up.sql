-- Fork schema: server-side stage dispatch of an epic, squashed from the fork's
-- migrations 587-588 (2026-10). Idempotent.
ALTER TABLE issue ADD COLUMN IF NOT EXISTS stage_squad_id UUID;
ALTER TABLE issue ADD COLUMN IF NOT EXISTS stage_dispatch_by UUID;
CREATE INDEX IF NOT EXISTS idx_issue_stage_dispatch ON issue USING btree (id) WHERE (stage_squad_id IS NOT NULL);
