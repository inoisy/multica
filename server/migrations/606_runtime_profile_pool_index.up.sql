CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS runtime_profile_workspace_pool_uidx ON runtime_profile (workspace_id, pool_id) WHERE pool_id IS NOT NULL;
