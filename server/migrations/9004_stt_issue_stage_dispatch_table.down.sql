-- Copies the rows back to the 9002 columns before the table goes.
UPDATE issue i
SET stage_squad_id = d.squad_id, stage_dispatch_by = d.dispatch_by
FROM issue_stage_dispatch d
WHERE d.issue_id = i.id;

DROP TABLE IF EXISTS issue_stage_dispatch;
