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
