-- Server-side stage dispatch of an epic: while stage_squad_id is set, the
-- unassigned sub-issues of the first open stage are assigned to that squad
-- as soon as the stages before it close. stage_dispatch_by is the person who
-- turned it on: the runs it starts answer to them.
ALTER TABLE issue ADD COLUMN IF NOT EXISTS stage_squad_id UUID;
ALTER TABLE issue ADD COLUMN IF NOT EXISTS stage_dispatch_by UUID;
