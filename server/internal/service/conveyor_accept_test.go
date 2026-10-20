package service

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
)

// withAcceptAgent adds the agent the accept step is given to.
func (r *conveyorRig) withAcceptAgent() {
	r.t.Helper()
	r.agents["accept"] = parseTestUUID(r.t, r.f.privateAgentOwnedBy(r.t, util.UUIDToString(r.owner), "accept"))
}

func TestConveyorAcceptStandsRightBeforeCloseAfterReview(t *testing.T) {
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
		"without review":      {"dev", "qa", "accept", "close"},
		"before review":       {"dev", "qa", "accept", "review", "close"},
		"twice":               {"dev", "qa", "review", "accept", "accept", "close"},
		"review only in from": {"accept", "close"},
	} {
		if err := enable(steps...); !errors.Is(err, ErrConveyorInvalid) {
			t.Errorf("%s: got %v, want invalid", name, err)
		}
	}
	if _, err := NormalizeSquadRouteTemplate(r.ctx, r.f.q, parseTestUUID(t, r.f.WorkspaceID), a("dev")+","+a("qa")+","+a("accept")+","+a("close")); !errors.Is(err, ErrConveyorInvalid) {
		t.Errorf("squad template without review before accept: %v", err)
	}
	if err := enable("dev", "qa", "review", "accept", "close"); err != nil {
		t.Fatalf("a valid route with accept: %v", err)
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
	task := r.start(r.only("accept"))
	r.mustSubmit(task, r.result(ConveyorOutcomeChangesRequested, commit))
	r.complete(task)
	if view := r.view(); view.Status != ConveyorAwaitingCoordinator || len(r.tasksOf("lead")) != 1 {
		t.Fatalf("a red accept: status %s, lead runs %d", view.Status, len(r.tasksOf("lead")))
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
	if view := r.view(); view.Status != ConveyorCompleted || len(r.tasksOf("lead")) != 1 {
		t.Fatalf("after accept: status %s, lead runs %d", view.Status, len(r.tasksOf("lead")))
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
	view := r.enable("dev", "qa", "review", "close")
	if got := steps(view.Route); got != "dev,qa,review,accept,close" {
		t.Fatalf("route %s, want accept added before close", got)
	}
	if view.Route[3].AgentID != util.UUIDToString(r.agents["accept"]) {
		t.Fatalf("accept runs on %s, want the accept agent", view.Route[3].AgentID)
	}
}

func TestConveyorLeavesRoutesWithoutReviewOrAcceptAgentAsWritten(t *testing.T) {
	r := newConveyorRig(t)
	r.withAcceptAgent()
	// The agent exists, but its runtime is not an accept runtime.
	if got := steps(r.enable("dev", "qa", "review", "close").Route); got != "dev,qa,review,close" {
		t.Fatalf("no accept runtime: route %s", got)
	}

	other := newConveyorRig(t)
	other.withAcceptAgent()
	other.onAcceptRuntime()
	if got := steps(other.enable("dev", "qa", "close").Route); got != "dev,qa,close" {
		t.Fatalf("no review: route %s", got)
	}
}
