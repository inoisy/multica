CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS subscription_pool_member_position_uidx ON subscription_pool_member (pool_id, position);
