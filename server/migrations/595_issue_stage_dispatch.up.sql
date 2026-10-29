-- Server-side stage dispatch for one parent issue (#7345). While a row exists,
-- the unassigned sub-issues of the parent's first open stage are assigned to
-- squad_id as soon as every earlier stage is closed. dispatch_by is the member
-- who turned it on: the runs it starts answer to them.
CREATE TABLE IF NOT EXISTS issue_stage_dispatch (
    issue_id     UUID PRIMARY KEY REFERENCES issue(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    squad_id     UUID NOT NULL REFERENCES squad(id) ON DELETE CASCADE,
    dispatch_by  UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
