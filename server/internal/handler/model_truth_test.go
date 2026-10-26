package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func patchAgent(t *testing.T, agentID string, body map[string]any) *testutil.Response {
	t.Helper()
	return testutil.Call(t, testHandler.UpdateAgent, withURLParam(newRequest("PATCH", "/api/agents/"+agentID, body), "id", agentID))
}

func TestUpdateAgentRefusesFieldsThatTheProfileOwns(t *testing.T) {
	r := newModelRig(t)
	profile := r.createProfile("owned fields", r.row("codex-main", "sol", "high"))["id"].(string)
	agent := dbfx.Agent(t, "owned fields agent", r.runtimes["codex-main"])
	r.agentModels(agent, map[string]any{"model_profile_id": profile}).Want(http.StatusOK)
	if got := dbfx.Count(t, "SELECT count(*) FROM agent WHERE id=$1 AND model='sol' AND thinking_level='high' AND runtime_id=$2", agent, r.runtimes["codex-main"]); got != 1 {
		t.Fatal("selecting the profile did not project row 1")
	}

	for name, body := range map[string]map[string]any{
		"runtime":  {"runtime_id": r.runtimes["claude-main"]},
		"model":    {"model": "opus"},
		"thinking": {"thinking_level": "low"},
	} {
		var out map[string]string
		patchAgent(t, agent, body).Want(http.StatusConflict).JSON(&out)
		if out["code"] != "agent_model_owned_by_profile" || !strings.Contains(out["error"], "Models") {
			t.Errorf("%s: %v", name, out)
		}
	}
	if got := dbfx.Count(t, "SELECT count(*) FROM agent WHERE id=$1 AND model='sol' AND thinking_level='high' AND runtime_id=$2", agent, r.runtimes["codex-main"]); got != 1 {
		t.Fatal("a refused PATCH changed the agent")
	}

	// The stored values sent back, and every other field, still go through.
	patchAgent(t, agent, map[string]any{"runtime_id": r.runtimes["codex-main"], "model": "sol", "thinking_level": "high", "description": "kept"}).Want(http.StatusOK)
}

func TestCreateAgentRunsOnAPersonalProfileOfOneRow(t *testing.T) {
	r := newModelRig(t)
	var out map[string]any
	testutil.Call(t, testHandler.CreateAgent, newRequest("POST", "/api/agents", map[string]any{
		"name": "truth created", "runtime_id": r.runtimes["codex-main"], "model": "sol", "thinking_level": "high",
	})).Want(http.StatusCreated).JSON(&out)
	id := out["id"].(string)
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, id) })

	if n := dbfx.Count(t, `SELECT count(*) FROM agent a JOIN model_profile p ON p.id = a.model_profile_id AND p.owner_agent_id = a.id
		JOIN model_profile_option o ON o.profile_id = p.id AND o.position = 1 AND o.runtime_id = a.runtime_id AND o.model = 'sol' AND o.thinking_level = 'high'
		WHERE a.id = $1 AND (SELECT count(*) FROM model_profile_option x WHERE x.profile_id = p.id) = 1`, id); n != 1 {
		t.Fatal("the created agent has no personal profile of its row")
	}

	// A built-in Claude/Codex runtime is refused and leaves no agent behind.
	builtin := dbfx.Runtime(t, "truth builtin", testutil.Cols{"provider": "codex", "visibility": "public"})
	testutil.Call(t, testHandler.CreateAgent, newRequest("POST", "/api/agents", map[string]any{
		"name": "truth builtin agent", "runtime_id": builtin,
	})).Want(http.StatusBadRequest)
	if n := dbfx.Count(t, "SELECT count(*) FROM agent WHERE name='truth builtin agent'"); n != 0 {
		t.Fatal("a refused create left an agent")
	}
}

func TestRuntimeModelUsageEndpoint(t *testing.T) {
	r := newModelRig(t)
	profile := r.createProfile("usage shared", r.row("glm", "glm", ""))["id"].(string)
	visible := dbfx.Agent(t, "usage visible", r.runtimes["glm"], testutil.Cols{"permission_mode": "public_to"})
	hidden := dbfx.Agent(t, "usage hidden", r.runtimes["glm"], testutil.Cols{"permission_mode": "private"})
	r.agentModels(visible, map[string]any{"model_profile_id": profile}).Want(http.StatusOK)
	r.agentModels(hidden, map[string]any{"model_profile_id": profile}).Want(http.StatusOK)
	testutil.Call(t, testHandler.SetAgentSingleModel, withURLParam(newRequest("PUT", "/", map[string]any{"runtime_id": r.runtimes["glm"], "model": "own"}), "id", hidden)).Want(http.StatusOK)
	dbfx.Insert(t, "agent_invocation_target", testutil.Cols{"agent_id": visible, "target_type": "workspace", "target_id": testWorkspaceID})

	call := func(actor string) RuntimeModelUsageResponse {
		var out RuntimeModelUsageResponse
		req := testutil.WithURLParams(newRequestAs(actor, "GET", "/api/runtimes/"+r.runtimes["glm"]+"/model-usage", nil), "runtimeId", r.runtimes["glm"])
		testutil.Call(t, testHandler.GetRuntimeModelUsage, req).Want(http.StatusOK).JSON(&out)
		return out
	}
	owner := call(testUserID)
	if len(owner.SelectedIn) != 2 || len(owner.ServingAgents) != 2 || owner.HiddenAgents != 0 || owner.HiddenProfiles != 0 || owner.Deletable {
		t.Fatalf("owner view: %+v", owner)
	}
	member := call(subtestOutsider(t))
	body := subtestJSON(member)
	if strings.Contains(body, hidden) || len(member.ServingAgents) != 1 || member.HiddenAgents != 1 || member.HiddenProfiles != 1 || member.Deletable {
		t.Fatalf("a member sees a private agent or the totals shrank: %s", body)
	}

	// An unused runtime can go.
	spare := dbfx.EnvironmentRuntime(t, "usage spare", testutil.Cols{"provider": "claude", "visibility": "public"})
	var unused RuntimeModelUsageResponse
	testutil.Call(t, testHandler.GetRuntimeModelUsage, testutil.WithURLParams(newRequest("GET", "/", nil), "runtimeId", spare)).Want(http.StatusOK).JSON(&unused)
	if !unused.Deletable || len(unused.SelectedIn) != 0 {
		t.Fatalf("unused runtime: %+v", unused)
	}
}

func TestPoolEnvironmentCommandCannotBecomeAWrapper(t *testing.T) {
	ctx := context.Background()
	var pool, env string
	if err := testPool.QueryRow(ctx, `INSERT INTO subscription_pool (name, runtime_type) VALUES ('truth pool', 'claude') RETURNING id::text`).Scan(&pool); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO runtime_profile (workspace_id, display_name, protocol_family, command_name, runtime_type, pool_id) VALUES ($1, 'truth pool env', 'claude', 'claude', 'claude', $2) RETURNING id::text`,
		testWorkspaceID, pool).Scan(&env); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM runtime_profile WHERE id = $1`, env)
		testPool.Exec(ctx, `DELETE FROM subscription_pool WHERE id = $1`, pool)
	})
	update := func(command string) *testutil.Response {
		req := testutil.WithURLParams(newRequest("PATCH", "/", map[string]any{"command_name": command}), "id", testWorkspaceID, "profileId", env)
		return testutil.Call(t, testHandler.UpdateRuntimeProfile, req)
	}
	update("claude-direct").Want(http.StatusBadRequest)
	update("claude").Want(http.StatusOK)
}
