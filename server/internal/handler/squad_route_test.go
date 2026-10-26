package handler

import (
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// Legacy squad routes are gone: route_template may only be cleared, and a
// saved squad reports it empty for installed clients.
func TestSquadRouteTemplateCanOnlyBeCleared(t *testing.T) {
	lead := dbfx.Agent(t, "route template lead", testRuntimeID)
	squad := dbfx.Insert(t, "squad", testutil.Cols{
		"workspace_id": testWorkspaceID, "name": "route template squad", "description": "",
		"leader_id": lead, "creator_id": testUserID,
	})
	dbfx.Cleanup(t, "DELETE FROM squad_member WHERE squad_id=$1", squad)
	dbfx.Cleanup(t, "DELETE FROM squad WHERE id=$1", squad)
	set := func(template string) *testutil.Response {
		return testutil.Call(t, testHandler.UpdateSquad, squadScopeReq("", http.MethodPut, "/api/squads/"+squad,
			map[string]any{"route_template": template}, map[string]string{"id": squad}))
	}
	set("dev:" + lead + ",close:" + lead).Want(http.StatusBadRequest)
	var out map[string]any
	set("").Want(http.StatusOK).JSON(&out)
	if out["route_template"] != "" {
		t.Fatalf("route_template = %v", out["route_template"])
	}
}
