package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestModelProfileReadsHideIssuesInPrivateProjects(t *testing.T) {
	rig := newModelRig(t)
	f := newPrivateProjectFixture(t)
	profile := rig.createProfile(f.marker+" profile", rig.row("codex-main", "base", ""))["id"].(string)
	agent := dbfx.Agent(t, f.marker+" agent", rig.runtimes["codex-main"], testutil.Cols{"model_profile_id": profile, "permission_mode": "public_to"})
	for _, member := range []string{f.insider, f.outsider} {
		dbfx.Insert(t, "agent_invocation_target", testutil.Cols{"agent_id": agent, "target_type": "member", "target_id": member})
	}
	for _, issue := range []string{f.secretIssue, f.publicIssue} {
		dbfx.Exec(t, "UPDATE issue SET model_profile_id=$1, assignee_type='agent', assignee_id=$2 WHERE id=$3", profile, agent, issue)
		dbfx.Task(t, agent, testutil.Cols{"issue_id": issue, "runtime_id": rig.runtimes["codex-main"], "status": "completed",
			"model_position": 1, "model_choice": testutil.Raw(`'{"reason":"chosen"}'::jsonb`)})
		dbfx.Task(t, agent, testutil.Cols{"issue_id": issue, "runtime_id": rig.runtimes["codex-main"], "status": "queued",
			"model_choice": testutil.Raw(`'{"reason":"all_unavailable"}'::jsonb`)})
	}
	secretTitle := f.marker + " secret issue"

	for _, tc := range []struct {
		name, userID string
		sees         bool
	}{
		{"project member", f.insider, true},
		{"workspace member outside the project", f.outsider, false},
		{"workspace admin outside the project", f.outsideAdmin, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			responses := map[string]any{}
			var prof, models, impact any
			testutil.Call(t, testHandler.GetModelProfile, withURLParam(newRequestAs(tc.userID, "GET", "/", nil), "id", profile)).Want(http.StatusOK).JSON(&prof)
			testutil.Call(t, testHandler.GetAgentModels, withURLParam(newRequestAs(tc.userID, "GET", "/", nil), "id", agent)).Want(http.StatusOK).JSON(&models)
			testutil.Call(t, testHandler.PreviewModelProfile, withURLParam(newRequestAs(tc.userID, "POST", "/",
				map[string]any{"options": []map[string]any{rig.row("claude-main", "edited", "")}}), "id", profile)).Want(http.StatusOK).JSON(&impact)
			responses["profile"], responses["agent models"], responses["preview"] = prof, models, impact
			for where, out := range responses {
				body := subtestJSON(out)
				if !strings.Contains(body, f.publicIssue) {
					t.Fatalf("%s omitted the workspace issue", where)
				}
				for _, secret := range []string{f.secretIssue, secretTitle} {
					if strings.Contains(body, secret) != tc.sees {
						t.Fatalf("%s: contains %q = %v, want %v", where, secret, !tc.sees, tc.sees)
					}
				}
			}
		})
	}
}
