-- Serves coverage lookups by the run that ended.
CREATE INDEX CONCURRENTLY IF NOT EXISTS issue_conveyor_event_source_task_idx ON issue_conveyor_event(source_task_id) WHERE source_task_id IS NOT NULL;
