package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestModelProfileReadsRespectAgentVisibility(t *testing.T) {
	rig := newModelRig(t)
	base := rig.createProfile("visibility base", rig.row("codex-main", "base", ""))["id"].(string)
	member := subtestOutsider(t)
	private := dbfx.Agent(t, "hidden personal owner", rig.runtimes["codex-main"], testutil.Cols{"model_profile_id": base, "permission_mode": "private"})
	hiddenShared := dbfx.Agent(t, "hidden shared consumer", rig.runtimes["codex-main"], testutil.Cols{"model_profile_id": base, "permission_mode": "private"})
	allowed := dbfx.Agent(t, "allow-listed personal owner", rig.runtimes["codex-main"], testutil.Cols{"model_profile_id": base, "permission_mode": "public_to"})
	denied := dbfx.Agent(t, "unlisted personal owner", rig.runtimes["codex-main"], testutil.Cols{"model_profile_id": base, "permission_mode": "public_to"})
	own := dbfx.Agent(t, "member personal owner", rig.runtimes["codex-main"], testutil.Cols{"model_profile_id": base, "owner_id": member, "permission_mode": "private"})
	dbfx.Insert(t, "agent_invocation_target", testutil.Cols{"agent_id": allowed, "target_type": "member", "target_id": member})
	personal := map[string]string{}
	for _, id := range []string{private, allowed, denied, own} {
		var out AgentModelsResponse
		testutil.Call(t, testHandler.CreateAgentPersonalProfile, withURLParam(newRequest("POST", "/", map[string]any{"from_profile_id": base}), "id", id)).Want(http.StatusOK).JSON(&out)
		personal[id] = *out.PersonalProfileID
	}

	for _, kind := range []string{"", "personal", "all"} {
		t.Run("list/"+kind, func(t *testing.T) {
			var out []ModelProfileResponse
			testutil.Call(t, testHandler.ListModelProfiles, newRequestAs(member, "GET", "/api/model-profiles?kind="+kind, nil)).Want(http.StatusOK).JSON(&out)
			body := subtestJSON(out)
			for _, hidden := range []string{private, personal[private], hiddenShared, denied, personal[denied]} {
				if strings.Contains(body, hidden) {
					t.Fatalf("list kind=%s exposed hidden agent/profile %s", kind, hidden)
				}
			}
			if kind != "" {
				for _, visible := range []string{allowed, personal[allowed], own, personal[own]} {
					if !strings.Contains(body, visible) {
						t.Fatalf("list kind=%s omitted visible agent/profile %s", kind, visible)
					}
				}
			}
		})
	}

	for _, id := range []string{private, denied} {
		testutil.Call(t, testHandler.GetAgentModels, withURLParam(newRequestAs(member, "GET", "/", nil), "id", id)).Want(http.StatusForbidden)
		testutil.Call(t, testHandler.GetModelProfile, withURLParam(newRequestAs(member, "GET", "/", nil), "id", personal[id])).Want(http.StatusNotFound)
		testutil.Call(t, testHandler.PreviewModelProfile, withURLParam(newRequestAs(member, "POST", "/", map[string]any{"options": []map[string]any{rig.row("codex-main", "edited", "")}}), "id", personal[id])).Want(http.StatusNotFound)
		// Workspace owners still see private agents and their personal profiles.
		testutil.Call(t, testHandler.GetAgentModels, withURLParam(newRequest("GET", "/", nil), "id", id)).Want(http.StatusOK)
		testutil.Call(t, testHandler.GetModelProfile, withURLParam(newRequest("GET", "/", nil), "id", personal[id])).Want(http.StatusOK)
	}
	for _, id := range []string{allowed, own} {
		testutil.Call(t, testHandler.GetAgentModels, withURLParam(newRequestAs(member, "GET", "/", nil), "id", id)).Want(http.StatusOK)
	}
	for _, handler := range []http.HandlerFunc{testHandler.GetModelProfile, testHandler.PreviewModelProfile} {
		var out any
		testutil.Call(t, handler, withURLParam(newRequestAs(member, "POST", "/", map[string]any{"options": []map[string]any{rig.row("codex-main", "edited", "")}}), "id", base)).Want(http.StatusOK).JSON(&out)
		body := subtestJSON(out)
		for _, hidden := range []string{private, personal[private], hiddenShared, denied, personal[denied]} {
			if strings.Contains(body, hidden) {
				t.Fatalf("shared profile references/impact exposed %s", hidden)
			}
		}
	}
}
