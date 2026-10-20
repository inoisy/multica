CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS runtime_profile_workspace_share_key_uidx ON runtime_profile (workspace_id, share_key) WHERE share_key IS NOT NULL;
