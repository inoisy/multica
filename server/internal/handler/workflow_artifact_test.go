package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
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

// A wakeup the reviewer set while waiting (PR checks, merge) is the
// assignee's run too: it carries the pin snapshot, so its handoff is not
// refused as workflow_artifact_changed (STT-483, STT-497).
func TestWorkflowArtifactRidesOnWakeupTask(t *testing.T) {
	seedTestCatalog(t)
	agentID := seededReadyAgentID(t)
	projectID := deliveryProject(t, "Workflow artifact wakeup project", agentID)
	setColumnHandoff(t, projectID, "in_review", handToAgent(agentID, "Review the pinned commit.")).Want(http.StatusOK)
	issueID := dbfx.Issue(t, "artifact wakeup", testutil.Cols{"project_id": projectID, "status": "todo"})

	updateIssueForTest(t, issueID, map[string]any{"status": "in_progress"}).Want(http.StatusOK)
	devTask := runningTaskOf(t, issueID, agentID)
	agentUpdateIssue(t, issueID, agentID, devTask, pinBody("in_review", testPinnedCommit)).Want(http.StatusOK)
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, devTask)
	reviewTask := runningTaskOf(t, issueID, agentID)
	// The review parks and registers a wakeup; its run ends.
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, reviewTask)

	var runtimeID string
	dbfx.QueryRow(t, `SELECT runtime_id::text FROM agent WHERE id = $1`, agentID).Scan(&runtimeID)
	wakeup, err := testHandler.Queries.CreateWakeupTask(context.Background(), db.CreateWakeupTaskParams{
		AgentID: util.MustParseUUID(agentID), RuntimeID: util.MustParseUUID(runtimeID), IssueID: util.MustParseUUID(issueID),
		TriggerSummary:      pgtype.Text{String: "Wakeup: PR checks finished", Valid: true},
		DelegatedFromTaskID: util.MustParseUUID(reviewTask),
		WakeupContext:       []byte(`{"wakeup_id":"00000000-0000-0000-0000-000000000001"}`),
	})
	if err != nil {
		t.Fatalf("CreateWakeupTask: %v", err)
	}
	var same bool
	var snapshot, wakeupID string
	dbfx.QueryRow(t, `SELECT COALESCE(t.context->'workflow_artifact' = a.artifact, false),
		COALESCE(t.context->>'workflow_artifact', ''), COALESCE(t.context->>'wakeup_id', '')
		FROM agent_task_queue t JOIN issue_workflow_artifact a ON a.issue_id = t.issue_id WHERE t.id = $1`,
		util.UUIDToString(wakeup.ID)).Scan(&same, &snapshot, &wakeupID)
	if !same || wakeupID == "" {
		t.Fatalf("wakeup context artifact = %q wakeup_id = %q, want the stored pin and the wakeup keys", snapshot, wakeupID)
	}

	wakeupTask := runningTaskOf(t, issueID, agentID)
	artifactForTest(t, issueID, agentID, wakeupTask).Want(http.StatusOK)
	agentUpdateIssue(t, issueID, agentID, wakeupTask, map[string]any{"status": "done"}).Want(http.StatusOK)
}

// The reviewer updates the branch while it waits (base moved), pins the new
// head with its own status change, and then hands the issue on. Its own pin
// is the commit it judged: the later handoff of the same run goes through,
// a member's replacement in between is still refused (STT-493).
func TestWorkflowArtifactOwnRepinKeepsTheRunCurrent(t *testing.T) {
	seedTestCatalog(t)
	agentID := seededReadyAgentID(t)
	projectID := deliveryProject(t, "Workflow artifact own repin project", agentID)
	setColumnHandoff(t, projectID, "in_review", handToAgent(agentID, "Review the pinned commit.")).Want(http.StatusOK)
	issueID := dbfx.Issue(t, "artifact own repin", testutil.Cols{"project_id": projectID, "status": "todo"})

	updateIssueForTest(t, issueID, map[string]any{"status": "in_progress"}).Want(http.StatusOK)
	devTask := runningTaskOf(t, issueID, agentID)
	agentUpdateIssue(t, issueID, agentID, devTask, pinBody("in_review", testPinnedCommit)).Want(http.StatusOK)
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, devTask)
	reviewTask := runningTaskOf(t, issueID, agentID)
	dbfx.Exec(t, `UPDATE agent_task_queue SET workflow_step = 'in_review' WHERE id = $1`, reviewTask)

	updatedHead := strings.Repeat("b", 40)
	agentUpdateIssue(t, issueID, agentID, reviewTask, pinBody("blocked", updatedHead)).Want(http.StatusOK)
	artifactForTest(t, issueID, agentID, reviewTask).Want(http.StatusOK)
	agentUpdateIssue(t, issueID, agentID, reviewTask, map[string]any{"status": "in_review"}).Want(http.StatusOK)

	// A member's replacement after the run's own pin is still a stale view.
	updateIssueForTest(t, issueID, pinBody("blocked", strings.Repeat("c", 40))).Want(http.StatusOK)
	updateIssueForTest(t, issueID, map[string]any{"status": "in_review", "assignee_type": "agent", "assignee_id": agentID}).Want(http.StatusOK)
	agentUpdateIssue(t, issueID, agentID, reviewTask, map[string]any{"status": "done"}).Want(http.StatusConflict)
}
