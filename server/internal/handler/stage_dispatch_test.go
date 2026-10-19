package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// stageRig is an epic with staged sub-issues and a squad led by an agent.
type stageRig struct {
	t      *testing.T
	epic   string
	squad  string
	leader string
	owner  string // the epic's own assignee, woken by child_done
	kids   map[string]string
}

func newStageRig(t *testing.T) *stageRig {
	t.Helper()
	r := &stageRig{t: t, kids: map[string]string{}}
	r.leader = dbfx.Agent(t, "stage leader", testRuntimeID)
	r.owner = dbfx.Agent(t, "epic owner", testRuntimeID)
	r.squad = dbfx.Insert(t, "squad", testutil.Cols{
		"workspace_id": testWorkspaceID, "name": "stage squad " + r.leader, "description": "",
		"leader_id": r.leader, "creator_id": testUserID,
	})
	if dbfx.Count(t, "SELECT count(*) FROM issue_status WHERE workspace_id=$1 AND key=$2", testWorkspaceID, service.IssueStatusWaiting) == 0 {
		dbfx.Insert(t, "issue_status", testutil.Cols{"workspace_id": testWorkspaceID, "key": service.IssueStatusWaiting,
			"name": "Ждёт заказчика", "category": "unstarted", "color": "#a855f7"})
	}
	r.epic = dbfx.Issue(t, "epic", testutil.Cols{"assignee_type": "agent", "assignee_id": r.owner, "status": "in_progress"})
	for name, cols := range map[string]testutil.Cols{
		"s1":      {"stage": 1},
		"s2-wait": {"stage": 2, "status": service.IssueStatusWaiting},
		"s2":      {"stage": 2, "status": "backlog"},
		"s3":      {"stage": 3},
		"loose":   {},
	} {
		cols["parent_issue_id"] = r.epic
		r.kids[name] = dbfx.Issue(t, "stage "+name, cols)
	}
	ids := []string{r.epic}
	for _, id := range r.kids {
		ids = append(ids, id)
	}
	for _, id := range ids {
		id := id
		t.Cleanup(func() { cleanupChildDoneIssue(id) })
		dbfx.Cleanup(t, "DELETE FROM issue_conveyor WHERE issue_id=$1", id)
	}
	dbfx.Cleanup(t, "DELETE FROM squad WHERE id=$1", r.squad)
	return r
}

func (r *stageRig) set(body map[string]any) *testutil.Response {
	return testutil.Call(r.t, testHandler.SetIssueStageDispatch, withURLParam(newRequest(http.MethodPut, "/", body), "id", r.epic))
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
	r := newStageRig(t)
	var resp StageDispatchResponse
	r.set(map[string]any{"squad_id": r.squad}).Want(http.StatusOK).JSON(&resp)
	if resp.SquadID == nil || *resp.SquadID != r.squad || resp.DispatchBy == nil || *resp.DispatchBy != testUserID {
		t.Fatalf("response %+v", resp)
	}
	// Stage 1 goes to the squad, its leader starts; the later stages and the
	// unstaged sub-issue stay as they are.
	if !r.onSquad("s1") || r.runs(r.leader, r.kids["s1"]) != 1 {
		t.Fatalf("stage 1 not dispatched: %v, leader runs %d", r.onSquad("s1"), r.runs(r.leader, r.kids["s1"]))
	}
	for _, name := range []string{"s2-wait", "s2", "s3", "loose"} {
		if typ, _, _ := r.assignee(name); typ != "" {
			t.Fatalf("%s was assigned (%s) before its turn", name, typ)
		}
	}

	// Closing stage 1 opens stage 2 at once; backlog moves to todo so work
	// starts, and the sub-issue waiting for the customer is left alone.
	updateChildStatus(t, r.kids["s1"], "done")
	if !r.onSquad("s2") {
		t.Fatal("stage 2 was not dispatched after stage 1 closed")
	}
	if _, _, status := r.assignee("s2"); status != "todo" {
		t.Fatalf("stage 2 status %q, want todo", status)
	}
	if r.onSquad("s3") || r.onSquad("s2-wait") {
		t.Fatal("dispatch ran ahead of the open stage")
	}
	// The epic's own assignee is not woken for a stage the server dispatched.
	if n := r.runs(r.owner, r.epic); n != 0 {
		t.Fatalf("epic owner woken %d times by a dispatched stage", n)
	}

	// A sub-issue the route merged through accept counts as closed, and one
	// waiting for the customer does not hold the stage: the sweep opens stage 3
	// without anyone marking stage 2 done.
	dbfx.Insert(t, "issue_conveyor", testutil.Cols{
		"workspace_id": testWorkspaceID, "issue_id": r.kids["s2"], "status": "completed", "cur_idx": 2,
		"route":                testutil.Raw(`'[{"idx":0,"step":"review","agent_id":"` + r.leader + `"},{"idx":1,"step":"accept","agent_id":"` + r.leader + `"},{"idx":2,"step":"close","agent_id":"` + r.leader + `"}]'::jsonb`),
		"coordinator_agent_id": r.leader, "enabled_by": testUserID,
	})
	sweep := &service.StageDispatchService{Issues: testHandler.IssueService}
	if err := sweep.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !r.onSquad("s3") {
		t.Fatal("a route merged through accept did not close its stage")
	}

	// Off: nothing further is assigned.
	r.set(map[string]any{"squad_id": nil}).Want(http.StatusOK).JSON(&resp)
	if resp.SquadID != nil {
		t.Fatalf("still on: %+v", resp)
	}
	dbfx.Exec(t, "UPDATE issue SET assignee_type=NULL, assignee_id=NULL WHERE id=$1", r.kids["s3"])
	if err := sweep.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.onSquad("s3") {
		t.Fatal("dispatch continued after it was turned off")
	}
}

func TestStageDispatchLeavesAssignedWorkAndNeedsAPerson(t *testing.T) {
	r := newStageRig(t)
	other := dbfx.Agent(t, "someone else", testRuntimeID)
	dbfx.Exec(t, "UPDATE issue SET assignee_type='agent', assignee_id=$2 WHERE id=$1", r.kids["s1"], other)
	// Turning dispatch on brings the waiting status into a catalog without it.
	dbfx.Exec(t, "DELETE FROM issue_status WHERE workspace_id=$1 AND key=$2", testWorkspaceID, service.IssueStatusWaiting)
	r.set(map[string]any{"squad_id": r.squad}).Want(http.StatusOK)
	if typ, id, _ := r.assignee("s1"); typ != "agent" || id != other {
		t.Fatalf("an assigned sub-issue was taken over: %s %s", typ, id)
	}
	if dbfx.Count(t, "SELECT count(*) FROM issue_status WHERE workspace_id=$1 AND key=$2", testWorkspaceID, service.IssueStatusWaiting) != 1 {
		t.Fatal("the waiting status is missing from the catalog")
	}

	agentReq := withURLParam(newRequest(http.MethodPut, "/", map[string]any{"squad_id": r.squad}), "id", r.epic)
	agentReq.Header.Set("X-Agent-ID", r.leader)
	agentReq.Header.Set("X-Task-ID", dbfx.Insert(t, "agent_task_queue", testutil.Cols{
		"agent_id": r.leader, "runtime_id": testRuntimeID, "issue_id": r.epic, "status": "running",
		"originator_user_id": testUserID, "accountable_user_id": testUserID,
	}))
	agentReq.Header.Set("X-Actor-Source", "task_token")
	testutil.Call(t, testHandler.SetIssueStageDispatch, agentReq).Want(http.StatusForbidden)

	var bad map[string]any
	r.set(map[string]any{"squad_id": "00000000-0000-0000-0000-000000000000"}).Want(http.StatusBadRequest).JSON(&bad)
}

// Without stage dispatch the closed stage wakes the epic's assignee as
// before: the silence above belongs to dispatch, not to a quiet rule.
func TestClosedStageStillWakesTheEpicOwnerWithoutDispatch(t *testing.T) {
	r := newStageRig(t)
	updateChildStatus(t, r.kids["s1"], "done")
	if n := r.runs(r.owner, r.epic); n != 1 {
		t.Fatalf("epic owner runs = %d, want 1", n)
	}
	if r.onSquad("s2") {
		t.Fatal("a stage was dispatched without stage dispatch")
	}
}
