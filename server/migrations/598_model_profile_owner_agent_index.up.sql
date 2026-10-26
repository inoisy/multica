CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS model_profile_owner_agent_uidx ON model_profile (owner_agent_id) WHERE owner_agent_id IS NOT NULL;
