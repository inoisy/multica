-- Why a runtime is unavailable, as a code (provider_limit, agent_cap_refusal,
-- disabled_refusal, pool_verdict, unknown), and the account a refusal belongs
-- to. Rows are lifted by code, never by searching the free-text reason.
ALTER TABLE runtime_unavailable ADD COLUMN IF NOT EXISTS reason_code text NOT NULL DEFAULT '';
ALTER TABLE runtime_unavailable ADD COLUMN IF NOT EXISTS subscription_id uuid;
