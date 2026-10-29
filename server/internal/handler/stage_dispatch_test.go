package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// stageRig is a parent issue with staged sub-issues and a squad led by an agent.
type stageRig struct {
	t      *testing.T
	parent string
	squad  string
	leader string
	owner  string // the parent's own assignee, woken by child_done
	kids   map[string]string
}

func newStageRig(t *testing.T, flagOn bool) *stageRig {
	t.Helper()
	withFeatureFlag(t, testHandler, featureflags.StageDispatchV1, flagOn)
	r := &stageRig{t: t, kids: map[string]string{}}
	r.leader = dbfx.Agent(t, "stage leader", testRuntimeID)
	r.owner = dbfx.Agent(t, "parent owner", testRuntimeID)
	r.squad = dbfx.Insert(t, "squad", testutil.Cols{
		"workspace_id": testWorkspaceID, "name": "stage squad " + r.leader, "description": "",
		"leader_id": r.leader, "creator_id": testUserID,
	})
	r.parent = dbfx.Issue(t, "parent", testutil.Cols{"assignee_type": "agent", "assignee_id": r.owner, "status": "in_progress"})
	for name, cols := range map[string]testutil.Cols{
		"s1":    {"stage": 1},
		"s2":    {"stage": 2, "status": "backlog"},
		"s3":    {"stage": 3},
		"loose": {},
	} {
		cols["parent_issue_id"] = r.parent
		r.kids[name] = dbfx.Issue(t, "stage "+name, cols)
	}
	ids := []string{r.parent}
	for _, id := range r.kids {
		ids = append(ids, id)
	}
	for _, id := range ids {
		id := id
		t.Cleanup(func() { cleanupChildDoneIssue(id) })
	}
	dbfx.Cleanup(t, "DELETE FROM squad WHERE id=$1", r.squad)
	return r
}

func (r *stageRig) set(body map[string]any) *testutil.Response {
	return testutil.Call(r.t, testHandler.SetIssueStageDispatch, withURLParam(newRequest(http.MethodPut, "/", body), "id", r.parent))
}

func (r *stageRig) sweep() {
	r.t.Helper()
	svc := &service.StageDispatchService{Issues: testHandler.IssueService, Flags: testHandler.FeatureFlags}
	if err := svc.Sweep(context.Background()); err != nil {
		r.t.Fatal(err)
	}
}

func (r *stageRig) assignee(name string) (assigneeType, assigneeID, status string) {
	dbfx.QueryRow(r.t, "SELECT COALESCE(assignee_type,''), COALESCE(assignee_id::text,''), status FROM issue WHERE id=$1", r.kids[name]).
		Scan(&assigneeType, &assigneeID, &status)
	return
}

func (r *stageRig) onSquad(name string) bool {
	typ, id, _ := r.assignee(name)
	return typ == "squad" && id == r.squad
}

func (r *stageRig) runs(agent, issue string) int {
	return dbfx.Count(r.t, "SELECT count(*) FROM agent_task_queue WHERE agent_id=$1 AND issue_id=$2", agent, issue)
}

func TestStageDispatchAssignsStagesInOrder(t *testing.T) {
	r := newStageRig(t, true)
	var resp StageDispatchResponse
	r.set(map[string]any{"squad_id": r.squad}).Want(http.StatusOK).JSON(&resp)
	if resp.SquadID == nil || *resp.SquadID != r.squad || resp.DispatchBy == nil || *resp.DispatchBy != testUserID {
		t.Fatalf("response %+v", resp)
	}
	// Stage 1 goes to the squad and its leader starts; the later stages and
	// the unstaged sub-issue stay as they are.
	if !r.onSquad("s1") || r.runs(r.leader, r.kids["s1"]) != 1 {
		t.Fatalf("stage 1 not dispatched: %v, leader runs %d", r.onSquad("s1"), r.runs(r.leader, r.kids["s1"]))
	}
	for _, name := range []string{"s2", "s3", "loose"} {
		if typ, _, _ := r.assignee(name); typ != "" {
			t.Fatalf("%s was assigned (%s) before its turn", name, typ)
		}
	}

	// Closing stage 1 opens stage 2 at once; backlog moves to todo so work
	// starts.
	updateChildStatus(t, r.kids["s1"], "done")
	if !r.onSquad("s2") {
		t.Fatal("stage 2 was not dispatched after stage 1 closed")
	}
	if _, _, status := r.assignee("s2"); status != "todo" {
		t.Fatalf("stage 2 status %q, want todo", status)
	}
	if r.onSquad("s3") {
		t.Fatal("dispatch ran ahead of the open stage")
	}
	// The parent's own assignee is not woken for a stage the server dispatched.
	if n := r.runs(r.owner, r.parent); n != 0 {
		t.Fatalf("parent owner woken %d times by a dispatched stage", n)
	}

	// A change that records no sub-issue event is caught by the sweep.
	dbfx.Exec(t, "UPDATE issue SET status='done' WHERE id=$1", r.kids["s2"])
	r.sweep()
	if !r.onSquad("s3") {
		t.Fatal("the sweep did not open stage 3")
	}

	// Off: nothing further is assigned.
	r.set(map[string]any{"squad_id": nil}).Want(http.StatusOK).JSON(&resp)
	if resp.SquadID != nil {
		t.Fatalf("still on: %+v", resp)
	}
	dbfx.Exec(t, "UPDATE issue SET assignee_type=NULL, assignee_id=NULL WHERE id=$1", r.kids["s3"])
	r.sweep()
	if r.onSquad("s3") {
		t.Fatal("dispatch continued after it was turned off")
	}
}

func TestStageDispatchLeavesAssignedWorkAndNeedsAMember(t *testing.T) {
	r := newStageRig(t, true)
	other := dbfx.Agent(t, "someone else", testRuntimeID)
	dbfx.Exec(t, "UPDATE issue SET assignee_type='agent', assignee_id=$2 WHERE id=$1", r.kids["s1"], other)
	r.set(map[string]any{"squad_id": r.squad}).Want(http.StatusOK)
	if typ, id, _ := r.assignee("s1"); typ != "agent" || id != other {
		t.Fatalf("an assigned sub-issue was taken over: %s %s", typ, id)
	}

	agentReq := withURLParam(newRequest(http.MethodPut, "/", map[string]any{"squad_id": r.squad}), "id", r.parent)
	agentReq.Header.Set("X-Agent-ID", r.leader)
	agentReq.Header.Set("X-Task-ID", dbfx.Insert(t, "agent_task_queue", testutil.Cols{
		"agent_id": r.leader, "runtime_id": testRuntimeID, "issue_id": r.parent, "status": "running",
		"originator_user_id": testUserID, "accountable_user_id": testUserID,
	}))
	agentReq.Header.Set("X-Actor-Source", "task_token")
	testutil.Call(t, testHandler.SetIssueStageDispatch, agentReq).Want(http.StatusForbidden)

	r.set(map[string]any{"squad_id": "00000000-0000-0000-0000-000000000000"}).Want(http.StatusBadRequest)
}

// Without stage dispatch the closed stage wakes the parent's assignee as
// before: the silence above belongs to dispatch, not to a quiet rule.
func TestClosedStageStillWakesTheParentOwnerWithoutDispatch(t *testing.T) {
	r := newStageRig(t, true)
	updateChildStatus(t, r.kids["s1"], "done")
	if n := r.runs(r.owner, r.parent); n != 1 {
		t.Fatalf("parent owner runs = %d, want 1", n)
	}
	if r.onSquad("s2") {
		t.Fatal("a stage was dispatched without stage dispatch")
	}
}

// With the flag off the endpoint is absent, and a parent that had dispatch
// on behaves as before: no assignment, and the closed stage wakes its owner.
func TestStageDispatchFlagOff(t *testing.T) {
	r := newStageRig(t, false)
	r.set(map[string]any{"squad_id": r.squad}).Want(http.StatusNotFound)
	dbfx.Exec(t, "INSERT INTO issue_stage_dispatch (issue_id, workspace_id, squad_id, dispatch_by) VALUES ($1, $2, $3, $4)",
		r.parent, testWorkspaceID, r.squad, testUserID)
	r.sweep()
	updateChildStatus(t, r.kids["s1"], "done")
	if r.onSquad("s1") || r.onSquad("s2") {
		t.Fatal("dispatch ran with the flag off")
	}
	if n := r.runs(r.owner, r.parent); n != 1 {
		t.Fatalf("parent owner runs = %d, want 1", n)
	}
}

// A stage that closes with cancelled work does not open the next one: the
// parent's assignee is woken to decide, and dispatch resumes once the
// cancelled sub-issue leaves the stage.
func TestCancelledStageHaltsDispatchAndWakesTheParentOwner(t *testing.T) {
	r := newStageRig(t, true)
	r.set(map[string]any{"squad_id": r.squad}).Want(http.StatusOK)
	updateChildStatus(t, r.kids["s1"], "cancelled")
	if r.onSquad("s2") {
		t.Fatal("stage 2 was dispatched after stage 1 was cancelled")
	}
	if n := r.runs(r.owner, r.parent); n != 1 {
		t.Fatalf("parent owner runs = %d, want 1", n)
	}

	dbfx.Exec(t, "UPDATE issue SET stage=NULL WHERE id=$1", r.kids["s1"])
	r.sweep()
	if !r.onSquad("s2") {
		t.Fatal("dispatch did not resume once the cancelled sub-issue left the stage")
	}
}
