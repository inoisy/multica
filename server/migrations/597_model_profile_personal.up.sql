-- A personal model profile belongs to one agent (owner_agent_id) and may say
-- which shared profile it was made from (based_on_profile_id). A shared profile
-- has neither. No foreign keys: agent and profile deletion clean these up in
-- application code.
ALTER TABLE model_profile ADD COLUMN IF NOT EXISTS owner_agent_id uuid;
ALTER TABLE model_profile ADD COLUMN IF NOT EXISTS based_on_profile_id uuid;
