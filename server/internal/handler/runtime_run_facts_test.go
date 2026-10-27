package handler

import (
	"net/http"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func runFacts(t *testing.T, rtID string) RuntimeRunFactsResponse {
	t.Helper()
	var out RuntimeRunFactsResponse
	req := withURLParam(newRequest("GET", "/api/runtimes/"+rtID+"/run-facts", nil), "runtimeId", rtID)
	testutil.Call(t, testHandler.GetRuntimeRunFacts, req).Want(http.StatusOK).JSON(&out)
	return out
}

func TestRuntimeRunFactsCountTaskRecordsInFixedWindow(t *testing.T) {
	h := subscriptionTestHandler(t)
	profileID, rtID := poolFixture(t, h, "subtest-facts")
	if got := runFacts(t, rtID); got.SelectionState != "unused" || got.RunFacts24h.Runs != 0 {
		t.Fatalf("nothing leads here: %+v", got)
	}

	agent := dbfx.Agent(t, "facts agent", rtID, nil)
	if got := runFacts(t, rtID); got.SelectionState != "reserve" {
		t.Fatalf("an agent leads here, no runs: %+v", got)
	}

	var accountID string
	if err := testPool.QueryRow(t.Context(), `SELECT id FROM subscription WHERE key='subtest-facts'`).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	fact := `{"subscription_id":"` + accountID + `","subscription_key":"subtest-facts"}`
	now := time.Now()
	issue := dbfx.Issue(t, "facts issue", nil)
	started := func(ago time.Duration, account string) {
		cols := testutil.Cols{"runtime_id": rtID, "issue_id": issue, "status": "completed", "started_at": now.Add(-ago), "dispatched_at": now.Add(-ago)}
		id := dbfx.Task(t, agent, cols)
		if account != "" {
			dbfx.Exec(t, `UPDATE agent_task_queue SET account_fact=$1::jsonb WHERE id=$2`, account, id)
		}
	}
	started(time.Hour, fact)
	started(2*time.Hour, fact)
	started(3*time.Hour, "")
	started(25*time.Hour, fact) // outside the window
	dbfx.Task(t, agent, testutil.Cols{"runtime_id": rtID, "issue_id": issue, "status": "queued"})
	// Several quota measurements of the same account are not runs.
	for i := 0; i < 3; i++ {
		dbfx.Exec(t, `INSERT INTO subscription_usage (subscription_id, window_key, used_percent, source, observed_at) VALUES ($1,$2,50,'daemon',now())
			ON CONFLICT DO NOTHING`, accountID, []string{"5h", "week", "month"}[i])
	}

	got := runFacts(t, rtID)
	if got.SelectionState != "active" || got.RunFacts24h.Runs != 3 || got.RunFacts24h.UnknownAccountRuns != 1 || got.RunFacts24h.Accounts[accountID] != 2 {
		t.Fatalf("window facts: %+v", got)
	}
	if got.MachineStatus == "" {
		t.Fatal("the machine state is its own field")
	}

	dbfx.Exec(t, `UPDATE runtime_profile SET enabled=false WHERE id=$1`, profileID)
	if got := runFacts(t, rtID); got.SelectionState != "closed" || got.RunFacts24h.Runs != 3 {
		t.Fatalf("forbidden selection: %+v", got)
	}
}

func TestRuntimeRunFactsHideOtherWorkspaceRuntime(t *testing.T) {
	h := subscriptionTestHandler(t)
	_, rtID := poolFixture(t, h, "subtest-facts-private")
	outsider := subtestOutsider(t)
	req := withURLParam(newRequestAs(outsider, "GET", "/api/runtimes/"+rtID+"/run-facts", nil), "runtimeId", rtID)
	testutil.Call(t, testHandler.GetRuntimeRunFacts, req).Want(http.StatusNotFound)
}
