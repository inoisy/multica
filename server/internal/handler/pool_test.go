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

func TestPoolAPIScopesGrantsAndRequiresAdminRevision(t *testing.T) {
	h := subscriptionTestHandler(t)
	otherWS := subtestWorkspace(t, "subtest-pool-private")
	member := subtestOutsider(t)
	created := callSubscription(t, h.CreatePool, newRequest("POST", "/api/pools", map[string]any{
		"name": "Subtest API", "runtime_type": "codex", "workspace_ids": []string{testWorkspaceID, otherWS},
	}), http.StatusCreated)
	id := created["id"].(string)
	read := withURLParam(newRequestAs(member, "GET", "/api/pools/"+id, nil), "id", id)
	var visible PoolResponse
	testutil.Call(t, h.GetPool, read).Want(http.StatusOK).JSON(&visible)
	if len(visible.WorkspaceIDs) != 1 || visible.WorkspaceIDs[0] != testWorkspaceID || len(visible.Environments) != 1 {
		t.Fatalf("private grants leaked: %+v", visible)
	}
	for _, source := range []string{"member", "task_token", "cloud_pat"} {
		req := newRequest("PATCH", "/api/pools/"+id, map[string]any{"name": "Subtest forbidden"})
		if source == "member" {
			req.Header.Set("X-User-ID", member)
		} else {
			req.Header.Set("X-Actor-Source", source)
		}
		testutil.Call(t, h.UpdatePool, withURLParam(req, "id", id)).Want(http.StatusForbidden)
	}
	patch := func(body any, want int) {
		t.Helper()
		testutil.Call(t, h.UpdatePool, withURLParam(newRequest("PATCH", "/api/pools/"+id, body), "id", id)).Want(want)
	}
	patch(map[string]any{"spend_order": "balanced"}, http.StatusBadRequest)
	patch(map[string]any{"spend_order": "balanced", "revision": 1}, http.StatusOK)
	patch(map[string]any{"spend_order": "first", "revision": 1}, http.StatusConflict)
	testutil.Call(t, h.SetPoolGrants, withURLParam(newRequest("PUT", "/api/pools/"+id+"/grants", map[string]any{"workspace_ids": []string{otherWS}, "revision": 2}), "id", id)).Want(http.StatusOK)
	// A revoked environment stays as disabled history, and grants no pool access.
	testutil.Call(t, h.GetPool, read).Want(http.StatusNotFound)
}

func TestConfirmTaskAccountChecksDaemonWorkspaceAndTerminalReplay(t *testing.T) {
	h := subscriptionTestHandler(t)
	_, runtimeID := poolFixture(t, h, "subtest-fact-api")
	dbfx.Exec(t, `UPDATE agent_runtime SET daemon_id='subtest-daemon' WHERE id=$1`, runtimeID)
	offer, err := service.BuildAccountOffer(context.Background(), h.Queries, parseUUID(runtimeID))
	if err != nil || offer == nil {
		t.Fatalf("offer: %+v %v", offer, err)
	}
	agent := dbfx.Agent(t, "account API", runtimeID, nil)
	issue := dbfx.Issue(t, "account API issue", nil)
	task := dbfx.Task(t, agent, testutil.Cols{"runtime_id": runtimeID, "issue_id": issue, "status": "dispatched"})
	raw, err := json.Marshal(offer)
	if err != nil {
		t.Fatal(err)
	}
	dbfx.Exec(t, `UPDATE agent_task_queue SET account_offer=$1 WHERE id=$2`, raw, task)
	body := service.AccountConfirmation{OfferID: offer.OfferID, PoolID: offer.PoolID, PoolRevision: offer.PoolRevision, SubscriptionID: offer.Subscriptions[0].SubscriptionID}
	request := func(ws, daemon string, body any) *http.Request {
		return withURLParam(newDaemonTokenRequest("POST", "/api/daemon/tasks/"+task+"/account", body, ws, daemon), "taskId", task)
	}
	testutil.Call(t, h.ConfirmTaskAccount, request(testWorkspaceID, "another-daemon", body)).Want(http.StatusNotFound)
	otherWS := subtestWorkspace(t, "subtest-fact-other")
	testutil.Call(t, h.ConfirmTaskAccount, request(otherWS, "subtest-daemon", body)).Want(http.StatusNotFound)
	bad := body
	bad.OfferID = runtimeID
	testutil.Call(t, h.ConfirmTaskAccount, request(testWorkspaceID, "subtest-daemon", bad)).Want(http.StatusConflict)
	first := testutil.Call(t, h.ConfirmTaskAccount, request(testWorkspaceID, "subtest-daemon", body)).Want(http.StatusOK)
	if strings.Contains(first.Body.String(), "credential_path") || strings.Contains(first.Body.String(), "account_offer") {
		t.Fatal("fact response leaks private offer or credential path")
	}
	dbfx.Exec(t, `UPDATE agent_task_queue SET status='completed' WHERE id=$1`, task)
	second := testutil.Call(t, h.ConfirmTaskAccount, request(testWorkspaceID, "subtest-daemon", body)).Want(http.StatusOK)
	if first.Body.String() != second.Body.String() {
		t.Fatal("replay changed the fact")
	}
	dbfx.Exec(t, `UPDATE agent_task_queue SET account_fact=NULL WHERE id=$1`, task)
	testutil.Call(t, h.ConfirmTaskAccount, request(testWorkspaceID, "subtest-daemon", body)).Want(http.StatusConflict)
}

func TestClaimPayloadKeepsRefusedAccountDisabledUntilItsRefusalEnds(t *testing.T) {
	h := subscriptionTestHandler(t)
	_, rtID := poolFixture(t, h, "subtest-refused-payload")
	rt, err := h.Queries.GetAgentRuntime(context.Background(), parseUUID(rtID))
	if err != nil {
		t.Fatal(err)
	}
	offer, err := service.BuildAccountOffer(context.Background(), h.Queries, rt.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	a := offer.Subscriptions[0].SubscriptionID
	dbfx.Exec(t, `INSERT INTO subscription_usage (subscription_id, window_key, used_percent, resets_at, source) VALUES ($1,'week',99,$2,'daemon')`, a, now.Add(3*time.Hour))
	dbfx.Exec(t, `INSERT INTO runtime_unavailable (workspace_id,runtime_id,subscription_id,until,reason,reason_code) VALUES ($1,$2,$3,$4,'refused','provider_limit')`, testWorkspaceID, rtID, a, now.Add(time.Hour))
	raw, err := json.Marshal(offer)
	if err != nil {
		t.Fatal(err)
	}
	task := &db.AgentTaskQueue{AccountOffer: raw}
	payload := h.subscriptionPoolForTask(context.Background(), rt, task)
	if payload == nil || len(payload.Subscriptions) != 1 || !payload.Subscriptions[0].Disabled {
		t.Fatalf("active refusal must disable the account even when a longer cap blocks it: %+v", payload)
	}
	if got := payload.Subscriptions[0].DisabledUntil; got == nil || !strings.HasPrefix(*got, now.Add(time.Hour).UTC().Format("2006-01-02T15:04:")) {
		t.Fatalf("disabled_until is not the refusal's end: %v", got)
	}
	// A later edit cannot change the order or revision already handed to this run.
	dbfx.Exec(t, `UPDATE subscription_pool SET spend_order='balanced',revision=revision+1 WHERE id=$1`, offer.PoolID)
	payload = h.subscriptionPoolForTask(context.Background(), rt, task)
	if payload.SpendOrder != offer.SpendOrder || payload.Revision != offer.PoolRevision {
		t.Fatal("pool edit rewrote the payload of an assignment")
	}
}

func TestDaemonFailureRejectsUnknownRefusalCode(t *testing.T) {
	h := subscriptionTestHandler(t)
	_, runtimeID := poolFixture(t, h, "subtest-fail-code")
	dbfx.Exec(t, `UPDATE agent_runtime SET daemon_id='subtest-daemon' WHERE id=$1`, runtimeID)
	agent := dbfx.Agent(t, "refusal API", runtimeID, nil)
	issue := dbfx.Issue(t, "refusal API")
	task := dbfx.Task(t, agent, testutil.Cols{"issue_id": issue, "runtime_id": runtimeID, "status": "running"})
	req := withURLParam(newDaemonTokenRequest("POST", "/api/daemon/tasks/"+task+"/fail", map[string]any{"error": "translated text", "refusal_code": "future_code"}, testWorkspaceID, "subtest-daemon"), "taskId", task)
	testutil.Call(t, h.FailTask, req).Want(http.StatusBadRequest)
	var status string
	dbfx.QueryRow(t, `SELECT status FROM agent_task_queue WHERE id=$1`, task).Scan(&status)
	if status != "running" {
		t.Fatalf("rejected request changed status to %s", status)
	}
}

func TestAccountWritesRefuseEveryMachineAndNonAdminCaller(t *testing.T) {
	h := subscriptionTestHandler(t)
	member := subtestOutsider(t)
	_, rtID := poolFixture(t, h, "subtest-denied")
	var poolID string
	if err := testPool.QueryRow(t.Context(), `SELECT pool_id FROM runtime_profile WHERE id = (SELECT profile_id FROM agent_runtime WHERE id=$1)`, rtID).Scan(&poolID); err != nil {
		t.Fatal(err)
	}
	writes := map[string]func(*http.Request) *http.Request{
		"create account": func(r *http.Request) *http.Request { return r },
		"create pool":    func(r *http.Request) *http.Request { return r },
		"set grants":     func(r *http.Request) *http.Request { return withURLParam(r, "id", poolID) },
	}
	handlers := map[string]http.HandlerFunc{"create account": h.CreateSubscription, "create pool": h.CreateRuntimePool, "set grants": h.SetPoolGrants}
	bodies := map[string]any{
		"create account": map[string]any{"key": "subtest-denied-new", "provider": "claude"},
		"create pool":    map[string]any{"display_name": "denied", "all_workspaces": true},
		"set grants":     map[string]any{"revision": 1, "workspace_ids": []string{testWorkspaceID}},
	}
	for name, wrap := range writes {
		for _, source := range []string{"member", "task_token", "cloud_pat"} {
			req := newRequest("POST", "/api/x", bodies[name])
			if source == "member" {
				req.Header.Set("X-User-ID", member)
			} else {
				req.Header.Set("X-Actor-Source", source)
			}
			testutil.Call(t, handlers[name], wrap(req)).Want(http.StatusForbidden)
		}
	}
	var n int
	_ = testPool.QueryRow(t.Context(), `SELECT count(*) FROM subscription WHERE key='subtest-denied-new'`).Scan(&n)
	if n != 0 {
		t.Fatal("a refused write created an account")
	}
}

func TestAddingAnAccountTwiceAfterPartialSuccessCreatesOne(t *testing.T) {
	h := subscriptionTestHandler(t)
	body := map[string]any{"key": "subtest-retry", "provider": "claude", "title": "Retry"}
	first := callSubscription(t, h.CreateSubscription, newRequest("POST", "/api/subscriptions", body), http.StatusCreated)
	again := callSubscription(t, h.CreateSubscription, newRequest("POST", "/api/subscriptions", body), http.StatusOK)
	if first["id"] != again["id"] {
		t.Fatalf("retry returned another account: %v %v", first["id"], again["id"])
	}
	// The same key for another kind of account is a conflict, not a reuse.
	callSubscription(t, h.CreateSubscription, newRequest("POST", "/api/subscriptions", map[string]any{"key": "subtest-retry", "provider": "codex"}), http.StatusConflict)
	// Without a key the server cannot know it is a retry and derives a new one.
	callSubscription(t, h.CreateSubscription, newRequest("POST", "/api/subscriptions", map[string]any{"provider": "claude", "title": "subtest-retry"}), http.StatusCreated)
	var n int
	_ = testPool.QueryRow(t.Context(), `SELECT count(*) FROM subscription WHERE key='subtest-retry'`).Scan(&n)
	if n != 1 {
		t.Fatalf("accounts with the key: %d", n)
	}
}
