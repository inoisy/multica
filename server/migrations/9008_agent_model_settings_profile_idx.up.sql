CREATE INDEX CONCURRENTLY IF NOT EXISTS agent_model_settings_profile_idx ON agent_model_settings (model_profile_id) WHERE model_profile_id IS NOT NULL;
