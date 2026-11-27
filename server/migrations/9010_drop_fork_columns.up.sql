-- Data moved to side tables in 9006; the stage_* columns had no reader left.
DROP INDEX IF EXISTS idx_issue_model_profile;
DROP INDEX IF EXISTS idx_agent_task_queue_model_choice;
DROP INDEX IF EXISTS idx_agent_task_queue_model_routed_queued;
DROP INDEX IF EXISTS runtime_profile_workspace_pool_uidx;
DROP INDEX IF EXISTS idx_issue_stage_dispatch;
ALTER TABLE agent DROP COLUMN IF EXISTS model_profile_id, DROP COLUMN IF EXISTS disallowed_tools;
ALTER TABLE issue DROP COLUMN IF EXISTS model_profile_id, DROP COLUMN IF EXISTS stage_squad_id, DROP COLUMN IF EXISTS stage_dispatch_by;
ALTER TABLE agent_task_queue DROP COLUMN IF EXISTS model_position, DROP COLUMN IF EXISTS model_choice,
    DROP COLUMN IF EXISTS account_offer, DROP COLUMN IF EXISTS account_fact;
-- The pool command rule is checked by the server (PoolCommandMatches) on
-- every write that can break it.
ALTER TABLE runtime_profile DROP CONSTRAINT IF EXISTS runtime_profile_pool_command_check;
ALTER TABLE runtime_profile DROP COLUMN IF EXISTS pool_id;
