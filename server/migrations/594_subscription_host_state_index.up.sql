CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS subscription_host_state_uidx ON subscription_host_state (subscription_id, daemon_id);
