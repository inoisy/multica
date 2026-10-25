-- Codes for the rows that exist. A row written by the pool's own verdict
-- (reason starts with "subscriptions: ") is pool_verdict and is no longer read;
-- a row of a failed run is a refusal of the daemon on the agent cap, on
-- disabled accounts, or a provider limit; anything else is unknown and is read
-- as a provider limit of the whole runtime.
UPDATE runtime_unavailable SET reason_code = CASE
    WHEN starts_with(reason, 'subscriptions: ') THEN 'pool_verdict'
    WHEN task_id IS NOT NULL AND strpos(reason, 'выше лимита агентам') > 0 THEN 'agent_cap_refusal'
    WHEN task_id IS NOT NULL AND strpos(reason, 'подписки среды отключены') > 0 THEN 'disabled_refusal'
    WHEN task_id IS NOT NULL THEN 'provider_limit'
    ELSE 'unknown'
END
WHERE reason_code = '';
