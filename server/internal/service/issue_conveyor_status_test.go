package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestConveyorStatusCatalog(t *testing.T) {
	t.Run("status name already used by another key", func(t *testing.T) {
		r := newConveyorRig(t)
		r.f.Insert(t, "issue_status", testutil.Cols{"workspace_id": r.f.WorkspaceID,
			"key": "acceptance", "name": "QA", "color": "#123456", "category": "started"})
		_, err := r.apply(ConveyorCommand{Action: ConveyorCmdEnable, Route: mustJSON(r.route("dev", "qa", "close"))})
		if !errors.Is(err, ErrConveyorConflict) || len(r.tasksOf("dev")) != 0 {
			t.Fatalf("name conflict must be actionable and leave no run: %v", err)
		}
	})
	t.Run("preserves existing custom status", func(t *testing.T) {
		r := newConveyorRig(t)
		id := r.f.Insert(t, "issue_status", testutil.Cols{"workspace_id": r.f.WorkspaceID,
			"key": "qa", "name": "Acceptance", "color": "#123456", "category": "started"})
		r.enable()
		var name, color string
		r.f.QueryRow(t, "SELECT name, color FROM issue_status WHERE id=$1", id).Scan(&name, &color)
		if name != "Acceptance" || color != "#123456" {
			t.Fatalf("existing status changed: %q %q", name, color)
		}
	})
	for _, category := range []string{"done", "closed", "unstarted", "archived"} {
		t.Run(category, func(t *testing.T) {
			r := newConveyorRig(t)
			cols := testutil.Cols{"workspace_id": r.f.WorkspaceID, "key": "qa", "name": "QA", "color": "#123456", "category": category}
			if category == "archived" {
				cols["category"], cols["archived_at"] = "started", testutil.Raw("now()")
			}
			r.f.Insert(t, "issue_status", cols)
			_, err := r.apply(ConveyorCommand{Action: ConveyorCmdEnable, Route: mustJSON(r.route("dev", "qa", "close"))})
			if !errors.Is(err, ErrConveyorConflict) || len(r.tasksOf("dev")) != 0 {
				t.Fatalf("invalid catalog must reject enable without starting a run: %v", err)
			}
			if r.loadIssue().Status != "todo" {
				t.Fatal("rejected enable changed the issue")
			}
		})
	}
	t.Run("member cannot create workspace statuses", func(t *testing.T) {
		r := newConveyorRig(t)
		r.f.Exec(t, "UPDATE member SET role='member' WHERE user_id=$1 AND workspace_id=$2", r.owner, r.f.WorkspaceID)
		cmd := ConveyorCommand{Action: ConveyorCmdEnable, Route: mustJSON(r.route("dev", "qa", "close"))}
		if _, err := r.apply(cmd); !errors.Is(err, ErrConveyorForbidden) {
			t.Fatalf("member creating missing status: %v", err)
		}
		if len(r.tasksOf("dev")) != 0 {
			t.Fatal("denied enable created a run")
		}
		r.f.Insert(t, "issue_status", testutil.Cols{"workspace_id": r.f.WorkspaceID, "key": "qa", "name": "QA", "color": "#123456", "category": "started"})
		if _, err := r.apply(cmd); err != nil {
			t.Fatalf("member may use existing statuses: %v", err)
		}
	})
}

func TestConveyorStatusDeliveryRecovery(t *testing.T) {
	r := newConveyorRig(t)
	r.enable()
	dev := r.start(r.only("dev"))
	r.mustSubmit(dev, r.result(ConveyorOutcomePass, "abcdef1234567"))
	r.f.Exec(t, "UPDATE agent_task_queue SET status='completed' WHERE id=$1", dev.ID)
	r.terminal(r.task(util.UUIDToString(dev.ID)))
	action := r.pending()[0]

	// A crash after writing the status and run but before commit must leave
	// neither visible; the persistent action is still deliverable on restart.
	injected := errors.New("delivery commit failed")
	r.svc.Tasks.TxStarter = &rejectIssueCommitTxStarter{pool: r.f.Pool, err: injected}
	if err := r.svc.deliver(r.ctx, action.ID); !errors.Is(err, injected) {
		t.Fatalf("delivery error = %v", err)
	}
	if got := r.loadIssue().Status; got != "in_progress" || len(r.tasksOf("qa")) != 0 || r.metadataStep() != "dev" {
		t.Fatalf("rollback leaked delivery: status=%s step=%s", got, r.metadataStep())
	}
	r.svc.Tasks.TxStarter = r.f.Pool
	var statusEvents int
	r.svc.Tasks.Bus.SubscribeAll(func(e events.Event) {
		if e.Type == protocol.EventIssueUpdated {
			statusEvents++
		}
	})
	if err := r.svc.DeliverAllPending(r.ctx); err != nil {
		t.Fatal(err)
	}
	before := r.loadIssue()
	if err := r.svc.deliver(r.ctx, action.ID); err != nil {
		t.Fatal(err)
	}
	if before.Status != "qa" || r.metadataStep() != "qa" || r.loadIssue().Revision != before.Revision || statusEvents != 1 {
		t.Fatalf("replayed delivery changed projection: status=%s step=%s events=%d", before.Status, r.metadataStep(), statusEvents)
	}
	r.only("qa")
}

func TestConveyorStatusUnavailableDuringDelivery(t *testing.T) {
	r := newConveyorRig(t)
	r.enable()
	r.f.Exec(t, "UPDATE issue_status SET archived_at=now() WHERE workspace_id=$1 AND key='qa'", r.f.WorkspaceID)
	r.pass("dev", "abcdef1234567")
	pending := r.pending()
	if len(pending) != 1 || !strings.Contains(pending[0].Reason.String, "unavailable") || len(r.tasksOf("qa")) != 0 {
		t.Fatalf("missing status must leave diagnosed pending delivery: %s", r.dump())
	}
	if r.loadIssue().Status != "in_progress" {
		t.Fatal("delivery assigned an archived status")
	}
}

func TestConveyorDeliveryPreservesClosedIssue(t *testing.T) {
	for _, status := range []string{"done", "cancelled", "custom_done"} {
		t.Run(status, func(t *testing.T) {
			r := newConveyorRig(t)
			r.enable()
			r.f.Exec(t, "UPDATE agent SET runtime_id=NULL WHERE id=$1", r.agents["qa"])
			r.pass("dev", "abcdef1234567")
			if status == "custom_done" {
				r.f.Insert(t, "issue_status", testutil.Cols{"workspace_id": r.f.WorkspaceID, "key": status, "name": "Finished", "color": "#123456", "category": "done"})
			}
			r.f.Exec(t, "UPDATE issue SET status=$2 WHERE id=$1", r.issue, status)
			r.svc.DeliverPending(r.ctx, r.issue)
			if r.loadIssue().Status != status || len(r.tasksOf("qa")) != 0 || len(r.pending()) != 0 {
				t.Fatal("closed issue was reopened or its action was not cancelled")
			}
		})
	}
}
