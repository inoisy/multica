ALTER TABLE agent_task_queue DROP COLUMN IF EXISTS model_choice;
ALTER TABLE agent_task_queue DROP COLUMN IF EXISTS model_position;
ALTER TABLE issue DROP COLUMN IF EXISTS model_profile_id;
ALTER TABLE agent DROP COLUMN IF EXISTS disallowed_tools;
ALTER TABLE agent DROP COLUMN IF EXISTS model_profile_id;
DROP TABLE IF EXISTS runtime_unavailable;
DROP TABLE IF EXISTS agent_model_option;
DROP TABLE IF EXISTS model_profile_option;
DROP TABLE IF EXISTS model_profile;
