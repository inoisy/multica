package service

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
)

// withAcceptAgent adds the agent the accept step is given to.
func (r *conveyorRig) withAcceptAgent() {
	r.t.Helper()
	r.agents["accept"] = parseTestUUID(r.t, r.f.privateAgentOwnedBy(r.t, util.UUIDToString(r.owner), "accept"))
}

func TestConveyorAcceptStandsRightBeforeClose(t *testing.T) {
	r := newConveyorRig(t)
	r.withAcceptAgent()
	a := func(step string) string { return step + ":" + util.UUIDToString(r.agents[routeAgent(step)]) }
	enable := func(steps ...string) error {
		parts := make([]string, len(steps))
		for i, s := range steps {
			parts[i] = a(s)
		}
		raw, _ := json.Marshal(strings.Join(parts, ","))
		_, err := r.apply(ConveyorCommand{Action: ConveyorCmdEnable, Route: raw})
		return err
	}
	for name, steps := range map[string][]string{
		"before review": {"dev", "qa", "accept", "review", "close"},
		"twice":         {"dev", "qa", "review", "accept", "accept", "close"},
		"first":         {"accept", "close"},
	} {
		if err := enable(steps...); !errors.Is(err, ErrConveyorInvalid) {
			t.Errorf("%s: got %v, want invalid", name, err)
		}
	}
	if _, err := NormalizeSquadRouteTemplate(r.ctx, r.f.q, parseTestUUID(t, r.f.WorkspaceID), a("accept")+","+a("close")); !errors.Is(err, ErrConveyorInvalid) {
		t.Errorf("squad template with accept first: %v", err)
	}
	// QA alone may hand work to acceptance: review is for risky work.
	if _, err := NormalizeSquadRouteTemplate(r.ctx, r.f.q, parseTestUUID(t, r.f.WorkspaceID), a("dev")+","+a("qa")+","+a("accept")+","+a("close")); err != nil {
		t.Errorf("squad template with accept after qa: %v", err)
	}
	if err := enable("dev", "qa", "accept", "close"); err != nil {
		t.Fatalf("accept after qa: %v", err)
	}
}

// The accept step judges the commit the route accepted, like qa and review:
// a pass advances to close, anything else goes to the coordinator.
func TestConveyorAcceptStepFlow(t *testing.T) {
	const commit = "abcdef1234567"
	r := newConveyorRig(t)
	r.withAcceptAgent()
	r.enable("dev", "qa", "review", "accept", "close")
	for _, step := range []string{"dev", "qa", "review"} {
		r.pass(step, commit)
	}
	if view := r.view(); view.Step != "accept" || view.AcceptedCommit != commit {
		t.Fatalf("before accept: step %s accepted %q", view.Step, view.AcceptedCommit)
	}
	if got := len(r.tasksOf("lead")); got != 0 {
		t.Fatalf("lead woken before accept (%d runs)", got)
	}
	// A red accept goes back to dev by itself: accept's default back.
	task := r.start(r.only("accept"))
	r.mustSubmit(task, r.result(ConveyorOutcomeChangesRequested, commit))
	r.complete(task)
	if view := r.view(); view.Status != ConveyorActive || view.Step != "dev" || len(r.tasksOf("lead")) != 0 {
		t.Fatalf("a red accept: status %s at %s, lead runs %d", view.Status, view.Step, len(r.tasksOf("lead")))
	}
}

// The live accept carrier is the server's hidden system agent (kind = 'system',
// system_key = 'accept'), which agent pickers do not see. The route must still
// start the accept step on it instead of waiting with "agent not found".
func TestConveyorAcceptRunsOnTheHiddenSystemCarrier(t *testing.T) {
	const commit = "abcdef1234567"
	r := newConveyorRig(t)
	r.withAcceptAgent()
	r.f.Exec(t, "UPDATE agent SET kind = 'system', system_key = 'accept' WHERE id = $1", r.agents["accept"])
	r.enable("dev", "qa", "review", "accept", "close")
	for _, step := range []string{"dev", "qa", "review"} {
		r.pass(step, commit)
	}
	task := r.start(r.only("accept"))
	r.mustSubmit(task, r.result(ConveyorOutcomePass, commit))
	r.complete(task)
	if view := r.view(); view.Status != ConveyorCompleted {
		t.Fatalf("accept on the system carrier: status %s", view.Status)
	}
}

func TestConveyorAcceptPassCompletesTheRoute(t *testing.T) {
	const commit = "abcdef1234567"
	r := newConveyorRig(t)
	r.withAcceptAgent()
	r.enable("dev", "qa", "review", "accept", "close")
	for _, step := range []string{"dev", "qa", "review", "accept"} {
		r.pass(step, commit)
	}
	// A route that passed every step closes the issue itself: no coordinator run.
	if view := r.view(); view.Status != ConveyorCompleted || len(r.tasksOf("lead")) != 0 || r.loadIssue().Status != "done" {
		t.Fatalf("after accept: status %s, lead runs %d, issue %s", view.Status, len(r.tasksOf("lead")), r.loadIssue().Status)
	}
	for _, step := range []string{"dev", "qa", "review", "accept"} {
		r.only(step)
	}
	r.everyInputDecided()
}

// onAcceptRuntime puts the accept agent on a runtime of the accept provider,
// as the daemon registers it.
func (r *conveyorRig) onAcceptRuntime() {
	r.t.Helper()
	r.f.Exec(r.t, "UPDATE agent_runtime SET provider = 'accept' WHERE id = (SELECT runtime_id FROM agent WHERE id = $1)", r.agents["accept"])
}

// agentRuntime is the runtime the rig's agent runs on.
func (r *conveyorRig) agentRuntime(name string) pgtype.UUID {
	r.t.Helper()
	a, err := r.f.q.GetAgent(r.ctx, r.agents[name])
	if err != nil {
		r.t.Fatal(err)
	}
	return a.RuntimeID
}

func steps(route []ConveyorNode) string {
	parts := make([]string, len(route))
	for i, n := range route {
		parts[i] = n.Step
	}
	return strings.Join(parts, ",")
}

func TestConveyorAddsAcceptBeforeCloseOfAReviewedRoute(t *testing.T) {
	r := newConveyorRig(t)
	r.withAcceptAgent()
	r.onAcceptRuntime()
	r.f.Exec(t, "UPDATE agent_runtime SET status = 'online' WHERE id = (SELECT runtime_id FROM agent WHERE id = $1)", r.agents["accept"])
	r.f.Cleanup(t, "DELETE FROM agent WHERE workspace_id = $1 AND system_key = 'accept'", r.f.WorkspaceID)
	view := r.enable("dev", "qa", "review", "close")
	if got := steps(view.Route); got != "dev,qa,review,accept,close" {
		t.Fatalf("route %s, want accept added before close", got)
	}
	carrier, err := r.f.q.GetAgent(r.ctx, parseTestUUID(t, view.Route[3].AgentID))
	if err != nil {
		t.Fatal(err)
	}
	// The server's own hidden agent, not the member's agent on that runtime.
	if !isAcceptCarrier(carrier) || carrier.RuntimeID != r.agentRuntime("accept") {
		t.Fatalf("accept runs on %s kind %s key %v", carrier.Name, carrier.Kind, carrier.SystemKey)
	}
	visible, err := r.f.q.ListAgents(r.ctx, parseTestUUID(t, r.f.WorkspaceID))
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range visible {
		if a.ID == carrier.ID {
			t.Fatal("the accept agent shows in the agent list")
		}
	}

	// A second reviewed route reuses the same agent.
	other := newConveyorRigIn(t, r)
	if got := other.enable("dev", "qa", "review", "close").Route[3].AgentID; got != view.Route[3].AgentID {
		t.Fatalf("second route got accept agent %s, want %s", got, view.Route[3].AgentID)
	}
	if n := r.f.Count(t, "SELECT count(*) FROM agent WHERE workspace_id = $1 AND system_key = 'accept'", r.f.WorkspaceID); n != 1 {
		t.Fatalf("%d accept agents, want one", n)
	}
}

func TestConveyorLeavesRoutesWithoutVerifierOrAcceptAgentAsWritten(t *testing.T) {
	r := newConveyorRig(t)
	r.withAcceptAgent()
	// The agent exists, but its runtime is not an accept runtime.
	if got := steps(r.enable("dev", "qa", "review", "close").Route); got != "dev,qa,review,close" {
		t.Fatalf("no accept runtime: route %s", got)
	}

	other := newConveyorRig(t)
	other.withAcceptAgent()
	other.onAcceptRuntime()
	other.agents["plan"] = other.agents["dev"]
	if got := steps(other.enable("plan", "close").Route); got != "plan,close" {
		t.Fatalf("no verifier: route %s", got)
	}
}

// A route checked only by QA merges through acceptance too, and the issue's
// metadata.accept overrides the rule both ways.
func TestConveyorAddsAcceptByVerifierOrIssueMetadata(t *testing.T) {
	r := newConveyorRig(t)
	r.withAcceptAgent()
	r.onAcceptRuntime()
	r.f.Exec(t, "UPDATE agent_runtime SET status = 'online' WHERE id = (SELECT runtime_id FROM agent WHERE id = $1)", r.agents["accept"])
	r.f.Cleanup(t, "DELETE FROM agent WHERE workspace_id = $1 AND system_key = 'accept'", r.f.WorkspaceID)
	if got := steps(r.enable("dev", "qa", "close").Route); got != "dev,qa,accept,close" {
		t.Fatalf("qa route %s, want accept before close", got)
	}

	off := newConveyorRigIn(t, r)
	off.f.Exec(t, `UPDATE issue SET metadata='{"accept":false}'::jsonb WHERE id=$1`, off.issue)
	if got := steps(off.enable("dev", "qa", "review", "close").Route); got != "dev,qa,review,close" {
		t.Fatalf("metadata.accept=false: route %s", got)
	}

	on := newConveyorRigIn(t, r)
	on.agents["build"], on.agents["critic"] = on.agents["dev"], on.agents["review"]
	on.f.Exec(t, `UPDATE issue SET metadata='{"accept":true}'::jsonb WHERE id=$1`, on.issue)
	if got := steps(on.enable("build", "critic", "close").Route); got != "build,critic,accept,close" {
		t.Fatalf("metadata.accept=true: route %s", got)
	}
}

// newConveyorRigIn is another issue of the same workspace, routed by the same agents.
func newConveyorRigIn(t *testing.T, r *conveyorRig) *conveyorRig {
	t.Helper()
	other := *r
	other.t = t
	other.issue = parseTestUUID(t, r.f.Issue(t, "Second routed issue"))
	r.f.Cleanup(t, "DELETE FROM issue_conveyor_event WHERE issue_id=$1", other.issue)
	r.f.Cleanup(t, "DELETE FROM issue_conveyor WHERE issue_id=$1", other.issue)
	r.f.Cleanup(t, "DELETE FROM issue_wakeup WHERE issue_id=$1", other.issue)
	r.f.Cleanup(t, "DELETE FROM comment WHERE issue_id=$1", other.issue)
	r.f.Cleanup(t, "DELETE FROM agent_task_queue WHERE issue_id=$1", other.issue)
	return &other
}

// An accept run cut short (pause, crash) is resumed on the same commit: a new
// round of accept must not lose it and pass with nothing merged.
func TestConveyorResumedAcceptKeepsTheAcceptedCommit(t *testing.T) {
	const commit = "abcdef1234567"
	r := newConveyorRig(t)
	r.withAcceptAgent()
	r.enable("dev", "qa", "review", "accept", "close")
	for _, step := range []string{"dev", "qa", "review"} {
		r.pass(step, commit)
	}
	r.mustApply(ConveyorCommand{Action: ConveyorCmdPause, ExpectedRevision: r.revision()})
	view := r.mustApply(ConveyorCommand{Action: ConveyorCmdResume, FromStep: "accept", ExpectedRevision: r.revision()})
	if view.Step != "accept" || view.AcceptedCommit != commit {
		t.Fatalf("resumed at %s with accepted commit %q, want accept with %q", view.Step, view.AcceptedCommit, commit)
	}
}

// The hidden accept agent is nobody's: its step is delivered whoever enabled
// the route, even when the agent is private to another member.
func TestConveyorDeliversTheHiddenAcceptAgent(t *testing.T) {
	const commit = "abcdef1234567"
	r := newConveyorRig(t)
	r.withAcceptAgent()
	r.onAcceptRuntime()
	r.f.Exec(t, "UPDATE agent_runtime SET status = 'online' WHERE id = (SELECT runtime_id FROM agent WHERE id = $1)", r.agents["accept"])
	r.f.Cleanup(t, "DELETE FROM agent WHERE workspace_id = $1 AND system_key = 'accept'", r.f.WorkspaceID)
	view := r.enable("dev", "qa", "review", "close")
	carrier := parseTestUUID(t, view.Route[3].AgentID)
	other := r.f.User(t, "someone else", "accept-owner-"+view.Route[3].AgentID+"@multica.test")
	r.f.Exec(t, "UPDATE agent SET owner_id = $2 WHERE id = $1", carrier, other)
	r.agents["carrier"] = carrier
	for _, step := range []string{"dev", "qa", "review"} {
		r.pass(step, commit)
	}
	if got := len(r.tasksOf("carrier")); got != 1 {
		t.Fatalf("accept agent has %d runs, want 1:%s", got, r.dump())
	}
}
