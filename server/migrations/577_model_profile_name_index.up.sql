CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS model_profile_workspace_name_uidx ON model_profile(workspace_id, lower(name));
