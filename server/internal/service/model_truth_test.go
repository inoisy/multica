package service

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// fieldsOf is the projection an agent carries: runtime name, model, thinking.
func (r *modelClaimRig) fieldsOf(agent db.Agent) string {
	r.t.Helper()
	a := r.refetch(agent)
	name := util.UUIDToString(a.RuntimeID)
	for n, id := range r.rt {
		if id == name {
			name = n
		}
	}
	return name + "|" + a.Model.String + "|" + a.ThinkingLevel.String
}

func (r *modelClaimRig) builtinRuntime(provider string) string {
	r.t.Helper()
	key := "builtin-" + provider
	if _, ok := r.rt[key]; !ok {
		r.rt[key] = r.fx.Runtime(r.t, "models-"+key, testutil.Cols{"provider": provider, "visibility": "public"})
	}
	return r.rt[key]
}

func TestProfileEditProjectsRowOneOnEveryAgentOfIt(t *testing.T) {
	r := newModelClaimRig(t)
	a1 := r.agent("proj first", r.strong)
	a2 := r.agent("proj second", r.strong)
	other := r.agent("proj other", r.mid)
	if got := r.fieldsOf(a1); got != "codex|sol|high" {
		t.Fatalf("selecting the profile: %s", got)
	}

	r.saveProfile(r.strong, "Strong", "", "glm:glm", "codex:sol:high")
	for _, a := range []db.Agent{a1, a2} {
		if got := r.fieldsOf(a); got != "glm|glm|" {
			t.Errorf("after the edit %s carries %s, want the new row 1", a.Name, got)
		}
	}
	if got := r.fieldsOf(other); got != "spare|mini|" {
		t.Errorf("an agent of another profile moved: %s", got)
	}

	// Reordering changes which row is row 1.
	r.saveProfile(r.strong, "Strong", "", "codex:sol:high", "glm:glm")
	for _, a := range []db.Agent{a1, a2} {
		if got := r.fieldsOf(a); got != "codex|sol|high" {
			t.Errorf("after the reorder %s carries %s", a.Name, got)
		}
	}
	// The forecast does not write to the agent.
	before := r.refetch(a1)
	if _, err := PreviewChoice(r.ctx, r.q, before, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if after := r.refetch(a1); after.UpdatedAt != before.UpdatedAt || after.RuntimeID != before.RuntimeID {
		t.Error("PreviewChoice wrote to the agent")
	}
}

func TestProjectionRollsBackWithTheChange(t *testing.T) {
	r := newModelClaimRig(t)
	agent := r.agent("proj rollback", r.mid)
	bad := []ModelOptionInput{{RuntimeID: r.builtinRuntime("claude"), Model: "opus"}}
	_, err := r.profiles.SaveAgentModels(r.ctx, agent, AgentModelsInput{ModelProfileID: ptr(util.UUIDToString(r.strong)), Options: &bad})
	if !errors.Is(err, ErrModelProfileInvalid) {
		t.Fatalf("a built-in row: %v", err)
	}
	got := r.refetch(agent)
	if got.ModelProfileID != r.mid || r.fieldsOf(agent) != "spare|mini|" {
		t.Fatalf("the selection was not rolled back: profile %v fields %s", got.ModelProfileID, r.fieldsOf(agent))
	}
}

func TestPersonalSingleAndResetProjectTheirRowOne(t *testing.T) {
	r := newModelClaimRig(t)
	agent := r.agent("proj personal", r.strong)
	agent, err := r.profiles.SetPersonalProfile(r.ctx, agent, PersonalProfileInput{Options: r.rows("claude:opus:max", "glm:glm")})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.fieldsOf(agent); got != "claude|opus|max" {
		t.Fatalf("personal profile: %s", got)
	}
	// A personal profile is a copy: editing the shared one leaves the agent alone.
	r.saveProfile(r.strong, "Strong", "", "glm:glm")
	if got := r.fieldsOf(agent); got != "claude|opus|max" {
		t.Fatalf("a shared edit moved a personal profile: %s", got)
	}
	agent, err = r.profiles.SetSingleModel(r.ctx, agent, r.rows("glm:other:low")[0])
	if err != nil {
		t.Fatal(err)
	}
	if got := r.fieldsOf(agent); got != "glm|other|low" {
		t.Fatalf("single model: %s", got)
	}
	agent, err = r.profiles.ResetPersonalProfile(r.ctx, agent)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.fieldsOf(agent); got != "glm|glm|" || agent.ModelProfileID != r.strong {
		t.Fatalf("reset: %s on %v", got, agent.ModelProfileID)
	}
}

func TestProfileFromAgentFieldsMakesAPersonalRow(t *testing.T) {
	r := newModelClaimRig(t)
	id := r.fx.Agent(t, "proj bench", r.rt["glm"], testutil.Cols{"model": "glm-5", "thinking_level": "max", "custom_args": testutil.Raw(`'["--x"]'::jsonb`)})
	agent, err := r.q.GetAgent(r.ctx, util.MustParseUUID(id))
	if err != nil {
		t.Fatal(err)
	}
	agent, err = ProfileFromAgentFields(r.ctx, r.q, agent)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := r.personalOf(agent)
	if !ok || agent.ModelProfileID != p.ID {
		t.Fatalf("no personal profile: %+v", p)
	}
	rows, err := r.q.ListModelProfileOptions(r.ctx, p.ID)
	if err != nil || len(rows) != 1 || rows[0].Model != "glm-5" || rows[0].ThinkingLevel != "max" || string(rows[0].CustomArgs) != `["--x"]` {
		t.Fatalf("rows %+v %v", rows, err)
	}
	if got := r.fieldsOf(agent); got != "glm|glm-5|max" {
		t.Fatalf("fields %s", got)
	}
	// The forecast is the row of the profile.
	c, err := PreviewChoice(r.ctx, r.q, agent, nil, time.Now())
	if err != nil || c.Source != ModelSourceAgentProfile || c.Chosen == nil || c.Chosen.Model != "glm-5" {
		t.Fatalf("choice %+v %v", c, err)
	}
}

func TestBuiltinEnvironmentsAreRefusedOnEveryWritePath(t *testing.T) {
	r := newModelClaimRig(t)
	builtin := r.builtinRuntime("claude")
	row := []ModelOptionInput{{RuntimeID: builtin, Model: "opus"}}
	profiles := r.count("SELECT count(*) FROM model_profile WHERE workspace_id=$1", r.ws)
	agent := r.agent("proj builtin", r.strong)
	before := r.fieldsOf(agent)

	checks := map[string]func() error{
		"new shared profile": func() error {
			_, err := r.profiles.SaveProfile(r.ctx, r.ws, pgtype.UUID{}, r.owner, ModelProfileInput{Name: "Builtin", Options: row})
			return err
		},
		"edited shared profile": func() error {
			_, err := r.profiles.SaveProfile(r.ctx, r.ws, r.strong, r.owner, ModelProfileInput{Name: "Strong", Options: row})
			return err
		},
		"personal profile": func() error {
			_, err := r.profiles.SetPersonalProfile(r.ctx, agent, PersonalProfileInput{Options: row})
			return err
		},
		"single model": func() error {
			_, err := r.profiles.SetSingleModel(r.ctx, agent, row[0])
			return err
		},
		"own list of an older client": func() error {
			_, err := r.profiles.SaveAgentModels(r.ctx, agent, AgentModelsInput{Options: &row})
			return err
		},
	}
	for name, write := range checks {
		if err := write(); !errors.Is(err, ErrModelProfileInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n := r.count("SELECT count(*) FROM model_profile WHERE workspace_id=$1", r.ws); n != profiles {
		t.Errorf("a refused write left %d profiles behind", n-profiles)
	}
	if got := r.fieldsOf(agent); got != before {
		t.Errorf("a refused write moved the agent: %s → %s", before, got)
	}

	// The type and the link decide, not the name: an own login (an environment
	// without a pool) and the runtimes of other providers are fine.
	hermes := r.builtinRuntime("hermes")
	if _, err := r.profiles.SetSingleModel(r.ctx, agent, ModelOptionInput{RuntimeID: hermes, Model: "m"}); err != nil {
		t.Errorf("a hermes runtime: %v", err)
	}
	if _, err := r.profiles.SetSingleModel(r.ctx, agent, ModelOptionInput{RuntimeID: r.rt["glm"], Model: "m"}); err != nil {
		t.Errorf("an environment with a login of its own: %v", err)
	}
}

func TestPoolEnvironmentMustRunTheStandardCommand(t *testing.T) {
	env := db.RuntimeProfile{ProtocolFamily: "claude", RuntimeType: "claude", CommandName: "claude", PoolID: util.MustParseUUID("11111111-1111-4111-8111-111111111111")}
	if !PoolCommandMatches(env) {
		t.Error("the standard command was refused")
	}
	// An empty pool is still a pool, however few accounts it has.
	env.CommandName = "claude-direct"
	if PoolCommandMatches(env) {
		t.Error("a wrapper in a pool environment passed")
	}
	// An old profile without runtime_type is read by its protocol family.
	env.CommandName, env.RuntimeType = "claude", ""
	if !PoolCommandMatches(env) {
		t.Error("an empty runtime_type must read as the protocol family")
	}
	// Without a pool the command is the environment's own business.
	env.CommandName, env.PoolID = "qwen-loodsen", pgtype.UUID{}
	if !PoolCommandMatches(env) {
		t.Error("an own login was refused")
	}
}

func TestEmptyPoolEnvironmentIsAllowedAndStaysAPool(t *testing.T) {
	r := newModelClaimRig(t)
	pool, _ := r.poolOn("spare", "first")
	if n := r.count("SELECT count(*) FROM subscription_pool_member WHERE pool_id=$1", pool); n != 0 {
		t.Fatalf("members: %d", n)
	}
	agent := r.agent("proj empty pool", r.mid)
	if _, err := r.profiles.SetSingleModel(r.ctx, agent, ModelOptionInput{RuntimeID: r.rt["spare"], Model: "m"}); err != nil {
		t.Fatalf("a row on the environment of an empty pool: %v", err)
	}
	world, err := LoadModelWorld(r.ctx, r.q, r.ws, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if world.Runtimes[r.rt["spare"]].PoolID != pool {
		t.Errorf("the empty pool is not the pool of its environment: %+v", world.Runtimes[r.rt["spare"]])
	}
}

func TestAgentWithoutRowOneDoesNotFallBackToItsFields(t *testing.T) {
	r := newModelClaimRig(t)
	empty := r.saveProfile(pgtype.UUID{}, "Empty", "")
	agent := r.agent("proj no row", empty)
	if got := r.fieldsOf(agent); got != "codex|plain|" {
		t.Fatalf("an empty profile must leave the fields alone: %s", got)
	}
	c, err := PreviewChoice(r.ctx, r.q, agent, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !c.Waiting() || c.Source != ModelSourceAgentProfile {
		t.Fatalf("an agent without row 1 runs: %+v", c)
	}
	task := r.enqueue(agent, pgtype.UUID{})
	if got := r.claim("codex"); got != nil && got.ID == task.ID {
		t.Fatal("the agent started on the runtime of its old fields")
	}
}

func TestRemovingARuntimeKeepsTheProjection(t *testing.T) {
	r := newModelClaimRig(t)
	profile := r.saveProfile(pgtype.UUID{}, "Chain", "", "spare:mini", "claude:opus", "glm:glm")
	agent := r.agent("proj chain", profile)
	if got := r.fieldsOf(agent); got != "spare|mini|" {
		t.Fatal(got)
	}
	if err := RemoveRuntimeModelRows(r.ctx, r.q, util.MustParseUUID(r.rt["spare"])); err != nil {
		t.Fatal(err)
	}
	if got := r.fieldsOf(agent); got != "claude|opus|" {
		t.Fatalf("after row 1 went: %s", got)
	}
	if n := r.count("SELECT count(*) FROM model_profile_option WHERE profile_id=$1 AND position IN (1,2)", profile); n != 2 {
		t.Fatalf("positions were not closed up")
	}
	if n := r.count("SELECT count(*) FROM model_profile_option WHERE profile_id=$1 AND position > 2", profile); n != 0 {
		t.Fatalf("a gap is left in the positions")
	}
	for _, name := range []string{"claude", "glm"} {
		if err := RemoveRuntimeModelRows(r.ctx, r.q, util.MustParseUUID(r.rt[name])); err != nil {
			t.Fatal(err)
		}
	}
	c, err := PreviewChoice(r.ctx, r.q, r.refetch(agent), nil, time.Now())
	if err != nil || !c.Waiting() || c.Source != ModelSourceAgentProfile {
		t.Fatalf("an agent whose profile lost every row runs: %+v %v", c, err)
	}
}

func TestMergedRuntimeCarriesItsRowsAndTheProjection(t *testing.T) {
	r := newModelClaimRig(t)
	profile := r.saveProfile(pgtype.UUID{}, "Merge", "", "glm:glm", "codex:sol")
	agent := r.agent("proj merge", profile)
	if err := MoveRuntimeModelRows(r.ctx, r.q, util.MustParseUUID(r.rt["glm"]), util.MustParseUUID(r.rt["spare"])); err != nil {
		t.Fatal(err)
	}
	if got := r.fieldsOf(agent); got != "spare|glm|" {
		t.Fatalf("after the merge: %s", got)
	}
}

func TestRuntimeModelUsageListsEveryReference(t *testing.T) {
	r := newModelClaimRig(t)
	base := r.saveProfile(pgtype.UUID{}, "Usage base", "", "spare:mini")
	fallback := r.saveProfile(pgtype.UUID{}, "Usage fallback", util.UUIDToString(base), "glm:glm")
	direct := r.agent("usage direct", base)
	viaFallback := r.agent("usage via fallback", fallback)
	personal, err := r.profiles.SetPersonalProfile(r.ctx, r.agent("usage personal", base), PersonalProfileInput{Options: r.rows("spare:own")})
	if err != nil {
		t.Fatal(err)
	}
	r.fx.Issue(t, "usage open issue", testutil.Cols{"model_profile_id": util.UUIDToString(fallback)})
	closed := r.fx.Issue(t, "usage closed issue", testutil.Cols{"model_profile_id": util.UUIDToString(base)})
	r.fx.Exec(t, "UPDATE issue SET status='done' WHERE id=$1", closed)
	plainID := r.fx.Agent(t, "usage plain", r.rt["spare"])

	rt, err := r.q.GetAgentRuntime(r.ctx, util.MustParseUUID(r.rt["spare"]))
	if err != nil {
		t.Fatal(err)
	}
	u, err := RuntimeModelUsageOf(r.ctx, r.q, rt)
	if err != nil {
		t.Fatal(err)
	}
	names := func(refs []RuntimeProfileRef) []string {
		var out []string
		for _, p := range refs {
			out = append(out, p.Name)
		}
		return out
	}
	if got := names(u.SelectedIn); len(got) != 3 || got[0] != "Mid" || got[1] != "Usage base" || got[2] != "usage personal" {
		t.Errorf("selected in: %v", got)
	}
	if got := names(u.FallbackProfiles); len(got) != 1 || got[0] != "Usage fallback" {
		t.Errorf("fallback profiles: %v", got)
	}
	served := map[string]bool{}
	for _, a := range u.Agents {
		served[a.Name] = true
	}
	for _, want := range []string{direct.Name, viaFallback.Name, personal.Name, "usage plain"} {
		if !served[want] {
			t.Errorf("%q is not listed as served: %v", want, served)
		}
	}
	if u.OpenIssues != 1 {
		t.Errorf("open issues: %d, want the one open issue through the fallback", u.OpenIssues)
	}
	_ = plainID
}
