CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS subscription_pool_grant_uidx ON subscription_pool_grant (pool_id, workspace_id);
