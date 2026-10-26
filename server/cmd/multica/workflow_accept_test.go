package main

import (
	"testing"
)

func TestAcceptWorkflowTarget(t *testing.T) {
	view := workflowAcceptView{Status: "merge", NextStatus: "done", BackStatus: "dev"}
	noBack := workflowAcceptView{Status: "merge", NextStatus: "done"}
	cases := []struct {
		name    string
		outcome acceptOutcome
		view    workflowAcceptView
		want    string
	}{
		{"pass goes on", acceptOutcome{Outcome: "pass"}, view, "done"},
		{"red checks go back", acceptOutcome{Outcome: "changes_requested"}, view, "dev"},
		{"red checks without a return block", acceptOutcome{Outcome: "changes_requested"}, noBack, "blocked"},
		{"busy resource blocks", acceptOutcome{Outcome: "blocked"}, view, "blocked"},
		{"unjudged work blocks", coordinator("checkout failed"), view, "blocked"},
	}
	for _, c := range cases {
		if got := acceptWorkflowTarget(c.outcome, c.view); got != c.want {
			t.Errorf("%s: target = %q, want %q", c.name, got, c.want)
		}
	}
}
