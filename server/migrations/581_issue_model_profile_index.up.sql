CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_model_profile ON issue(model_profile_id) WHERE model_profile_id IS NOT NULL;
