-- What each daemon host knows about a subscription's credential file:
-- present / missing / revoked / unreadable. Feeds the login status and the
-- "credential missing on the host" discrepancy. No FKs (house rule):
-- subscription_id references subscription.id and is cleaned up with it.
CREATE TABLE IF NOT EXISTS subscription_host_state (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subscription_id UUID NOT NULL,
    -- The daemon's stable id (agent_runtime.daemon_id) and its device name.
    daemon_id TEXT NOT NULL,
    device_name TEXT NOT NULL DEFAULT '',
    credential_state TEXT NOT NULL CHECK (credential_state IN ('present', 'missing', 'revoked', 'unreadable', 'unknown')),
    error TEXT NOT NULL DEFAULT '',
    checked_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
