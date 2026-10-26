-- Serves the lookups from an action to the runs that serve it (current run of a
-- step, runs still waiting to start, liveness of a route). Partial: almost
-- every run belongs to no route.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_task_queue_conveyor_action ON agent_task_queue(conveyor_action_id) WHERE conveyor_action_id IS NOT NULL;
