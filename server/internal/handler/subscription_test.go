package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// subscriptionTestHandler returns a handler whose only instance admin is the
// handler test user.
func subscriptionTestHandler(t *testing.T) *Handler {
	t.Helper()
	if testHandler == nil {
		t.Skip("database not available")
	}
	h := *testHandler
	h.cfg.InstanceAdmins = []string{" HANDLER-TEST@multica.ai "}
	cleanSubtestPools()
	t.Cleanup(cleanSubtestPools)
	return &h
}

// cleanSubtestPools removes what the subscription tests create: their
// accounts, usage, pools with their members and grants, and environments.
func cleanSubtestPools() {
	ctx := context.Background()
	testPool.Exec(ctx, `DELETE FROM subscription_manual_run WHERE subscription_id IN (SELECT id FROM subscription WHERE key LIKE 'subtest-%')`)
	testPool.Exec(ctx, `DELETE FROM runtime_unavailable WHERE subscription_id IN (SELECT id FROM subscription WHERE key LIKE 'subtest-%')`)
	testPool.Exec(ctx, `DELETE FROM subscription_usage WHERE subscription_id IN (SELECT id FROM subscription WHERE key LIKE 'subtest-%')`)
	testPool.Exec(ctx, `DELETE FROM subscription_host_state WHERE subscription_id IN (SELECT id FROM subscription WHERE key LIKE 'subtest-%')`)
	testPool.Exec(ctx, `DELETE FROM runtime_profile WHERE display_name LIKE 'Subtest %' OR pool_id IN (SELECT id FROM subscription_pool WHERE name LIKE 'Subtest %')`)
	testPool.Exec(ctx, `DELETE FROM subscription_pool_member WHERE pool_id IN (SELECT id FROM subscription_pool WHERE name LIKE 'Subtest %')`)
	testPool.Exec(ctx, `DELETE FROM subscription_pool_grant WHERE pool_id IN (SELECT id FROM subscription_pool WHERE name LIKE 'Subtest %')`)
	testPool.Exec(ctx, `DELETE FROM subscription_pool WHERE name LIKE 'Subtest %'`)
	testPool.Exec(ctx, `DELETE FROM subscription WHERE key LIKE 'subtest-%'`)
}

func callSubscription(t *testing.T, fn http.HandlerFunc, req *http.Request, want int) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	fn(w, req)
	if w.Code != want {
		t.Fatalf("%s %s: status %d, want %d: %s", req.Method, req.URL.Path, w.Code, want, w.Body.String())
	}
	out := map[string]any{}
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &out)
	}
	return out
}

// subtestWorkspace adds a second workspace the test user is a member of.
func subtestWorkspace(t *testing.T, slug string) string {
	t.Helper()
	ctx := context.Background()
	var wsID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, '', 'SUB') RETURNING id`, "Subtest "+slug, slug).Scan(&wsID); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`, wsID, testUserID); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		testPool.Exec(ctx, `DELETE FROM runtime_profile WHERE workspace_id = $1`, wsID)
		testPool.Exec(ctx, `DELETE FROM member WHERE workspace_id = $1`, wsID)
		testPool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, wsID)
	})
	return wsID
}

// subtestOutsider is a user who is a plain member of the test workspace and
// not an instance admin.
func subtestOutsider(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	var userID string
	if err := testPool.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ('Subtest Member', 'subtest-member@multica.ai') RETURNING id`).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'member')`, testWorkspaceID, userID); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		testPool.Exec(ctx, `DELETE FROM member WHERE user_id = $1`, userID)
		testPool.Exec(ctx, `DELETE FROM "user" WHERE id = $1`, userID)
	})
	return userID
}

func createSubtestSubscription(t *testing.T, h *Handler, body map[string]any) map[string]any {
	t.Helper()
	return callSubscription(t, h.CreateSubscription, newRequest("POST", "/api/subscriptions", body), http.StatusCreated)
}

func TestSubscriptionWritesNeedInstanceAdmin(t *testing.T) {
	h := subscriptionTestHandler(t)
	outsider := subtestOutsider(t)
	body := map[string]any{"key": "subtest-claude", "provider": "claude", "title": "Claude, test"}

	// A plain member reads but cannot write.
	callSubscription(t, h.CreateSubscription, newRequestAs(outsider, "POST", "/api/subscriptions", body), http.StatusForbidden)

	// No admins configured: nobody can write, not even the workspace owner.
	noAdmins := *h
	noAdmins.cfg.InstanceAdmins = nil
	callSubscription(t, noAdmins.CreateSubscription, newRequest("POST", "/api/subscriptions", body), http.StatusForbidden)

	// An agent running on the admin's account cannot write either.
	agentReq := newRequest("POST", "/api/subscriptions", body)
	agentReq.Header.Set("X-Actor-Source", "task_token")
	callSubscription(t, h.CreateSubscription, agentReq, http.StatusForbidden)

	created := createSubtestSubscription(t, h, body)
	if created["credential_path"] != "~/.config/subtest-claude/token" || created["auth"] != "claude_oauth" || created["family"] != "claude" {
		t.Fatalf("defaults: %v", created)
	}

	list := callSubscription(t, h.ListSubscriptions, newRequestAs(outsider, "GET", "/api/subscriptions", nil), http.StatusOK)
	if list["can_manage"] != false {
		t.Fatalf("member can_manage = %v", list["can_manage"])
	}
	found := false
	for _, s := range list["subscriptions"].([]any) {
		if s.(map[string]any)["key"] == "subtest-claude" {
			found = true
		}
	}
	if !found {
		t.Fatalf("member does not see the subscription: %v", list)
	}

	upd := withURLParam(newRequestAs(outsider, "PATCH", "/api/subscriptions/subtest-claude", map[string]any{"reserve": 10}), "key", "subtest-claude")
	callSubscription(t, h.UpdateSubscription, upd, http.StatusForbidden)
	del := withURLParam(newRequestAs(outsider, "DELETE", "/api/subscriptions/subtest-claude", nil), "key", "subtest-claude")
	callSubscription(t, h.DeleteSubscription, del, http.StatusForbidden)
}

func TestSubscriptionRefusesSecrets(t *testing.T) {
	h := subscriptionTestHandler(t)
	cases := []map[string]any{
		{"key": "subtest-a", "provider": "claude", "token": "sk-ant-oat01-x"},
		{"key": "subtest-b", "provider": "zai", "api_key": "abc"},
		{"key": "subtest-c", "provider": "zai", "settings": map[string]any{"env": map[string]any{"ANTHROPIC_AUTH_TOKEN": "x"}}},
		{"key": "subtest-d", "provider": "zai", "settings": map[string]any{"base_url": "sk-live-123"}},
		{"key": "subtest-e", "provider": "claude", "credential_path": "sk-ant-oat01-abc"},
		{"key": "subtest-f", "provider": "claude", "credential_path": "relative/token"},
		{"key": "subtest-g", "provider": "claude", "auth": "api_key"},
	}
	for _, body := range cases {
		callSubscription(t, h.CreateSubscription, newRequest("POST", "/api/subscriptions", body), http.StatusBadRequest)
	}
	ok := createSubtestSubscription(t, h, map[string]any{
		"key": "subtest-zai", "provider": "zai",
		"settings": map[string]any{"env": map[string]any{"ANTHROPIC_BASE_URL": "https://api.z.ai/api/anthropic", "ANTHROPIC_MODEL": "glm"}},
	})
	if ok["auth"] != "api_key" || ok["credential_path"] != "~/.config/subtest-zai/key" {
		t.Fatalf("api key defaults: %v", ok)
	}
	or := createSubtestSubscription(t, h, map[string]any{"key": "subtest-or", "provider": "openrouter"})
	env := or["settings"].(map[string]any)["env"].(map[string]any)
	if env["ANTHROPIC_BASE_URL"] != "https://openrouter.ai/api" || len(or["windows"].([]any)) != 0 {
		t.Fatalf("provider defaults: %v", or)
	}
}

func TestSubscriptionUpdateMergesCapsAndUsage(t *testing.T) {
	h := subscriptionTestHandler(t)
	createSubtestSubscription(t, h, map[string]any{"key": "subtest-codex", "provider": "codex", "caps": map[string]int{"5h": 85, "week": 95}, "reserve": 10})

	upd := withURLParam(newRequest("PATCH", "/api/subscriptions/subtest-codex", map[string]any{
		"caps": map[string]int{"5h": 70}, "disabled_until": "2099-10-21T12:00Z",
	}), "key", "subtest-codex")
	got := callSubscription(t, h.UpdateSubscription, upd, http.StatusOK)
	caps := got["caps"].(map[string]any)
	// The reserve sent at creation was folded into the caps (week: min(95, 100-10)).
	if caps["5h"] != float64(70) || caps["week"] != float64(90) || got["reserve"] != float64(0) {
		t.Fatalf("caps not merged: %v reserve %v", caps, got["reserve"])
	}
	if eff := got["effective_caps"].(map[string]any); eff["5h"] != float64(70) || eff["week"] != float64(90) {
		t.Fatalf("effective caps = %v, want 5h 70 and week 90", eff)
	}
	if got["disabled"] != true {
		t.Fatalf("disabled_until not applied: %v", got)
	}
	immutable := withURLParam(newRequest("PATCH", "/api/subscriptions/subtest-codex", map[string]any{"provider": "claude"}), "key", "subtest-codex")
	callSubscription(t, h.UpdateSubscription, immutable, http.StatusBadRequest)

	rec := withURLParam(newRequest("POST", "/api/subscriptions/subtest-codex/usage", map[string]any{
		"snapshots": []map[string]any{
			{"window": "5h", "used_percent": 12.5, "source": "test", "observed_at": time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)},
			{"window": "5h", "used_percent": 40, "source": "test"},
			{"window": "week", "used_percent": 60, "limit_reached": true, "source": "test"},
		},
	}), "key", "subtest-codex")
	callSubscription(t, h.RecordSubscriptionUsage, rec, http.StatusCreated)

	one := callSubscription(t, h.GetSubscription, withURLParam(newRequest("GET", "/api/subscriptions/subtest-codex", nil), "key", "subtest-codex"), http.StatusOK)
	latest := map[string]float64{}
	for _, u := range one["usage"].([]any) {
		m := u.(map[string]any)
		latest[m["window"].(string)] = m["used_percent"].(float64)
	}
	if latest["5h"] != 40 || latest["week"] != 60 {
		t.Fatalf("latest usage = %v", latest)
	}
	history := callSubscription(t, h.ListSubscriptionUsage,
		withURLParam(newRequest("GET", "/api/subscriptions/subtest-codex/usage?since=1h", nil), "key", "subtest-codex"), http.StatusOK)
	if n := len(history["usage"].([]any)); n != 2 {
		t.Fatalf("usage since 1h: %d rows, want 2", n)
	}
}

func TestRuntimePoolSharedAcrossWorkspaces(t *testing.T) {
	h := subscriptionTestHandler(t)
	otherWS := subtestWorkspace(t, "subtest-pool-ws")
	createSubtestSubscription(t, h, map[string]any{"key": "subtest-c1", "provider": "claude"})
	createSubtestSubscription(t, h, map[string]any{"key": "subtest-c2", "provider": "claude"})
	createSubtestSubscription(t, h, map[string]any{"key": "subtest-x1", "provider": "codex"})

	// Mixing families is refused.
	callSubscription(t, h.CreateRuntimePool, newRequest("POST", "/api/runtime-pools", map[string]any{
		"display_name": "Subtest Claude pool", "runtime_type": "claude",
		"subscriptions": []string{"subtest-c1", "subtest-x1"}, "workspace_ids": []string{testWorkspaceID},
	}), http.StatusBadRequest)

	created := callSubscription(t, h.CreateRuntimePool, newRequest("POST", "/api/runtime-pools", map[string]any{
		"display_name": "Subtest Claude pool", "runtime_type": "claude", "spend_order": "balanced",
		"subscriptions": []string{"subtest-c1", "subtest-c2"}, "workspace_ids": []string{testWorkspaceID, otherWS},
	}), http.StatusCreated)
	profiles := created["runtime_profiles"].([]any)
	if len(profiles) != 2 {
		t.Fatalf("want a copy in each workspace: %v", created)
	}
	first := profiles[0].(map[string]any)
	if first["command_name"] != "claude" || len(first["fixed_args"].([]any)) != 0 || first["share_key"] != created["share_key"] {
		t.Fatalf("pool profile: %v", first)
	}
	var mine, theirs string
	for _, p := range profiles {
		m := p.(map[string]any)
		if m["workspace_id"] == testWorkspaceID {
			mine = m["id"].(string)
		} else {
			theirs = m["id"].(string)
		}
	}

	// subs-run reads the pool with the run's task token.
	read := withURLParams(newRequest("GET", "/x", nil), "id", otherWS, "profileId", theirs)
	read.Header.Set("X-Actor-Source", "task_token")
	pool := callSubscription(t, h.GetRuntimeProfileSubscriptions, read, http.StatusOK)
	if pool["spend_order"] != "balanced" || len(pool["subscriptions"].([]any)) != 2 ||
		pool["subscriptions"].([]any)[0].(map[string]any)["key"] != "subtest-c1" {
		t.Fatalf("pool read: %v", pool)
	}

	// Reordering in one workspace reorders every copy; a member may not.
	outsider := subtestOutsider(t)
	set := map[string]any{"subscriptions": []string{"subtest-c2", "subtest-c1"}, "spend_order": "first"}
	callSubscription(t, h.SetRuntimeProfileSubscriptions,
		withURLParams(newRequestAs(outsider, "PUT", "/x", set), "id", testWorkspaceID, "profileId", mine), http.StatusForbidden)
	callSubscription(t, h.SetRuntimeProfileSubscriptions,
		withURLParams(newRequest("PUT", "/x", map[string]any{"subscriptions": []string{"subtest-x1"}}), "id", testWorkspaceID, "profileId", mine), http.StatusBadRequest)
	callSubscription(t, h.SetRuntimeProfileSubscriptions,
		withURLParams(newRequest("PUT", "/x", set), "id", testWorkspaceID, "profileId", mine), http.StatusOK)
	other := callSubscription(t, h.GetRuntimeProfileSubscriptions, withURLParams(newRequest("GET", "/x", nil), "id", otherWS, "profileId", theirs), http.StatusOK)
	if other["spend_order"] != "first" || other["subscriptions"].([]any)[0].(map[string]any)["key"] != "subtest-c2" {
		t.Fatalf("copy not updated: %v", other)
	}

	// Renaming one copy renames every copy.
	rename := map[string]any{"display_name": "Subtest Claude · подписки"}
	callSubscription(t, h.UpdateRuntimeProfile,
		withURLParams(newRequest("PATCH", "/x", rename), "id", testWorkspaceID, "profileId", mine), http.StatusOK)
	var name string
	testPool.QueryRow(context.Background(), `SELECT display_name FROM runtime_profile WHERE id = $1`, theirs).Scan(&name)
	if name != "Subtest Claude · подписки" {
		t.Fatalf("copy name = %q", name)
	}

	// Deleting a subscription detaches it from every runtime first.
	callSubscription(t, h.DeleteSubscription,
		withURLParam(newRequest("DELETE", "/api/subscriptions/subtest-c2", nil), "key", "subtest-c2"), http.StatusOK)
	var subs string
	testPool.QueryRow(context.Background(), `SELECT coalesce(string_agg(s.key || '@' || m.position, ',' ORDER BY m.position), '') FROM runtime_profile rp
		JOIN subscription_pool_member m ON m.pool_id = rp.pool_id JOIN subscription s ON s.id = m.subscription_id WHERE rp.id = $1`, theirs).Scan(&subs)
	if subs != "subtest-c1@1" {
		t.Fatalf("pool members after delete = %q, want the one left with the gap closed", subs)
	}
}

func TestSharedRuntimeEditNeedsInstanceAdmin(t *testing.T) {
	h := subscriptionTestHandler(t)
	created := callSubscription(t, h.CreateRuntimePool, newRequest("POST", "/api/runtime-pools", map[string]any{
		"display_name": "Subtest guarded pool", "runtime_type": "codex", "workspace_ids": []string{testWorkspaceID},
	}), http.StatusCreated)
	id := created["runtime_profiles"].([]any)[0].(map[string]any)["id"].(string)

	// The workspace owner who is not an instance admin edits plain profiles
	// but not shared ones.
	noAdmins := *h
	noAdmins.cfg.InstanceAdmins = nil
	callSubscription(t, noAdmins.UpdateRuntimeProfile,
		withURLParams(newRequest("PATCH", "/x", map[string]any{"display_name": "Subtest renamed"}), "id", testWorkspaceID, "profileId", id), http.StatusForbidden)
}

func TestAllWorkspacesRuntimeReachesNewWorkspace(t *testing.T) {
	h := subscriptionTestHandler(t)
	created := callSubscription(t, h.CreateRuntimePool, newRequest("POST", "/api/runtime-pools", map[string]any{
		"display_name": "Subtest everywhere", "runtime_type": "claude", "all_workspaces": true,
	}), http.StatusCreated)
	_ = created["share_key"].(string)

	newWS := subtestWorkspace(t, "subtest-new-ws")
	// The new workspace was inserted after the pool, so it has no copy yet;
	// CreateWorkspace runs this inside its transaction.
	if err := copyAllWorkspaceRuntimeProfiles(context.Background(), testHandler.Queries, parseUUID(newWS), parseUUID(testUserID)); err != nil {
		t.Fatalf("copy: %v", err)
	}
	poolID := created["pool_id"].(string)
	var n int
	testPool.QueryRow(context.Background(), `SELECT count(*) FROM runtime_profile WHERE workspace_id = $1 AND pool_id = $2`, newWS, poolID).Scan(&n)
	if n != 1 {
		t.Fatalf("new workspace copies = %d, want 1", n)
	}
	// Copying again is a no-op.
	if err := copyAllWorkspaceRuntimeProfiles(context.Background(), testHandler.Queries, parseUUID(newWS), parseUUID(testUserID)); err != nil {
		t.Fatalf("second copy: %v", err)
	}
	testPool.QueryRow(context.Background(), `SELECT count(*) FROM runtime_profile WHERE workspace_id = $1 AND pool_id = $2`, newWS, poolID).Scan(&n)
	if n != 1 {
		t.Fatalf("copies after second run = %d, want 1", n)
	}
}

func TestIsSecretKeyName(t *testing.T) {
	for _, name := range []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "api_key", "apikey", "client-secret", "password", "OPENAI_ACCESS_KEY"} {
		if !isSecretKeyName(name) {
			t.Errorf("%s not flagged", name)
		}
	}
	for _, name := range []string{"CLAUDE_CODE_MAX_CONTEXT_TOKENS", "ANTHROPIC_BASE_URL", "quota_row", "cli", "env", "API_TIMEOUT_MS", "key_hint"} {
		if isSecretKeyName(name) {
			t.Errorf("%s flagged", name)
		}
	}
}

func TestDeriveSubscriptionKey(t *testing.T) {
	taken := map[string]bool{"claude-1": true, "claude-2": true}
	if got := deriveSubscriptionKey("Claude, третья", "claude", taken); got != "claude" {
		t.Errorf("latin part: got %q", got)
	}
	if got := deriveSubscriptionKey("Третья", "claude", taken); got != "claude-3" {
		t.Errorf("no latin: got %q", got)
	}
	if got := deriveSubscriptionKey("Claude 2", "claude", taken); got != "claude-3" {
		t.Errorf("taken: got %q", got)
	}
}
