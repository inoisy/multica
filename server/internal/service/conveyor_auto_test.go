package service

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
)

// STT-368: a daemon restart cut the dev run, and the coordinator spent a run
// on `resume --step dev`. The route now starts the step again by itself while
// the node's retries last, then wakes the coordinator.
func TestConveyorRetriesARunInfrastructureCutShort(t *testing.T) {
	r := newConveyorRig(t)
	r.enable("dev", "qa", "close")
	for i := 1; i <= conveyorDefaultRetries; i++ {
		tasks := r.tasksOf("dev")
		r.fail(r.start(tasks[len(tasks)-1]), "cancelled", "task cancelled by server")
		if view := r.view(); view.Status != ConveyorActive || view.Step != "dev" || len(r.tasksOf("dev")) != i+1 || len(r.tasksOf("lead")) != 0 {
			t.Fatalf("cut %d: %s at %s, dev runs %d, lead runs %d; journal:%s", i, view.Status, view.Step, len(r.tasksOf("dev")), len(r.tasksOf("lead")), r.dump())
		}
	}
	tasks := r.tasksOf("dev")
	r.fail(r.start(tasks[len(tasks)-1]), "cancelled", "task cancelled by server")
	if view := r.view(); view.Status != ConveyorAwaitingCoordinator || len(r.tasksOf("lead")) != 1 {
		t.Fatalf("retries spent: %s, lead runs %d; journal:%s", view.Status, len(r.tasksOf("lead")), r.dump())
	}
	if note := r.only("lead").HandoffNote.String; !strings.Contains(note, "retr(ies) spent") {
		t.Fatalf("coordinator note does not say the retries are spent:\n%s", note)
	}
}

// A person's cancel and a failure of the work itself are not retried.
func TestConveyorDoesNotRetryWorkFailures(t *testing.T) {
	for _, reason := range []string{"user_cancelled", "agent_error"} {
		r := newConveyorRig(t)
		r.enable("dev", "qa", "close")
		r.fail(r.start(r.only("dev")), reason, "stopped")
		if view := r.view(); view.Status != ConveyorAwaitingCoordinator || len(r.tasksOf("dev")) != 1 {
			t.Fatalf("%s: %s, dev runs %d", reason, view.Status, len(r.tasksOf("dev")))
		}
	}
}

// STT-364: the reviewer fixed the work itself (f57b2f7 on top of the 71b7e42
// QA passed). The reviewer's commit becomes the accepted one and the route
// goes on: no new round through dev, no second qa, no coordinator.
func TestConveyorVerifiersOwnCommitAdvancesTheRoute(t *testing.T) {
	const dev, fix = "abcdef1234567", "1234567abcdef"
	r := newConveyorRig(t)
	r.withAcceptAgent()
	r.enable("dev", "qa", "review", "accept", "close")
	r.pass("dev", dev)
	r.pass("qa", dev)
	r.pass("review", fix)
	view := r.view()
	if view.Status != ConveyorActive || view.Step != "accept" || view.AcceptedCommit != fix || len(r.tasksOf("lead")) != 0 || len(r.tasksOf("qa")) != 1 {
		t.Fatalf("after the reviewer's fix: %s at %s, accepted %q, qa runs %d, lead runs %d; journal:%s", view.Status, view.Step, view.AcceptedCommit, len(r.tasksOf("qa")), len(r.tasksOf("lead")), r.dump())
	}
	if d := r.decisionFor(r.only("review")); d.Action.String != conveyorDecisionAdvance || d.Reason.String != "accepted commit advanced by review: abcdef123 → 1234567ab" {
		t.Fatalf("review decision %s %q", d.Action.String, d.Reason.String)
	}
	r.pass("accept", fix)
	if view := r.view(); view.Status != ConveyorCompleted {
		t.Fatalf("after accept: %s; journal:%s", view.Status, r.dump())
	}
}

// qa's own commit moves the route on just the same.
func TestConveyorFirstVerifiersOwnCommitAdvancesTheRoute(t *testing.T) {
	r := newConveyorRig(t)
	r.enable("dev", "qa", "review", "close")
	r.pass("dev", "c000000000000")
	r.pass("qa", "c111111111111")
	if view := r.view(); view.Step != "review" || view.AcceptedCommit != "c111111111111" {
		t.Fatalf("after qa's fix: at %s, accepted %q; journal:%s", view.Step, view.AcceptedCommit, r.dump())
	}
}

// STT-354: acceptance found the ADR number taken. dev renames it and the fix
// goes straight back to accept, not through qa and review again; changes
// asked by qa or review still go dev → qa → review.
func TestConveyorFixForARedAcceptGoesStraightToAccept(t *testing.T) {
	const first, fix = "abcdef1234567", "1234567abcdef"
	r := newConveyorRig(t)
	r.withAcceptAgent()
	r.enable("dev", "qa", "review", "accept", "close")
	for _, step := range []string{"dev", "qa", "review"} {
		r.pass(step, first)
	}
	task := r.start(r.only("accept"))
	r.mustSubmit(task, r.result(ConveyorOutcomeChangesRequested, first))
	r.complete(task)
	if view := r.view(); view.Step != "dev" {
		t.Fatalf("red accept: at %s; journal:%s", view.Step, r.dump())
	}
	r.pass("dev", fix)
	view := r.view()
	if view.Step != "accept" || view.AcceptedCommit != fix || len(r.tasksOf("qa")) != 1 || len(r.tasksOf("review")) != 1 {
		t.Fatalf("after the fix: at %s, accepted %q, qa runs %d, review runs %d; journal:%s", view.Step, view.AcceptedCommit, len(r.tasksOf("qa")), len(r.tasksOf("review")), r.dump())
	}
	devs := r.tasksOf("dev")
	if d := r.decisionFor(devs[len(devs)-1]); d.Reason.String != "accept retry: dev → accept" {
		t.Fatalf("dev decision %q", d.Reason.String)
	}
	r.pass("accept", fix)
	if view := r.view(); view.Status != ConveyorCompleted {
		t.Fatalf("after accept: %s", view.Status)
	}
}

// Both executors of a shared dev step fix a red acceptance before accept runs.
func TestConveyorAcceptRetryPassesThroughDevPeers(t *testing.T) {
	r := newConveyorRig(t)
	r.withAcceptAgent()
	r.agents["front"] = parseTestUUID(t, r.f.privateAgentOwnedBy(t, util.UUIDToString(r.owner), "front"))
	raw, _ := json.Marshal(r.route("dev") + ",dev:" + util.UUIDToString(r.agents["front"]) + "," + r.route("qa", "accept", "close"))
	r.mustApply(ConveyorCommand{Action: ConveyorCmdEnable, Route: raw})
	r.pass("dev", "c000000000000")
	r.pass("front", "c000000000000")
	r.pass("qa", "c000000000000")
	task := r.start(r.only("accept"))
	r.mustSubmit(task, r.result(ConveyorOutcomeChangesRequested, "c000000000000"))
	r.complete(task)
	view := r.view()
	if view.Step != "dev" {
		t.Fatalf("red accept: at %s", view.Step)
	}
	// back=dev opens the round at the last dev node: the frontend.
	r.pass("front", "c111111111111")
	if view := r.view(); view.Step != "accept" || len(r.tasksOf("qa")) != 1 {
		t.Fatalf("after the frontend's fix: at %s, qa runs %d; journal:%s", view.Step, len(r.tasksOf("qa")), r.dump())
	}
}

// qa's changes_requested still runs the whole way again: dev → qa → review.
func TestConveyorVerifierChangesStillGoThroughQA(t *testing.T) {
	r := newConveyorRig(t)
	r.withAcceptAgent()
	r.enable("dev", "qa", "review", "accept", "close")
	r.pass("dev", "c000000000000")
	r.pass("qa", "c000000000000")
	task := r.start(r.only("review"))
	r.mustSubmit(task, r.result(ConveyorOutcomeChangesRequested, "c000000000000"))
	r.complete(task)
	r.pass("dev", "c111111111111")
	if view := r.view(); view.Step != "qa" {
		t.Fatalf("after dev's fix for review: at %s; journal:%s", view.Step, r.dump())
	}
}

// A verifier without back returns changes to the closest earlier work step;
// back=none keeps the old way, to the coordinator.
func TestConveyorVerifierGoesBackToWorkByDefault(t *testing.T) {
	const commit = "abcdef1234567"
	r := newConveyorRig(t)
	view := r.enable("dev", "qa", "review", "close")
	if view.Route[1].Back != "dev" || view.Route[2].Back != "dev" {
		t.Fatalf("default back: qa %q, review %q", view.Route[1].Back, view.Route[2].Back)
	}
	r.pass("dev", commit)
	task := r.start(r.only("qa"))
	r.mustSubmit(task, r.result(ConveyorOutcomeChangesRequested, commit))
	r.complete(task)
	if view := r.view(); view.Status != ConveyorActive || view.Step != "dev" || len(r.tasksOf("lead")) != 0 {
		t.Fatalf("qa changes: %s at %s, lead runs %d", view.Status, view.Step, len(r.tasksOf("lead")))
	}

	other := newConveyorRig(t)
	if got := other.enable("dev", "qa[back=none]", "close").Route[1].Back; got != "" {
		t.Fatalf("back=none kept back %q", got)
	}
}

func TestConveyorNodeRetriesOption(t *testing.T) {
	entries, err := ParseConveyorRouteSpec("dev:A[retries=0],qa:B[retries=4],close:C")
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].Retries != -1 || entries[1].Retries != 4 {
		t.Fatalf("retries %d, %d", entries[0].Retries, entries[1].Retries)
	}
	if got := (ConveyorNode{Retries: entries[0].Retries}).retries(); got != 0 {
		t.Fatalf("retries=0 gives %d", got)
	}
	if got := (ConveyorNode{}).retries(); got != conveyorDefaultRetries {
		t.Fatalf("default retries %d", got)
	}
	if got := entries[0].spec(); got != "dev:A[retries=0]" {
		t.Fatalf("spec %q", got)
	}
	if _, err := ParseConveyorRouteSpec("dev:A[retries=9],close:C"); !errors.Is(err, ErrConveyorInvalid) {
		t.Fatalf("retries=9: %v", err)
	}
}
