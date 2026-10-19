CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_task_queue_model_choice ON agent_task_queue(agent_id, created_at DESC) WHERE model_position IS NOT NULL;
