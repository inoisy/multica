package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

func TestParseConveyorRouteSpecOptions(t *testing.T) {
	entries, err := ParseConveyorRouteSpec("idea:Креатор[profile=Креатив · ультра], build:Учёба — Монтажёр[profile=Сильная],critic:Учёба — Критик[cross=BUILD; back=build; rounds=3],close:Продюсер")
	if err != nil {
		t.Fatal(err)
	}
	want := []ConveyorRouteEntry{
		{Step: "idea", Agent: "Креатор", Profile: "Креатив · ультра"},
		{Step: "build", Agent: "Учёба — Монтажёр", Profile: "Сильная"},
		{Step: "critic", Agent: "Учёба — Критик", Cross: "build", Back: "build", Rounds: 3},
		{Step: "close", Agent: "Продюсер"},
	}
	if len(entries) != len(want) {
		t.Fatalf("got %d entries: %+v", len(entries), entries)
	}
	for i := range want {
		if entries[i] != want[i] {
			t.Fatalf("entry %d = %+v, want %+v", i, entries[i], want[i])
		}
	}
	if got := entries[2].spec(); got != "critic:Учёба — Критик[cross=build;back=build;rounds=3]" {
		t.Fatalf("spec = %q", got)
	}
	if got := entries[3].spec(); got != "close:Продюсер" {
		t.Fatalf("plain spec = %q", got)
	}
	for _, bad := range []string{"dev:A[profile=x", "dev:A[color=red],close:B", "dev:A[rounds=two],close:B", "dev:A[profile],close:B"} {
		if _, err := ParseConveyorRouteSpec(bad); err == nil {
			t.Fatalf("%q parsed", bad)
		}
	}
}

func TestValidateNodeRefs(t *testing.T) {
	ok := []ConveyorRouteEntry{{Step: "build"}, {Step: "critic", Cross: "build", Back: "build", Rounds: 2}, {Step: "close"}}
	if err := validateNodeRefs(ok); err != nil {
		t.Fatal(err)
	}
	bad := map[string][]ConveyorRouteEntry{
		"cross ahead":      {{Step: "critic", Cross: "build"}, {Step: "build"}, {Step: "close"}},
		"back unknown":     {{Step: "build"}, {Step: "critic", Back: "dev"}, {Step: "close"}},
		"rounds alone":     {{Step: "build"}, {Step: "critic", Rounds: 2}, {Step: "close"}},
		"one round":        {{Step: "build"}, {Step: "critic", Back: "build", Rounds: 1}, {Step: "close"}},
		"too many rounds":  {{Step: "build"}, {Step: "critic", Back: "build", Rounds: 9}, {Step: "close"}},
		"options on close": {{Step: "build"}, {Step: "close", Profile: "x"}},
		"back to itself":   {{Step: "critic", Back: "critic"}, {Step: "close"}},
	}
	for name, entries := range bad {
		if err := validateNodeRefs(entries); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

func TestModelFamily(t *testing.T) {
	cases := []struct{ provider, model, want string }{
		{"claude", "claude-opus-5-5", "anthropic"},
		{"claude", "claude-fable-5-1", "anthropic"},
		{"claude", "glm-5.3", "zhipu"},
		{"claude", "MiniMax-M3", "minimax"},
		{"codex", "gpt-6.1-sol", "openai"},
		{"codex", "", "openai"},
		{"claude", "", "anthropic"},
		{"hermes", "", "hermes"},
	}
	for _, c := range cases {
		if got := ModelFamily(c.provider, c.model); got != c.want {
			t.Fatalf("ModelFamily(%q, %q) = %q, want %q", c.provider, c.model, got, c.want)
		}
	}
}

func TestChooseModelExcludesFamily(t *testing.T) {
	now := time.Now()
	in := ModelChoiceInput{
		Agent: ModelChoiceAgent{Options: []ModelOption{
			{Position: 1, RuntimeID: "codex", Model: "gpt-6.1-sol"},
			{Position: 2, RuntimeID: "claude", Model: "claude-opus-5-5"},
		}},
		Runtimes: map[string]RuntimeAvailability{
			"codex":  {Name: "Codex", Provider: "codex", Online: true},
			"claude": {Name: "Claude", Provider: "claude", Online: true},
		},
	}
	if c := ChooseModel(in, now); c.RuntimeID() != "codex" {
		t.Fatalf("without exclusion chose %q", c.RuntimeID())
	}
	in.ExcludeFamily = "openai"
	c := ChooseModel(in, now)
	if c.RuntimeID() != "claude" || c.Rows[0].State != ModelRowSameFamily || c.ExcludedFamily != "openai" {
		t.Fatalf("exclusion: %+v", c)
	}
	// Only the excluded family left: the run waits rather than breaking the rule.
	in.ExcludeFamily = "anthropic"
	in.Agent.Options = in.Agent.Options[1:]
	if c := ChooseModel(in, now); !c.Waiting() || !strings.Contains(c.Summary(), "anthropic excluded") {
		t.Fatalf("a same-family-only list ran: %+v / %s", c, c.Summary())
	}
}

// back=qa: a verifier asking for changes opens the next round itself, with its
// report in the executor's instructions; when the rounds run out the
// coordinator decides.
func TestConveyorBackReturnsWork(t *testing.T) {
	r := newConveyorRig(t)
	route := "dev:" + util.UUIDToString(r.agents["dev"]) +
		",qa:" + util.UUIDToString(r.agents["qa"]) + "[back=dev;rounds=2]" +
		",close:" + util.UUIDToString(r.agents["lead"])
	raw, _ := json.Marshal(route)
	r.mustApply(ConveyorCommand{Action: ConveyorCmdEnable, Route: raw})
	const commit, fixed = "abcdef1234567", "fedcba7654321"
	r.pass("dev", commit)

	qa := r.start(r.only("qa"))
	if !strings.Contains(qa.HandoffNote.String, "вернёт работу на шаг «dev»") {
		t.Fatalf("qa is not told where changes_requested goes:\n%s", qa.HandoffNote.String)
	}
	in := r.result(ConveyorOutcomeChangesRequested, commit)
	in.Report = "s3: подписи наезжают на субтитры"
	r.mustSubmit(qa, in)
	r.complete(qa)

	view := r.view()
	if view.Status != ConveyorActive || view.Step != "dev" || view.Round != 2 || view.AcceptedCommit != "" {
		t.Fatalf("after changes_requested: %+v; journal:%s", view, r.dump())
	}
	if got := len(r.tasksOf("lead")); got != 0 {
		t.Fatalf("coordinator woken on a back round (%d runs)", got)
	}
	devs := r.tasksOf("dev")
	if len(devs) != 2 || !strings.Contains(devs[1].HandoffNote.String, "подписи наезжают") || !strings.Contains(devs[1].HandoffNote.String, "вернул шаг «qa»") {
		t.Fatalf("second dev run does not carry the verifier's report: %d runs\n%s", len(devs), devs[len(devs)-1].HandoffNote.String)
	}
	if d := r.decisionFor(qa); d.Action.String != conveyorDecisionBack {
		t.Fatalf("decision = %s", d.Action.String)
	}

	r.pass("dev", fixed)
	qa2 := r.start(r.tasksOf("qa")[1])
	r.mustSubmit(qa2, r.result(ConveyorOutcomeChangesRequested, fixed))
	r.complete(qa2)
	if view = r.view(); view.Status != ConveyorAwaitingCoordinator {
		t.Fatalf("rounds spent, status %s; journal:%s", view.Status, r.dump())
	}
	if lead := r.only("lead"); !strings.Contains(lead.HandoffNote.String, "back=dev spent") {
		t.Fatalf("coordinator is not told the rounds ran out:\n%s", lead.HandoffNote.String)
	}
	r.everyInputDecided()
}

// profile= and cross= reach the model choice of the step's run.
func TestConveyorNodeRunModel(t *testing.T) {
	r := newConveyorRig(t)
	ws := parseTestUUID(t, r.f.WorkspaceID)
	profile, err := r.f.q.CreateModelProfile(r.ctx, db.CreateModelProfileParams{ID: dbid.NewV7(), WorkspaceID: ws, Name: "Сильная", CreatedBy: r.owner})
	if err != nil {
		t.Fatal(err)
	}
	r.f.Cleanup(t, "DELETE FROM model_profile WHERE id=$1", profile.ID)
	route := "dev:" + util.UUIDToString(r.agents["dev"]) + "[profile=сильная]" +
		",qa:" + util.UUIDToString(r.agents["qa"]) + "[cross=dev]" +
		",close:" + util.UUIDToString(r.agents["lead"])
	raw, _ := json.Marshal(route)
	r.mustApply(ConveyorCommand{Action: ConveyorCmdEnable, Route: raw})

	dev := r.only("dev")
	got, err := r.svc.RunModel(r.ctx, r.f.q, dev)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProfileID != profile.ID || got.ExcludeFamily != "" {
		t.Fatalf("dev run model = %+v, want profile %s", got, util.UUIDToString(profile.ID))
	}
	choice, _ := json.Marshal(ModelChoice{Provider: "codex", Chosen: &ModelOption{Model: "gpt-6.1-sol"}})
	r.f.Exec(t, "UPDATE agent_task_queue SET model_choice=$2 WHERE id=$1", dev.ID, choice)
	r.pass("dev", "abcdef1234567")

	got, err = r.svc.RunModel(r.ctx, r.f.q, r.only("qa"))
	if err != nil {
		t.Fatal(err)
	}
	if got.ExcludeFamily != "openai" || got.ProfileID.Valid {
		t.Fatalf("qa run model = %+v, want openai excluded", got)
	}

	bad, _ := json.Marshal("dev:" + util.UUIDToString(r.agents["dev"]) + "[profile=нет такого],close:" + util.UUIDToString(r.agents["lead"]))
	r2 := newConveyorRig(t)
	if _, err := r2.apply(ConveyorCommand{Action: ConveyorCmdEnable, Route: bad}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unknown profile: %v", err)
	}
}
