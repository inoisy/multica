-- Serves the delivery job: actions still owed a run.
CREATE INDEX CONCURRENTLY IF NOT EXISTS issue_conveyor_event_pending_idx ON issue_conveyor_event(created_at) WHERE kind = 'action' AND state = 'pending';
