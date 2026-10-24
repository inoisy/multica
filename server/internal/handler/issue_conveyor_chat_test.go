package handler

import (
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestConveyorChatControl(t *testing.T) {
	for _, role := range []string{"owner", "admin"} {
		t.Run(role, func(t *testing.T) {
			r := newConveyorHandlerRig(t)
			view := r.enable()
			dbfx.Exec(t, "UPDATE member SET role=$1 WHERE workspace_id=$2 AND user_id=$3", role, testWorkspaceID, testUserID)
			t.Cleanup(func() {
				dbfx.Exec(t, "UPDATE member SET role='owner' WHERE workspace_id=$1 AND user_id=$2", testWorkspaceID, testUserID)
			})
			session := dbfx.ChatSession(t, r.agents["bystander"])
			task := dbfx.Task(t, r.agents["bystander"], testutil.Cols{
				"runtime_id": testRuntimeID, "status": "running", "chat_session_id": session,
				"originator_user_id": testUserID, "accountable_user_id": testUserID,
			})
			for _, action := range []string{"pause", "resume", "disable"} {
				testutil.Call(t, testHandler.ApplyIssueConveyor, asConveyorRun(r.req("PUT", "/", map[string]any{
					"action": action, "expected_revision": view["revision"],
				}), r.agents["bystander"], task)).Want(http.StatusOK).JSON(&view)
			}
			if view["status"] != "disabled" {
				t.Fatalf("status = %v, want disabled", view["status"])
			}
		})
	}
}

func TestConveyorChatControlRejectsUntrustedAuthority(t *testing.T) {
	for _, name := range []string{
		"no task token", "no originator", "completed run", "cancelled run", "ordinary issue run",
		"different task agent", "different session agent", "different chat creator", "different workspace",
		"archived chat", "member without management rights",
	} {
		t.Run(name, func(t *testing.T) {
			r := newConveyorHandlerRig(t)
			view := r.enable()
			session := dbfx.ChatSession(t, r.agents["bystander"])
			cols := testutil.Cols{
				"runtime_id": testRuntimeID, "status": "running", "chat_session_id": session,
				"originator_user_id": testUserID, "accountable_user_id": testUserID,
			}
			switch name {
			case "no originator":
				cols["originator_user_id"] = nil
				cols["accountable_user_id"] = nil
			case "completed run":
				cols["status"] = "completed"
			case "cancelled run":
				cols["status"] = "cancelled"
			case "ordinary issue run":
				cols["chat_session_id"] = nil
				cols["issue_id"] = r.issue
			case "different session agent":
				dbfx.Exec(t, "UPDATE chat_session SET agent_id=$1 WHERE id=$2", r.agents["dev"], session)
			case "different chat creator":
				other := dbfx.User(t, "Other chat creator", "conveyor-chat-creator@test.invalid")
				dbfx.Exec(t, "UPDATE chat_session SET creator_id=$1 WHERE id=$2", other, session)
			case "different workspace":
				other := dbfx.Insert(t, "workspace", testutil.Cols{"name": "Other chat workspace", "slug": "other-conveyor-chat-workspace"})
				dbfx.Exec(t, "UPDATE chat_session SET workspace_id=$1 WHERE id=$2", other, session)
			case "archived chat":
				dbfx.Exec(t, "UPDATE chat_session SET status='archived' WHERE id=$1", session)
			case "member without management rights":
				var role string
				dbfx.QueryRow(t, "SELECT role FROM member WHERE workspace_id=$1 AND user_id=$2", testWorkspaceID, testUserID).Scan(&role)
				t.Cleanup(func() {
					dbfx.Exec(t, "UPDATE member SET role=$1 WHERE workspace_id=$2 AND user_id=$3", role, testWorkspaceID, testUserID)
				})
				dbfx.Exec(t, "UPDATE member SET role='member' WHERE workspace_id=$1 AND user_id=$2", testWorkspaceID, testUserID)
			}
			taskAgent := r.agents["bystander"]
			if name == "different task agent" {
				taskAgent = r.agents["dev"]
			}
			task := dbfx.Task(t, taskAgent, cols)
			req := asConveyorRun(r.req("PUT", "/", map[string]any{
				"action": "pause", "expected_revision": view["revision"],
			}), r.agents["bystander"], task)
			if name == "no task token" {
				req.Header.Del("X-Actor-Source")
			}
			testutil.Call(t, testHandler.ApplyIssueConveyor, req).Want(http.StatusForbidden)
			var after map[string]any
			testutil.Call(t, testHandler.GetIssueConveyor, r.req("GET", "/", nil)).Want(http.StatusOK).JSON(&after)
			if after["status"] != "active" || after["revision"] != view["revision"] {
				t.Fatalf("refused chat command changed route: %+v", after)
			}
		})
	}
}

func TestConveyorChatControlPreservesRevisionAndInvocationGuards(t *testing.T) {
	r := newConveyorHandlerRig(t)
	view := r.enable()
	session := dbfx.ChatSession(t, r.agents["bystander"])
	task := dbfx.Task(t, r.agents["bystander"], testutil.Cols{
		"runtime_id": testRuntimeID, "status": "running", "chat_session_id": session,
		"originator_user_id": testUserID, "accountable_user_id": testUserID,
	})
	call := func(body map[string]any) *testutil.Response {
		return testutil.Call(t, testHandler.ApplyIssueConveyor, asConveyorRun(r.req("PUT", "/", body), r.agents["bystander"], task))
	}
	call(map[string]any{"action": "pause", "expected_revision": 0}).Want(http.StatusConflict)
	call(map[string]any{"action": "pause", "expected_revision": view["revision"]}).Want(http.StatusOK).JSON(&view)
	// Admin may manage the route, but may not spend another member's private agent.
	other := dbfx.User(t, "Private agent owner", "conveyor-private-owner@test.invalid")
	dbfx.Exec(t, "UPDATE agent SET owner_id=$1 WHERE id=$2", other, r.agents["dev"])
	// Release the foreign key before the newer user fixture is deleted.
	dbfx.Cleanup(t, "UPDATE agent SET owner_id=$1 WHERE id=$2", testUserID, r.agents["dev"])
	before := r.runs("dev")
	call(map[string]any{"action": "resume", "expected_revision": view["revision"]}).Want(http.StatusForbidden)
	if got := r.runs("dev"); got != before {
		t.Fatalf("refused resume started a private agent (%d -> %d)", before, got)
	}
}
