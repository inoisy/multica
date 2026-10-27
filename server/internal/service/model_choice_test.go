package service

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

var choiceNow = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func choiceWorld() ModelChoiceInput {
	return ModelChoiceInput{
		Agent: ModelChoiceAgent{ID: "agent", OwnerID: "owner", RuntimeID: "rt-codex", Model: "gpt", ThinkingLevel: "high"},
		Profiles: map[string]ModelProfileData{
			"strong": {ID: "strong", Name: "Strong", Options: []ModelOption{
				{Position: 1, RuntimeID: "rt-codex", Model: "sol", ThinkingLevel: "high"},
				{Position: 2, RuntimeID: "rt-claude", Model: "opus"},
				{Position: 3, RuntimeID: "rt-glm", Model: "glm"},
			}},
			"mid": {ID: "mid", Name: "Mid", Options: []ModelOption{
				{Position: 1, RuntimeID: "rt-mini", Model: "mini"},
			}},
		},
		Runtimes: map[string]RuntimeAvailability{
			"rt-codex":  {Name: "codex", Provider: "codex", Online: true},
			"rt-claude": {Name: "claude", Provider: "claude", Online: true},
			"rt-glm":    {Name: "glm", Provider: "claude", Online: true},
			"rt-mini":   {Name: "mini", Provider: "codex", Online: true},
		},
	}
}

// personal gives the agent a personal profile of the rows and points the agent
// at it, as the own list used to be set.
func personal(in *ModelChoiceInput, rows ...ModelOption) {
	if in.Profiles == nil {
		in.Profiles = map[string]ModelProfileData{}
	}
	id := "personal-" + in.Agent.ID
	in.Profiles[id] = ModelProfileData{ID: id, Name: "Agent · personal", OwnerAgentID: in.Agent.ID, Options: rows}
	in.Agent.ModelProfileID = id
}

func limit(in *ModelChoiceInput, runtime string, until time.Time) {
	rt := in.Runtimes[runtime]
	rt.UnavailableUntil = until
	in.Runtimes[runtime] = rt
}

func TestModelOptionChoosesFirstAvailableRow(t *testing.T) {
	in := choiceWorld()
	in.Agent.ModelProfileID = "strong"

	c := ChooseModel(in, choiceNow)
	if c.Reason != ModelReasonPrimary || c.Position != 1 || c.RuntimeID() != "rt-codex" || c.Source != ModelSourceAgentProfile {
		t.Fatalf("primary: %+v", c)
	}

	limit(&in, "rt-codex", choiceNow.Add(time.Hour))
	c = ChooseModel(in, choiceNow)
	if c.Reason != ModelReasonFallbackRow || c.Position != 2 || c.RuntimeID() != "rt-claude" {
		t.Fatalf("first row limited: %+v", c)
	}
	if c.Rows[0].State != ModelRowLimited || c.Rows[0].Until == nil {
		t.Fatalf("row 1 must show its limit: %+v", c.Rows[0])
	}

	// An offline runtime and a runtime that is gone are skipped the same way.
	rt := in.Runtimes["rt-claude"]
	rt.Online = false
	in.Runtimes["rt-claude"] = rt
	c = ChooseModel(in, choiceNow)
	if c.Position != 3 || c.Rows[1].State != ModelRowOffline {
		t.Fatalf("offline row 2: %+v", c)
	}
	delete(in.Runtimes, "rt-glm")
	c = ChooseModel(in, choiceNow)
	if !c.Waiting() || c.Rows[2].State != ModelRowMissing {
		t.Fatalf("missing row 3: %+v", c)
	}
}

func TestModelOptionPrivateRuntimeOfAnotherOwnerIsSkipped(t *testing.T) {
	in := choiceWorld()
	in.Agent.ModelProfileID = "strong"
	rt := in.Runtimes["rt-codex"]
	rt.Private, rt.OwnerID = true, "someone-else"
	in.Runtimes["rt-codex"] = rt

	c := ChooseModel(in, choiceNow)
	if c.Position != 2 || c.Rows[0].State != ModelRowNotPermitted {
		t.Fatalf("private row of another owner must be skipped: %+v", c)
	}
	in.Agent.OwnerID = "someone-else"
	if c = ChooseModel(in, choiceNow); c.Position != 1 {
		t.Fatalf("the owner's own private runtime is usable: %+v", c)
	}
}

func TestModelOptionPrimaryReturnsOnceAvailable(t *testing.T) {
	in := choiceWorld()
	in.Agent.ModelProfileID = "strong"
	limit(&in, "rt-codex", choiceNow.Add(30*time.Minute))
	if c := ChooseModel(in, choiceNow); c.Position != 2 {
		t.Fatalf("during the limit: %+v", c)
	}
	// The next run after the reset takes the primary row again, mid-issue or not.
	if c := ChooseModel(in, choiceNow.Add(31*time.Minute)); c.Position != 1 || c.Reason != ModelReasonPrimary {
		t.Fatalf("after the reset: %+v", c)
	}
}

func TestModelOptionListPriority(t *testing.T) {
	in := choiceWorld()

	// No profile, no own list: the plain fields, with their explicit args.
	in.Agent.CustomArgs = []string{"--x"}
	c := ChooseModel(in, choiceNow)
	if c.Source != ModelSourceAgentFields || c.Chosen.Model != "gpt" || !reflect.DeepEqual(c.Chosen.CustomArgs, []string{"--x"}) {
		t.Fatalf("plain fields: %+v", c)
	}

	in.Agent.ModelProfileID = "strong"
	if c = ChooseModel(in, choiceNow); c.Source != ModelSourceAgentProfile || c.ProfileName != "Strong" {
		t.Fatalf("agent profile: %+v", c)
	}

	// A personal profile is the agent's profile; one row pins the model.
	personal(&in, ModelOption{Position: 1, RuntimeID: "rt-claude", Model: "sonnet"})
	if c = ChooseModel(in, choiceNow); c.Source != ModelSourceAgentProfile || !c.ProfilePersonal || c.Chosen.Model != "sonnet" {
		t.Fatalf("personal profile: %+v", c)
	}

	// The issue's profile wins over everything for runs of that issue only.
	in.IssueID, in.IssueProfileID = "issue", "mid"
	if c = ChooseModel(in, choiceNow); c.Source != ModelSourceIssueProfile || c.Chosen.Model != "mini" || c.IssueID != "issue" {
		t.Fatalf("issue profile: %+v", c)
	}
	in.IssueID, in.IssueProfileID = "other", ""
	if c = ChooseModel(in, choiceNow); c.Source != ModelSourceAgentProfile || c.IssueID != "" {
		t.Fatalf("another issue keeps the agent's list: %+v", c)
	}

	// A profile that no longer exists leaves the agent without a list: the
	// projection in its fields never starts a run.
	in.Agent.ModelProfileID = "gone"
	if c = ChooseModel(in, choiceNow); c.Reason != ModelReasonNoModels || !c.Waiting() || c.Source != ModelSourceAgentProfile {
		t.Fatalf("missing profile: %+v", c)
	}
	in.Agent.ModelProfileID, in.Agent.RuntimeID = "", ""
	if c = ChooseModel(in, choiceNow); c.Reason != ModelReasonNoModels || !c.Waiting() {
		t.Fatalf("nothing at all: %+v", c)
	}
}

func TestModelOptionAllUnavailableWaitsUntilEarliestReset(t *testing.T) {
	in := choiceWorld()
	in.Agent.ModelProfileID = "strong"
	early, late := choiceNow.Add(20*time.Minute), choiceNow.Add(3*time.Hour)
	limit(&in, "rt-codex", late)
	limit(&in, "rt-claude", early)
	limit(&in, "rt-glm", late)

	c := ChooseModel(in, choiceNow)
	if !c.Waiting() || c.Reason != ModelReasonAllUnavailable || c.WaitUntil == nil || !c.WaitUntil.Equal(early) {
		t.Fatalf("all limited: %+v", c)
	}
	if got := c.LimitedRuntimes(); len(got) != 3 {
		t.Fatalf("limited runtimes: %v", got)
	}
	if !strings.Contains(c.Summary(), "the run waits") {
		t.Fatalf("summary: %s", c.Summary())
	}
}

func TestModelOptionExhaustedProfileFallsBack(t *testing.T) {
	in := choiceWorld()
	in.Agent.ModelProfileID = "strong"
	strong := in.Profiles["strong"]
	strong.WhenExhaustedProfileID = "mid"
	in.Profiles["strong"] = strong
	for _, rt := range []string{"rt-codex", "rt-claude", "rt-glm"} {
		limit(&in, rt, choiceNow.Add(time.Hour))
	}

	c := ChooseModel(in, choiceNow)
	if c.Reason != ModelReasonFallbackProfile || c.ProfileName != "Mid" || c.Chosen.Model != "mini" ||
		!reflect.DeepEqual(c.ExhaustedProfiles, []string{"Strong"}) || c.WaitUntil != nil {
		t.Fatalf("fallback profile: %+v", c)
	}
	if !strings.Contains(c.Summary(), `"Strong"`) {
		t.Fatalf("summary names the exhausted profile: %s", c.Summary())
	}

	// A cycle in stored data cannot loop: each profile is walked once.
	mid := in.Profiles["mid"]
	mid.WhenExhaustedProfileID = "strong"
	in.Profiles["mid"] = mid
	limit(&in, "rt-mini", choiceNow.Add(time.Hour))
	if c = ChooseModel(in, choiceNow); !c.Waiting() || !reflect.DeepEqual(c.ExhaustedProfiles, []string{"Strong", "Mid"}) {
		t.Fatalf("cycle: %+v", c)
	}

	// A migrated personal profile has no exhausted-profile: it waits.
	personal(&in, ModelOption{Position: 1, RuntimeID: "rt-codex"})
	if c = ChooseModel(in, choiceNow); !c.Waiting() || c.Source != ModelSourceAgentProfile || !c.ProfilePersonal {
		t.Fatalf("personal profile waits: %+v", c)
	}
}

func TestModelOptionExhaustedChainRejectsCycles(t *testing.T) {
	if err := checkExhaustedChain("a", map[string]string{"a": "b", "b": "c"}); err != nil {
		t.Fatalf("chain without cycle: %v", err)
	}
	if err := checkExhaustedChain("a", map[string]string{"a": "b", "b": "a"}); err == nil {
		t.Fatal("cycle must be refused")
	}
}

func TestModelOptionDisallowedToolsRenderAlikeForClaudeAndCodex(t *testing.T) {
	tools := []string{"github:create_issue", "github:delete_repo", "multica:issue_delete"}
	claude := RenderDisallowedTools("claude", tools)
	codex := RenderDisallowedTools("codex", tools)
	if !reflect.DeepEqual(claude, []string{"--disallowedTools", "mcp__github__create_issue,mcp__github__delete_repo,mcp__multica__issue_delete"}) {
		t.Fatalf("claude: %v", claude)
	}
	if !reflect.DeepEqual(codex, []string{
		"-c", `mcp_servers.github.disabled_tools=["create_issue","delete_repo"]`,
		"-c", `mcp_servers.multica.disabled_tools=["issue_delete"]`,
	}) {
		t.Fatalf("codex: %v", codex)
	}
	// Both renderings mean the same set: reading them back gives the input.
	for name, args := range map[string][]string{"claude": claude, "codex": codex} {
		back, rest := SplitDisallowedArgs(args)
		got, _ := NormalizeDisallowedTools(back)
		if !reflect.DeepEqual(got, tools) || len(rest) != 0 {
			t.Fatalf("%s round trip: %v rest %v", name, got, rest)
		}
	}
	if got := RenderDisallowedTools("opencode", tools); len(got) != 0 {
		t.Fatalf("other families get no args: %v", got)
	}
	// A bare name is a Claude built-in; Codex has no switch for it.
	if got := RenderDisallowedTools("codex", []string{"Bash"}); len(got) != 0 {
		t.Fatalf("codex bare: %v", got)
	}
	if got := RenderDisallowedTools("claude", []string{"Bash"}); !reflect.DeepEqual(got, []string{"--disallowedTools", "Bash"}) {
		t.Fatalf("claude bare: %v", got)
	}
}

func TestModelOptionCustomArgsDerivedOnlyWhenRowHasNone(t *testing.T) {
	in := choiceWorld()
	in.Agent.ModelProfileID = "strong"
	in.Agent.DisallowedTools = []string{"github:delete_repo"}

	c := ChooseModel(in, choiceNow)
	if !reflect.DeepEqual(c.Chosen.CustomArgs, []string{"-c", `mcp_servers.github.disabled_tools=["delete_repo"]`}) {
		t.Fatalf("derived for codex: %v", c.Chosen.CustomArgs)
	}
	limit(&in, "rt-codex", choiceNow.Add(time.Hour))
	if c = ChooseModel(in, choiceNow); !reflect.DeepEqual(c.Chosen.CustomArgs, []string{"--disallowedTools", "mcp__github__delete_repo"}) {
		t.Fatalf("derived for claude: %v", c.Chosen.CustomArgs)
	}
	// Explicit args of a row go through untranslated, even when empty.
	p := in.Profiles["strong"]
	p.Options[1].CustomArgs = []string{}
	in.Profiles["strong"] = p
	if c = ChooseModel(in, choiceNow); c.Chosen.CustomArgs == nil || len(c.Chosen.CustomArgs) != 0 {
		t.Fatalf("explicit empty: %v", c.Chosen.CustomArgs)
	}
}

func TestModelOptionDisallowedToolValidation(t *testing.T) {
	got, err := NormalizeDisallowedTools([]string{" github:b ", "github:a", "github:a", "Bash"})
	if err != nil || !reflect.DeepEqual(got, []string{"Bash", "github:a", "github:b"}) {
		t.Fatalf("normalize: %v %v", got, err)
	}
	for _, bad := range []string{"", ":tool", "server:", "a b:c", `x:y"`, "x:y,z"} {
		if _, err := NormalizeDisallowedTools([]string{bad}); err == nil {
			t.Fatalf("%q must be refused", bad)
		}
	}
}
