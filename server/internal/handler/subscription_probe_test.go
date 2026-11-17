package handler

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// probePoolFixture: a claude pool spending two subscriptions — keyA is probed,
// keyB is a bystander — with one online runtime and a provider_limit refusal
// row pinned to each subscription until tomorrow.
func probePoolFixture(t *testing.T, h *Handler, suffix string) (runtimeID, keyA, keyB string) {
	t.Helper()
	ctx := context.Background()
	keyA, keyB = "subtest-probe-a"+suffix, "subtest-probe-b"+suffix
	createSubtestSubscription(t, h, map[string]any{"key": keyA, "provider": "claude", "windows": []string{"5h", "week"}})
	createSubtestSubscription(t, h, map[string]any{"key": keyB, "provider": "claude", "windows": []string{"5h", "week"}})
	created := callSubscription(t, h.CreateRuntimePool, newRequest("POST", "/api/runtime-pools", map[string]any{
		"display_name": "Subtest probe pool" + suffix, "runtime_type": "claude", "subscriptions": []string{keyA, keyB}, "workspace_ids": []string{testWorkspaceID},
	}), http.StatusCreated)
	profileID := created["runtime_profiles"].([]any)[0].(map[string]any)["id"].(string)
	runtimeID = insertProfileRuntimeFixture(t, ctx, profileID, "Subtest probe runtime"+suffix, "claude")
	refuse := func(key string) {
		dbfx.Exec(t, `INSERT INTO runtime_unavailable (workspace_id, runtime_id, until, reason, reason_code, subscription_id)
			VALUES ($1, $2, $3, 'agent_provider_quota_limit: usage limit', 'provider_limit', (SELECT id FROM subscription WHERE key = $4))`,
			testWorkspaceID, runtimeID, time.Now().Add(24*time.Hour), key)
	}
	refuse(keyA)
	refuse(keyB)
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM runtime_unavailable WHERE runtime_id = $1`, runtimeID)
		testPool.Exec(ctx, `DELETE FROM subscription_host_state WHERE daemon_id = 'subtest-daemon'`)
	})
	return runtimeID, keyA, keyB
}

// activeRefusals counts the provider_limit rows still holding for the key.
func activeRefusals(t *testing.T, key string) int {
	t.Helper()
	var n int
	testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM runtime_unavailable
		WHERE subscription_id = (SELECT id FROM subscription WHERE key = $1)
		  AND reason_code = 'provider_limit' AND until > now()`, key).Scan(&n)
	return n
}

// probeReport sends the daemon's usage report for one key. probeID tags the
// report as the answer to a probe; atLimit makes the 5h window spent.
func probeReport(t *testing.T, h *Handler, key, probeID string, atLimit bool) {
	t.Helper()
	reset := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	item := map[string]any{"key": key, "credential_state": "present", "windows": []map[string]any{
		{"window": "5h", "used_percent": 96.0, "limit_reached": atLimit, "resets_at": reset},
		{"window": "week", "used_percent": 40, "limit_reached": false},
	}}
	if probeID != "" {
		item["probe_id"] = probeID
	}
	callSubscription(t, h.DaemonReportSubscriptionUsage, withURLParam(newDaemonTokenRequest("POST", "/x", map[string]any{
		"daemon_id": "subtest-daemon", "device_name": "host-1", "reports": []map[string]any{item},
	}, testWorkspaceID, "subtest-daemon"), "workspaceId", testWorkspaceID), http.StatusOK)
}

// startProbe starts a probe as the workspace owner and hands it to the
// runtime's daemon, the way a heartbeat would.
func startProbe(t *testing.T, h *Handler, key, runtimeID string) string {
	t.Helper()
	started := callSubscription(t, h.StartSubscriptionProbe, withURLParam(newRequest("POST", "/x", nil), "key", key), http.StatusAccepted)
	probeID := started["probe"].(map[string]any)["id"].(string)
	pending := h.pendingSubscriptionProbeFor(context.Background(), runtimeID)
	if pending == nil || pending.Key != key || pending.ID != probeID {
		t.Fatalf("pending probe: %+v want key %s id %s", pending, key, probeID)
	}
	if again := h.pendingSubscriptionProbeFor(context.Background(), runtimeID); again != nil {
		t.Fatal("a probe is handed out once")
	}
	return probeID
}

func probeState(t *testing.T, h *Handler, key, probeID string) map[string]any {
	t.Helper()
	got := callSubscription(t, h.GetSubscriptionProbe, withURLParams(newRequest("GET", "/x", nil), "key", key, "probeId", probeID), http.StatusOK)
	return got["probe"].(map[string]any)
}

// A probe the provider answers without a limit frees this subscription's
// provider_limit rows; another subscription's rows stay untouched.
func TestSubscriptionProbeLiftsProviderLimit(t *testing.T) {
	h := subscriptionTestHandler(t)
	h.SubscriptionProbes = NewSubscriptionProbeStore()
	runtimeID, keyA, keyB := probePoolFixture(t, h, "-lift")

	probeID := startProbe(t, h, keyA, runtimeID)
	probeReport(t, h, keyA, probeID, false)

	if n := activeRefusals(t, keyA); n != 0 {
		t.Fatalf("the probed subscription's provider_limit rows are lifted, %d remain", n)
	}
	if n := activeRefusals(t, keyB); n != 1 {
		t.Fatalf("the bystander's provider_limit row must stay, got %d", n)
	}
	state := probeState(t, h, keyA, probeID)
	if state["state"] != "done" || state["lifted"] != float64(1) {
		t.Fatalf("probe state: %v", state)
	}
	if state["resets_at"] != nil {
		t.Fatalf("a done probe names no reset: %v", state)
	}
}

// A probe that hits the limit again keeps the block and answers with the new
// reset.
func TestSubscriptionProbeKeepsLimit(t *testing.T) {
	h := subscriptionTestHandler(t)
	h.SubscriptionProbes = NewSubscriptionProbeStore()
	runtimeID, keyA, _ := probePoolFixture(t, h, "-limit")

	probeID := startProbe(t, h, keyA, runtimeID)
	probeReport(t, h, keyA, probeID, true)

	if n := activeRefusals(t, keyA); n != 1 {
		t.Fatalf("the provider_limit row must stay, got %d", n)
	}
	state := probeState(t, h, keyA, probeID)
	if state["state"] != "limited" || state["resets_at"] == nil {
		t.Fatalf("probe state: %v", state)
	}
}

// The same report without a probe id changes nothing: an automatic snapshot
// never lifts a provider limit on its own.
func TestUsageReportWithoutProbeIDDoesNotLift(t *testing.T) {
	h := subscriptionTestHandler(t)
	h.SubscriptionProbes = NewSubscriptionProbeStore()
	runtimeID, keyA, _ := probePoolFixture(t, h, "-plain")

	probeID := startProbe(t, h, keyA, runtimeID)
	probeReport(t, h, keyA, "", false)

	if n := activeRefusals(t, keyA); n != 1 {
		t.Fatalf("a plain usage report must not lift the provider limit, got %d rows", n)
	}
	state := probeState(t, h, keyA, probeID)
	if state["state"] != "probing" {
		t.Fatalf("the probe is still waiting for its own answer: %v", state)
	}
}

// Probing is the recheck of unavailable runtimes: a plain member cannot ask
// for one.
func TestSubscriptionProbeRequiresWorkspaceAdmin(t *testing.T) {
	h := subscriptionTestHandler(t)
	h.SubscriptionProbes = NewSubscriptionProbeStore()
	_, keyA, _ := probePoolFixture(t, h, "-rights")

	outsider := subtestOutsider(t)
	callSubscription(t, h.StartSubscriptionProbe, withURLParam(newRequestAs(outsider, "POST", "/x", nil), "key", keyA), http.StatusForbidden)
}

// A probe the host could not complete lifts nothing and says why.
func TestSubscriptionProbeErrorKeepsBlock(t *testing.T) {
	h := subscriptionTestHandler(t)
	h.SubscriptionProbes = NewSubscriptionProbeStore()
	runtimeID, keyA, _ := probePoolFixture(t, h, "-err")

	probeID := startProbe(t, h, keyA, runtimeID)
	callSubscription(t, h.DaemonReportSubscriptionUsage, withURLParam(newDaemonTokenRequest("POST", "/x", map[string]any{
		"daemon_id": "subtest-daemon", "device_name": "host-1",
		"reports": []map[string]any{{"key": keyA, "probe_id": probeID, "credential_state": "revoked", "error": "401 unauthorized", "windows": []map[string]any{}}},
	}, testWorkspaceID, "subtest-daemon"), "workspaceId", testWorkspaceID), http.StatusOK)

	if n := activeRefusals(t, keyA); n != 1 {
		t.Fatalf("a failed probe keeps the block, got %d rows", n)
	}
	state := probeState(t, h, keyA, probeID)
	if state["state"] != "failed" || state["message"] != "401 unauthorized" {
		t.Fatalf("probe state: %v", state)
	}
}

// An older snapshot at the limit does not outlive a fresh free one: the
// availability verdict reads the newest snapshot of each window.
func TestFreshProbeSnapshotOverridesOldLimit(t *testing.T) {
	h := subscriptionTestHandler(t)
	h.SubscriptionProbes = NewSubscriptionProbeStore()
	runtimeID, keyA, _ := probePoolFixture(t, h, "-old")
	probeReport(t, h, keyA, "", true)

	probeID := startProbe(t, h, keyA, runtimeID)
	probeReport(t, h, keyA, probeID, false)

	var limited int
	testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM (
			SELECT DISTINCT ON (window_key) limit_reached FROM subscription_usage
			WHERE subscription_id = (SELECT id FROM subscription WHERE key = $1)
			ORDER BY window_key, observed_at DESC) latest WHERE limit_reached`, keyA).Scan(&limited)
	if limited != 0 || activeRefusals(t, keyA) != 0 {
		t.Fatalf("the newest snapshot must be free and the block lifted: limited windows %d, refusals %d", limited, activeRefusals(t, keyA))
	}
}
