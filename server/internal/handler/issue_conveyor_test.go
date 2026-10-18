package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// conveyorHandlerRig is a routed issue behind the real handlers: dev, qa and
// lead agents, and the people and tokens that talk to it.
type conveyorHandlerRig struct {
	t      *testing.T
	issue  string
	agents map[string]string
}

func newConveyorHandlerRig(t *testing.T) *conveyorHandlerRig {
	t.Helper()
	r := &conveyorHandlerRig{t: t, issue: dbfx.Issue(t, "routed through the API"), agents: map[string]string{}}
	for _, name := range []string{"dev", "qa", "lead", "bystander"} {
		r.agents[name] = dbfx.Agent(t, "conveyor "+name, testRuntimeID)
	}
	dbfx.Cleanup(t, "DELETE FROM issue_conveyor_event WHERE issue_id=$1", r.issue)
	dbfx.Cleanup(t, "DELETE FROM issue_conveyor WHERE issue_id=$1", r.issue)
	dbfx.Cleanup(t, "DELETE FROM comment WHERE issue_id=$1", r.issue)
	dbfx.Cleanup(t, "DELETE FROM agent_task_queue WHERE issue_id=$1", r.issue)
	dbfx.Cleanup(t, "DELETE FROM issue_wakeup WHERE issue_id=$1", r.issue)
	dbfx.Cleanup(t, "DELETE FROM issue_wakeup_receipt WHERE wakeup_id IN(SELECT id FROM issue_wakeup WHERE issue_id=$1)", r.issue)
	return r
}

func (r *conveyorHandlerRig) req(method, path string, body any) *http.Request {
	return withURLParam(newRequest(method, path, body), "id", r.issue)
}

// asConveyorRun makes the request come from the task token of a run.
func asConveyorRun(req *http.Request, agent, task string) *http.Request {
	req.Header.Set("X-Agent-ID", agent)
	req.Header.Set("X-Task-ID", task)
	req.Header.Set("X-Actor-Source", "task_token")
	return req
}

func (r *conveyorHandlerRig) put(cmd map[string]any) *testutil.Response {
	return testutil.Call(r.t, testHandler.ApplyIssueConveyor, r.req("PUT", "/", cmd))
}

func (r *conveyorHandlerRig) route(steps ...string) []map[string]string {
	out := make([]map[string]string, len(steps))
	for i, s := range steps {
		agent := s
		if s == "close" {
			agent = "lead"
		}
		out[i] = map[string]string{"step": s, "agent": r.agents[agent]}
	}
	return out
}

func (r *conveyorHandlerRig) enable() map[string]any {
	var view map[string]any
	r.put(map[string]any{"action": "enable", "expected_revision": 0, "route": r.route("dev", "qa", "close")}).Want(http.StatusOK).JSON(&view)
	return view
}

func (r *conveyorHandlerRig) runOf(agent string) string {
	var id string
	dbfx.QueryRow(r.t, "SELECT id::text FROM agent_task_queue WHERE issue_id=$1 AND agent_id=$2 ORDER BY created_at DESC LIMIT 1", r.issue, r.agents[agent]).Scan(&id)
	if id == "" {
		r.t.Fatalf("%s has no run on the issue", agent)
	}
	return id
}

func (r *conveyorHandlerRig) runs(agent string) int {
	return dbfx.Count(r.t, "SELECT count(*) FROM agent_task_queue WHERE issue_id=$1 AND agent_id=$2", r.issue, r.agents[agent])
}

func (r *conveyorHandlerRig) running(task string) {
	dbfx.Exec(r.t, "UPDATE agent_task_queue SET status='running',dispatched_at=clock_timestamp(),started_at=clock_timestamp() WHERE id=$1", task)
}

func (r *conveyorHandlerRig) result(agent, task string, body any) *testutil.Response {
	return testutil.Call(r.t, testHandler.SubmitIssueConveyorResult, asConveyorRun(r.req("POST", "/", body), r.agents[agent], task))
}

func validResult(commit string) map[string]any {
	return map[string]any{"schema_version": 1, "outcome": "pass", "report": "all done",
		"artifact": map[string]string{"repo": "r", "branch": "b", "commit": commit},
		"checks":   []map[string]any{{"name": "tests", "command": "make test", "ok": true}}}
}

func TestIssueConveyorAPI(t *testing.T) {
	r := newConveyorHandlerRig(t)

	var none map[string]any
	testutil.Call(t, testHandler.GetIssueConveyor, r.req("GET", "/", nil)).Want(http.StatusNotFound).JSON(&none)

	for name, body := range map[string]map[string]any{
		"unknown action":   {"action": "start", "expected_revision": 0},
		"no coordinator":   {"action": "enable", "expected_revision": 0, "route": r.route("dev", "qa")},
		"unknown field":    {"action": "enable", "expected_revision": 0, "route": r.route("dev", "qa", "close"), "task_id": "x"},
		"wrong revision":   {"action": "enable", "expected_revision": 7, "route": r.route("dev", "qa", "close")},
		"disable no-route": {"action": "disable", "expected_revision": 0},
	} {
		want := http.StatusBadRequest
		switch name {
		case "wrong revision":
			want = http.StatusConflict
		case "disable no-route":
			want = http.StatusNotFound
		}
		testutil.Call(t, testHandler.ApplyIssueConveyor, r.req("PUT", "/", body)).Want(want)
	}
	if n := r.runs("dev"); n != 0 {
		t.Fatalf("refused commands started %d runs", n)
	}

	view := r.enable()
	if view["status"] != "active" || view["step"] != "dev" || view["revision"] != float64(1) {
		t.Fatalf("enable: %v", view)
	}
	r.put(map[string]any{"action": "enable", "expected_revision": 1, "route": r.route("dev", "qa", "close")}).Want(http.StatusConflict)
	dev := r.runOf("dev")
	r.running(dev)

	// Results come from the run's own token and nowhere else.
	body := validResult("abcdef1234567")
	testutil.Call(t, testHandler.SubmitIssueConveyorResult, r.req("POST", "/", body)).Want(http.StatusForbidden)
	r.result("qa", dev, body).Want(http.StatusForbidden)
	extra := validResult("abcdef1234567")
	extra["task_id"] = dev
	r.result("dev", dev, extra).Want(http.StatusBadRequest)
	var created, again map[string]any
	r.result("dev", dev, body).Want(http.StatusCreated).JSON(&created)
	r.result("dev", dev, body).Want(http.StatusOK).JSON(&again)
	if created["id"] != again["id"] || created["created"] != true || again["created"] != false {
		t.Fatalf("hand-in and its repeat: %v / %v", created, again)
	}
	changed := validResult("abcdef1234567")
	changed["report"] = "different"
	r.result("dev", dev, changed).Want(http.StatusConflict)
	if n := dbfx.Count(t, "SELECT count(*) FROM comment WHERE issue_id=$1 AND source_task_id=$2 AND cardinality(suppressed_agent_ids)=3", r.issue, dev); n != 1 {
		t.Fatalf("report comments with the route suppressed: %d", n)
	}

	// The run ends; the next step starts without anyone being woken in between.
	if _, err := testHandler.TaskService.CompleteTask(context.Background(), parseUUID(dev), []byte(`{"output":"done"}`), "", "", "", false, "", ""); err != nil {
		t.Fatal(err)
	}
	if r.runs("qa") != 1 || r.runs("lead") != 0 {
		t.Fatalf("after dev: qa=%d lead=%d", r.runs("qa"), r.runs("lead"))
	}
	testutil.Call(t, testHandler.GetIssueConveyor, r.req("GET", "/", nil)).Want(http.StatusOK).JSON(&view)
	if view["step"] != "qa" || view["current_task_id"] != r.runOf("qa") {
		t.Fatalf("state: %v", view)
	}

	// The journal reads input -> decision -> action, newest pages by cursor.
	var page struct {
		Events     []ConveyorEventResponse `json:"events"`
		NextCursor string                  `json:"next_cursor"`
	}
	testutil.Call(t, testHandler.ListIssueConveyorEvents, r.req("GET", "/?limit=2", nil)).Want(http.StatusOK).JSON(&page)
	if len(page.Events) != 2 || page.NextCursor == "" {
		t.Fatalf("first page: %+v", page)
	}
	next := withURLParam(newRequest("GET", "/?cursor="+page.NextCursor, nil), "id", r.issue)
	var rest struct {
		Events []ConveyorEventResponse `json:"events"`
	}
	testutil.Call(t, testHandler.ListIssueConveyorEvents, next).Want(http.StatusOK).JSON(&rest)
	kinds := map[string]bool{}
	for _, e := range append(page.Events, rest.Events...) {
		kinds[e.Kind] = true
	}
	for _, want := range []string{"state", "action", "result", "input", "decision"} {
		if !kinds[want] {
			t.Errorf("journal has no %s event: %v", want, kinds)
		}
	}
	testutil.Call(t, testHandler.ListIssueConveyorEvents, r.req("GET", "/?cursor=abc", nil)).Want(http.StatusBadRequest)

	// Pause, resume and disable carry the revision they act on.
	r.put(map[string]any{"action": "pause", "expected_revision": 7}).Want(http.StatusConflict)
	testutil.Call(t, testHandler.GetIssueConveyor, r.req("GET", "/", nil)).JSON(&view)
	rev := view["revision"]
	r.put(map[string]any{"action": "pause", "expected_revision": rev}).Want(http.StatusOK).JSON(&view)
	if view["status"] != "paused" {
		t.Fatalf("pause: %v", view)
	}
	r.put(map[string]any{"action": "disable", "expected_revision": view["revision"]}).Want(http.StatusOK).JSON(&view)
	if view["status"] != "disabled" {
		t.Fatalf("disable: %v", view)
	}
}

func TestIssueConveyorRefusesAgentsThePersonMayNotInvoke(t *testing.T) {
	r := newConveyorHandlerRig(t)
	owner := dbfx.User(t, "other owner", "conveyor-other-owner@multica.test")
	dbfx.Member(t, testWorkspaceID, owner, "member")
	runtime := dbfx.Runtime(t, "other runtime", testutil.Cols{"owner_id": owner})
	foreign := dbfx.Agent(t, "conveyor private", runtime, testutil.Cols{"owner_id": owner, "visibility": "private", "permission_mode": "private"})
	route := r.route("dev", "qa", "close")
	route[1]["agent"] = foreign
	r.put(map[string]any{"action": "enable", "expected_revision": 0, "route": route}).Want(http.StatusForbidden)
	if r.runs("dev") != 0 {
		t.Fatal("a refused enable started a run")
	}
}

// The mention in a step's own report would wake the next agent a second time:
// the route does that itself. Other people's comments and other agents are
// untouched.
func TestConveyorSuppressesManagedMention(t *testing.T) {
	r := newConveyorHandlerRig(t)
	r.enable()
	dev := r.runOf("dev")
	r.running(dev)

	mention := func(agent string) string {
		return fmt.Sprintf("[@%s](mention://agent/%s) please look", agent, r.agents[agent])
	}
	comment := func(req *http.Request) {
		testutil.Call(t, testHandler.CreateComment, req).Want(http.StatusCreated)
	}
	// The step's own run mentions the lead and the qa agent: both are the route's.
	comment(asConveyorRun(r.req("POST", "/", map[string]any{"content": mention("lead") + " " + mention("qa")}), r.agents["dev"], dev))
	if r.runs("lead") != 0 || r.runs("qa") != 0 {
		t.Fatalf("a mention in the step's comment woke the route's agents: lead=%d qa=%d", r.runs("lead"), r.runs("qa"))
	}
	var covered int
	svc := service.IssueConveyorService{Tasks: testHandler.TaskService}
	rows, err := svc.Events(context.Background(), parseUUID(r.issue), 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range rows {
		if e.Kind == "superseded" && e.Reason.String == "managed_route" && strings.HasPrefix(e.CoveredBy.String, "conveyor_action:") {
			covered++
		}
	}
	if covered != 2 {
		t.Fatalf("journal shows %d suppressed mentions, want 2", covered)
	}

	// The same run mentioning an agent outside the route reaches it.
	comment(asConveyorRun(r.req("POST", "/", map[string]any{"content": mention("bystander")}), r.agents["dev"], dev))
	if r.runs("bystander") != 1 {
		t.Fatalf("a mention of an agent outside the route was dropped")
	}
	// A person mentioning the lead while the run executes reaches it as before.
	comment(r.req("POST", "/", map[string]any{"content": mention("lead")}))
	if r.runs("lead") != 1 {
		t.Fatalf("a person's mention of the lead was dropped (runs=%d)", r.runs("lead"))
	}
}

// A run the route left behind is withdrawn at claim, before the daemon sees it.
func TestConveyorStaleRunIsNotClaimed(t *testing.T) {
	r := newConveyorHandlerRig(t)
	r.enable()
	dev := r.runOf("dev")
	var view map[string]any
	testutil.Call(t, testHandler.GetIssueConveyor, r.req("GET", "/", nil)).JSON(&view)
	// The task was enqueued under revision 1; the operator pauses before it is claimed.
	r.put(map[string]any{"action": "pause", "expected_revision": view["revision"]}).Want(http.StatusOK)
	// Pausing withdraws the unclaimed run on its own; put it back as a daemon
	// that read the queue just before would still see it.
	withdrawn := dbfx.Count(t, "SELECT count(*) FROM issue_conveyor_event WHERE issue_id=$1 AND kind='rejected_stale' AND source_task_id=$2", r.issue, dev)
	dbfx.Exec(t, "UPDATE agent_task_queue SET status='queued',completed_at=NULL WHERE id=$1", dev)

	req := newDaemonTokenRequest("POST", "/api/daemon/runtimes/"+testRuntimeID+"/claim", nil, testWorkspaceID, "test-conveyor-stale-claim")
	req = withURLParam(req, "runtimeId", testRuntimeID)
	rec := httptest.NewRecorder()
	testHandler.ClaimTaskByRuntime(rec, req)
	var status string
	dbfx.QueryRow(t, "SELECT status FROM agent_task_queue WHERE id=$1", dev).Scan(&status)
	if status != "cancelled" {
		t.Fatalf("a stale route run was handed to the daemon (%s): %s", status, rec.Body.String())
	}
	if n := dbfx.Count(t, "SELECT count(*) FROM issue_conveyor_event WHERE issue_id=$1 AND kind='rejected_stale' AND source_task_id=$2", r.issue, dev); n != withdrawn+1 {
		t.Fatalf("claim journaled %d rejected_stale events, want %d", n, withdrawn+1)
	}
}

// The handler wires the same owner as the service: a rerun on a routed issue
// is one run, however it is asked.
func TestConveyorRerunIsIdempotentThroughTheAPI(t *testing.T) {
	r := newConveyorHandlerRig(t)
	r.enable()
	dev := r.runOf("dev")
	r.running(dev)
	svcTask := testHandler.TaskService
	if _, err := svcTask.FailTask(context.Background(), parseUUID(dev), "You have hit your usage limit", "", "", "", "agent_error.provider_quota_limit", false, "", ""); err != nil {
		t.Fatal(err)
	}
	var first, second struct {
		ID string `json:"id"`
	}
	testutil.Call(t, testHandler.RerunIssue, r.req("POST", "/", map[string]any{})).Want(http.StatusAccepted).JSON(&first)
	testutil.Call(t, testHandler.RerunIssue, r.req("POST", "/", map[string]any{"task_id": dev})).Want(http.StatusAccepted).JSON(&second)
	if first.ID == "" || first.ID != second.ID {
		t.Fatalf("two reruns, two runs: %s %s", first.ID, second.ID)
	}
	if r.runs("dev") != 2 || r.runs("lead") != 0 {
		t.Fatalf("dev=%d lead=%d", r.runs("dev"), r.runs("lead"))
	}
}

// Deleting the issue takes its route and journal with it; nothing is left
// behind for a foreign-key-free schema to orphan.
func TestConveyorIsDeletedWithItsIssue(t *testing.T) {
	r := newConveyorHandlerRig(t)
	r.enable()
	if dbfx.Count(t, "SELECT count(*) FROM issue_conveyor_event WHERE issue_id=$1", r.issue) == 0 {
		t.Fatal("the route wrote no journal")
	}
	testutil.Call(t, testHandler.DeleteIssue, r.req("DELETE", "/", nil)).Want(http.StatusNoContent)
	if n := dbfx.Count(t, "SELECT count(*) FROM issue_conveyor WHERE issue_id=$1", r.issue) + dbfx.Count(t, "SELECT count(*) FROM issue_conveyor_event WHERE issue_id=$1", r.issue); n != 0 {
		t.Fatalf("%d route rows outlived the issue", n)
	}
}

// The completion replay of comments posted while the lead was busy must not
// re-wake it for a mention the route already answers, and must still do so for
// a person's.
func TestConveyorSuppressesManagedMentionInCompletionReplay(t *testing.T) {
	r := newConveyorHandlerRig(t)
	r.enable()
	dev := r.runOf("dev")
	r.running(dev)
	// The lead's own run works a thread; replies in it are what the replay
	// looks at once that run ends.
	root := dbfx.Comment(t, r.issue, "lead, take this")
	leadRun := dbfx.Task(t, r.agents["lead"], testutil.Cols{"issue_id": r.issue, "runtime_id": testRuntimeID, "status": "running", "started_at": testutil.Raw("now()"),
		"trigger_comment_id": root, "originator_user_id": testUserID, "accountable_user_id": testUserID})
	mention := fmt.Sprintf("[@lead](mention://agent/%s) report", r.agents["lead"])
	dbfx.Insert(t, "comment", testutil.Cols{"issue_id": r.issue, "workspace_id": testWorkspaceID, "author_type": "agent", "author_id": r.agents["dev"],
		"content": mention, "type": "comment", "source_task_id": dev, "parent_id": root})
	dbfx.Exec(t, "UPDATE agent_task_queue SET status='completed',completed_at=now() WHERE id=$1", leadRun)
	task, err := testHandler.Queries.GetAgentTask(context.Background(), parseUUID(leadRun))
	if err != nil {
		t.Fatal(err)
	}
	testHandler.reconcileCommentsOnCompletion(context.Background(), &task)
	if r.runs("lead") != 1 {
		t.Fatalf("the replay woke the lead for the step's mention (runs=%d)", r.runs("lead"))
	}
	if n := dbfx.Count(t, "SELECT count(*) FROM issue_conveyor_event WHERE issue_id=$1 AND kind='superseded' AND reason='managed_route'", r.issue); n != 1 {
		t.Fatalf("journal shows %d suppressed mentions, want 1", n)
	}

	dbfx.Insert(t, "comment", testutil.Cols{"issue_id": r.issue, "workspace_id": testWorkspaceID, "author_type": "member", "author_id": testUserID,
		"content": mention, "type": "comment", "parent_id": root})
	testHandler.reconcileCommentsOnCompletion(context.Background(), &task)
	if r.runs("lead") != 2 {
		t.Fatalf("the replay dropped a person's mention (runs=%d)", r.runs("lead"))
	}
}

// The coordinator's run is not a step: what it says reaches the route's agents.
func TestConveyorCoordinatorMentionsAreNotSuppressed(t *testing.T) {
	r := newConveyorHandlerRig(t)
	r.enable()
	dev := r.runOf("dev")
	r.running(dev)
	testutil.Call(t, testHandler.GetIssueConveyor, r.req("GET", "/", nil)).Want(http.StatusOK)
	result := validResult("abcdef1234567")
	result["outcome"] = "blocked"
	r.result("dev", dev, result).Want(http.StatusCreated)
	if _, err := testHandler.TaskService.CompleteTask(context.Background(), parseUUID(dev), []byte(`{"output":"stuck"}`), "", "", "", false, "", ""); err != nil {
		t.Fatal(err)
	}
	lead := r.runOf("lead")
	r.running(lead)
	mention := fmt.Sprintf("[@dev](mention://agent/%s) fix this", r.agents["dev"])
	before := r.runs("dev")
	testutil.Call(t, testHandler.CreateComment, asConveyorRun(r.req("POST", "/", map[string]any{"content": mention}), r.agents["lead"], lead)).Want(http.StatusCreated)
	if r.runs("dev") != before+1 {
		t.Fatalf("the coordinator's mention of a route agent was dropped (%d -> %d runs)", before, r.runs("dev"))
	}

	// Only the coordinator may steer the route through the API.
	var view map[string]any
	testutil.Call(t, testHandler.GetIssueConveyor, r.req("GET", "/", nil)).JSON(&view)
	stranger := asConveyorRun(r.req("PUT", "/", map[string]any{"action": "disable", "expected_revision": view["revision"]}), r.agents["bystander"], dbfx.Task(t, r.agents["bystander"], testutil.Cols{"issue_id": r.issue, "runtime_id": testRuntimeID, "status": "running"}))
	testutil.Call(t, testHandler.ApplyIssueConveyor, stranger).Want(http.StatusForbidden)
	byLead := asConveyorRun(r.req("PUT", "/", map[string]any{"action": "disable", "expected_revision": view["revision"]}), r.agents["lead"], lead)
	testutil.Call(t, testHandler.ApplyIssueConveyor, byLead).Want(http.StatusOK)
}
