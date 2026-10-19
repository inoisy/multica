-- One input, one result, one decision and one refusal per run: a replayed
-- terminal path or a repeated submission finds the row instead of writing a
-- second one, and a client retrying a refused hand-in cannot grow the journal.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS issue_conveyor_event_input_uidx ON issue_conveyor_event(conveyor_id, source_task_id, kind) WHERE kind IN ('input','result','decision','rejected');
