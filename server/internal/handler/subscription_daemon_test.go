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
