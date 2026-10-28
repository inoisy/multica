-- Removes the schema of the issue route ("conveyor") and of squad route
-- templates. Project Workflows replaced both on 27.10.2026 (STT-429); since then
-- nothing reads or writes these tables, agent_task_queue.conveyor_action_id or
-- squad.route_template. It is destructive and is applied by hand
-- (multica-autodeploy --allow-destructive <sha>).
--
-- issue.metadata keeps old 'route' / 'accept' keys: they are inert JSON, and the
-- hidden accept agents stay so the names in old runs still resolve.
DROP TABLE IF EXISTS issue_conveyor_event;
DROP TABLE IF EXISTS issue_conveyor;

DROP INDEX IF EXISTS idx_agent_task_queue_conveyor_action;
ALTER TABLE agent_task_queue DROP COLUMN IF EXISTS conveyor_action_id;

ALTER TABLE squad DROP COLUMN IF EXISTS route_template;
