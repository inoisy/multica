package handler

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
)

const testPinnedCommit = "0123456789abcdef0123456789abcdef01234567"

func pinBody(status, commit string) map[string]any {
	return map[string]any{"status": status, "workflow_artifact": map[string]any{
		"repo": "git@example.test:team/app.git", "branch": "feat/x", "commit": commit,
	}}
}

func artifactForTest(t *testing.T, issueID, agentID, taskID string) *testutil.Response {
	t.Helper()
	req := withURLParam(newRequest("GET", "/api/issues/"+issueID+"/workflow-artifact", nil), "id", issueID)
	if taskID != "" {
		req.Header.Set("X-Actor-Source", "task_token")
		req.Header.Set("X-Agent-ID", agentID)
		req.Header.Set("X-Task-ID", taskID)
	}
	return testutil.Call(t, testHandler.GetIssueWorkflowArtifact, req)
}

func runningTaskOf(t *testing.T, issueID, agentID string) string {
	t.Helper()
	var taskID string
	dbfx.QueryRow(t, `SELECT id::text FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2
		AND status = 'queued' ORDER BY created_at DESC LIMIT 1`, issueID, agentID).Scan(&taskID)
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1`, taskID)
	return taskID
}

// Acceptance was removed (STT-429): a step can no longer hand the issue to
// a system accept handler, the reviewer finishes the work itself.
func TestWorkflowRejectsTheRemovedAcceptHandler(t *testing.T) {
	seedTestCatalog(t)
	agentID := seededReadyAgentID(t)
	projectID := deliveryProject(t, "Workflow without acceptance", agentID)
	setColumnHandoff(t, projectID, "in_review", map[string]any{
		"handler": map[string]any{"type": "accept"}, "next_status_key": "done", "back_status_key": "in_progress",
	}).Want(http.StatusBadRequest)
}

// A run that saw an older pin may not hand the issue on: someone replaced
// the commit it was asked to judge. A pin needs a status change, a full SHA,
// and the run of the step's own handler.
func TestWorkflowArtifactGuards(t *testing.T) {
	seedTestCatalog(t)
	agentID := seededReadyAgentID(t)
	projectID := deliveryProject(t, "Workflow artifact guard project", agentID)
	setColumnHandoff(t, projectID, "in_review", handToAgent(agentID, "Review the pinned commit.")).Want(http.StatusOK)
	issueID := dbfx.Issue(t, "artifact guards", testutil.Cols{"project_id": projectID, "status": "todo"})

	updateIssueForTest(t, issueID, map[string]any{"status": "in_progress"}).Want(http.StatusOK)
	devTask := runningTaskOf(t, issueID, agentID)
	agentUpdateIssue(t, issueID, agentID, devTask, pinBody("in_progress", testPinnedCommit)).Want(http.StatusBadRequest)
	agentUpdateIssue(t, issueID, agentID, devTask, pinBody("in_review", "0123abc")).Want(http.StatusBadRequest)
	agentUpdateIssue(t, issueID, agentID, devTask, pinBody("in_review", testPinnedCommit)).Want(http.StatusOK)
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, devTask)
	reviewTask := runningTaskOf(t, issueID, agentID)

	// A member replaces the commit while the review runs.
	updateIssueForTest(t, issueID, pinBody("in_progress", strings.Repeat("a", 40))).Want(http.StatusOK)
	updateIssueForTest(t, issueID, map[string]any{"status": "in_review", "assignee_type": "agent", "assignee_id": agentID}).Want(http.StatusOK)
	dbfx.Exec(t, `UPDATE agent_task_queue SET workflow_step = 'in_review' WHERE id = $1`, reviewTask)

	artifactForTest(t, issueID, agentID, reviewTask).Want(http.StatusConflict)
	body := agentUpdateIssue(t, issueID, agentID, reviewTask, map[string]any{"status": "done"}).Want(http.StatusConflict).Map()
	if body["code"] != "workflow_artifact_changed" {
		t.Fatalf("stale review refusal = %v", body)
	}

	var runtimeID string
	dbfx.QueryRow(t, `SELECT runtime_id::text FROM agent WHERE id = $1`, agentID).Scan(&runtimeID)
	other := dbfx.Agent(t, "workflow-artifact-observer-"+workflowTestSuffix(), runtimeID)
	observerTask := dbfx.Task(t, other, testutil.Cols{
		"issue_id": issueID, "runtime_id": runtimeID, "status": "running", "started_at": time.Now(), "workflow_step": "in_review",
	})
	agentUpdateIssue(t, issueID, other, observerTask, pinBody("done", testPinnedCommit)).Want(http.StatusForbidden)
}
