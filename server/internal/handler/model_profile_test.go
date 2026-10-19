package handler

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// modelRig is a workspace slice with three runtimes, a strong and a mid
// profile, and the rows each test adds. Everything is removed afterwards.
type modelRig struct {
	t        *testing.T
	runtimes map[string]string
}

func newModelRig(t *testing.T) *modelRig {
	t.Helper()
	if testHandler == nil {
		t.Skip("database not available")
	}
	r := &modelRig{t: t, runtimes: map[string]string{}}
	for name, provider := range map[string]string{"codex-main": "codex", "claude-main": "claude", "glm": "claude"} {
		r.runtimes[name] = dbfx.Runtime(t, "model "+name, testutil.Cols{"provider": provider, "visibility": "public"})
	}
	dbfx.Cleanup(t, "DELETE FROM model_profile_option WHERE workspace_id=$1", testWorkspaceID)
	dbfx.Cleanup(t, "DELETE FROM model_profile WHERE workspace_id=$1", testWorkspaceID)
	dbfx.Cleanup(t, "DELETE FROM agent_model_option WHERE workspace_id=$1", testWorkspaceID)
	dbfx.Cleanup(t, "DELETE FROM runtime_unavailable WHERE workspace_id=$1", testWorkspaceID)
	dbfx.Cleanup(t, "UPDATE issue SET model_profile_id=NULL WHERE workspace_id=$1", testWorkspaceID)
	dbfx.Cleanup(t, "UPDATE agent SET model_profile_id=NULL WHERE workspace_id=$1", testWorkspaceID)
	return r
}

func (r *modelRig) row(runtime, model, thinking string) map[string]any {
	return map[string]any{"runtime_id": r.runtimes[runtime], "model": model, "thinking_level": thinking}
}

func (r *modelRig) createProfile(name string, rows ...map[string]any) map[string]any {
	r.t.Helper()
	var out map[string]any
	testutil.Call(r.t, testHandler.CreateModelProfile, newRequest("POST", "/api/model-profiles", map[string]any{"name": name, "options": rows})).
		Want(http.StatusCreated).JSON(&out)
	return out
}

func (r *modelRig) updateProfile(id string, body map[string]any) *testutil.Response {
	return testutil.Call(r.t, testHandler.UpdateModelProfile, withURLParam(newRequest("PUT", "/api/model-profiles/"+id, body), "id", id))
}

func (r *modelRig) agentModels(agent string, body map[string]any) *testutil.Response {
	return testutil.Call(r.t, testHandler.UpdateAgentModels, withURLParam(newRequest("PUT", "/api/agents/"+agent+"/models", body), "id", agent))
}

func (r *modelRig) preview(agent string) map[string]any {
	r.t.Helper()
	var out map[string]any
	testutil.Call(r.t, testHandler.GetAgentModels, withURLParam(newRequest("GET", "/api/agents/"+agent+"/models", nil), "id", agent)).
		Want(http.StatusOK).JSON(&out)
	return out["preview"].(map[string]any)
}

func chosenModel(preview map[string]any) string {
	chosen, _ := preview["chosen"].(map[string]any)
	if chosen == nil {
		return ""
	}
	return chosen["model"].(string)
}

func TestModelProfileCRUDAndReferences(t *testing.T) {
	r := newModelRig(t)
	strong := r.createProfile("Strong", r.row("codex-main", "sol", "high"), r.row("claude-main", "opus", ""))
	mid := r.createProfile("Mid", r.row("glm", "glm", ""))
	strongID, midID := strong["id"].(string), mid["id"].(string)
	if opts := strong["options"].([]any); len(opts) != 2 || opts[0].(map[string]any)["position"].(float64) != 1 {
		t.Fatalf("rows numbered by order: %v", opts)
	}

	// Name is unique per workspace, case-insensitively.
	testutil.Call(t, testHandler.CreateModelProfile, newRequest("POST", "/api/model-profiles", map[string]any{"name": "strong"})).Want(http.StatusConflict)
	// A runtime outside the workspace is refused.
	other := dbfx.Insert(t, "workspace", testutil.Cols{"name": "Other", "slug": "model-other", "description": "", "issue_prefix": "OTH"})
	foreign := dbfx.Runtime(t, "foreign", testutil.Cols{"workspace_id": other})
	testutil.Call(t, testHandler.CreateModelProfile, newRequest("POST", "/api/model-profiles", map[string]any{
		"name": "Bad", "options": []map[string]any{{"runtime_id": foreign}},
	})).Want(http.StatusBadRequest)

	// Fallback chain: strong -> mid is fine, mid -> strong closes a cycle: 400.
	r.updateProfile(strongID, map[string]any{"name": "Strong", "when_exhausted_profile_id": midID,
		"options": []map[string]any{r.row("codex-main", "sol", "high")}}).Want(http.StatusOK)
	r.updateProfile(midID, map[string]any{"name": "Mid", "when_exhausted_profile_id": strongID,
		"options": []map[string]any{r.row("glm", "glm", "")}}).Want(http.StatusBadRequest)
	r.updateProfile(midID, map[string]any{"name": "Mid", "when_exhausted_profile_id": midID}).Want(http.StatusBadRequest)

	// Referenced profiles cannot be deleted: by another profile, an agent, an
	// open issue. Unreferenced ones can.
	del := func(id string) *testutil.Response {
		return testutil.Call(t, testHandler.DeleteModelProfile, withURLParam(newRequest("DELETE", "/api/model-profiles/"+id, nil), "id", id))
	}
	del(midID).Want(http.StatusConflict)
	agent := dbfx.Agent(t, "model ref agent", r.runtimes["codex-main"])
	r.agentModels(agent, map[string]any{"model_profile_id": strongID}).Want(http.StatusOK)
	issue := dbfx.Issue(t, "model ref issue")
	testutil.Call(t, testHandler.SetIssueModelProfile, withURLParam(newRequest("PUT", "/", map[string]any{"model_profile_id": strongID}), "id", issue)).Want(http.StatusOK)
	r.updateProfile(strongID, map[string]any{"name": "Strong", "options": []map[string]any{r.row("codex-main", "sol", "high")}}).Want(http.StatusOK)
	del(strongID).Want(http.StatusConflict)
	r.agentModels(agent, map[string]any{"model_profile_id": nil}).Want(http.StatusOK)
	del(strongID).Want(http.StatusConflict) // the open issue still holds it
	dbfx.Exec(t, "UPDATE issue SET status='done' WHERE id=$1", issue)
	del(strongID).Want(http.StatusNoContent)
	if n := dbfx.Count(t, "SELECT count(*) FROM issue WHERE id=$1 AND model_profile_id IS NOT NULL", issue); n != 0 {
		t.Fatalf("a closed issue is detached from the deleted profile")
	}
	if n := dbfx.Count(t, "SELECT count(*) FROM model_profile_option WHERE profile_id=$1", strongID); n != 0 {
		t.Fatalf("rows of a deleted profile remain: %d", n)
	}
	del(midID).Want(http.StatusNoContent)
}

func TestModelProfilePermissions(t *testing.T) {
	r := newModelRig(t)
	profile := r.createProfile("Shared", r.row("codex-main", "sol", ""))
	member := dbfx.User(t, "model member", "model-member@multica.ai")
	dbfx.Member(t, testWorkspaceID, member, "member")
	agent := dbfx.Agent(t, "model perm agent", r.runtimes["codex-main"])

	asMember := func(method, path string, body any) *http.Request { return newRequestAs(member, method, path, body) }
	testutil.Call(t, testHandler.CreateModelProfile, asMember("POST", "/api/model-profiles", map[string]any{"name": "Mine"})).Want(http.StatusForbidden)
	testutil.Call(t, testHandler.UpdateModelProfile, withURLParam(asMember("PUT", "/", map[string]any{"name": "Shared"}), "id", profile["id"].(string))).Want(http.StatusForbidden)
	testutil.Call(t, testHandler.DeleteModelProfile, withURLParam(asMember("DELETE", "/", nil), "id", profile["id"].(string))).Want(http.StatusForbidden)
	testutil.Call(t, testHandler.ImportModelProfiles, asMember("POST", "/", map[string]any{"document": map[string]any{}})).Want(http.StatusForbidden)
	// Not the agent's owner and not an admin: its list does not change.
	testutil.Call(t, testHandler.UpdateAgentModels, withURLParam(asMember("PUT", "/", map[string]any{"model_profile_id": profile["id"]}), "id", agent)).Want(http.StatusForbidden)
	if n := dbfx.Count(t, "SELECT count(*) FROM agent WHERE id=$1 AND model_profile_id IS NOT NULL", agent); n != 0 {
		t.Fatal("a forbidden write changed the agent")
	}
	// Reading is open to members.
	testutil.Call(t, testHandler.ListModelProfiles, asMember("GET", "/api/model-profiles", nil)).Want(http.StatusOK)
	// The agent's owner may change it.
	owned := dbfx.Agent(t, "model owned agent", r.runtimes["codex-main"], testutil.Cols{"owner_id": member})
	testutil.Call(t, testHandler.UpdateAgentModels, withURLParam(asMember("PUT", "/", map[string]any{"model_profile_id": profile["id"]}), "id", owned)).Want(http.StatusOK)
	// Any member who can edit the issue sets its profile.
	issue := dbfx.Issue(t, "model perm issue")
	testutil.Call(t, testHandler.SetIssueModelProfile, withURLParam(asMember("PUT", "/", map[string]any{"model_profile_id": profile["id"]}), "id", issue)).Want(http.StatusOK)
}

func TestModelProfileEditReachesEveryReferencingAgent(t *testing.T) {
	r := newModelRig(t)
	profile := r.createProfile("Strong", r.row("codex-main", "sol", "high"), r.row("claude-main", "opus", ""))
	id := profile["id"].(string)
	a := dbfx.Agent(t, "model agent a", r.runtimes["codex-main"])
	b := dbfx.Agent(t, "model agent b", r.runtimes["codex-main"])
	own := dbfx.Agent(t, "model agent own", r.runtimes["codex-main"])
	for _, agent := range []string{a, b, own} {
		r.agentModels(agent, map[string]any{"model_profile_id": id}).Want(http.StatusOK)
	}
	// Pin "own" to one row: an own list of one row, the profile is not used.
	r.agentModels(own, map[string]any{"options": []map[string]any{r.row("glm", "glm", "")}}).Want(http.StatusOK)

	// Preview shows who changes, and saves nothing.
	var pv struct {
		Changes []struct {
			Kind string `json:"kind"`
			Ref  struct {
				ID string `json:"id"`
			} `json:"ref"`
		} `json:"changes"`
	}
	edit := map[string]any{"name": "Strong", "options": []map[string]any{r.row("claude-main", "opus", "max")}}
	testutil.Call(t, testHandler.PreviewModelProfile, withURLParam(newRequest("POST", "/", edit), "id", id)).Want(http.StatusOK).JSON(&pv)
	changed := map[string]bool{}
	for _, c := range pv.Changes {
		changed[c.Ref.ID] = true
	}
	if !changed[a] || !changed[b] || changed[own] {
		t.Fatalf("preview must list a and b, not the agent with its own list: %+v", pv.Changes)
	}
	if got := chosenModel(r.preview(a)); got != "sol" {
		t.Fatalf("preview saved the edit: %s", got)
	}

	r.updateProfile(id, edit).Want(http.StatusOK)
	for _, agent := range []string{a, b} {
		if got := chosenModel(r.preview(agent)); got != "opus" {
			t.Fatalf("agent %s did not follow the profile edit: %s", agent, got)
		}
	}
	if got := chosenModel(r.preview(own)); got != "glm" {
		t.Fatalf("the agent with its own list changed: %s", got)
	}
	// Back on the profile: removing the own list.
	r.agentModels(own, map[string]any{"options": []any{}}).Want(http.StatusOK)
	if got := chosenModel(r.preview(own)); got != "opus" {
		t.Fatalf("after removing the own list: %s", got)
	}
}

func TestModelProfileAgentModelsView(t *testing.T) {
	r := newModelRig(t)
	strong := r.createProfile("Strong", r.row("codex-main", "sol", "high"), r.row("claude-main", "opus", ""))
	agent := dbfx.Agent(t, "model view agent", r.runtimes["codex-main"])
	var view map[string]any
	r.agentModels(agent, map[string]any{"model_profile_id": strong["id"], "disallowed_tools": []string{"github:delete_repo", "github:delete_repo"}}).
		Want(http.StatusOK).JSON(&view)
	if tools := view["disallowed_tools"].([]any); len(tools) != 1 {
		t.Fatalf("tools de-duplicated: %v", tools)
	}
	r.agentModels(agent, map[string]any{"disallowed_tools": []string{"bad tool"}}).Want(http.StatusBadRequest)

	// A limited first row: preview takes the second and says why.
	dbfx.Exec(t, "INSERT INTO runtime_unavailable (workspace_id, runtime_id, until, reason) VALUES ($1,$2,now()+interval '1 hour','quota')", testWorkspaceID, r.runtimes["codex-main"])
	pv := r.preview(agent)
	if pv["reason"] != "fallback_row" || chosenModel(pv) != "opus" {
		t.Fatalf("limited row: %v", pv)
	}
	var list []map[string]any
	testutil.Call(t, testHandler.ListRuntimeUnavailable, newRequest("GET", "/api/runtime-unavailable", nil)).Want(http.StatusOK).JSON(&list)
	if len(list) != 1 || list[0]["runtime_id"] != r.runtimes["codex-main"] {
		t.Fatalf("unavailable list: %v", list)
	}

	// A run waiting for a free model is listed with its issue.
	waitingIssue := dbfx.Issue(t, "model waiting issue")
	dbfx.Task(t, agent, testutil.Cols{"runtime_id": r.runtimes["codex-main"], "issue_id": waitingIssue,
		"model_choice": testutil.Raw(`'{"source":"agent_profile","reason":"all_unavailable","rows":[]}'::jsonb`)})
	var withWaiting struct {
		Waiting []struct {
			IssueIdentifier string `json:"issue_identifier"`
			Choice          struct {
				Reason string `json:"reason"`
			} `json:"choice"`
		} `json:"waiting"`
	}
	testutil.Call(t, testHandler.GetAgentModels, withURLParam(newRequest("GET", "/", nil), "id", agent)).Want(http.StatusOK).JSON(&withWaiting)
	if len(withWaiting.Waiting) != 1 || withWaiting.Waiting[0].IssueIdentifier == "" || withWaiting.Waiting[0].Choice.Reason != "all_unavailable" {
		t.Fatalf("waiting runs: %+v", withWaiting.Waiting)
	}

	// An own list on a private runtime of another member is refused.
	member := dbfx.User(t, "model other owner", "model-other-owner@multica.ai")
	private := dbfx.Runtime(t, "model private", testutil.Cols{"owner_id": member})
	r.agentModels(agent, map[string]any{"options": []map[string]any{{"runtime_id": private}}}).Want(http.StatusBadRequest)

	// The issue's profile is readable back.
	issue := dbfx.Issue(t, "model view issue")
	testutil.Call(t, testHandler.SetIssueModelProfile, withURLParam(newRequest("PUT", "/", map[string]any{"model_profile_id": strong["id"]}), "id", issue)).Want(http.StatusOK)
	var ip map[string]any
	testutil.Call(t, testHandler.GetIssueModelProfile, withURLParam(newRequest("GET", "/", nil), "id", issue)).Want(http.StatusOK).JSON(&ip)
	if ip["profile_name"] != "Strong" {
		t.Fatalf("issue profile: %v", ip)
	}
	testutil.Call(t, testHandler.SetIssueModelProfile, withURLParam(newRequest("PUT", "/", map[string]any{"model_profile_id": nil}), "id", issue)).Want(http.StatusOK).JSON(&ip)
	if ip["model_profile_id"] != nil {
		t.Fatalf("issue profile cleared: %v", ip)
	}
}

func TestModelProfileImportDryRunFixture(t *testing.T) {
	r := newModelRig(t)
	same := dbfx.Agent(t, "model import same", r.runtimes["codex-main"], testutil.Cols{"model": "sol", "thinking_level": "high"})
	differs := dbfx.Agent(t, "model import differs", r.runtimes["claude-main"], testutil.Cols{"model": "opus"})
	pinned := dbfx.Agent(t, "model import pinned", r.runtimes["glm"], testutil.Cols{"model": "glm"})
	issue := dbfx.Issue(t, "model import override")
	var number int
	dbfx.QueryRow(t, "SELECT number FROM issue WHERE id=$1", issue).Scan(&number)

	// The fixture mirrors controls.json (tiers by id, chains by agent name) and
	// the watchdog config (providers, epics). Rows name runtimes by kind.
	doc := map[string]any{
		"tiers": map[string]any{
			"strong": map[string]any{"name": "Сильная", "chain": []map[string]any{
				{"runtime": "model codex-main", "model": "sol", "thinking_level": "high", "custom_args": []string{"-c", `mcp_servers.github.disabled_tools=["delete_repo"]`}},
				{"runtime": "model claude-main", "model": "opus"},
			}},
		},
		"providers": map[string]any{
			"tiers": []map[string]any{{"id": "mid", "name": "Средняя", "chain": []map[string]any{{"kind": "model glm", "model": "glm"}}}},
			"agents": map[string]any{
				"model import differs": []map[string]any{{"runtime": "model claude-main", "model": "opus"}, {"runtime": "model glm", "model": "glm"}},
			},
		},
		"chains": map[string]any{
			"model import same": map[string]any{"tier": "strong", "chain": []map[string]any{
				{"runtime": "model codex-main", "model": "sol", "thinking_level": "high", "custom_args": []string{"--disallowedTools", "mcp__github__delete_repo"}},
				{"runtime": "model claude-main", "model": "opus"},
			}},
			"model import pinned": map[string]any{"tier": "mid", "mode": "manual", "selected": 0},
			"nobody":              map[string]any{"tier": "mid"},
		},
		"epics": []map[string]any{{"overrides": map[string]any{
			"HAN-" + strconv.Itoa(number): map[string]any{"model": "sol", "thinking_level": "high"},
			"HAN-999999":                  map[string]any{"model": "x"},
		}}},
	}
	var plan struct {
		DryRun   bool `json:"dry_run"`
		Profiles []struct {
			Name, Action string
		} `json:"profiles"`
		Agents []struct {
			AgentID         string   `json:"agent_id"`
			Profile         string   `json:"profile"`
			OwnListWhy      string   `json:"own_list_why"`
			DisallowedTools []string `json:"disallowed_tools"`
			Matches         bool     `json:"matches"`
		} `json:"agents"`
		Issues []struct {
			Issue, Profile, Note string
		} `json:"issues"`
		Report      []string `json:"report"`
		Unexplained int      `json:"unexplained"`
	}
	testutil.Call(t, testHandler.ImportModelProfiles, newRequest("POST", "/", map[string]any{"document": doc})).Want(http.StatusOK).JSON(&plan)
	if !plan.DryRun || len(plan.Profiles) != 2 || plan.Profiles[0].Action != "create" {
		t.Fatalf("profiles: %+v", plan)
	}
	if n := dbfx.Count(t, "SELECT count(*) FROM model_profile WHERE workspace_id=$1", testWorkspaceID); n != 0 {
		t.Fatalf("a dry run wrote %d profiles", n)
	}
	byAgent := map[string]int{}
	for i, a := range plan.Agents {
		byAgent[a.AgentID] = i
	}
	if a := plan.Agents[byAgent[same]]; a.Profile != "Сильная" || a.OwnListWhy != "" || !a.Matches || len(a.DisallowedTools) != 1 || a.DisallowedTools[0] != "github:delete_repo" {
		t.Fatalf("same agent: %+v", a)
	}
	if a := plan.Agents[byAgent[differs]]; a.OwnListWhy != "differs" || !a.Matches {
		t.Fatalf("differs agent: %+v", a)
	}
	if a := plan.Agents[byAgent[pinned]]; a.OwnListWhy != "pinned" || !a.Matches {
		t.Fatalf("pinned agent: %+v", a)
	}
	if plan.Unexplained != 0 {
		t.Fatalf("unexplained mismatches: %d (%v)", plan.Unexplained, plan.Report)
	}
	if len(plan.Issues) != 2 || plan.Issues[0].Profile == "" && plan.Issues[1].Profile == "" {
		t.Fatalf("issue overrides: %+v", plan.Issues)
	}

	// Applying writes what the dry run planned, and a second dry run is a no-op.
	testutil.Call(t, testHandler.ImportModelProfiles, newRequest("POST", "/", map[string]any{"document": doc, "dry_run": false})).Want(http.StatusOK)
	if n := dbfx.Count(t, "SELECT count(*) FROM agent WHERE id=$1 AND model_profile_id IS NOT NULL", same); n != 1 {
		t.Fatal("agent not put on its profile")
	}
	if n := dbfx.Count(t, "SELECT count(*) FROM agent_model_option WHERE agent_id=$1", pinned); n != 1 {
		t.Fatal("pinned agent has no one-row own list")
	}
	if n := dbfx.Count(t, "SELECT count(*) FROM issue WHERE id=$1 AND model_profile_id IS NOT NULL", issue); n != 1 {
		t.Fatal("epic override not carried to the issue")
	}
	testutil.Call(t, testHandler.ImportModelProfiles, newRequest("POST", "/", map[string]any{"document": doc})).Want(http.StatusOK).JSON(&plan)
	for _, p := range plan.Profiles {
		if p.Action != "unchanged" {
			t.Fatalf("second import changes %s: %s", p.Name, p.Action)
		}
	}
}

func TestModelOptionClaimPayloadUsesChosenRow(t *testing.T) {
	plain := &db.AgentTaskQueue{}
	if claimedModelOption(plain) != nil {
		t.Fatal("a plain run has no chosen row")
	}
	routed := &db.AgentTaskQueue{
		ModelChoice:   []byte(`{"source":"agent_profile","reason":"fallback_row","position":2,"chosen":{"position":2,"runtime_id":"rt","model":"opus","thinking_level":"max","custom_args":["--disallowedTools","mcp__github__delete_repo"]},"rows":[]}`),
		ModelPosition: pgtype.Int4{Int32: 2, Valid: true},
	}
	got := claimedModelOption(routed)
	if got == nil || got.Model != "opus" || got.ThinkingLevel != "max" || len(got.CustomArgs) != 2 {
		t.Fatalf("chosen row: %+v", got)
	}
	// Routed but not yet claimed (no position): the agent's fields stay.
	routed.ModelPosition = pgtype.Int4{}
	if claimedModelOption(routed) != nil {
		t.Fatal("an unclaimed route must not change the payload")
	}
}
