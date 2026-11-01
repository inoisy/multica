package handler

import (
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// Fork: a sub-issue in "waiting" waits for the customer. Dispatch does not
// assign it, and it does not hold the stages after it.
func TestStageDispatchSkipsWaitingSubIssues(t *testing.T) {
	r := newStageRigWith(t, true, map[string]testutil.Cols{
		"s1":      {"stage": 1},
		"s2-wait": {"stage": 2},
		"s2":      {"stage": 2, "status": "backlog"},
		"s3":      {"stage": 3},
	})
	// Turning dispatch on adds the status to the catalog; it is set after that.
	r.set(map[string]any{"squad_id": r.squad}).Want(http.StatusOK)
	if n := dbfx.Count(t, "SELECT count(*) FROM issue_status WHERE workspace_id=$1 AND key=$2", testWorkspaceID, service.IssueStatusWaiting); n != 1 {
		t.Fatalf("waiting status entries %d, want 1", n)
	}
	dbfx.Exec(t, "UPDATE issue SET status=$2 WHERE id=$1", r.kids["s2-wait"], service.IssueStatusWaiting)

	updateChildStatus(t, r.kids["s1"], "done")
	if !r.onSquad("s2") {
		t.Fatal("stage 2 was not dispatched after stage 1 closed")
	}
	if r.onSquad("s2-wait") {
		t.Fatal("a sub-issue waiting for the customer was assigned")
	}

	dbfx.Exec(t, "UPDATE issue SET status='done' WHERE id=$1", r.kids["s2"])
	r.sweep()
	if !r.onSquad("s3") {
		t.Fatal("a done stage with a waiting sub-issue did not open the next one")
	}
	if r.onSquad("s2-wait") {
		t.Fatal("the sweep assigned the waiting sub-issue")
	}
}
