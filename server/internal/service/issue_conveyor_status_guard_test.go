package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
)

func TestConveyorGuardsAgentStatusWrites(t *testing.T) {
	guard := func(r *conveyorRig, actor, status string) error {
		r.t.Helper()
		return r.svc.GuardAgentStatusWrite(r.ctx, r.loadIssue(), actor, status)
	}

	t.Run("live route refuses an agent", func(t *testing.T) {
		r := newConveyorRig(t)
		r.enable()
		err := guard(r, "agent", "done")
		if !errors.Is(err, ErrConveyorOwnsStatus) {
			t.Fatalf("agent closed a routed issue: %v", err)
		}
		if want := "multica issue conveyor result " + util.UUIDToString(r.issue) + " --file result.json"; !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal does not say how to hand the step in: %q", err)
		}
		r.f.Exec(t, "UPDATE issue_conveyor SET status=$2 WHERE issue_id=$1", r.issue, ConveyorWaitingProvider)
		if err := guard(r, "agent", "in_review"); !errors.Is(err, ErrConveyorOwnsStatus) {
			t.Fatalf("route waiting for a provider let an agent move the status: %v", err)
		}
	})
	t.Run("a person and a no-op write pass", func(t *testing.T) {
		r := newConveyorRig(t)
		r.enable()
		if err := guard(r, "member", "done"); err != nil {
			t.Fatalf("person refused: %v", err)
		}
		if err := guard(r, "agent", r.loadIssue().Status); err != nil {
			t.Fatalf("write of the current status refused: %v", err)
		}
	})
	for name, status := range map[string]string{
		"paused":               ConveyorPaused,
		"awaiting coordinator": ConveyorAwaitingCoordinator,
		"disabled":             ConveyorDisabled,
		"completed":            ConveyorCompleted,
	} {
		t.Run(name+" route lets an agent", func(t *testing.T) {
			r := newConveyorRig(t)
			r.enable()
			r.f.Exec(t, "UPDATE issue_conveyor SET status=$2 WHERE issue_id=$1", r.issue, status)
			if err := guard(r, "agent", "done"); err != nil {
				t.Fatalf("agent refused on a %s route: %v", status, err)
			}
		})
	}
	t.Run("no route lets an agent", func(t *testing.T) {
		r := newConveyorRig(t)
		if err := guard(r, "agent", "done"); err != nil {
			t.Fatalf("agent refused without a route: %v", err)
		}
	})
	t.Run("pause through the command lets an agent", func(t *testing.T) {
		r := newConveyorRig(t)
		r.enable()
		r.mustApply(ConveyorCommand{Action: ConveyorCmdPause, ExpectedRevision: r.revision()})
		if err := guard(r, "agent", "done"); err != nil {
			t.Fatalf("agent refused on a paused route: %v", err)
		}
	})
}

func TestConveyorHandoffKeepsAgentsOffTheStatus(t *testing.T) {
	r := newConveyorRig(t)
	r.enable()
	dev := r.only("dev")
	if !strings.Contains(dev.HandoffNote.String, "Статус задачи не меняйте и подзадач не заводите — это делает маршрут") {
		t.Fatalf("executor handoff does not keep the agent off the status:\n%s", dev.HandoffNote.String)
	}
	r.fail(r.start(dev), "agent_error", "the agent crashed")
	lead := r.only("lead")
	for _, want := range []string{"conveyor resume", "conveyor disable", "Статус задачи не меняйте", "`blocked`"} {
		if !strings.Contains(lead.HandoffNote.String, want) {
			t.Fatalf("coordinator handoff lacks %q:\n%s", want, lead.HandoffNote.String)
		}
	}
}
