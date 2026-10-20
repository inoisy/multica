ALTER TABLE runtime_profile DROP COLUMN IF EXISTS all_workspaces;
ALTER TABLE runtime_profile DROP COLUMN IF EXISTS share_key;
ALTER TABLE runtime_profile DROP COLUMN IF EXISTS spend_order;
ALTER TABLE runtime_profile DROP COLUMN IF EXISTS subscriptions;
DROP TABLE IF EXISTS subscription_usage;
DROP TABLE IF EXISTS subscription;
