CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS subscription_pool_member_subscription_uidx ON subscription_pool_member (pool_id, subscription_id);
