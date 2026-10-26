package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestConveyorRerunAfterCoordinatorFailure(t *testing.T) {
	for _, mode := range []string{"named step", "default step", "existing running successor"} {
		t.Run(mode, func(t *testing.T) {
			r := newConveyorRig(t)
			r.enable()
			const commit = "abcdef1234567"
			r.pass("dev", commit)
			qa := r.start(r.only("qa"))
			r.fail(qa, "agent_error.unknown", "You've hit your session limit")
			lead := r.start(r.only("lead"))
			r.fail(lead, "agent_error.unknown", "You've hit your session limit")
			if view := r.view(); view.Status != ConveyorAwaitingCoordinator {
				t.Fatalf("failure did not escalate: %+v", view)
			}

			tasks := r.f.svc.TaskSvc
			source := qa.ID
			var existing pgtype.UUID
			if mode == "default step" {
				source = pgtype.UUID{}
			}
			if mode == "existing running successor" {
				// Reproduce a manual retry made before the route could adopt it.
				orphan, err := tasks.rerunIssue(r.ctx, r.issue, qa.ID, pgtype.UUID{}, r.owner, nil)
				if err != nil {
					t.Fatal(err)
				}
				existing = r.start(*orphan).ID
			}

			rerun, err := tasks.RerunIssue(r.ctx, r.issue, source, pgtype.UUID{}, r.owner, nil)
			if err != nil {
				t.Fatal(err)
			}
			if existing.Valid && rerun.ID != existing {
				t.Fatal("retry replaced the already running successor")
			}
			if rerun.ConveyorActionID != qa.ConveyorActionID || !strings.Contains(rerun.HandoffNote.String, "conveyor result") {
				t.Fatalf("retry is detached from its step: %+v", rerun)
			}
			if view := r.view(); view.Status != ConveyorActive || view.CurrentTaskID != util.UUIDToString(rerun.ID) || view.Round != 1 || view.AcceptedCommit != commit {
				t.Fatalf("retry did not restore the route and its accepted work: %+v", view)
			}
			again, err := tasks.RerunIssue(r.ctx, r.issue, qa.ID, pgtype.UUID{}, r.owner, nil)
			if err != nil || again.ID != rerun.ID {
				t.Fatalf("repeated retry is not idempotent: %+v %v", again, err)
			}
			if len(r.tasksOf("dev")) != 1 || len(r.tasksOf("qa")) != 2 || len(r.tasksOf("lead")) != 1 {
				t.Fatal("retry duplicated work or restarted the coordinator")
			}
			rerunTask := r.start(*rerun)
			r.mustSubmit(rerunTask, r.result(ConveyorOutcomePass, commit))
			r.complete(rerunTask)
			r.only("review")
			r.everyInputDecided()
		})
	}
}

func TestConveyorRerunWaitsForUnfinishedCoordinator(t *testing.T) {
	r := newConveyorRig(t)
	r.enable()
	dev := r.start(r.only("dev"))
	r.fail(dev, "agent_error.unknown", "failed")
	// The coordinator is still queued; retry must not start a competing step.
	if _, err := r.f.svc.TaskSvc.RerunIssue(r.ctx, r.issue, dev.ID, pgtype.UUID{}, r.owner, nil); !errors.Is(err, ErrConveyorConflict) {
		t.Fatalf("retry while the coordinator is unfinished: %v", err)
	}
	if len(r.tasksOf("dev")) != 1 || r.view().Status != ConveyorAwaitingCoordinator {
		t.Fatal("refused retry changed the route or created a run")
	}
}

func TestConveyorRerunCannotAdoptSuccessorWithoutInvokePermission(t *testing.T) {
	r := newConveyorRig(t)
	r.enable()
	dev := r.start(r.only("dev"))
	r.fail(dev, "agent_error.unknown", "failed")
	r.complete(r.start(r.only("lead")))
	tasks := r.f.svc.TaskSvc
	orphan, err := tasks.rerunIssue(r.ctx, r.issue, dev.ID, pgtype.UUID{}, r.owner, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.start(*orphan)
	if _, err = tasks.RerunIssue(r.ctx, r.issue, dev.ID, pgtype.UUID{}, r.owner, func(db.Agent) bool { return false }); !errors.Is(err, ErrRerunInvokeNotAllowed) {
		t.Fatalf("adoption without invoke permission: %v", err)
	}
	if r.task(util.UUIDToString(orphan.ID)).ConveyorActionID.Valid || r.view().Status != ConveyorAwaitingCoordinator || len(r.tasksOf("dev")) != 2 {
		t.Fatal("refused adoption mutated the run or the route")
	}
}

func TestConveyorRerunConcurrentAdoption(t *testing.T) {
	r := newConveyorRig(t)
	r.enable()
	dev := r.start(r.only("dev"))
	r.fail(dev, "agent_error.unknown", "failed")
	r.complete(r.start(r.only("lead")))
	tasks := r.f.svc.TaskSvc
	orphan, err := tasks.rerunIssue(r.ctx, r.issue, dev.ID, pgtype.UUID{}, r.owner, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.start(*orphan)
	type reply struct {
		task *db.AgentTaskQueue
		err  error
	}
	replies := make(chan reply, 2)
	for range 2 {
		go func() {
			task, err := tasks.RerunIssue(r.ctx, r.issue, dev.ID, pgtype.UUID{}, r.owner, nil)
			replies <- reply{task, err}
		}()
	}
	for range 2 {
		got := <-replies
		if got.err != nil || got.task.ID != orphan.ID {
			t.Errorf("concurrent adoption returned a different run: %+v %v", got.task, got.err)
		}
	}
	bound := 0
	for _, event := range r.eventsOf(conveyorEventState) {
		if event.Action.String == "rerun_bound" {
			bound++
		}
	}
	if bound != 1 || len(r.tasksOf("dev")) != 2 || r.view().Status != ConveyorActive {
		t.Fatal("concurrent retries did not bind exactly one successor")
	}
}

func TestConveyorRerunCompletionBeforeAdoption(t *testing.T) {
	r := newConveyorRig(t)
	r.enable()
	dev := r.start(r.only("dev"))
	r.fail(dev, "agent_error.unknown", "failed")
	r.complete(r.start(r.only("lead")))
	orphan, err := r.f.svc.TaskSvc.rerunIssue(r.ctx, r.issue, dev.ID, pgtype.UUID{}, r.owner, nil)
	if err != nil {
		t.Fatal(err)
	}
	orphanTask := r.start(*orphan)
	plan, err := r.svc.planRerun(r.ctx, r.issue, dev.ID)
	if err != nil || plan.existing == nil {
		t.Fatalf("running successor was not found: %+v %v", plan, err)
	}
	// Completion wins the race between planning and locking the successor.
	r.complete(orphanTask)
	if _, err := r.svc.bindRerun(r.ctx, r.issue, plan.failed, *plan.existing); !errors.Is(err, ErrConveyorConflict) {
		t.Fatalf("adopted an already completed run: %v", err)
	}
	if r.view().Status != ConveyorAwaitingCoordinator || r.task(util.UUIDToString(orphan.ID)).ConveyorActionID.Valid {
		t.Fatal("completion before adoption left an active route without a live run")
	}
}
