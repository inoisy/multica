package service

import (
	"encoding/json"
	"strings"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	judgedCommit = "abcdef1234567"
	newerCommit  = "1234567abcdef"
)

// docsCarry is what a run's CLI reports from .multica/acceptance.json.
var docsCarry = &ConveyorCarry{Paths: []string{"docs/**"}}

// passWith plays a passing run whose result carries carry.
func (r *conveyorRig) passWith(name, commit string, carry *ConveyorCarry) {
	r.t.Helper()
	tasks := r.tasksOf(name)
	task := r.start(tasks[len(tasks)-1])
	in := r.result(ConveyorOutcomePass, commit)
	in.Carry = carry
	r.mustSubmit(task, in)
	r.complete(task)
}

// carriedRig is a route whose review fixed the work with its own commit in a
// repository that lets verdicts carry over docs/**.
func carriedRig(t *testing.T) *conveyorRig {
	t.Helper()
	r := newConveyorRig(t)
	r.withAcceptAgent()
	r.enable("dev", "qa", "review", "accept", "close")
	r.pass("dev", judgedCommit)
	r.pass("qa", judgedCommit)
	r.passWith("review", newerCommit, docsCarry)
	return r
}

// handInAccept plays the accept run with a result carrying what it found.
func (r *conveyorRig) handInAccept(outcome string, carry *ConveyorCarry) {
	r.t.Helper()
	tasks := r.tasksOf("accept")
	task := r.start(tasks[len(tasks)-1])
	in := r.result(outcome, r.view().AcceptedCommit)
	in.Carry = carry
	r.mustSubmit(task, in)
	r.complete(task)
}

func (r *conveyorRig) owedCarry(step string) bool {
	r.t.Helper()
	tasks := r.tasksOf(step)
	action, err := r.f.q.GetIssueConveyorEvent(r.ctx, tasks[len(tasks)-1].ConveyorActionID)
	if err != nil {
		r.t.Fatal(err)
	}
	return actionCarries(action)
}

func (r *conveyorRig) last(name string) db.AgentTaskQueue {
	r.t.Helper()
	tasks := r.tasksOf(name)
	return tasks[len(tasks)-1]
}

// The reviewer's own commit goes to acceptance, not back to qa, when the
// repository lets verdicts carry; a docs-only diff keeps qa's verdict and
// the journal names the files.
func TestConveyorOwnCommitGoesToAcceptWhenVerdictsMayCarry(t *testing.T) {
	r := carriedRig(t)
	view := r.view()
	if view.Step != "accept" || view.AcceptedCommit != newerCommit || len(r.tasksOf("qa")) != 1 || len(r.tasksOf("lead")) != 0 {
		t.Fatalf("after review's own commit: step %s accepted %q qa runs %d;%s", view.Step, view.AcceptedCommit, len(r.tasksOf("qa")), r.dump())
	}
	if !r.owedCarry("accept") {
		t.Fatalf("the accept run is not owed as a carry check")
	}
	if d := r.decisionFor(r.only("review")); d.Action.String != conveyorDecisionAdvance || !strings.Contains(d.Reason.String, "carry") {
		t.Fatalf("review decision %s %q", d.Action.String, d.Reason.String)
	}

	r.handInAccept(ConveyorOutcomePass, &ConveyorCarry{Paths: []string{"docs/**"},
		Carried: []ConveyorCarried{{Step: "qa", From: judgedCommit, Files: []string{"docs/adr/0032-x.md"}}}})
	if view := r.view(); view.Status != ConveyorCompleted {
		t.Fatalf("carried accept: status %s;%s", view.Status, r.dump())
	}
	d := r.decisionFor(r.only("accept"))
	if !strings.Contains(d.Reason.String, "verdicts carried: qa from abcdef123456 (docs/adr/0032-x.md)") || !strings.Contains(string(d.Details), "docs/adr/0032-x.md") {
		t.Fatalf("carry is not journaled: %q %s", d.Reason.String, d.Details)
	}
	r.everyInputDecided()
}

// Code outside the carry paths is rechecked from qa; the reviewer who made
// the commit is passed over, as in a recheck without acceptance.
func TestConveyorAcceptSendsCodeBackToTheVerifier(t *testing.T) {
	r := carriedRig(t)
	round := r.view().Round
	r.handInAccept(ConveyorOutcomeChangesRequested, &ConveyorCarry{Reverify: "qa", Files: []string{"src/app.go"}})
	view := r.view()
	if view.Step != "qa" || view.AcceptedCommit != newerCommit || view.Round != round+1 || len(r.tasksOf("lead")) != 0 {
		t.Fatalf("after accept: step %s accepted %q round %d lead runs %d;%s", view.Step, view.AcceptedCommit, view.Round, len(r.tasksOf("lead")), r.dump())
	}
	if d := r.decisionFor(r.last("accept")); d.Action.String != conveyorDecisionRecheck || !strings.Contains(d.Reason.String, "src/app.go") || !strings.Contains(string(d.Details), "src/app.go") {
		t.Fatalf("decision %s %q %s", d.Action.String, d.Reason.String, d.Details)
	}
	r.pass("qa", newerCommit)
	if view := r.view(); view.Step != "accept" || len(r.tasksOf("review")) != 1 || r.owedCarry("accept") {
		t.Fatalf("after qa on the reviewer's commit: step %s review runs %d carry %v;%s", view.Step, len(r.tasksOf("review")), r.owedCarry("accept"), r.dump())
	}
}

// Without verdict_carry_paths the reviewer's own commit is rechecked as before.
func TestConveyorOwnCommitWithoutCarryPathsIsRechecked(t *testing.T) {
	r := newConveyorRig(t)
	r.withAcceptAgent()
	r.enable("dev", "qa", "review", "accept", "close")
	r.pass("dev", judgedCommit)
	r.pass("qa", judgedCommit)
	r.pass("review", newerCommit)
	if view := r.view(); view.Step != "qa" || view.AcceptedCommit != newerCommit {
		t.Fatalf("step %s accepted %q", view.Step, view.AcceptedCommit)
	}
	if d := r.decisionFor(r.only("review")); d.Action.String != conveyorDecisionRecheck {
		t.Fatalf("decision %s", d.Action.String)
	}
}

// New work after every verifier passed goes straight to accept once the
// repository is known to let verdicts carry; otherwise qa judges it again.
func TestConveyorNewWorkGoesStraightToAcceptWhenVerdictsMayCarry(t *testing.T) {
	for _, tc := range []struct {
		name        string
		accept, dev *ConveyorCarry
		want        string
	}{
		{"acceptance reported carry paths", docsCarry, nil, "accept"},
		{"dev reported carry paths", nil, docsCarry, "accept"},
		{"no carry paths", nil, nil, "qa"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newConveyorRig(t)
			r.withAcceptAgent()
			r.enable("dev", "qa", "review", "accept", "close")
			for _, step := range []string{"dev", "qa", "review"} {
				r.pass(step, judgedCommit)
			}
			// Acceptance found the ADR number taken: the work goes back to dev.
			r.handInAccept(ConveyorOutcomeChangesRequested, tc.accept)
			if view := r.view(); view.Step != "dev" {
				t.Fatalf("red accept: step %s;%s", view.Step, r.dump())
			}
			r.passWith("dev", newerCommit, tc.dev)
			view := r.view()
			if view.Step != tc.want || view.AcceptedCommit != newerCommit {
				t.Fatalf("after new work: step %s accepted %q, want %s;%s", view.Step, view.AcceptedCommit, tc.want, r.dump())
			}
			if tc.want == "accept" && !r.owedCarry("accept") {
				t.Fatalf("accept is not owed as a carry check")
			}
		})
	}
}

// A result may name what acceptance found; the canonical form keeps it.
func TestConveyorResultKeepsCarry(t *testing.T) {
	in := ConveyorResultInput{SchemaVersion: 1, Outcome: ConveyorOutcomePass, Report: "ok",
		Carry: &ConveyorCarry{Carried: []ConveyorCarried{{Step: "qa", From: judgedCommit, Files: []string{}}}}}
	if err := in.normalize(); err != nil {
		t.Fatal(err)
	}
	var back ConveyorResultInput
	if err := json.Unmarshal(in.canonical(), &back); err != nil || back.Carry == nil || back.Carry.Carried[0].Step != "qa" {
		t.Fatalf("round trip: %v %+v", err, back.Carry)
	}
}
