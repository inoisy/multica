-- Fork-only model and subscription data lives beside upstream tables, not in
-- their columns: upstream's generated row structs stay identical, so syncing
-- upstream does not conflict across every query that selects these tables.
CREATE TABLE IF NOT EXISTS agent_model_settings (
    agent_id UUID PRIMARY KEY,
    workspace_id UUID NOT NULL,
    model_profile_id UUID,
    disallowed_tools JSONB NOT NULL DEFAULT '[]'::jsonb
);

CREATE TABLE IF NOT EXISTS issue_model_profile (
    issue_id UUID PRIMARY KEY,
    workspace_id UUID NOT NULL,
    model_profile_id UUID NOT NULL
);

CREATE TABLE IF NOT EXISTS task_model_choice (
    task_id UUID PRIMARY KEY,
    model_position INTEGER,
    model_choice JSONB,
    account_offer JSONB,
    account_fact JSONB
);

CREATE TABLE IF NOT EXISTS runtime_profile_pool (
    runtime_profile_id UUID PRIMARY KEY,
    workspace_id UUID NOT NULL,
    pool_id UUID NOT NULL
);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'agent' AND column_name = 'model_profile_id') THEN
        INSERT INTO agent_model_settings (agent_id, workspace_id, model_profile_id, disallowed_tools)
        SELECT id, workspace_id, model_profile_id, disallowed_tools FROM agent
        WHERE model_profile_id IS NOT NULL OR disallowed_tools <> '[]'::jsonb
        ON CONFLICT (agent_id) DO NOTHING;
    END IF;
    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'issue' AND column_name = 'model_profile_id') THEN
        INSERT INTO issue_model_profile (issue_id, workspace_id, model_profile_id)
        SELECT id, workspace_id, model_profile_id FROM issue WHERE model_profile_id IS NOT NULL
        ON CONFLICT (issue_id) DO NOTHING;
    END IF;
    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'agent_task_queue' AND column_name = 'model_choice') THEN
        INSERT INTO task_model_choice (task_id, model_position, model_choice, account_offer, account_fact)
        SELECT id, model_position, model_choice, account_offer, account_fact FROM agent_task_queue
        WHERE model_position IS NOT NULL OR model_choice IS NOT NULL OR account_offer IS NOT NULL OR account_fact IS NOT NULL
        ON CONFLICT (task_id) DO NOTHING;
    END IF;
    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'runtime_profile' AND column_name = 'pool_id') THEN
        INSERT INTO runtime_profile_pool (runtime_profile_id, workspace_id, pool_id)
        SELECT id, workspace_id, pool_id FROM runtime_profile WHERE pool_id IS NOT NULL
        ON CONFLICT (runtime_profile_id) DO NOTHING;
    END IF;
END $$;
