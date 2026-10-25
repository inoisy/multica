package main

import (
	"strings"
	"testing"
)

func args(a ...string) *[]string {
	out := append([]string{}, a...)
	return &out
}

func snap(contexts ...contextResult) snapshot {
	s := snapshot{Contexts: contexts}
	finishManifest(&s, manifest{Now: "2026-10-24T12:00:00Z", WorkspaceID: "ws"})
	return s
}

func ctxOf(key string, c choice) contextResult {
	return contextResult{Key: "agent/" + key, Kind: kindAgent, Choice: &c}
}

func baseChoice() choice {
	return choice{
		Source: "agent_list", Reason: "primary", Position: 1,
		Chosen: &option{RuntimeID: "r1", Model: "m", ThinkingLevel: "high", CustomArgs: args("--x")},
		Rows:   []row{{option: option{RuntimeID: "r1", Model: "m", ThinkingLevel: "high", CustomArgs: args("--x")}, State: "available", Chosen: true}},
	}
}

func TestIdenticalSnapshotsHaveNoDifferences(t *testing.T) {
	diffs, normalized := compareSnapshots(snap(ctxOf("a", baseChoice())), snap(ctxOf("a", baseChoice())))
	if len(diffs) != 0 || normalized != 0 {
		t.Fatalf("diffs=%v normalized=%d", diffs, normalized)
	}
}

func TestPersonalProfileNormalizationIsNarrow(t *testing.T) {
	after := baseChoice()
	after.Source, after.ProfileID, after.ProfileName, after.ProfilePersonal = "agent_profile", "p1", "Agent · personal", true
	after.Rows[0].ProfileID = "p1"
	diffs, normalized := compareSnapshots(snap(ctxOf("a", baseChoice())), snap(ctxOf("a", after)))
	if len(diffs) != 0 || normalized != 1 {
		t.Fatalf("diffs=%v normalized=%d", diffs, normalized)
	}

	// A changed row is still a difference inside a normalized context.
	after.Rows[0].State = "limited"
	diffs, _ = compareSnapshots(snap(ctxOf("a", baseChoice())), snap(ctxOf("a", after)))
	if len(diffs) != 1 || diffs[0].Path != "choice.rows[0].state" {
		t.Fatalf("diffs=%v", diffs)
	}
}

func TestPersonalNormalizationPreservesFallbackProfiles(t *testing.T) {
	before := baseChoice()
	after := baseChoice()
	after.Source, after.ProfileID, after.ProfileName, after.ProfilePersonal = "agent_profile", "personal", "Personal", true
	after.ExhaustedProfiles = []string{"Personal", "Shared fallback"}
	if diffs, _ := compareSnapshots(snap(ctxOf("a", before)), snap(ctxOf("a", after))); len(diffs) == 0 {
		t.Fatal("normalization hid a changed fallback graph")
	}
}

func TestMatchingErrorsAreNotAnEquivalenceProof(t *testing.T) {
	s := snap(contextResult{Key: "agent/a", Kind: kindAgent, Error: "choice failed"})
	if diffs, _ := compareSnapshots(s, s); len(diffs) == 0 {
		t.Fatal("failed previews were counted as equal choices")
	}
}

func TestSharedProfileSourceChangeIsADifference(t *testing.T) {
	before := baseChoice()
	before.Source = "agent_profile"
	after := baseChoice()
	after.Source, after.ProfilePersonal = "agent_profile", true
	diffs, normalized := compareSnapshots(snap(ctxOf("a", before)), snap(ctxOf("a", after)))
	if normalized != 0 || len(diffs) == 0 {
		t.Fatalf("a personal profile that replaced a shared one must show: diffs=%v", diffs)
	}
}

func TestNullAndEmptyArgsDiffer(t *testing.T) {
	after := baseChoice()
	after.Chosen.CustomArgs = nil
	after.Rows[0].CustomArgs = args()
	diffs, _ := compareSnapshots(snap(ctxOf("a", baseChoice())), snap(ctxOf("a", after)))
	if len(diffs) != 2 {
		t.Fatalf("diffs=%v", diffs)
	}
	nullVsEmpty := baseChoice()
	nullVsEmpty.Chosen.CustomArgs = nil
	other := baseChoice()
	other.Chosen.CustomArgs = args()
	diffs, _ = compareSnapshots(snap(ctxOf("a", nullVsEmpty)), snap(ctxOf("a", other)))
	if len(diffs) != 1 || diffs[0].Path != "choice.chosen.custom_args" {
		t.Fatalf("NULL and [] must not be merged: %v", diffs)
	}
}

func TestMissingAndAddedContextsAreDifferences(t *testing.T) {
	diffs, _ := compareSnapshots(snap(ctxOf("a", baseChoice())), snap(ctxOf("b", baseChoice())))
	var kinds []string
	for _, d := range diffs {
		kinds = append(kinds, d.Path)
	}
	joined := strings.Join(kinds, ",")
	for _, want := range []string{"context", "context_set_sha256"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected %q in %v", want, kinds)
		}
	}
}

func TestReportDropsNamesAndShortensIDs(t *testing.T) {
	got := anon("choice.profile_name", "Secret profile")
	if strings.Contains(got, "Secret") {
		t.Fatalf("name leaked: %s", got)
	}
	got = anon("choice.chosen.runtime_id", "0194b0a0-0000-7000-8000-000000000001")
	if strings.Contains(got, "0194b0a0") || !strings.HasPrefix(got, `"id:`) {
		t.Fatalf("id not shortened: %s", got)
	}
}

// fullWindow is the accepted change seen on the production copy: the only row
// is limited by a window at 100 % and the run now waits for its reset.
func fullWindow() choice {
	after := baseChoice()
	until := "2026-10-26T05:34:35Z"
	after.Reason, after.Position, after.Chosen, after.WaitUntil = "all_unavailable", 0, nil, &until
	after.Rows[0].State, after.Rows[0].Until, after.Rows[0].ReasonCode, after.Rows[0].Chosen = "limited", &until, "provider_limit", false
	return after
}

func TestFullWindowIsAcceptedOnlyWhenEveryDifferenceFollowsFromIt(t *testing.T) {
	before, after := snap(ctxOf("a", baseChoice()), ctxOf("b", baseChoice())), snap(ctxOf("a", fullWindow()), ctxOf("b", baseChoice()))
	diffs, _ := compareSnapshots(before, after)
	// The seven fields per context recorded on the production copy (STT-424).
	if len(diffs) != 7 {
		t.Fatalf("diffs=%v", diffs)
	}
	if got := fullWindowContexts(before, after, diffs); !got["agent/a"] || len(got) != 1 {
		t.Fatalf("accepted=%v", got)
	}

	cases := map[string]func(c *choice){
		"refusal":            func(c *choice) { c.Rows[0].ReasonCode = "provider_refusal" },
		"agent cap":          func(c *choice) { c.Rows[0].ReasonCode = "agent_cap" },
		"offline":            func(c *choice) { c.Rows[0].State, c.Rows[0].ReasonCode = "offline", "offline" },
		"no reset":           func(c *choice) { c.Rows[0].Until = nil },
		"other wait":         func(c *choice) { other := "2026-10-27T00:00:00Z"; c.WaitUntil = &other },
		"changed args":       func(c *choice) { c.Rows[0].CustomArgs = args("--y") },
		"changed source":     func(c *choice) { c.Source = "agent_profile" },
		"runs without a row": func(c *choice) { c.Chosen = &option{RuntimeID: "r1", Model: "m"} },
	}
	for name, mutate := range cases {
		changed := fullWindow()
		mutate(&changed)
		after := snap(ctxOf("a", changed))
		diffs, _ := compareSnapshots(snap(ctxOf("a", baseChoice())), after)
		if got := fullWindowContexts(snap(ctxOf("a", baseChoice())), after, diffs); len(got) != 0 {
			t.Errorf("%s: accepted %v, diffs=%v", name, got, diffs)
		}
	}
}

func TestFullWindowFallbackToAnAvailableRowIsAccepted(t *testing.T) {
	second := option{RuntimeID: "r2", Model: "m2", CustomArgs: args()}
	before := baseChoice()
	before.Rows = append(before.Rows, row{option: second, State: "available"})
	after := fullWindow()
	after.Reason, after.Position, after.WaitUntil = "fallback_row", 2, nil
	after.Chosen = &second
	after.Rows = append(after.Rows, row{option: second, State: "available", Chosen: true})
	b, a := snap(ctxOf("a", before)), snap(ctxOf("a", after))
	diffs, _ := compareSnapshots(b, a)
	if got := fullWindowContexts(b, a, diffs); !got["agent/a"] {
		t.Fatalf("accepted=%v diffs=%v", got, diffs)
	}

	// Falling back onto a row the original algorithm held back is a change.
	before.Rows[1].State = "limited"
	b = snap(ctxOf("a", before))
	diffs, _ = compareSnapshots(b, a)
	if got := fullWindowContexts(b, a, diffs); len(got) != 0 {
		t.Fatalf("accepted=%v diffs=%v", got, diffs)
	}
}

func TestReasonCodeIsComparedOnlyWhenBothSnapshotsHaveIt(t *testing.T) {
	limited := fullWindow()
	if diffs, _ := compareSnapshots(snap(ctxOf("a", limited)), snap(ctxOf("a", fullWindow()))); len(diffs) != 0 {
		t.Fatalf("diffs=%v", diffs)
	}
	refused := fullWindow()
	refused.Rows[0].ReasonCode = "provider_refusal"
	diffs, _ := compareSnapshots(snap(ctxOf("a", limited)), snap(ctxOf("a", refused)))
	if len(diffs) != 1 || diffs[0].Path != "choice.rows[0].reason_code" {
		t.Fatalf("diffs=%v", diffs)
	}
}
