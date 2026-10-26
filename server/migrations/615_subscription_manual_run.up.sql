-- Journal of the commands an instance admin ran by hand on an account
-- (`multica subscription run`). It is not a run of an agent: nothing here is
-- written to agent_task_queue, and nothing here feeds the 24-hour run facts.
--
-- Only who, which account, which machine, which of the two CLIs and when are
-- kept. No arguments, no credential path, no token, no output. finished_at
-- stays empty when the process was cut off; readers show such a row as not
-- finished. agent_cap_exceeded says whether the account was already over the
-- limit agents may spend when the command started (a warning, not a block).
--
-- No FKs (house rule): subscription_id and user_id are cleaned up in code, and
-- rows older than 90 days are removed by the server's periodic cleanup. The
-- index is built concurrently in the next file.
CREATE TABLE IF NOT EXISTS subscription_manual_run (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subscription_id UUID NOT NULL,
    user_id UUID NOT NULL,
    daemon_id TEXT NOT NULL CHECK (char_length(daemon_id) BETWEEN 1 AND 200),
    command TEXT NOT NULL CHECK (command IN ('claude', 'codex')),
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    exit_code INTEGER,
    agent_cap_exceeded BOOLEAN NOT NULL DEFAULT false,
    CHECK ((finished_at IS NULL) = (exit_code IS NULL))
);
