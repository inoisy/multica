package main

// Fork migrations register their concurrent index builds here instead of in
// the upstream table, so upstream edits to main.go do not conflict with them.
func init() {
	for version, index := range map[string]string{
		"9007_issue_model_profile_profile_idx":          "issue_model_profile_profile_idx",
		"9008_agent_model_settings_profile_idx":         "agent_model_settings_profile_idx",
		"9009_runtime_profile_pool_workspace_pool_uidx": "runtime_profile_pool_workspace_pool_uidx",
	} {
		concurrentIndexCleanups[version] = index
		preMigrationHooks[version] = cleanupInvalidConcurrentIndexHook(index)
	}
}
