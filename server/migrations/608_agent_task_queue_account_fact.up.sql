-- account_offer: the accounts the server handed to the daemon at claim, with
-- the id of that assignment. account_fact: the account the daemon confirmed.
-- Neither holds a token or a path.
ALTER TABLE agent_task_queue ADD COLUMN IF NOT EXISTS account_fact jsonb;
ALTER TABLE agent_task_queue ADD COLUMN IF NOT EXISTS account_offer jsonb;
