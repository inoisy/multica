-- Removes the schema that the single model source and the pools replaced. Only
-- run after the clients and the daemon stopped reading it; it is destructive
-- and is applied by hand (multica-autodeploy --allow-destructive <sha>).
--
--   agent_model_option                  an agent's own list; profiles hold the rows now
--   subscription.reserve                folded into caps by 611
--   subscription_limit_backup           the 611 backup; 611 down needs it, see the down file
--   model_truth_backup                  the 613 backup; 613 down turns into a no-op
--   runtime_profile.subscriptions / spend_order / share_key / all_workspaces
--                                       the order and the sharing live in the pool
--   subscription_pool.legacy_share_key  only read by the 610 down
--
-- runtime_unavailable.reason_code becomes a closed set. The stored verdicts of
-- the pool were never read and are removed; an empty or unrecognised code is an
-- unknown refusal, which is read as a provider limit.
--
-- Groups of one account: 613 cleared them, a group that was made a single
-- record since is cleared the same way.
DROP TABLE IF EXISTS agent_model_option;
DROP TABLE IF EXISTS subscription_limit_backup;
DROP TABLE IF EXISTS model_truth_backup;

ALTER TABLE subscription DROP COLUMN IF EXISTS reserve;

DROP INDEX IF EXISTS runtime_profile_workspace_share_key_uidx;
ALTER TABLE runtime_profile DROP COLUMN IF EXISTS subscriptions;
ALTER TABLE runtime_profile DROP COLUMN IF EXISTS spend_order;
ALTER TABLE runtime_profile DROP COLUMN IF EXISTS share_key;
ALTER TABLE runtime_profile DROP COLUMN IF EXISTS all_workspaces;

ALTER TABLE subscription_pool DROP COLUMN IF EXISTS legacy_share_key;

DELETE FROM runtime_unavailable WHERE reason_code = 'pool_verdict';
UPDATE runtime_unavailable SET reason_code = 'unknown'
WHERE reason_code NOT IN ('provider_limit', 'agent_cap_refusal', 'disabled_refusal', 'unknown');
ALTER TABLE runtime_unavailable ALTER COLUMN reason_code SET DEFAULT 'unknown';
ALTER TABLE runtime_unavailable DROP CONSTRAINT IF EXISTS runtime_unavailable_reason_code_check;
ALTER TABLE runtime_unavailable ADD CONSTRAINT runtime_unavailable_reason_code_check
    CHECK (reason_code IN ('provider_limit', 'agent_cap_refusal', 'disabled_refusal', 'unknown'));

UPDATE subscription s SET account_group = '', updated_at = now()
WHERE s.account_group <> ''
  AND (SELECT count(*) FROM subscription x WHERE x.account_group = s.account_group) = 1;
