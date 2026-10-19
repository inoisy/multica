CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS model_idle_episode_open_uidx ON model_idle_episode(agent_id) WHERE ended_at IS NULL;
