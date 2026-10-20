CREATE INDEX CONCURRENTLY IF NOT EXISTS subscription_usage_latest_idx ON subscription_usage (subscription_id, window_key, observed_at DESC);
