package handler

import (
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// An epic's sub-issue list shows where each routed child stands.
func TestChildIssuesCarryTheirRouteStep(t *testing.T) {
	epic := dbfx.Issue(t, "routed epic")
	routed := dbfx.Issue(t, "routed child", testutil.Cols{"parent_issue_id": epic, "stage": 1})
	plain := dbfx.Issue(t, "plain child", testutil.Cols{"parent_issue_id": epic, "stage": 1})
	lead := dbfx.Agent(t, "children route lead", testRuntimeID)
	dbfx.Insert(t, "issue_conveyor", testutil.Cols{
		"workspace_id": testWorkspaceID, "issue_id": routed, "status": "waiting_provider", "cur_idx": 1, "round": 2,
		"route":                testutil.Raw(`'[{"idx":0,"step":"dev","agent_id":"` + lead + `"},{"idx":1,"step":"qa","agent_id":"` + lead + `"},{"idx":2,"step":"close","agent_id":"` + lead + `"}]'::jsonb`),
		"coordinator_agent_id": lead, "enabled_by": testUserID, "waiting_reason": "provider limit until 15:53",
	})
	var out struct {
		Issues []IssueResponse `json:"issues"`
	}
	testutil.Call(t, testHandler.ListChildIssues, withURLParam(newRequest(http.MethodGet, "/", nil), "id", epic)).Want(http.StatusOK).JSON(&out)
	got := map[string]*IssueRouteResponse{}
	for _, i := range out.Issues {
		got[i.ID] = i.Route
	}
	if r := got[routed]; r == nil || r.Step != "qa" || r.Status != "waiting_provider" || r.Round != 2 || r.WaitingReason != "provider limit until 15:53" {
		t.Fatalf("routed child route = %+v", r)
	}
	if got[plain] != nil {
		t.Fatalf("a child without a route got %+v", got[plain])
	}
}
