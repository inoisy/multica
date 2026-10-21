package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
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
	pool := h.subscriptionPoolForRuntime(context.Background(), rt)
	if pool == nil || pool.SpendOrder != "first" || len(pool.Subscriptions) != 1 || pool.Subscriptions[0].Key != "subtest-claim" ||
		pool.Subscriptions[0].CredentialPath != "~/.config/subtest-claim/token" {
		t.Fatalf("pool: %+v", pool)
	}
	if len(pool.Subscriptions) == 0 {
		t.Fatal("no subscriptions")
	}
	rt.ProfileID.Valid = false
	if h.subscriptionPoolForRuntime(context.Background(), rt) != nil {
		t.Fatal("built-in runtimes carry no pool")
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
	parked := func() (int, string) {
		var n int
		var reason string
		testPool.QueryRow(context.Background(), `SELECT count(*), coalesce(max(reason), '') FROM runtime_unavailable WHERE runtime_id = $1 AND until > now()`, runtimeID).Scan(&n, &reason)
		return n, reason
	}
	report(95)
	if n, reason := parked(); n != 1 || !strings.Contains(reason, "usage limit") {
		t.Fatalf("over cap: %d %q", n, reason)
	}
	report(10)
	if n, _ := parked(); n != 0 {
		t.Fatalf("below cap must lift the park, still %d", n)
	}

	one := callSubscription(t, h.GetSubscription, withURLParam(newRequest("GET", "/x", nil), "key", "subtest-park"), http.StatusOK)
	hosts := one["hosts"].([]any)
	if len(hosts) != 1 || hosts[0].(map[string]any)["device_name"] != "host-1" || one["login_state"] != "logged_in" {
		t.Fatalf("host state: %v", one)
	}
}

func TestSubscriptionLoginThroughHeartbeat(t *testing.T) {
	h := subscriptionTestHandler(t)
	h.SubscriptionLogins = NewSubscriptionLoginStore()
	_, runtimeID := poolFixture(t, h, "subtest-login")

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
	profileID, _ := poolFixture(t, h, "subtest-disc")
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
	_ = profileID
}

func subtestJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// TOON-274: a runtime of two subscriptions is parked until the first of them
// frees up, and a window whose reset has passed does not park it at all.
func TestDaemonUsageParksTwoSubscriptionPoolUntilEarliestReset(t *testing.T) {
	h := subscriptionTestHandler(t)
	ctx := context.Background()
	createSubtestSubscription(t, h, map[string]any{"key": "subtest-second", "provider": "claude", "caps": map[string]int{"5h": 50, "week": 50}})
	createSubtestSubscription(t, h, map[string]any{"key": "subtest-main", "provider": "claude", "caps": map[string]int{"5h": 75, "week": 76}})
	created := callSubscription(t, h.CreateRuntimePool, newRequest("POST", "/api/runtime-pools", map[string]any{
		"display_name": "Subtest pool two", "runtime_type": "claude", "subscriptions": []string{"subtest-second", "subtest-main"}, "workspace_ids": []string{testWorkspaceID},
	}), http.StatusCreated)
	profileID := created["runtime_profiles"].([]any)[0].(map[string]any)["id"].(string)
	runtimeID := insertProfileRuntimeFixture(t, ctx, profileID, "Subtest pool two runtime", "claude")
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM runtime_unavailable WHERE runtime_id = $1`, runtimeID)
		testPool.Exec(ctx, `DELETE FROM subscription_host_state WHERE daemon_id = 'subtest-daemon'`)
	})

	now := time.Now().UTC().Truncate(time.Second)
	weekReset := now.Add(23 * time.Hour).Format(time.RFC3339)
	report := func(main5h float64, main5hReset time.Time) {
		callSubscription(t, h.DaemonReportSubscriptionUsage, withURLParam(newDaemonTokenRequest("POST", "/x", map[string]any{
			"daemon_id": "subtest-daemon", "device_name": "host-1",
			"reports": []map[string]any{
				{"key": "subtest-second", "credential_state": "present", "windows": []map[string]any{
					{"window": "5h", "used_percent": 0}, {"window": "week", "used_percent": 54, "resets_at": weekReset}}},
				{"key": "subtest-main", "credential_state": "present", "windows": []map[string]any{
					{"window": "5h", "used_percent": main5h, "resets_at": main5hReset.Format(time.RFC3339)}, {"window": "week", "used_percent": 63, "resets_at": weekReset}}},
			},
		}, testWorkspaceID, "subtest-daemon"), "workspaceId", testWorkspaceID), http.StatusOK)
	}
	parkedUntil := func() (int, time.Time) {
		var n int
		var until time.Time
		testPool.QueryRow(ctx, `SELECT count(*), coalesce(max(until), 'epoch') FROM runtime_unavailable WHERE runtime_id = $1 AND until > now()`, runtimeID).Scan(&n, &until)
		return n, until
	}

	mainReset := now.Add(30 * time.Minute)
	report(97, mainReset)
	if n, until := parkedUntil(); n != 1 || !until.Equal(mainReset) {
		t.Fatalf("both over cap: parked %d until %v, want %v (claude-main's 5h reset, not the week)", n, until, mainReset)
	}
	report(97, now.Add(-10*time.Second))
	if n, _ := parkedUntil(); n != 0 {
		t.Fatalf("main's 5h reset has passed: the runtime is free, still %d parked", n)
	}
}

func TestTaskFailRequestDecodesQuotaReset(t *testing.T) {
	var req TaskFailRequest
	if err := json.Unmarshal([]byte(`{"error":"usage limit","quota_reset_at":"2026-10-20T20:30:00Z"}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.QuotaResetAt == nil || !req.QuotaResetAt.Equal(time.Date(2026, 10, 20, 20, 30, 0, 0, time.UTC)) {
		t.Fatalf("quota_reset_at: %v", req.QuotaResetAt)
	}
	req = TaskFailRequest{}
	json.Unmarshal([]byte(`{"error":"x"}`), &req)
	if req.QuotaResetAt != nil {
		t.Fatal("older daemons send none")
	}
}
