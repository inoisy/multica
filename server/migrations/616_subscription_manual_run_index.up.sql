CREATE INDEX CONCURRENTLY IF NOT EXISTS subscription_manual_run_account_started_idx ON subscription_manual_run (subscription_id, started_at DESC);
