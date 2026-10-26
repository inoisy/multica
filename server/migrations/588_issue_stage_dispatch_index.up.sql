CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_stage_dispatch ON issue (id) WHERE stage_squad_id IS NOT NULL;
