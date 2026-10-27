package service

import (
	"testing"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func hostStates(daemon, state string, checked time.Time) []db.SubscriptionHostState {
	return []db.SubscriptionHostState{{DaemonID: daemon, CredentialState: state, CheckedAt: ts(checked)}}
}

// An account whose reset is known takes its own place in the pool again right
// after the reset: nothing remembers that it was held, and the pool keeps the
// order the admin set.
func TestAccountWithKnownResetReturnsToItsPlaceRightAfterTheReset(t *testing.T) {
	reset := availNow.Add(30 * time.Minute)
	first := account("first", nil, usage("5h", 100, reset, true, availNow))
	second := account("second", nil)
	accounts := []Account{first, second}

	before := EvaluatePool(accounts, "", availNow)
	if before.Accounts[0].ID != "first" || !before.Accounts[0].Blocked || before.Accounts[1].Blocked {
		t.Fatalf("while the window is spent only the second runs: %+v", before.Accounts)
	}
	if !before.Accounts[0].UntilKnown || !before.Accounts[0].Until.Equal(reset) {
		t.Fatalf("the first account reports its reset: %+v", before.Accounts[0])
	}

	after := EvaluatePool(accounts, "", reset.Add(time.Second))
	if after.Accounts[0].ID != "first" || after.Accounts[1].ID != "second" {
		t.Fatalf("the order is the admin's order: %+v", after.Accounts)
	}
	if after.Accounts[0].Blocked {
		t.Fatalf("the first account is free one second after its reset: %+v", after.Accounts[0])
	}
}

// A lost sign-in is a stated reason on the account, with no end in sight; the
// account does not come back by waiting.
func TestLostSignInStaysAStatedReasonUntilTheHostSignsInAgain(t *testing.T) {
	a := account("a", nil)
	a.Hosts = hostStates("d1", "revoked", availNow)
	v := EvaluateAccount(a, "d1", availNow)
	if !v.Blocked || v.Code != ReasonLoginRequired || v.UntilKnown {
		t.Fatalf("revoked sign-in: %+v", v)
	}
	if later := EvaluateAccount(a, "d1", availNow.Add(30*time.Minute)); !later.Blocked || later.Code != ReasonLoginRequired {
		t.Fatalf("waiting does not restore a sign-in: %+v", later)
	}
	a.Hosts = hostStates("d1", "present", availNow.Add(31*time.Minute))
	if v := EvaluateAccount(a, "d1", availNow.Add(32*time.Minute)); v.Blocked {
		t.Fatalf("a new sign-in frees it: %+v", v)
	}
}
