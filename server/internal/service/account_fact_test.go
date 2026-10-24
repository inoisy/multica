package service

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func claimedAccountRun(t *testing.T) (*poolRig, db.AgentTaskQueue, AccountOffer) {
	t.Helper()
	r := newPoolRig(t)
	res, err := r.pools.Create(r.ctx, r.owner, PoolInput{Name: "ptest offered", RuntimeType: "codex", Subscriptions: []string{r.account("first", "codex"), r.account("second", "codex")}, Workspaces: []pgtype.UUID{r.ws}})
	if err != nil {
		t.Fatal(err)
	}
	r.fx.Exec(t, `UPDATE agent_runtime SET profile_id=$1, daemon_id='fact-daemon' WHERE id=$2`, res.Profiles[0].ID, r.rt["codex"])
	a := r.agent("account runner", pgtype.UUID{})
	task := r.enqueue(a, pgtype.UUID{})
	claimed := r.claim("codex")
	if claimed == nil || claimed.ID != task.ID {
		t.Fatal("run was not claimed")
	}
	offer, ok := ParseAccountOffer(claimed.AccountOffer)
	if !ok || len(offer.Subscriptions) != 2 {
		t.Fatalf("claim has no offer: %s", claimed.AccountOffer)
	}
	stored := r.task(task.ID)
	storedOffer, ok := ParseAccountOffer(stored.AccountOffer)
	if !ok || !reflect.DeepEqual(storedOffer, offer) || stored.AccountFact != nil {
		t.Fatal("offer was not stored with claim")
	}
	return r, *claimed, offer
}

func confirmation(o AccountOffer, position int) AccountConfirmation {
	return AccountConfirmation{OfferID: o.OfferID, PoolID: o.PoolID, PoolRevision: o.PoolRevision, SubscriptionID: o.Subscriptions[position].SubscriptionID}
}

func TestAccountFactUsesTheClaimSnapshotAndSurvivesPoolChanges(t *testing.T) {
	r, task, offer := claimedAccountRun(t)
	empty := []string{}
	if _, err := r.pools.Update(r.ctx, util.MustParseUUID(offer.PoolID), r.owner, &offer.PoolRevision, PoolPatch{Subscriptions: &empty}); err != nil {
		t.Fatal(err)
	}
	r.fx.Exec(t, `UPDATE subscription SET account_group='changed-after-claim' WHERE id=$1`, offer.Subscriptions[0].SubscriptionID)
	req := confirmation(offer, 0)
	first, err := r.svc.ConfirmTaskAccount(r.ctx, task.ID, task.RuntimeID, "fact-daemon", req, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if first.SubscriptionKey != offer.Subscriptions[0].SubscriptionKey || first.AccountGroup != offer.Subscriptions[0].AccountGroup || first.PoolRevision != offer.PoolRevision {
		t.Fatalf("fact came from current pool: %+v", first)
	}
	r.fx.Exec(t, `UPDATE agent_task_queue SET status='completed' WHERE id=$1`, task.ID)
	second, err := r.svc.ConfirmTaskAccount(r.ctx, task.ID, task.RuntimeID, "fact-daemon", req, time.Now().Add(time.Hour))
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("terminal repeat: %+v %v", second, err)
	}
	if _, err := r.svc.ConfirmTaskAccount(r.ctx, task.ID, task.RuntimeID, "fact-daemon", confirmation(offer, 1), time.Now()); !errors.Is(err, ErrAccountConflict) {
		t.Fatalf("conflicting repeat: %v", err)
	}
}

func TestAccountConfirmationRejectsUnrelatedAssignments(t *testing.T) {
	r, task, offer := claimedAccountRun(t)
	for _, field := range []string{"offer", "pool", "revision", "account", "runtime", "daemon"} {
		t.Run(field, func(t *testing.T) {
			req, rt, daemon := confirmation(offer, 0), task.RuntimeID, "fact-daemon"
			switch field {
			case "offer":
				req.OfferID = r.rt["glm"]
			case "pool":
				req.PoolID = r.rt["glm"]
			case "revision":
				req.PoolRevision++
			case "account":
				req.SubscriptionID = r.rt["glm"]
			case "runtime":
				rt = util.MustParseUUID(r.rt["glm"])
			case "daemon":
				daemon = "another-daemon"
			}
			if _, err := r.svc.ConfirmTaskAccount(r.ctx, task.ID, rt, daemon, req, time.Now()); err == nil {
				t.Fatal("unrelated confirmation succeeded")
			}
			if r.task(task.ID).AccountFact != nil {
				t.Fatal("rejected confirmation wrote a fact")
			}
		})
	}
	r.fx.Exec(t, `UPDATE agent_task_queue SET status='failed' WHERE id=$1`, task.ID)
	if _, err := r.svc.ConfirmTaskAccount(r.ctx, task.ID, task.RuntimeID, "fact-daemon", confirmation(offer, 0), time.Now()); !errors.Is(err, ErrAccountTaskState) {
		t.Fatalf("first confirmation after terminal: %v", err)
	}
}

func TestConcurrentAccountConfirmationsKeepTheFirstFact(t *testing.T) {
	r, task, offer := claimedAccountRun(t)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := r.svc.ConfirmTaskAccount(r.ctx, task.ID, task.RuntimeID, "fact-daemon", confirmation(offer, i), time.Now())
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	successes, conflicts := 0, 0
	for err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrAccountConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	if _, ok := ParseAccountFact(r.task(task.ID).AccountFact); !ok {
		t.Fatal("no fact stored")
	}
}

func TestReclaimReplacesTheOfferAndClearsThePreviousFact(t *testing.T) {
	r, task, previous := claimedAccountRun(t)
	if _, err := r.svc.ConfirmTaskAccount(r.ctx, task.ID, task.RuntimeID, "fact-daemon", confirmation(previous, 0), time.Now()); err != nil {
		t.Fatal(err)
	}
	r.fx.Exec(t, `UPDATE agent_task_queue SET status='queued' WHERE id=$1`, task.ID)
	r.fx.Exec(t, `UPDATE agent_runtime SET daemon_id='replacement-daemon' WHERE id=$1`, task.RuntimeID)
	claimed := r.claim("codex")
	if claimed == nil || claimed.ID != task.ID {
		t.Fatal("task was not reassigned")
	}
	next, ok := ParseAccountOffer(claimed.AccountOffer)
	if !ok || next.OfferID == previous.OfferID || next.DaemonID != "replacement-daemon" || claimed.AccountFact != nil {
		t.Fatalf("reclaim kept old assignment: %+v", claimed)
	}
	if _, err := r.svc.ConfirmTaskAccount(r.ctx, task.ID, task.RuntimeID, "replacement-daemon", confirmation(previous, 0), time.Now()); !errors.Is(err, ErrAccountStaleOffer) {
		t.Fatalf("stale offer: %v", err)
	}
	if _, err := r.svc.ConfirmTaskAccount(r.ctx, task.ID, task.RuntimeID, "replacement-daemon", confirmation(next, 0), time.Now()); err != nil {
		t.Fatal(err)
	}
}
