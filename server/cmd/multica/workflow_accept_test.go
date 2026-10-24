package main

import (
	"errors"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
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

func TestNoLiveRoute(t *testing.T) {
	notFound := &cli.HTTPError{StatusCode: http.StatusNotFound}
	if !noLiveRoute(notFound, "") {
		t.Error("an issue without a route must use the Workflow acceptance")
	}
	if !noLiveRoute(nil, "completed") || !noLiveRoute(nil, "disabled") {
		t.Error("a finished route must use the Workflow acceptance")
	}
	if noLiveRoute(nil, "running") {
		t.Error("a live route keeps its own acceptance")
	}
	if noLiveRoute(errors.New("connection refused"), "") {
		t.Error("a failed read must not switch modes")
	}
}
