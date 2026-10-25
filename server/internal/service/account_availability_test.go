package service

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var availNow = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func usage(window string, used float64, resets time.Time, limit bool, observed time.Time) db.SubscriptionUsage {
	u := db.SubscriptionUsage{WindowKey: window, UsedPercent: used, LimitReached: limit, ObservedAt: ts(observed)}
	if !resets.IsZero() {
		u.ResetsAt = ts(resets)
	}
	return u
}

func account(key string, caps map[string]int, usage ...db.SubscriptionUsage) Account {
	return Account{ID: key, Key: key, Windows: []string{"5h", "week"}, Caps: caps, Usage: usage}
}

func TestAccountBelowItsLimitIsUsable(t *testing.T) {
	a := account("a", map[string]int{"week": 80}, usage("week", 40, availNow.Add(time.Hour), false, availNow))
	v := EvaluateAccount(a, "", availNow)
	if v.Blocked || !v.Usable {
		t.Fatalf("below the limit: %+v", v)
	}
}

func TestFullWindowBlocksEvenWithoutProviderFlag(t *testing.T) {
	a := account("a", nil, usage("week", 100, availNow.Add(time.Hour), false, availNow))
	if v := EvaluateAccount(a, "", availNow); !v.Blocked || v.Code != ReasonProviderLimit {
		t.Fatalf("100 percent must block: %+v", v)
	}
}

func TestAccountAtItsLimitWaitsForTheResetOfEveryBlockingWindow(t *testing.T) {
	short, long := availNow.Add(30*time.Minute), availNow.Add(5*time.Hour)
	a := account("a", map[string]int{"5h": 70, "week": 80},
		usage("5h", 90, short, false, availNow), usage("week", 85, long, false, availNow))
	v := EvaluateAccount(a, "", availNow)
	if !v.Blocked || v.Code != ReasonAgentCap || !v.UntilKnown || !v.Until.Equal(long) {
		t.Fatalf("two windows over their limits free the account at the later reset: %+v", v)
	}

	// The provider's own flag is told from our limit.
	b := account("b", nil, usage("5h", 100, short, true, availNow))
	if v := EvaluateAccount(b, "", availNow); !v.Blocked || v.Code != ReasonProviderLimit || !v.Until.Equal(short) {
		t.Fatalf("provider limit: %+v", v)
	}

	// A window whose reset has passed is fresh, whatever the snapshot says.
	c := account("c", map[string]int{"5h": 70}, usage("5h", 97, availNow.Add(-time.Minute), false, availNow.Add(-10*time.Minute)))
	if v := EvaluateAccount(c, "", availNow); v.Blocked {
		t.Fatalf("a passed reset must free the account: %+v", v)
	}
}

func TestUnknownResetIsAnEstimateNotATime(t *testing.T) {
	a := account("a", map[string]int{"week": 50}, usage("week", 60, time.Time{}, false, availNow))
	v := EvaluateAccount(a, "", availNow)
	if !v.Blocked || v.UntilKnown || !v.Until.Equal(availNow.Add(time.Hour)) {
		t.Fatalf("no reset reported: %+v", v)
	}
	pool := EvaluatePool([]Account{a}, "", availNow)
	if !pool.Blocked || pool.UntilKnown {
		t.Fatalf("the pool keeps the estimate marked: %+v", pool)
	}
}

func TestOldSnapshotsDoNotBlock(t *testing.T) {
	a := account("a", map[string]int{"week": 50}, usage("week", 99, availNow.Add(time.Hour), false, availNow.Add(-2*time.Hour)))
	v := EvaluateAccount(a, "", availNow)
	if v.Blocked || v.Usable {
		t.Fatalf("a snapshot older than an hour says nothing: %+v", v)
	}
}

func TestFreshWindowDoesNotReviveStaleBlockingWindow(t *testing.T) {
	a := account("a", map[string]int{"week": 50},
		usage("week", 99, availNow.Add(time.Hour), false, availNow.Add(-2*time.Hour)),
		usage("5h", 10, availNow.Add(time.Hour), false, availNow))
	if v := EvaluateAccount(a, "", availNow); v.Blocked {
		t.Fatalf("stale window revived by another window: %+v", v)
	}
}

func TestDisabledAccountAndProviderRefusal(t *testing.T) {
	until := availNow.Add(2 * time.Hour)
	a := account("a", nil, usage("week", 1, availNow.Add(time.Hour), false, availNow))
	a.DisabledUntil = until
	if v := EvaluateAccount(a, "", availNow); !v.Blocked || v.Code != ReasonDisabled || !v.Until.Equal(until) || !v.UntilKnown {
		t.Fatalf("disabled: %+v", v)
	}
	a.DisabledUntil = availNow.Add(-time.Minute)
	if v := EvaluateAccount(a, "", availNow); v.Blocked {
		t.Fatalf("a passed disabled_until must not block: %+v", v)
	}
	a.Refusals = []db.RuntimeUnavailable{{Until: ts(availNow.Add(40 * time.Minute))}}
	if v := EvaluateAccount(a, "", availNow); !v.Blocked || v.Code != ReasonProviderRefusal || !v.Until.Equal(availNow.Add(40*time.Minute)) {
		t.Fatalf("provider refusal pinned to the account: %+v", v)
	}
	a.Refusals = []db.RuntimeUnavailable{{Until: ts(availNow.Add(-time.Minute))}}
	if v := EvaluateAccount(a, "", availNow); v.Blocked {
		t.Fatalf("an ended refusal must not block: %+v", v)
	}
}

func TestSignInIsReadPerHost(t *testing.T) {
	host := func(daemon, state string, checked time.Time) db.SubscriptionHostState {
		return db.SubscriptionHostState{DaemonID: daemon, CredentialState: state, CheckedAt: ts(checked)}
	}
	a := account("a", nil)
	a.Hosts = []db.SubscriptionHostState{host("d1", "revoked", availNow), host("d2", "present", availNow)}

	if v := EvaluateAccount(a, "d1", availNow); !v.Blocked || v.Code != ReasonLoginRequired || v.UntilKnown {
		t.Fatalf("host d1 lost its sign-in: %+v", v)
	}
	if v := EvaluateAccount(a, "d2", availNow); v.Blocked {
		t.Fatalf("host d2 has it: %+v", v)
	}
	if v := EvaluateAccount(a, "", availNow); v.Blocked {
		t.Fatalf("seen anywhere, the account is signed in: %+v", v)
	}
	a.Hosts = []db.SubscriptionHostState{host("d1", "missing", availNow), host("d2", "revoked", availNow)}
	if v := EvaluateAccount(a, "", availNow); !v.Blocked || v.Code != ReasonLoginRequired {
		t.Fatalf("no host has it: %+v", v)
	}
	// An unknown host, an unreadable file or an old report block nothing.
	a.Hosts = []db.SubscriptionHostState{host("d1", "unreadable", availNow), host("d2", "missing", availNow.Add(-3*time.Hour))}
	if v := EvaluateAccount(a, "d1", availNow); v.Blocked {
		t.Fatalf("unreadable is not absent: %+v", v)
	}
	if v := EvaluateAccount(a, "d2", availNow); v.Blocked {
		t.Fatalf("an old report is not read: %+v", v)
	}
}

func TestPoolIsOpenWhileOneAccountCanRun(t *testing.T) {
	capped := account("capped", map[string]int{"week": 50}, usage("week", 60, availNow.Add(3*time.Hour), false, availNow))
	free := account("free", map[string]int{"week": 50}, usage("week", 10, availNow.Add(3*time.Hour), false, availNow))
	unmeasured := account("unmeasured", map[string]int{"week": 50})

	if v := EvaluatePool([]Account{capped, free}, "", availNow); v.Blocked || !v.Usable {
		t.Fatalf("a mix of limited and available accounts: %+v", v)
	}
	// An account nobody measured may be fine: the pool is not closed on a guess.
	if v := EvaluatePool([]Account{capped, unmeasured}, "", availNow); v.Blocked || v.Usable {
		t.Fatalf("limited and unmeasured: %+v", v)
	}
	if v := EvaluatePool(nil, "", availNow); v.Blocked || v.Usable || len(v.Accounts) != 0 {
		t.Fatalf("an empty pool has no verdict: %+v", v)
	}
}

func TestPoolFreesWithItsFirstAccount(t *testing.T) {
	soon, later := availNow.Add(20*time.Minute), availNow.Add(4*time.Hour)
	a := account("a", map[string]int{"week": 50}, usage("week", 60, later, false, availNow))
	b := account("b", map[string]int{"5h": 50}, usage("5h", 60, soon, false, availNow))
	v := EvaluatePool([]Account{a, b}, "", availNow)
	if !v.Blocked || !v.Until.Equal(soon) || !v.UntilKnown || v.Code != ReasonAgentCap {
		t.Fatalf("pool: %+v", v)
	}

	// A disabled account that returns before the others frees the pool then.
	c := account("c", nil)
	c.DisabledUntil = availNow.Add(10 * time.Minute)
	v = EvaluatePool([]Account{a, c}, "", availNow)
	if !v.Blocked || v.Code != ReasonDisabled || !v.Until.Equal(c.DisabledUntil) {
		t.Fatalf("disabled account returns first: %+v", v)
	}

	// An estimate wins over a later known time, and says it is one.
	d := account("d", map[string]int{"week": 50}, usage("week", 60, time.Time{}, false, availNow))
	v = EvaluatePool([]Account{a, d}, "", availNow)
	if !v.Blocked || v.UntilKnown || !v.Until.Equal(availNow.Add(time.Hour)) {
		t.Fatalf("estimate: %+v", v)
	}
}

func TestOneRefusalOfASharedAccountClosesItInEveryPool(t *testing.T) {
	shared := account("shared", nil, usage("week", 1, availNow.Add(time.Hour), false, availNow))
	shared.Refusals = []db.RuntimeUnavailable{{Until: ts(availNow.Add(time.Hour))}}
	own := account("own", map[string]int{"week": 50}, usage("week", 1, availNow.Add(time.Hour), false, availNow))

	if v := EvaluatePool([]Account{shared}, "", availNow); !v.Blocked || v.Code != ReasonProviderRefusal {
		t.Fatalf("pool of the shared account only: %+v", v)
	}
	if v := EvaluatePool([]Account{shared, own}, "", availNow); v.Blocked {
		t.Fatalf("a pool with another account stays open: %+v", v)
	}
}

func TestEffectiveCapsKeepOnlyWindowsBelowAHundred(t *testing.T) {
	got := EffectiveCaps([]string{"5h", "week"}, map[string]int{"5h": 100, "week": 70, "month": 90})
	if len(got) != 2 || got["week"] != 70 || got["month"] != 90 {
		t.Fatalf("effective caps: %v", got)
	}
}

func TestStoredRefusalCodeDoesNotReclassifyRowsByTheirText(t *testing.T) {
	task := pgtype.UUID{Valid: true}
	for _, c := range []struct {
		row  db.RuntimeUnavailable
		want string
	}{
		{db.RuntimeUnavailable{ReasonCode: "provider_limit"}, UnavailableProviderLimit},
		{db.RuntimeUnavailable{Reason: "subscriptions: usage limit"}, UnavailableUnknown},
		{db.RuntimeUnavailable{Reason: "x выше лимита агентам y", TaskID: task}, UnavailableUnknown},
		{db.RuntimeUnavailable{Reason: "подписки среды отключены до", TaskID: task}, UnavailableUnknown},
		{db.RuntimeUnavailable{Reason: "You've hit your usage limit", TaskID: task}, UnavailableUnknown},
		{db.RuntimeUnavailable{Reason: "manual"}, UnavailableUnknown},
	} {
		if got := StoredRefusalCode(c.row); got != c.want {
			t.Errorf("%q: %s, want %s", c.row.Reason, got, c.want)
		}
	}
	if RefusalCodeForFailure("usage limit: подписки runtime выше лимита агентам") != UnavailableAgentCapRefusal ||
		RefusalCodeForFailure("usage limit: подписки среды отключены до 2026") != UnavailableDisabledRefusal ||
		RefusalCodeForFailure("You've hit your limit") != UnavailableProviderLimit {
		t.Error("failure texts are classified wrongly")
	}
}

func TestRuntimeBlocksReadStoredRefusalsByCode(t *testing.T) {
	until := ts(availNow.Add(time.Hour))
	rows := []db.RuntimeUnavailable{
		{Until: until, ReasonCode: UnavailablePoolVerdict, Reason: "subscriptions: x"},
		{Until: until, ReasonCode: UnavailableAgentCapRefusal},
		{Until: until, ReasonCode: UnavailableProviderLimit},
		{Until: until, ReasonCode: UnavailableProviderLimit, SubscriptionID: pgtype.UUID{Valid: true}}, // pinned to an account
		{Until: ts(availNow.Add(-time.Minute)), ReasonCode: UnavailableProviderLimit},
	}
	blocks := runtimeBlocks("rt", rows, nil, availNow)
	if len(blocks) != 2 || blocks[0].Code != ReasonAgentCap || blocks[1].Code != ReasonProviderRefusal {
		t.Fatalf("blocks: %+v", blocks)
	}
	// With a usable account in the pool the daemon's refusal is stale.
	blocks = runtimeBlocks("rt", rows, &PoolVerdict{Usable: true}, availNow)
	if len(blocks) != 1 || blocks[0].Code != ReasonProviderRefusal {
		t.Fatalf("stale refusal still counted: %+v", blocks)
	}
	// A blocked pool adds its computed block.
	blocks = runtimeBlocks("rt", nil, &PoolVerdict{Blocked: true, Code: ReasonDisabled, Until: availNow.Add(time.Hour)}, availNow)
	if len(blocks) != 1 || !blocks[0].Computed || blocks[0].Code != ReasonDisabled {
		t.Fatalf("computed block: %+v", blocks)
	}
}

func TestStructuredAccountRefusalIgnoresLegacyText(t *testing.T) {
	for _, code := range []string{UnavailableProviderLimit, UnavailableAgentCapRefusal, UnavailableDisabledRefusal} {
		if got := RefusalCodeForFailure("usage limit: подписки среды отключены до завтра", code); got != code {
			t.Fatalf("code %s became %s", code, got)
		}
	}
	if got := RefusalCodeForFailure("выше лимита агентам", "future_code"); got != UnavailableUnknown {
		t.Fatalf("unknown code inferred from text: %s", got)
	}
}
