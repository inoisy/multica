package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func (r *conveyorHandlerRig) status() string {
	var status string
	dbfx.QueryRow(r.t, "SELECT status FROM issue WHERE id=$1", r.issue).Scan(&status)
	return status
}

func (r *conveyorHandlerRig) setStatus(req *http.Request) *testutil.Response {
	return testutil.Call(r.t, testHandler.UpdateIssue, req)
}

func TestConveyorRouteOwnsIssueStatus(t *testing.T) {
	r := newConveyorHandlerRig(t)
	r.enable()
	dev := r.runOf("dev")
	r.running(dev)
	before := r.status()

	var refusal map[string]any
	r.setStatus(asConveyorRun(r.req("PUT", "/", map[string]any{"status": "done"}), r.agents["dev"], dev)).
		Want(http.StatusConflict).JSON(&refusal)
	if msg, _ := refusal["error"].(string); !strings.Contains(msg, "conveyor result "+r.issue) || !strings.Contains(msg, "статус ведёт сервер") {
		t.Fatalf("refusal does not tell the agent what to do: %v", refusal)
	}
	batch := map[string]any{"issue_ids": []string{r.issue}, "updates": map[string]any{"status": "done"}}
	testutil.Call(t, testHandler.BatchUpdateIssues, asConveyorRun(newRequest("POST", "/", batch), r.agents["dev"], dev)).
		Want(http.StatusConflict)
	if got := r.status(); got != before {
		t.Fatalf("agent moved the routed issue to %s", got)
	}
	// A write that keeps the status, or touches other fields, still goes through.
	r.setStatus(asConveyorRun(r.req("PUT", "/", map[string]any{"status": before, "priority": "high"}), r.agents["dev"], dev)).
		Want(http.StatusOK)

	// Once the route is paused the agent may move the status again.
	var view map[string]any
	r.put(map[string]any{"action": "pause", "expected_revision": 1}).Want(http.StatusOK).JSON(&view)
	r.setStatus(asConveyorRun(r.req("PUT", "/", map[string]any{"status": "in_review"}), r.agents["dev"], dev)).
		Want(http.StatusOK)
	if got := r.status(); got != "in_review" {
		t.Fatalf("agent on a paused route: status %s", got)
	}
}

func TestConveyorPersonMovesRoutedIssueStatus(t *testing.T) {
	r := newConveyorHandlerRig(t)
	r.enable()
	r.setStatus(r.req("PUT", "/", map[string]any{"status": "done"})).Want(http.StatusOK)
	if got := r.status(); got != "done" {
		t.Fatalf("person could not close a routed issue: status %s", got)
	}
}
