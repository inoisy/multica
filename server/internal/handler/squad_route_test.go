package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// squadRouteRig is the conveyor rig's issue plus a squad led by its lead
// agent, so assigning the issue to the squad exercises the default route.
type squadRouteRig struct {
	*conveyorHandlerRig
	squad string
}

func newSquadRouteRig(t *testing.T) *squadRouteRig {
	t.Helper()
	r := &squadRouteRig{conveyorHandlerRig: newConveyorHandlerRig(t)}
	r.squad = dbfx.Insert(t, "squad", testutil.Cols{
		"workspace_id": testWorkspaceID, "name": "route squad " + r.issue, "description": "",
		"leader_id": r.agents["lead"], "creator_id": testUserID,
	})
	dbfx.Cleanup(t, "DELETE FROM squad_member WHERE squad_id=$1", r.squad)
	dbfx.Cleanup(t, "DELETE FROM squad WHERE id=$1", r.squad)
	return r
}

func (r *squadRouteRig) spec(steps ...string) string {
	parts := make([]string, len(steps))
	for i, s := range steps {
		agent := s
		if s == "close" {
			agent = "lead"
		}
		parts[i] = s + ":" + r.agents[agent]
	}
	return strings.Join(parts, ",")
}

func (r *squadRouteRig) setTemplate(template string) *testutil.Response {
	return testutil.Call(r.t, testHandler.UpdateSquad, squadScopeReq("", http.MethodPut, "/api/squads/"+r.squad,
		map[string]any{"route_template": template}, map[string]string{"id": r.squad}))
}

func (r *squadRouteRig) assignToSquad() {
	r.t.Helper()
	testutil.Call(r.t, testHandler.UpdateIssue, r.req(http.MethodPatch, "/",
		map[string]any{"assignee_type": "squad", "assignee_id": r.squad})).Want(http.StatusOK)
}

func (r *squadRouteRig) conveyorStatus() (status, step string) {
	dbfx.QueryRow(r.t, "SELECT status, (route->cur_idx->>'step') FROM issue_conveyor WHERE issue_id=$1", r.issue).Scan(&status, &step)
	return status, step
}

func (r *squadRouteRig) superseded(reason string) int {
	svc := service.IssueConveyorService{Tasks: testHandler.TaskService}
	rows, err := svc.Events(context.Background(), parseUUID(r.issue), 0, 200)
	if err != nil {
		r.t.Fatal(err)
	}
	n := 0
	for _, e := range rows {
		if e.Kind == "superseded" && e.Reason.String == reason {
			n++
		}
	}
	return n
}

func TestSquadRouteTemplateIsValidatedOnSave(t *testing.T) {
	r := newSquadRouteRig(t)
	for name, bad := range map[string]string{
		"no close":       r.spec("dev", "qa"),
		"close in body":  "close:" + r.agents["lead"] + ",dev:" + r.agents["dev"] + ",close:" + r.agents["lead"],
		"dev without qa": r.spec("dev", "close"),
		"unknown agent":  "dev:nobody,qa:" + r.agents["qa"] + ",close:" + r.agents["lead"],
		"malformed":      "dev,close",
	} {
		t.Run(name, func(t *testing.T) {
			r.setTemplate(bad).Want(http.StatusBadRequest)
		})
	}
	var squad map[string]any
	r.setTemplate(" " + r.spec("dev", "qa", "close") + " ").Want(http.StatusOK).JSON(&squad)
	if squad["route_template"] != r.spec("dev", "qa", "close") {
		t.Fatalf("route_template = %v", squad["route_template"])
	}
	r.setTemplate("").Want(http.StatusOK).JSON(&squad)
	if squad["route_template"] != "" {
		t.Fatalf("clearing left %v", squad["route_template"])
	}
}

// The acceptance scenario of stage 4: an issue of a squad with a default route
// walks every step with exactly one run per step, and the leader wakes once,
// at close.
func TestSquadDefaultRouteRunsTheIssueWithoutTheLeader(t *testing.T) {
	r := newSquadRouteRig(t)
	r.setTemplate(r.spec("dev", "qa", "close")).Want(http.StatusOK)
	r.assignToSquad()

	if status, step := r.conveyorStatus(); status != "active" || step != "dev" {
		t.Fatalf("route after assignment: status %q step %q", status, step)
	}
	if r.runs("dev") != 1 || r.runs("lead") != 0 {
		t.Fatalf("assignment started dev=%d lead=%d runs, want 1 and 0", r.runs("dev"), r.runs("lead"))
	}

	// A run of another agent on the issue reports without naming anyone: the
	// squad's implicit routing would hand it to the leader, the route holds it.
	bystander := dbfx.Insert(t, "agent_task_queue", testutil.Cols{
		"agent_id": r.agents["bystander"], "runtime_id": testRuntimeID, "issue_id": r.issue, "status": "running",
		"originator_user_id": testUserID, "accountable_user_id": testUserID,
		"started_at": testutil.Raw("now()"), "dispatched_at": testutil.Raw("now()"),
	})
	testutil.Call(t, testHandler.CreateComment, asConveyorRun(r.req("POST", "/", map[string]any{"content": "checked the logs, nothing odd"}),
		r.agents["bystander"], bystander)).Want(http.StatusCreated)
	if r.runs("lead") != 0 {
		t.Fatalf("an agent's comment woke the leader while the route leads the issue")
	}
	if r.superseded("route_leads_issue") != 1 {
		t.Fatalf("the held hand-back is not journaled")
	}

	for _, step := range []string{"dev", "qa"} {
		task := r.runOf(step)
		r.running(task)
		r.result(step, task, validResult("abc1234")).Want(http.StatusCreated)
		if _, err := testHandler.TaskService.CompleteTask(context.Background(), parseUUID(task), []byte(`{"output":"done"}`), "", "", "", false, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, step := range []string{"dev", "qa"} {
		if r.runs(step) != 1 {
			t.Fatalf("%s has %d runs, want 1", step, r.runs(step))
		}
	}
	if status, _ := r.conveyorStatus(); status != "completed" {
		t.Fatalf("route status %q, want completed", status)
	}
	if r.runs("lead") != 1 {
		t.Fatalf("leader has %d runs after close, want 1", r.runs("lead"))
	}

	// Assigning again while the route already ran does not start a second one
	// behind the leader's back; a person's comment still reaches the leader.
	testutil.Call(t, testHandler.CreateComment, r.req("POST", "/", map[string]any{"content": "please merge"})).Want(http.StatusCreated)
	if r.runs("lead") < 1 {
		t.Fatalf("a person's comment lost the leader")
	}
}

func TestIssueRouteWinsOverSquadTemplate(t *testing.T) {
	r := newSquadRouteRig(t)
	// The squad's template sends dev to the bystander; the leader wrote this
	// issue's own route with another dev, and that one runs.
	r.setTemplate("dev:" + r.agents["bystander"] + ",qa:" + r.agents["qa"] + ",close:" + r.agents["lead"]).Want(http.StatusOK)
	dbfx.Exec(t, `UPDATE issue SET metadata = jsonb_build_object('route', $2::text) WHERE id = $1`, r.issue, r.spec("dev", "qa", "close"))
	r.assignToSquad()

	if status, step := r.conveyorStatus(); status != "active" || step != "dev" {
		t.Fatalf("route after assignment: status %q step %q", status, step)
	}
	if r.runs("dev") != 1 || r.runs("bystander") != 0 || r.runs("lead") != 0 {
		t.Fatalf("dev=%d bystander=%d lead=%d runs, want the issue's dev only", r.runs("dev"), r.runs("bystander"), r.runs("lead"))
	}
}

func TestIssueRouteStartsWithoutSquadTemplate(t *testing.T) {
	r := newSquadRouteRig(t)
	dbfx.Exec(t, `UPDATE issue SET metadata = jsonb_build_object('route', $2::text) WHERE id = $1`, r.issue, r.spec("dev", "qa", "close"))
	r.assignToSquad()

	if status, step := r.conveyorStatus(); status != "active" || step != "dev" {
		t.Fatalf("route after assignment: status %q step %q", status, step)
	}
	if r.runs("dev") != 1 || r.runs("lead") != 0 {
		t.Fatalf("dev=%d lead=%d runs, want the issue's dev only", r.runs("dev"), r.runs("lead"))
	}
}

func TestSquadWithoutTemplateWakesTheLeader(t *testing.T) {
	r := newSquadRouteRig(t)
	r.assignToSquad()
	if r.runs("lead") != 1 || r.runs("dev") != 0 {
		t.Fatalf("lead=%d dev=%d, want the leader's run only", r.runs("lead"), r.runs("dev"))
	}
	if dbfx.Count(t, "SELECT count(*) FROM issue_conveyor WHERE issue_id=$1", r.issue) != 0 {
		t.Fatal("a squad without a template enabled a route")
	}
}

func TestSquadRouteThatCannotStartGoesToTheLeaderWithTheReason(t *testing.T) {
	r := newSquadRouteRig(t)
	r.setTemplate(r.spec("dev", "qa", "close")).Want(http.StatusOK)
	// The template was valid when saved; its dev agent was archived since.
	dbfx.Exec(t, "UPDATE agent SET archived_at=now() WHERE id=$1", r.agents["dev"])
	r.assignToSquad()
	if r.runs("lead") != 1 || r.runs("dev") != 0 {
		t.Fatalf("lead=%d dev=%d, want the leader to take the issue", r.runs("lead"), r.runs("dev"))
	}
	var note string
	dbfx.QueryRow(t, "SELECT COALESCE(handoff_note,'') FROM agent_task_queue WHERE issue_id=$1 AND agent_id=$2", r.issue, r.agents["lead"]).Scan(&note)
	if !strings.Contains(note, "default route was not enabled") || !strings.Contains(note, r.agents["dev"]) {
		t.Fatalf("leader is not told why the route did not start: %q", note)
	}
}

func TestDisabledRouteIsNotReenabledByAssignment(t *testing.T) {
	r := newSquadRouteRig(t)
	r.setTemplate(r.spec("dev", "qa", "close")).Want(http.StatusOK)
	r.assignToSquad()
	var revision int64
	dbfx.QueryRow(t, "SELECT revision FROM issue_conveyor WHERE issue_id=$1", r.issue).Scan(&revision)
	var view map[string]any
	r.put(map[string]any{"action": "disable", "expected_revision": revision}).Want(http.StatusOK).JSON(&view)
	// Hand the issue to an agent and back: the operator's disable holds.
	testutil.Call(t, testHandler.UpdateIssue, r.req(http.MethodPatch, "/",
		map[string]any{"assignee_type": "agent", "assignee_id": r.agents["bystander"]})).Want(http.StatusOK)
	r.assignToSquad()
	if status, _ := r.conveyorStatus(); status != "disabled" {
		t.Fatalf("route status %q, want disabled", status)
	}
	if r.runs("lead") != 1 {
		t.Fatalf("leader runs = %d, want 1", r.runs("lead"))
	}
}

func TestCompletedRouteStartsANewRoundOnReassignment(t *testing.T) {
	r := newSquadRouteRig(t)
	r.setTemplate(r.spec("dev", "qa", "close")).Want(http.StatusOK)
	r.assignToSquad()
	dbfx.Exec(t, "UPDATE issue_conveyor SET status='completed' WHERE issue_id=$1", r.issue)
	dbfx.Exec(t, "UPDATE agent_task_queue SET status='completed', completed_at=now() WHERE issue_id=$1", r.issue)
	testutil.Call(t, testHandler.UpdateIssue, r.req(http.MethodPatch, "/",
		map[string]any{"assignee_type": "agent", "assignee_id": r.agents["bystander"]})).Want(http.StatusOK)
	r.assignToSquad()
	if status, step := r.conveyorStatus(); status != "active" || step != "dev" {
		t.Fatalf("route after reassignment: status %q step %q", status, step)
	}
	if r.runs("dev") != 2 || r.runs("lead") != 0 {
		t.Fatalf("dev=%d lead=%d, want a second dev run and no leader run", r.runs("dev"), r.runs("lead"))
	}
}
