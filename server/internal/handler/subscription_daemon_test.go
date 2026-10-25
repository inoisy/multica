package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// poolFixture: a claude pool in the test workspace spending one subscription,
// with a registered runtime instance.
func poolFixture(t *testing.T, h *Handler, key string) (profileID, runtimeID string) {
	t.Helper()
	createSubtestSubscription(t, h, map[string]any{"key": key, "provider": "claude", "caps": map[string]int{"week": 90}})
	created := callSubscription(t, h.CreateRuntimePool, newRequest("POST", "/api/runtime-pools", map[string]any{
		"display_name": "Subtest pool " + key, "runtime_type": "claude", "subscriptions": []string{key}, "workspace_ids": []string{testWorkspaceID},
	}), http.StatusCreated)
	profileID = created["runtime_profiles"].([]any)[0].(map[string]any)["id"].(string)
	runtimeID = insertProfileRuntimeFixture(t, context.Background(), profileID, "Subtest pool runtime "+key, "claude")
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM runtime_unavailable WHERE runtime_id = $1`, runtimeID)
		testPool.Exec(context.Background(), `DELETE FROM subscription_host_state WHERE daemon_id = 'subtest-daemon'`)
	})
	return profileID, runtimeID
}

func TestClaimCarriesSubscriptionPool(t *testing.T) {
	h := subscriptionTestHandler(t)
	_, runtimeID := poolFixture(t, h, "subtest-claim")
	rt, err := h.Queries.GetAgentRuntime(context.Background(), parseUUID(runtimeID))
	if err != nil {
		t.Fatal(err)
	}
	offer, err := service.BuildAccountOffer(context.Background(), h.Queries, rt.ID)
	if err != nil || offer == nil {
		t.Fatalf("offer: %+v %v", offer, err)
	}
	raw, _ := json.Marshal(offer)
	task := &db.AgentTaskQueue{AccountOffer: raw}
	pool := h.subscriptionPoolForTask(context.Background(), rt, task)
	if pool == nil || pool.SpendOrder != "first" || len(pool.Subscriptions) != 1 || pool.Subscriptions[0].Key != "subtest-claim" ||
		pool.Subscriptions[0].CredentialPath != "~/.config/subtest-claim/token" {
		t.Fatalf("pool: %+v", pool)
	}
	if pool.OfferID != offer.OfferID || pool.PoolID != offer.PoolID || pool.Revision != offer.PoolRevision {
		t.Fatalf("payload does not carry the offer: %+v vs %+v", pool, offer)
	}
	rt.ProfileID.Valid = false
	if h.subscriptionPoolForTask(context.Background(), rt, task) != nil {
		t.Fatal("built-in runtimes carry no pool")
	}
	if h.subscriptionPoolForTask(context.Background(), rt, &db.AgentTaskQueue{}) != nil {
		t.Fatal("a run handed no offer carries no pool")
	}
}

func TestDaemonUsageParksRuntimeAtCap(t *testing.T) {
	h := subscriptionTestHandler(t)
	_, runtimeID := poolFixture(t, h, "subtest-park")
	listed := callSubscription(t, h.DaemonListSubscriptions,
		withURLParam(newDaemonTokenRequest("GET", "/x", nil, testWorkspaceID, "subtest-daemon"), "workspaceId", testWorkspaceID), http.StatusOK)
	found := false
	for _, s := range listed["subscriptions"].([]any) {
		found = found || s.(map[string]any)["key"] == "subtest-park"
	}
	if !found {
		t.Fatalf("daemon list: %v", listed)
	}

	reset := time.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339)
	report := func(week float64) {
		callSubscription(t, h.DaemonReportSubscriptionUsage, withURLParam(newDaemonTokenRequest("POST", "/x", map[string]any{
			"daemon_id": "subtest-daemon", "device_name": "host-1",
			"reports": []map[string]any{
				{"key": "subtest-park", "credential_state": "present", "windows": []map[string]any{{"window": "week", "used_percent": week, "resets_at": reset}}},
				{"key": "not-on-this-workspace", "credential_state": "present"},
			},
		}, testWorkspaceID, "subtest-daemon"), "workspaceId", testWorkspaceID), http.StatusOK)
	}
	report(95)
	if until, code, _ := runtimeClosedUntil(t, h, runtimeID); until.IsZero() || code != service.ReasonAgentCap {
		t.Fatalf("over cap: closed until %v for %q, want the agent limit", until, code)
	}
	report(10)
	if until, _, _ := runtimeClosedUntil(t, h, runtimeID); !until.IsZero() {
		t.Fatalf("below cap must open the runtime, still closed until %v", until)
	}

	one := callSubscription(t, h.GetSubscription, withURLParam(newRequest("GET", "/x", nil), "key", "subtest-park"), http.StatusOK)
	hosts := one["hosts"].([]any)
	if len(hosts) != 1 || hosts[0].(map[string]any)["device_name"] != "host-1" || one["login_state"] != "logged_in" {
		t.Fatalf("host state: %v", one)
	}
}

// runtimeClosedUntil is when the runtime can run again as the model choice sees
// it now, with the reason code and whether the time is only a guess; zero when
// it is open.
func runtimeClosedUntil(t *testing.T, h *Handler, runtimeID string) (time.Time, string, bool) {
	t.Helper()
	world, err := service.LoadModelWorld(context.Background(), h.Queries, parseUUID(testWorkspaceID), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rt := world.Runtimes[runtimeID]
	return rt.UnavailableUntil, rt.UnavailableCode, rt.UnavailableEstimated
}

func TestSubscriptionLoginThroughHeartbeat(t *testing.T) {
	h := subscriptionTestHandler(t)
	h.SubscriptionLogins = NewSubscriptionLoginStore()
	_, runtimeID := poolFixture(t, h, "subtest-login")
	// A daemon token is accepted only for a runtime that carries its daemon id.
	dbfx.Exec(t, `UPDATE agent_runtime SET daemon_id = 'subtest-daemon' WHERE id = $1`, runtimeID)

	callSubscription(t, h.StartSubscriptionLogin,
		withURLParam(newRequest("POST", "/x", map[string]any{"token": "not-a-token"}), "key", "subtest-login"), http.StatusBadRequest)
	outsider := subtestOutsider(t)
	callSubscription(t, h.StartSubscriptionLogin,
		withURLParam(newRequestAs(outsider, "POST", "/x", map[string]any{"token": "sk-ant-oat01-x"}), "key", "subtest-login"), http.StatusForbidden)

	started := callSubscription(t, h.StartSubscriptionLogin, withURLParam(newRequest("POST", "/x", map[string]any{
		"token": "sk-ant-oat01-secret", "runtime_id": runtimeID}), "key", "subtest-login"), http.StatusAccepted)
	loginID := started["login"].(map[string]any)["id"].(string)

	pending := h.pendingSubscriptionLoginFor(context.Background(), runtimeID)
	if pending == nil || pending.Token != "sk-ant-oat01-secret" || pending.CredentialPath != "~/.config/subtest-login/token" {
		t.Fatalf("pending: %+v", pending)
	}
	if again := h.pendingSubscriptionLoginFor(context.Background(), runtimeID); again != nil {
		t.Fatal("a sign-in is handed out once")
	}
	got := callSubscription(t, h.GetSubscriptionLogin, withURLParam(newRequest("GET", "/x", nil), "key", "subtest-login"), http.StatusOK)
	if strings.Contains(strings.ToLower(subtestJSON(got)), "secret") {
		t.Fatal("the token must never be served back")
	}
	callSubscription(t, h.DaemonReportSubscriptionLogin, withURLParams(newDaemonTokenRequest("POST", "/x", map[string]any{"state": "done", "message": "вошли"},
		testWorkspaceID, "subtest-daemon"), "runtimeId", runtimeID, "loginId", loginID), http.StatusOK)
	got = callSubscription(t, h.GetSubscriptionLogin, withURLParam(newRequest("GET", "/x", nil), "key", "subtest-login"), http.StatusOK)
	if got["login"].(map[string]any)["state"] != "done" {
		t.Fatalf("login: %v", got)
	}
	callSubscription(t, h.DaemonReportSubscriptionLogin, withURLParams(newDaemonTokenRequest("POST", "/x", map[string]any{"state": "failed"},
		testWorkspaceID, "subtest-daemon"), "runtimeId", runtimeID, "loginId", loginID), http.StatusNotFound)
}

func TestSubscriptionDiscrepancies(t *testing.T) {
	h := subscriptionTestHandler(t)
	profileID, runtimeID := poolFixture(t, h, "subtest-disc")
	createSubtestSubscription(t, h, map[string]any{"key": "subtest-idle", "provider": "codex"})
	empty := callSubscription(t, h.CreateRuntimePool, newRequest("POST", "/api/runtime-pools", map[string]any{
		"display_name": "Subtest empty pool", "runtime_type": "codex", "workspace_ids": []string{testWorkspaceID},
	}), http.StatusCreated)
	emptyID := empty["runtime_profiles"].([]any)[0].(map[string]any)["id"].(string)
	callSubscription(t, h.DaemonReportSubscriptionUsage, withURLParam(newDaemonTokenRequest("POST", "/x", map[string]any{
		"daemon_id": "subtest-daemon", "device_name": "host-1",
		"reports": []map[string]any{{"key": "subtest-disc", "credential_state": "missing"}},
	}, testWorkspaceID, "subtest-daemon"), "workspaceId", testWorkspaceID), http.StatusOK)

	out := callSubscription(t, h.ListSubscriptionDiscrepancies, newRequest("GET", "/x", nil), http.StatusOK)
	kinds := map[string]bool{}
	for _, it := range out["items"].([]any) {
		m := it.(map[string]any)
		switch {
		case m["kind"] == "credential_missing" && m["subscription_key"] == "subtest-disc":
			kinds["missing"] = true
		case m["kind"] == "unused_subscription" && m["subscription_key"] == "subtest-idle":
			kinds["unused"] = true
		case m["kind"] == "runtime_without_subscriptions" && m["runtime_profile_id"] == emptyID:
			kinds["empty"] = true
		}
	}
	if !kinds["missing"] || !kinds["unused"] || !kinds["empty"] {
		t.Fatalf("discrepancies %v: %v", kinds, out)
	}
	if strings.Contains(subtestJSON(out), "~/.config/") || strings.Contains(subtestJSON(out), "никто") {
		t.Fatalf("discrepancies leak paths or mixed-language prose: %v", out)
	}
	member := subtestOutsider(t)
	memberOut := callSubscription(t, h.ListSubscriptionDiscrepancies, newRequestAs(member, "GET", "/x", nil), http.StatusOK)
	if strings.Contains(subtestJSON(memberOut), "host-1") || strings.Contains(subtestJSON(memberOut), "credential_missing") {
		t.Fatalf("member sees private host diagnostics: %v", memberOut)
	}
	privateAgent := dbfx.Agent(t, "subtest hidden disabled agent", runtimeID, testutil.Cols{"permission_mode": "private", "owner_id": testUserID})
	dbfx.Exec(t, `UPDATE runtime_profile SET enabled = false WHERE id = $1`, profileID)
	memberOut = callSubscription(t, h.ListSubscriptionDiscrepancies, newRequestAs(member, "GET", "/x", nil), http.StatusOK)
	if strings.Contains(subtestJSON(memberOut), privateAgent) || strings.Contains(subtestJSON(memberOut), "subtest hidden disabled agent") {
		t.Fatalf("discrepancies reveal a private agent: %v", memberOut)
	}
	adminOut := callSubscription(t, h.ListSubscriptionDiscrepancies, newRequest("GET", "/x", nil), http.StatusOK)
	if !strings.Contains(subtestJSON(adminOut), privateAgent) {
		t.Fatalf("private agent owner lost their diagnostic: %v", adminOut)
	}
}

func subtestJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// TOON-274: a runtime of two subscriptions is parked until the first of them
// frees up, and a window whose reset has passed does not park it at all.
// twoSubscriptionPool is the pool of TOON-274: "second" (agents up to 50%
// of 5h and week) then "main" (75% / 76%). report sends a fresh snapshot:
// second at 54% of its week, main at main5h% of 5h resetting at main5hReset.
func twoSubscriptionPool(t *testing.T, h *Handler, suffix string) (runtimeID string, report func(main5h float64, main5hReset time.Time)) {
	t.Helper()
	ctx := context.Background()
	second, main := "subtest-second"+suffix, "subtest-main"+suffix
	createSubtestSubscription(t, h, map[string]any{"key": second, "provider": "claude", "caps": map[string]int{"5h": 50, "week": 50}})
	createSubtestSubscription(t, h, map[string]any{"key": main, "provider": "claude", "caps": map[string]int{"5h": 75, "week": 76}})
	created := callSubscription(t, h.CreateRuntimePool, newRequest("POST", "/api/runtime-pools", map[string]any{
		"display_name": "Subtest pool two" + suffix, "runtime_type": "claude", "subscriptions": []string{second, main}, "workspace_ids": []string{testWorkspaceID},
	}), http.StatusCreated)
	profileID := created["runtime_profiles"].([]any)[0].(map[string]any)["id"].(string)
	runtimeID = insertProfileRuntimeFixture(t, ctx, profileID, "Subtest pool two runtime"+suffix, "claude")
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM runtime_unavailable WHERE runtime_id = $1`, runtimeID)
		testPool.Exec(ctx, `DELETE FROM subscription_host_state WHERE daemon_id = 'subtest-daemon'`)
	})
	weekReset := time.Now().UTC().Add(23 * time.Hour).Format(time.RFC3339)
	report = func(main5h float64, main5hReset time.Time) {
		callSubscription(t, h.DaemonReportSubscriptionUsage, withURLParam(newDaemonTokenRequest("POST", "/x", map[string]any{
			"daemon_id": "subtest-daemon", "device_name": "host-1",
			"reports": []map[string]any{
				{"key": second, "credential_state": "present", "windows": []map[string]any{
					{"window": "5h", "used_percent": 0}, {"window": "week", "used_percent": 54, "resets_at": weekReset}}},
				{"key": main, "credential_state": "present", "windows": []map[string]any{
					{"window": "5h", "used_percent": main5h, "resets_at": main5hReset.Format(time.RFC3339)}, {"window": "week", "used_percent": 63, "resets_at": weekReset}}},
			},
		}, testWorkspaceID, "subtest-daemon"), "workspaceId", testWorkspaceID), http.StatusOK)
	}
	return runtimeID, report
}

func TestDaemonUsageParksTwoSubscriptionPoolUntilEarliestReset(t *testing.T) {
	h := subscriptionTestHandler(t)
	runtimeID, report := twoSubscriptionPool(t, h, "")
	now := time.Now().UTC().Truncate(time.Second)
	mainReset := now.Add(30 * time.Minute)
	report(97, mainReset)
	if until, _, estimated := runtimeClosedUntil(t, h, runtimeID); !until.Equal(mainReset) || estimated {
		t.Fatalf("both over cap: closed until %v (estimated %v), want %v (claude-main's 5h reset, not the week)", until, estimated, mainReset)
	}
	report(97, now.Add(-10*time.Second))
	if until, _, _ := runtimeClosedUntil(t, h, runtimeID); !until.IsZero() {
		t.Fatalf("main's 5h reset has passed: the runtime is free, still closed until %v", until)
	}
}

func TestTaskFailRequestDecodesQuotaReset(t *testing.T) {
	var req TaskFailRequest
	if err := json.Unmarshal([]byte(`{"error":"arbitrary text","refusal_code":"agent_cap_refusal","quota_reset_at":"2026-10-20T20:30:00Z"}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.RefusalCode != "agent_cap_refusal" {
		t.Fatalf("refusal_code: %s", req.RefusalCode)
	}
	if req.QuotaResetAt == nil || !req.QuotaResetAt.Equal(time.Date(2026, 10, 20, 20, 30, 0, 0, time.UTC)) {
		t.Fatalf("quota_reset_at: %v", req.QuotaResetAt)
	}
	req = TaskFailRequest{}
	json.Unmarshal([]byte(`{"error":"x"}`), &req)
	if req.QuotaResetAt != nil || req.RefusalCode != "" {
		t.Fatal("older daemons send none")
	}
}

// TOON-275: the refusal a failed run wrote on the pool's own caps parks the
// runtime until tomorrow. A fresh snapshot with one subscription usable lifts
// it — a week over its cap on the other one does not hold the pool — and a
// provider's own limit stays. recheck does the same on demand.
func TestFreshUsageLiftsStaleLocalRefusal(t *testing.T) {
	h := subscriptionTestHandler(t)
	ctx := context.Background()
	runtimeID, report := twoSubscriptionPool(t, h, "-lift")
	agentID := dbfx.Agent(t, "subtest refusal agent", runtimeID)
	taskID := dbfx.Task(t, agentID, testutil.Cols{"runtime_id": runtimeID, "status": "failed"})
	tomorrow := time.Now().Add(24 * time.Hour)
	refuse := func() {
		testPool.Exec(ctx, `INSERT INTO runtime_unavailable (workspace_id, runtime_id, until, reason, task_id, reason_code) VALUES ($1,$2,$3,$4,$5,'agent_cap_refusal')`, testWorkspaceID, runtimeID, tomorrow,
			"agent_provider_quota_limit: usage limit: подписки runtime выше лимита агентам (остаток — резерв людям): subtest-second неделя 54% ≥ 50%; subtest-main 5ч 97% ≥ 75%", taskID)
	}
	refusals := func() int {
		var n int
		testPool.QueryRow(ctx, `SELECT count(*) FROM runtime_unavailable WHERE runtime_id = $1 AND task_id IS NOT NULL AND until > now()`, runtimeID).Scan(&n)
		return n
	}

	refuse()
	report(97, time.Now().Add(30*time.Minute))
	if n := refusals(); n != 1 {
		t.Fatalf("every subscription over its cap: the refusal holds, got %d", n)
	}
	report(0, time.Now().Add(5*time.Hour))
	if n := refusals(); n != 0 {
		t.Fatalf("main is usable: the stale refusal is lifted, still %d", n)
	}

	testPool.Exec(ctx, `INSERT INTO runtime_unavailable (workspace_id, runtime_id, until, reason, task_id, reason_code) VALUES ($1,$2,$3,$4,$5,'provider_limit')`, testWorkspaceID, runtimeID, tomorrow,
		"agent_provider_quota_limit: You've hit your usage limit · resets 10:44", taskID)
	refuse()
	var out struct {
		Lifted    []map[string]any `json:"lifted"`
		Remaining []map[string]any `json:"remaining"`
	}
	testutil.Call(t, h.RecheckRuntimeUnavailable, newRequest("POST", "/api/runtime-unavailable/recheck", nil)).Want(http.StatusOK).JSON(&out)
	if len(out.Lifted) != 1 || !strings.Contains(out.Lifted[0]["reason"].(string), "выше лимита агентам") {
		t.Fatalf("recheck lifted: %v", out.Lifted)
	}
	if n := refusals(); n != 1 {
		t.Fatalf("the provider's own limit stays: %d", n)
	}
}
