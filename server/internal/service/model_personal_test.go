package service

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (r *modelClaimRig) refetch(agent db.Agent) db.Agent {
	r.t.Helper()
	a, err := r.q.GetAgent(r.ctx, agent.ID)
	if err != nil {
		r.t.Fatal(err)
	}
	return a
}

func (r *modelClaimRig) personalOf(agent db.Agent) (db.ModelProfile, bool) {
	r.t.Helper()
	p, err := r.q.GetPersonalModelProfileOfAgent(r.ctx, agent.ID)
	if err != nil {
		return db.ModelProfile{}, false
	}
	return p, true
}

func TestPersonalProfileIsMadeEditedAndReset(t *testing.T) {
	r := newModelClaimRig(t)
	agent := r.agent("personal agent", r.strong)

	agent, err := r.profiles.SetPersonalProfile(r.ctx, agent, PersonalProfileInput{Options: r.rows("claude:opus:max", "glm:glm")})
	if err != nil {
		t.Fatal(err)
	}
	p, ok := r.personalOf(agent)
	if !ok || agent.ModelProfileID != p.ID || p.BasedOnProfileID != r.strong || p.OwnerAgentID != agent.ID {
		t.Fatalf("personal profile: %+v agent on %v", p, agent.ModelProfileID)
	}
	if p.Name != "personal agent" {
		t.Fatalf("name %q", p.Name)
	}
	world, err := LoadModelWorld(r.ctx, r.q, r.ws, timeNow())
	if err != nil {
		t.Fatal(err)
	}
	if got := world.Profiles[util.UUIDToString(p.ID)]; len(got.Options) != 2 || !got.Personal() || got.BasedOnProfileID != util.UUIDToString(r.strong) {
		t.Fatalf("world: %+v", got)
	}

	// Editing keeps the profile and its base.
	agent, err = r.profiles.SetPersonalProfile(r.ctx, agent, PersonalProfileInput{Options: r.rows("glm:glm")})
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := r.personalOf(agent); again.ID != p.ID || again.BasedOnProfileID != r.strong {
		t.Fatalf("edit changed the profile: %+v", again)
	}
	if n := r.count("SELECT count(*) FROM model_profile_option WHERE profile_id=$1", p.ID); n != 1 {
		t.Fatalf("rows after the edit: %d", n)
	}

	// Back to the shared profile: one transaction removes the personal one.
	agent, err = r.profiles.ResetPersonalProfile(r.ctx, agent)
	if err != nil {
		t.Fatal(err)
	}
	if agent.ModelProfileID != r.strong {
		t.Fatalf("agent on %v after the reset", agent.ModelProfileID)
	}
	if _, ok := r.personalOf(agent); ok {
		t.Fatal("personal profile survived the reset")
	}
	if n := r.count("SELECT count(*) FROM model_profile_option WHERE profile_id=$1", p.ID); n != 0 {
		t.Fatalf("personal rows survived: %d", n)
	}
	if _, err := r.profiles.ResetPersonalProfile(r.ctx, agent); err != nil {
		t.Fatalf("a second reset must be a no-op: %v", err)
	}
}

func TestPersonalProfileNeedsAModelAndAnOwnerAllowedRuntime(t *testing.T) {
	r := newModelClaimRig(t)
	agent := r.agent("personal strict", r.strong)
	if _, err := r.profiles.SetPersonalProfile(r.ctx, agent, PersonalProfileInput{}); !errors.Is(err, ErrModelProfileInvalid) {
		t.Fatalf("no rows: %v", err)
	}
	// A private runtime of another member cannot be a row of this agent's profile.
	other := r.fx.User(r.t, "someone else", "someone-else-personal@example.com")
	private := r.fx.Runtime(r.t, "models-private", map[string]any{"provider": "claude", "visibility": "private", "owner_id": other})
	_, err := r.profiles.SetPersonalProfile(r.ctx, agent, PersonalProfileInput{Options: []ModelOptionInput{{RuntimeID: private, Model: "x"}}})
	if !errors.Is(err, ErrModelProfileInvalid) || !strings.Contains(err.Error(), "private") {
		t.Fatalf("private runtime of another member: %v", err)
	}
}

func TestOnlyTheOwnerUsesAPersonalProfile(t *testing.T) {
	r := newModelClaimRig(t)
	owner := r.agent("personal owner", r.strong)
	stranger := r.agent("personal stranger", r.strong)
	owner, err := r.profiles.SetPersonalProfile(r.ctx, owner, PersonalProfileInput{Options: r.rows("glm:glm")})
	if err != nil {
		t.Fatal(err)
	}
	personal := util.UUIDToString(owner.ModelProfileID)

	if _, err := r.profiles.SaveAgentModels(r.ctx, stranger, AgentModelsInput{ModelProfileID: &personal}); !errors.Is(err, ErrModelProfileInvalid) {
		t.Fatalf("another agent on the personal profile: %v", err)
	}
	issue := r.fx.Issue(r.t, "personal issue")
	row, err := r.q.GetIssue(r.ctx, util.MustParseUUID(issue))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.profiles.SetIssueProfile(r.ctx, row, owner.ModelProfileID); !errors.Is(err, ErrModelProfileInvalid) {
		t.Fatalf("issue on a personal profile: %v", err)
	}
	if _, err := r.profiles.SaveProfile(r.ctx, r.ws, r.mid, r.owner, ModelProfileInput{Name: "Mid", WhenExhaustedProfileID: &personal, Options: r.rows("spare:mini")}); !errors.Is(err, ErrModelProfileInvalid) {
		t.Fatalf("fallback to a personal profile: %v", err)
	}
	// A personal profile is not edited through the shared profile API and cannot be deleted there.
	if _, err := r.profiles.SaveProfile(r.ctx, r.ws, owner.ModelProfileID, r.owner, ModelProfileInput{Name: "x", Options: r.rows("glm:glm")}); !errors.Is(err, ErrModelProfileNotFound) {
		t.Fatalf("shared API on a personal profile: %v", err)
	}
	if err := r.profiles.DeleteProfile(r.ctx, r.ws, owner.ModelProfileID); !errors.Is(err, ErrModelProfileNotFound) {
		t.Fatalf("delete of a personal profile through the shared API: %v", err)
	}
	if _, err := resolveNodeProfile(r.ctx, r.q, r.ws, personal); err == nil {
		t.Fatal("a route step named a personal profile")
	}
}

func TestDeletingTheBaseKeepsThePersonalProfile(t *testing.T) {
	r := newModelClaimRig(t)
	base := r.saveProfile(pgtype.UUID{}, "Short lived", "", "spare:mini")
	agent := r.agent("personal heir", base)
	agent, err := r.profiles.SetPersonalProfile(r.ctx, agent, PersonalProfileInput{Options: r.rows("glm:glm")})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.profiles.DeleteProfile(r.ctx, r.ws, base); err != nil {
		t.Fatalf("the personal profile must not keep the base alive: %v", err)
	}
	p, ok := r.personalOf(agent)
	if !ok || p.BasedOnProfileID.Valid {
		t.Fatalf("personal profile after the base is gone: %+v %v", p, ok)
	}
	if got := r.refetch(agent); got.ModelProfileID != p.ID {
		t.Fatalf("agent moved off its personal profile: %v", got.ModelProfileID)
	}
	if n := r.count("SELECT count(*) FROM model_profile_option WHERE profile_id=$1", p.ID); n != 1 {
		t.Fatalf("rows of the personal profile: %d", n)
	}
	// Without a base there is nothing to return to, and an agent never runs
	// without a profile: the reset is refused and the personal profile stays.
	if _, err := r.profiles.ResetPersonalProfile(r.ctx, r.refetch(agent)); !errors.Is(err, ErrModelProfileInvalid) {
		t.Fatalf("reset without a base: %v", err)
	}
	if got := r.refetch(agent); got.ModelProfileID != p.ID {
		t.Fatalf("agent left its personal profile: %v", got.ModelProfileID)
	}
}

func TestDeletingSystemAgentRemovesItsPersonalProfile(t *testing.T) {
	r := newModelClaimRig(t)
	a := r.agent("personal builder", r.strong)
	r.fx.Exec(t, `UPDATE agent SET kind='system', system_key='agent_builder:test' WHERE id=$1`, a.ID)
	a, err := r.profiles.SetPersonalProfile(r.ctx, a, PersonalProfileInput{Options: r.rows("codex:sol")})
	if err != nil {
		t.Fatal(err)
	}
	p, ok := r.personalOf(a)
	if !ok {
		t.Fatal("personal profile missing")
	}
	if err := r.q.DeleteSystemAgentByID(r.ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if r.count(`SELECT count(*) FROM model_profile WHERE id=$1`, p.ID) != 0 || r.count(`SELECT count(*) FROM model_profile_option WHERE profile_id=$1`, p.ID) != 0 {
		t.Fatal("deleted agent left personal model rows")
	}
}

func TestOlderClientsOwnListIsAPersonalProfile(t *testing.T) {
	r := newModelClaimRig(t)
	agent := r.agent("personal compat", r.strong)
	args := []string{}
	rows := []ModelOptionInput{{RuntimeID: r.rt["glm"], Model: "glm", CustomArgs: &args}, {RuntimeID: r.rt["claude"], Model: "opus"}}
	agent, err := r.profiles.SaveAgentModels(r.ctx, agent, AgentModelsInput{Options: &rows})
	if err != nil {
		t.Fatal(err)
	}
	p, ok := r.personalOf(agent)
	if !ok || agent.ModelProfileID != p.ID || p.BasedOnProfileID != r.strong {
		t.Fatalf("own list → personal: %+v", p)
	}
	world, _ := LoadModelWorld(r.ctx, r.q, r.ws, timeNow())
	got := world.Profiles[util.UUIDToString(p.ID)].Options
	if got[0].CustomArgs == nil || len(got[0].CustomArgs) != 0 || got[1].CustomArgs != nil {
		t.Fatalf("[] and NULL args must survive: %+v", got)
	}
	// "No own list" is a reset.
	none := []ModelOptionInput{}
	agent, err = r.profiles.SaveAgentModels(r.ctx, agent, AgentModelsInput{Options: &none})
	if err != nil || agent.ModelProfileID != r.strong {
		t.Fatalf("empty list: %v on %v", err, agent.ModelProfileID)
	}
	// Choosing a shared profile keeps the personal one aside, unused.
	agent, _ = r.profiles.SaveAgentModels(r.ctx, agent, AgentModelsInput{Options: &rows})
	shared := util.UUIDToString(r.mid)
	agent, err = r.profiles.SaveAgentModels(r.ctx, agent, AgentModelsInput{ModelProfileID: &shared})
	if err != nil || agent.ModelProfileID != r.mid {
		t.Fatalf("shared profile chosen: %v %v", err, agent.ModelProfileID)
	}
	if _, ok := r.personalOf(agent); !ok {
		t.Fatal("the personal profile must wait, not vanish")
	}
}

func TestAgentsNamedAlikeGetDistinctPersonalProfiles(t *testing.T) {
	r := newModelClaimRig(t)
	upper := r.agent("Claude", r.strong)
	lower := r.agent("claude", r.strong)
	var names []string
	for _, a := range []db.Agent{upper, lower} {
		a, err := r.profiles.SetPersonalProfile(r.ctx, a, PersonalProfileInput{Options: r.rows("glm:glm")})
		if err != nil {
			t.Fatal(err)
		}
		p, _ := r.personalOf(a)
		names = append(names, p.Name)
	}
	if strings.EqualFold(names[0], names[1]) {
		t.Fatalf("names collide: %v", names)
	}
	if !strings.HasSuffix(names[1], util.UUIDToString(lower.ID)[:8]) {
		t.Fatalf("the second name must carry its agent's id: %v", names)
	}
}

func TestOneAgentGetsOnePersonalProfileUnderRaces(t *testing.T) {
	r := newModelClaimRig(t)
	agent := r.agent("personal race", r.strong)
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = r.profiles.SetPersonalProfile(r.ctx, agent, PersonalProfileInput{Options: r.rows("glm:glm")})
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("racing writers: %v", err)
		}
	}
	if n := r.count("SELECT count(*) FROM model_profile WHERE owner_agent_id=$1", agent.ID); n != 1 {
		t.Fatalf("personal profiles of one agent: %d", n)
	}
}

func TestDiffRowsTellsEveryKindOfDifference(t *testing.T) {
	row := func(rt, model, thinking string, args []string) ModelOption {
		return ModelOption{RuntimeID: rt, Model: model, ThinkingLevel: thinking, CustomArgs: args}
	}
	base := []ModelOption{{Position: 1, RuntimeID: "a", Model: "m"}, {Position: 2, RuntimeID: "b", Model: "n", ThinkingLevel: "high"}, {Position: 3, RuntimeID: "c", Model: "o"}}
	same := append([]ModelOption(nil), base...)
	if d := DiffRows(same, base); len(d) != 0 {
		t.Fatalf("equal lists differ: %v", d)
	}
	changed := []ModelOption{base[0], row("b", "n", "low", nil), base[2]}
	changed[1].Position = 2
	if d := DiffRows(changed, base); !reflect.DeepEqual(d, []int32{2}) {
		t.Fatalf("changed row: %v", d)
	}
	if d := DiffRows(base[:2], base); !reflect.DeepEqual(d, []int32{3}) {
		t.Fatalf("missing row: %v", d)
	}
	if d := DiffRows(append(append([]ModelOption(nil), base...), ModelOption{Position: 4, RuntimeID: "d", Model: "p"}), base); !reflect.DeepEqual(d, []int32{4}) {
		t.Fatalf("extra row: %v", d)
	}
	nullArgs := []ModelOption{{Position: 1, RuntimeID: "a", Model: "m"}}
	emptyArgs := []ModelOption{{Position: 1, RuntimeID: "a", Model: "m", CustomArgs: []string{}}}
	if d := DiffRows(nullArgs, emptyArgs); !reflect.DeepEqual(d, []int32{1}) {
		t.Fatalf("NULL and [] args are different: %v", d)
	}
	withArgs := []ModelOption{{Position: 1, RuntimeID: "a", Model: "m", CustomArgs: []string{"--x"}}}
	if d := DiffRows(withArgs, []ModelOption{{Position: 1, RuntimeID: "a", Model: "m", CustomArgs: []string{"--y"}}}); !reflect.DeepEqual(d, []int32{1}) {
		t.Fatalf("different args: %v", d)
	}
}

func timeNow() time.Time { return time.Now() }
