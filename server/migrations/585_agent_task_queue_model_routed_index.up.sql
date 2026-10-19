CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_task_queue_model_routed_queued ON agent_task_queue(agent_id) WHERE status = 'queued' AND model_choice IS NOT NULL;
