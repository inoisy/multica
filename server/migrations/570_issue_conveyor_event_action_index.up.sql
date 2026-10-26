-- One action per route position and round: concurrent deciders cannot owe the same run twice.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS issue_conveyor_event_action_uidx ON issue_conveyor_event(conveyor_id, revision, idx, round, action) WHERE kind = 'action';
