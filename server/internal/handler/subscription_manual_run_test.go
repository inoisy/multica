package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/service"
)

func manualRunPath(key string, tail ...string) string {
	path := "/api/subscriptions/" + key + "/manual-runs"
	for _, t := range tail {
		path += "/" + t
	}
	return path
}

func openManualRun(t *testing.T, h *Handler, key string, body map[string]any, want int) map[string]any {
	t.Helper()
	return callSubscription(t, h.OpenSubscriptionManualRun, withURLParam(newRequest("POST", manualRunPath(key), body), "key", key), want)
}

func finishManualRun(t *testing.T, h *Handler, key, id string, body map[string]any, want int) map[string]any {
	t.Helper()
	req := withURLParams(newRequest("POST", manualRunPath(key, id, "finish"), body), "key", key, "runId", id)
	return callSubscription(t, h.FinishSubscriptionManualRun, req, want)
}

func listManualRuns(t *testing.T, h *Handler, key, query string, want int) map[string]any {
	t.Helper()
	return callSubscription(t, h.ListSubscriptionManualRuns, withURLParam(newRequest("GET", manualRunPath(key)+query, nil), "key", key), want)
}

func TestManualRunJournalOpensClosesAndLists(t *testing.T) {
	h := subscriptionTestHandler(t)
	createSubtestSubscription(t, h, map[string]any{"key": "subtest-manual", "provider": "claude"})

	opened := openManualRun(t, h, "subtest-manual", map[string]any{"daemon_id": "mac-1", "command": "claude"}, http.StatusCreated)
	id := opened["id"].(string)
	if opened["unfinished"] != true || opened["finished_at"] != nil || opened["exit_code"] != nil || opened["agent_cap_exceeded"] != false {
		t.Fatalf("a fresh record is unfinished: %v", opened)
	}
	// Nothing the admin typed after the command, no path and no token is kept.
	for _, key := range []string{"args", "arguments", "credential_path", "token", "env"} {
		if _, ok := opened[key]; ok {
			t.Fatalf("record carries %q: %v", key, opened)
		}
	}

	closed := finishManualRun(t, h, "subtest-manual", id, map[string]any{"exit_code": 3}, http.StatusOK)
	if closed["unfinished"] != false || closed["exit_code"] != float64(3) || closed["finished_at"] == nil {
		t.Fatalf("closed record: %v", closed)
	}
	// A retry after a lost answer returns the first close, not a rewrite.
	again := finishManualRun(t, h, "subtest-manual", id, map[string]any{"exit_code": 0}, http.StatusOK)
	if again["exit_code"] != float64(3) || again["finished_at"] != closed["finished_at"] {
		t.Fatalf("a repeated close rewrote the record: %v", again)
	}

	// A command cut off without a close stays open and is shown as unfinished.
	cut := openManualRun(t, h, "subtest-manual", map[string]any{"daemon_id": "mac-1", "command": "codex"}, http.StatusCreated)
	runs := listManualRuns(t, h, "subtest-manual", "", http.StatusOK)["runs"].([]any)
	if len(runs) != 2 || runs[0].(map[string]any)["id"] != cut["id"] {
		t.Fatalf("newest first: %v", runs)
	}
	if first := runs[0].(map[string]any); first["unfinished"] != true || first["finished_at"] != nil || first["user_name"] == "" {
		t.Fatalf("unfinished record: %v", first)
	}

	page := listManualRuns(t, h, "subtest-manual", "?limit=1", http.StatusOK)
	if len(page["runs"].([]any)) != 1 || page["next_before"] == nil {
		t.Fatalf("paging: %v", page)
	}
	rest := listManualRuns(t, h, "subtest-manual", "?limit=1&before="+page["next_before"].(string), http.StatusOK)
	if len(rest["runs"].([]any)) != 1 || rest["runs"].([]any)[0].(map[string]any)["id"] != id {
		t.Fatalf("second page: %v", rest)
	}
	listManualRuns(t, h, "subtest-manual", "?limit=0", http.StatusBadRequest)
	listManualRuns(t, h, "subtest-manual", "?before=yesterday", http.StatusBadRequest)
}

func TestManualRunJournalRefusesArgumentsAndBadInput(t *testing.T) {
	h := subscriptionTestHandler(t)
	createSubtestSubscription(t, h, map[string]any{"key": "subtest-manual-bad", "provider": "claude"})
	for name, body := range map[string]map[string]any{
		"arguments in the command": {"daemon_id": "mac-1", "command": "claude --resume abc"},
		"another command":          {"daemon_id": "mac-1", "command": "bash"},
		"unknown field":            {"daemon_id": "mac-1", "command": "claude", "args": []string{"-p", "secret"}},
		"credential path":          {"daemon_id": "mac-1", "command": "claude", "credential_path": "~/.config/x/token"},
		"no machine":               {"command": "claude"},
		"machine with a path":      {"daemon_id": "/Users/x/.config", "command": "claude"},
	} {
		t.Run(name, func(t *testing.T) {
			openManualRun(t, h, "subtest-manual-bad", body, http.StatusBadRequest)
		})
	}
	openManualRun(t, h, "subtest-missing", map[string]any{"daemon_id": "mac-1", "command": "claude"}, http.StatusNotFound)
	var n int
	_ = testPool.QueryRow(t.Context(), `SELECT count(*) FROM subscription_manual_run WHERE subscription_id = (SELECT id FROM subscription WHERE key='subtest-manual-bad')`).Scan(&n)
	if n != 0 {
		t.Fatalf("refused requests left %d records", n)
	}
	opened := openManualRun(t, h, "subtest-manual-bad", map[string]any{"daemon_id": "mac-1", "command": "codex"}, http.StatusCreated)
	finishManualRun(t, h, "subtest-manual-bad", opened["id"].(string), map[string]any{}, http.StatusBadRequest)
	finishManualRun(t, h, "subtest-manual-bad", opened["id"].(string), map[string]any{"exit_code": 999}, http.StatusBadRequest)
	finishManualRun(t, h, "subtest-manual-bad", "5f6a1f0c-0000-4000-8000-000000000000", map[string]any{"exit_code": 0}, http.StatusNotFound)
	finishManualRun(t, h, "subtest-manual-bad", "not-a-uuid", map[string]any{"exit_code": 0}, http.StatusNotFound)
}

func TestManualRunJournalIsAdminInPersonOnly(t *testing.T) {
	h := subscriptionTestHandler(t)
	createSubtestSubscription(t, h, map[string]any{"key": "subtest-manual-auth", "provider": "claude"})
	opened := openManualRun(t, h, "subtest-manual-auth", map[string]any{"daemon_id": "mac-1", "command": "claude"}, http.StatusCreated)
	id := opened["id"].(string)
	member := subtestOutsider(t)

	for _, source := range []string{"member", "task_token", "cloud_pat"} {
		set := func(req *http.Request) *http.Request {
			if source == "member" {
				req.Header.Set("X-User-ID", member)
			} else {
				req.Header.Set("X-Actor-Source", source)
			}
			return req
		}
		open := set(withURLParam(newRequest("POST", manualRunPath("subtest-manual-auth"), map[string]any{"daemon_id": "mac-1", "command": "claude"}), "key", "subtest-manual-auth"))
		finish := set(withURLParams(newRequest("POST", manualRunPath("subtest-manual-auth", id, "finish"), map[string]any{"exit_code": 0}), "key", "subtest-manual-auth", "runId", id))
		list := set(withURLParam(newRequest("GET", manualRunPath("subtest-manual-auth"), nil), "key", "subtest-manual-auth"))
		for name, call := range map[string]struct {
			fn  http.HandlerFunc
			req *http.Request
		}{"open": {h.OpenSubscriptionManualRun, open}, "finish": {h.FinishSubscriptionManualRun, finish}, "list": {h.ListSubscriptionManualRuns, list}} {
			w := httptest.NewRecorder()
			call.fn(w, call.req)
			if w.Code != http.StatusForbidden {
				t.Errorf("%s as %s: status %d, want 403", name, source, w.Code)
			}
		}
	}

	// Another admin may read, but a record is closed by the admin who opened it.
	other := *h
	other.cfg.InstanceAdmins = append(append([]string(nil), h.cfg.InstanceAdmins...), "subtest-member@multica.ai")
	req := withURLParams(newRequestAs(member, "POST", manualRunPath("subtest-manual-auth", id, "finish"), map[string]any{"exit_code": 0}), "key", "subtest-manual-auth", "runId", id)
	callSubscription(t, other.FinishSubscriptionManualRun, req, http.StatusForbidden)
	if got := listManualRuns(t, h, "subtest-manual-auth", "", http.StatusOK)["runs"].([]any)[0].(map[string]any); got["unfinished"] != true {
		t.Fatalf("a refused close changed the record: %v", got)
	}
}

func TestManualRunJournalNotesAccountOverAgentLimit(t *testing.T) {
	h := subscriptionTestHandler(t)
	createSubtestSubscription(t, h, map[string]any{"key": "subtest-manual-cap", "provider": "claude", "caps": map[string]int{"week": 90}})
	opened := openManualRun(t, h, "subtest-manual-cap", map[string]any{"daemon_id": "mac-1", "command": "claude"}, http.StatusCreated)
	if opened["agent_cap_exceeded"] != false {
		t.Fatalf("no measurement, no excess: %v", opened)
	}
	dbfx.Exec(t, `INSERT INTO subscription_usage (subscription_id, window_key, used_percent, source, observed_at)
		SELECT id, 'week', 95, 'daemon', now() FROM subscription WHERE key='subtest-manual-cap'`)
	// Over the agents' limit is a note on the record, never a refusal.
	opened = openManualRun(t, h, "subtest-manual-cap", map[string]any{"daemon_id": "mac-1", "command": "claude"}, http.StatusCreated)
	if opened["agent_cap_exceeded"] != true {
		t.Fatalf("over the agents' limit at the start: %v", opened)
	}
}

func TestManualRunJournalNeverFeedsRunFactsAndGoesWithTheAccount(t *testing.T) {
	h := subscriptionTestHandler(t)
	_, rtID := poolFixture(t, h, "subtest-manual-facts")
	openManualRun(t, h, "subtest-manual-facts", map[string]any{"daemon_id": "mac-1", "command": "claude"}, http.StatusCreated)
	if got := runFacts(t, rtID); got.RunFacts24h.Runs != 0 || got.SelectionState == "active" {
		t.Fatalf("a manual run is not an agent run: %+v", got)
	}
	var tasks int
	_ = testPool.QueryRow(t.Context(), `SELECT count(*) FROM agent_task_queue WHERE runtime_id = $1`, rtID).Scan(&tasks)
	if tasks != 0 {
		t.Fatalf("the journal wrote %d task records", tasks)
	}

	createSubtestSubscription(t, h, map[string]any{"key": "subtest-manual-gone", "provider": "claude"})
	openManualRun(t, h, "subtest-manual-gone", map[string]any{"daemon_id": "mac-1", "command": "codex"}, http.StatusCreated)
	callSubscription(t, h.DeleteSubscription, withURLParam(newRequest("DELETE", "/api/subscriptions/subtest-manual-gone", nil), "key", "subtest-manual-gone"), http.StatusOK)
	var left int
	_ = testPool.QueryRow(t.Context(), `SELECT count(*) FROM subscription_manual_run WHERE daemon_id = 'mac-1' AND command = 'codex' AND subscription_id NOT IN (SELECT id FROM subscription)`).Scan(&left)
	if left != 0 {
		t.Fatalf("%d journal rows outlived their account", left)
	}
}

func TestManualRunJournalIsPrunedAfterNinetyDays(t *testing.T) {
	h := subscriptionTestHandler(t)
	createSubtestSubscription(t, h, map[string]any{"key": "subtest-manual-old", "provider": "claude"})
	now := time.Now()
	insert := func(age time.Duration, finished bool) {
		dbfx.Exec(t, `INSERT INTO subscription_manual_run (subscription_id, user_id, daemon_id, command, started_at, finished_at, exit_code)
			SELECT id, $1::uuid, 'mac-1', 'claude', $2::timestamptz, CASE WHEN $3 THEN $2::timestamptz ELSE NULL END, CASE WHEN $3 THEN 0 ELSE NULL END
			FROM subscription WHERE key='subtest-manual-old'`, testUserID, now.Add(-age), finished)
	}
	insert(service.ManualRunRetention+time.Hour, true)
	insert(service.ManualRunRetention+time.Hour, false) // cut off long ago, still expires
	insert(service.ManualRunRetention-time.Hour, true)
	insert(time.Hour, false)

	removed, err := service.PruneManualRuns(t.Context(), h.Queries, now)
	if err != nil || removed < 2 {
		t.Fatalf("pruned %d rows: %v", removed, err)
	}
	var left int
	_ = testPool.QueryRow(t.Context(), `SELECT count(*) FROM subscription_manual_run WHERE subscription_id = (SELECT id FROM subscription WHERE key='subtest-manual-old')`).Scan(&left)
	if left != 2 {
		t.Fatalf("rows kept after the cleanup: %d, want the two inside 90 days", left)
	}
}
