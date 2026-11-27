package handler

import "testing"

// setAgentProfile puts the agent on the profile through its side row.
func setAgentProfile(t *testing.T, agentID, profileID string) {
	t.Helper()
	dbfx.Exec(t, `INSERT INTO agent_model_settings (agent_id, workspace_id, model_profile_id)
SELECT id, workspace_id, $2 FROM agent WHERE id = $1
ON CONFLICT (agent_id) DO UPDATE SET model_profile_id = EXCLUDED.model_profile_id`, agentID, profileID)
}

// setIssueProfile sets the issue's profile through its side row.
func setIssueProfile(t *testing.T, issueID, profileID string) {
	t.Helper()
	dbfx.Exec(t, `INSERT INTO issue_model_profile (issue_id, workspace_id, model_profile_id)
SELECT id, workspace_id, $2 FROM issue WHERE id = $1
ON CONFLICT (issue_id) DO UPDATE SET model_profile_id = EXCLUDED.model_profile_id`, issueID, profileID)
}

// setTaskModelChoice records the run's model choice; position is nil for a
// run not claimed yet.
func setTaskModelChoice(t *testing.T, taskID string, position any, choice string) {
	t.Helper()
	dbfx.Exec(t, `INSERT INTO task_model_choice (task_id, model_position, model_choice) VALUES ($1, $2, $3::jsonb)
ON CONFLICT (task_id) DO UPDATE SET model_position = EXCLUDED.model_position, model_choice = EXCLUDED.model_choice`, taskID, position, choice)
}
