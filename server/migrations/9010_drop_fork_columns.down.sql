ALTER TABLE agent ADD COLUMN IF NOT EXISTS model_profile_id UUID, ADD COLUMN IF NOT EXISTS disallowed_tools JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE issue ADD COLUMN IF NOT EXISTS model_profile_id UUID;
ALTER TABLE agent_task_queue ADD COLUMN IF NOT EXISTS model_position INTEGER, ADD COLUMN IF NOT EXISTS model_choice JSONB,
    ADD COLUMN IF NOT EXISTS account_offer JSONB, ADD COLUMN IF NOT EXISTS account_fact JSONB;
ALTER TABLE runtime_profile ADD COLUMN IF NOT EXISTS pool_id UUID;
UPDATE agent a SET model_profile_id = s.model_profile_id, disallowed_tools = s.disallowed_tools FROM agent_model_settings s WHERE s.agent_id = a.id;
UPDATE issue i SET model_profile_id = p.model_profile_id FROM issue_model_profile p WHERE p.issue_id = i.id;
UPDATE agent_task_queue t SET model_position = c.model_position, model_choice = c.model_choice, account_offer = c.account_offer, account_fact = c.account_fact
    FROM task_model_choice c WHERE c.task_id = t.id;
UPDATE runtime_profile r SET pool_id = p.pool_id FROM runtime_profile_pool p WHERE p.runtime_profile_id = r.id;
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'runtime_profile_pool_command_check' AND conrelid = 'runtime_profile'::regclass) THEN
        ALTER TABLE runtime_profile ADD CONSTRAINT runtime_profile_pool_command_check CHECK (((pool_id IS NULL) OR (command_name = runtime_type))) NOT VALID;
    END IF;
END $$;
