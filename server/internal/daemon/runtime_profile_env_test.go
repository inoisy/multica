package daemon

import (
	"slices"
	"testing"

	"github.com/multica-ai/multica/server/pkg/agent"
)

// A run on a custom-profile runtime carries the profile id, so the profile's
// launch wrapper (subs-run) can look up the subscriptions it spends.
func TestRuntimeProfileIDForRun(t *testing.T) {
	d := freshDaemon("")
	d.runtimeIndex["rt-pool"] = Runtime{ID: "rt-pool", Provider: "claude", ProfileID: "prof-pool"}
	d.runtimeIndex["rt-builtin"] = Runtime{ID: "rt-builtin", Provider: "claude"}

	if got := d.runtimeProfileID("rt-pool"); got != "prof-pool" {
		t.Errorf("profile runtime: got %q, want prof-pool", got)
	}
	for _, id := range []string{"rt-builtin", "rt-unknown", ""} {
		if got := d.runtimeProfileID(id); got != "" {
			t.Errorf("runtime %q: got %q, want empty", id, got)
		}
	}
}

// Subscription pools run `subs-run <family>`: the family word is a positional
// launch prefix, so it survives the prefix filter and reaches both the run and
// the `--version` probe, which runs without any task environment.
func TestSubscriptionPoolFamilyWordSurvivesLaunchPrefix(t *testing.T) {
	for _, family := range []string{"claude", "codex"} {
		got := agent.FilterLaunchPrefix(family, []string{family}, nil)
		if !slices.Equal(got, []string{family}) {
			t.Errorf("%s: prefix = %v, want [%s]", family, got, family)
		}
		argv := agent.NewCommand("/usr/local/bin/subs-run", got).Argv("--version")
		if !slices.Equal(argv, []string{family, "--version"}) {
			t.Errorf("%s: version probe argv = %v", family, argv)
		}
	}
}
