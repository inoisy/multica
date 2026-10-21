package service

import (
	"errors"
	"strings"
	"testing"
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

// STT-368: the reviewer fixed a regression itself (3a29349 on top of the
// d9d89b5 QA passed). The route sends the reviewer's commit to QA and, once QA
// passes it, goes on without running the reviewer again.
func TestConveyorRechecksAVerifiersOwnCommit(t *testing.T) {
	const dev, fix = "abcdef1234567", "1234567abcdef"
	r := newConveyorRig(t)
	r.enable("dev", "qa", "review", "close")
	r.pass("dev", dev)
	r.pass("qa", dev)
	r.pass("review", fix)
	view := r.view()
	if view.Status != ConveyorActive || view.Step != "qa" || view.AcceptedCommit != fix || len(r.tasksOf("lead")) != 0 {
		t.Fatalf("after the reviewer's fix: %s at %s, accepted %q, lead runs %d; journal:%s", view.Status, view.Step, view.AcceptedCommit, len(r.tasksOf("lead")), r.dump())
	}
	qa := r.tasksOf("qa")
	if len(qa) != 2 || !strings.Contains(qa[1].HandoffNote.String, "сам поправил") || !strings.Contains(qa[1].HandoffNote.String, fix) {
		t.Fatalf("qa re-check run %d, note:\n%s", len(qa), qa[len(qa)-1].HandoffNote.String)
	}
	r.pass("qa", fix)
	if view := r.view(); view.Status != ConveyorCompleted || len(r.tasksOf("review")) != 1 {
		t.Fatalf("after qa passed the fix: %s, review runs %d; journal:%s", view.Status, len(r.tasksOf("review")), r.dump())
	}
}

// The re-checks of one revision are bounded: reviewers that keep rewriting
// the work end at the coordinator.
func TestConveyorRechecksAreBounded(t *testing.T) {
	r := newConveyorRig(t)
	r.enable("dev", "qa", "review", "review", "close")
	r.pass("dev", "c000000000000")
	r.pass("qa", "c000000000000")
	r.pass("review", "c111111111111") // first review: re-check 1
	r.pass("qa", "c111111111111")     // the first review is passed over
	r.pass("review", "c222222222222") // second review: re-check 2
	r.pass("qa", "c222222222222")
	if view := r.view(); view.Step != "review" || view.CurrentIdx != 2 {
		t.Fatalf("after re-check 2: at %s (%d); journal:%s", view.Step, view.CurrentIdx, r.dump())
	}
	r.pass("review", "c333333333333") // a third rewrite goes to the coordinator
	if view := r.view(); view.Status != ConveyorAwaitingCoordinator {
		t.Fatalf("after %d re-checks: %s; journal:%s", conveyorMaxRechecks+1, view.Status, r.dump())
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
