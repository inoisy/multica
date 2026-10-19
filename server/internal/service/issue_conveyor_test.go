package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// conveyorRig is one routed issue with a dev, a qa, a review and a lead agent.
// Agents are only rows: nothing here starts a real run, the tests play the
// part of the daemon by moving task rows through their statuses.
type conveyorRig struct {
	t      *testing.T
	f      principalFixture
	svc    *IssueConveyorService
	ctx    context.Context
	issue  pgtype.UUID
	owner  pgtype.UUID
	agents map[string]pgtype.UUID
}

func newConveyorRig(t *testing.T) *conveyorRig {
	t.Helper()
	f, owner := newPrincipalFixture(t)
	if err := f.q.SeedIssueStatusEntries(context.Background(), parseTestUUID(t, f.WorkspaceID)); err != nil {
		t.Fatal(err)
	}
	r := &conveyorRig{t: t, f: f, ctx: context.Background(), owner: parseTestUUID(t, owner), agents: map[string]pgtype.UUID{}}
	r.issue = parseTestUUID(t, f.Issue(t, "Routed issue"))
	for _, name := range []string{"dev", "qa", "review", "lead"} {
		r.agents[name] = parseTestUUID(t, f.privateAgentOwnedBy(t, owner, name))
	}
	// Service-created rows go before the fixture owners.
	f.Cleanup(t, "DELETE FROM issue_conveyor_event WHERE issue_id=$1", r.issue)
	f.Cleanup(t, "DELETE FROM issue_conveyor WHERE issue_id=$1", r.issue)
	f.Cleanup(t, "DELETE FROM issue_wakeup WHERE issue_id=$1", r.issue)
	f.Cleanup(t, "DELETE FROM issue_wakeup_receipt WHERE wakeup_id IN (SELECT id FROM issue_wakeup WHERE issue_id=$1)", r.issue)
	f.Cleanup(t, "DELETE FROM comment WHERE issue_id=$1", r.issue)
	f.Cleanup(t, "DELETE FROM agent_task_queue WHERE issue_id=$1", r.issue)
	r.svc = &IssueConveyorService{Tasks: f.svc.TaskSvc}
	return r
}

func (r *conveyorRig) route(steps ...string) string {
	parts := make([]string, len(steps))
	for i, s := range steps {
		parts[i] = s + ":" + util.UUIDToString(r.agents[routeAgent(s)])
	}
	return strings.Join(parts, ",")
}

// routeAgent names the agent a step is given to in the standard route.
func routeAgent(step string) string {
	if step == "close" {
		return "lead"
	}
	return step
}

func (r *conveyorRig) loadIssue() db.Issue {
	r.t.Helper()
	issue, err := r.f.q.GetIssue(r.ctx, r.issue)
	if err != nil {
		r.t.Fatal(err)
	}
	return issue
}

func (r *conveyorRig) apply(cmd ConveyorCommand) (ConveyorView, error) {
	return r.svc.Apply(r.ctx, r.loadIssue(), r.owner, cmd)
}

func (r *conveyorRig) mustApply(cmd ConveyorCommand) ConveyorView {
	r.t.Helper()
	view, err := r.apply(cmd)
	if err != nil {
		r.t.Fatalf("%s: %v", cmd.Action, err)
	}
	return view
}

func (r *conveyorRig) enable(steps ...string) ConveyorView {
	r.t.Helper()
	if len(steps) == 0 {
		steps = []string{"dev", "qa", "review", "close"}
	}
	raw, _ := json.Marshal(r.route(steps...))
	return r.mustApply(ConveyorCommand{Action: ConveyorCmdEnable, Route: raw})
}

func (r *conveyorRig) revision() int64 {
	r.t.Helper()
	view, err := r.svc.View(r.ctx, r.issue)
	if err != nil {
		r.t.Fatal(err)
	}
	return view.Revision
}

func (r *conveyorRig) view() ConveyorView {
	r.t.Helper()
	view, err := r.svc.View(r.ctx, r.issue)
	if err != nil {
		r.t.Fatal(err)
	}
	return view
}

func (r *conveyorRig) task(id string) db.AgentTaskQueue {
	r.t.Helper()
	task, err := r.f.q.GetAgentTask(r.ctx, parseTestUUID(r.t, id))
	if err != nil {
		r.t.Fatal(err)
	}
	return task
}

// tasksOf are the runs of an agent on the issue, oldest first.
func (r *conveyorRig) tasksOf(name string) []db.AgentTaskQueue {
	r.t.Helper()
	rows, err := r.f.Pool.Query(r.ctx, "SELECT id FROM agent_task_queue WHERE issue_id=$1 AND agent_id=$2 ORDER BY created_at, id", r.issue, r.agents[name])
	if err != nil {
		r.t.Fatal(err)
	}
	var ids []pgtype.UUID
	for rows.Next() {
		var id pgtype.UUID
		if err := rows.Scan(&id); err != nil {
			r.t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	out := make([]db.AgentTaskQueue, 0, len(ids))
	for _, id := range ids {
		out = append(out, r.task(util.UUIDToString(id)))
	}
	return out
}

// only is the single run an agent has on the issue.
func (r *conveyorRig) only(name string) db.AgentTaskQueue {
	r.t.Helper()
	tasks := r.tasksOf(name)
	if len(tasks) != 1 {
		r.t.Fatalf("%s has %d runs on the issue, want 1", name, len(tasks))
	}
	return tasks[0]
}

func (r *conveyorRig) start(task db.AgentTaskQueue) db.AgentTaskQueue {
	r.t.Helper()
	r.f.Exec(r.t, "UPDATE agent_task_queue SET status='running',dispatched_at=clock_timestamp(),started_at=clock_timestamp() WHERE id=$1", task.ID)
	return r.task(util.UUIDToString(task.ID))
}

func (r *conveyorRig) result(outcome, commit string) ConveyorResultInput {
	return ConveyorResultInput{SchemaVersion: 1, Outcome: outcome, Artifact: ConveyorArtifact{Repo: "r", Branch: "b", Commit: commit},
		Checks: []ConveyorCheck{{Name: "tests", Command: "make test", OK: true}}, Report: "step report"}
}

func (r *conveyorRig) submit(task db.AgentTaskQueue, in ConveyorResultInput) (ConveyorSubmission, error) {
	return r.svc.SubmitResult(r.ctx, r.loadIssue(), task.ID, task.AgentID, in)
}

func (r *conveyorRig) mustSubmit(task db.AgentTaskQueue, in ConveyorResultInput) ConveyorSubmission {
	r.t.Helper()
	sub, err := r.submit(task, in)
	if err != nil {
		r.t.Fatalf("submit: %v", err)
	}
	return sub
}

func (r *conveyorRig) complete(task db.AgentTaskQueue) {
	r.t.Helper()
	if _, err := r.f.svc.TaskSvc.CompleteTask(r.ctx, task.ID, []byte(`{"output":"done"}`), "", "", "", false, "", ""); err != nil {
		r.t.Fatal(err)
	}
}

func (r *conveyorRig) fail(task db.AgentTaskQueue, reason, message string) {
	r.t.Helper()
	if _, err := r.f.svc.TaskSvc.FailTask(r.ctx, task.ID, message, "", "", "", reason, false, "", ""); err != nil {
		r.t.Fatal(err)
	}
}

// pass plays a whole executor run: start, hand in a passing result, end.
func (r *conveyorRig) pass(name, commit string) {
	r.t.Helper()
	tasks := r.tasksOf(name)
	task := r.start(tasks[len(tasks)-1])
	r.mustSubmit(task, r.result(ConveyorOutcomePass, commit))
	r.complete(task)
}

func (r *conveyorRig) events() []db.IssueConveyorEvent {
	r.t.Helper()
	events, err := r.svc.Events(r.ctx, r.issue, 0, 200)
	if err != nil {
		r.t.Fatal(err)
	}
	return events
}

func (r *conveyorRig) eventsOf(kind string) []db.IssueConveyorEvent {
	var out []db.IssueConveyorEvent
	for _, e := range r.events() {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func (r *conveyorRig) decisionFor(task db.AgentTaskQueue) db.IssueConveyorEvent {
	r.t.Helper()
	for _, e := range r.eventsOf(conveyorEventDecision) {
		if e.SourceTaskID == task.ID {
			return e
		}
	}
	r.t.Fatalf("no decision for run %s; journal: %s", util.UUIDToString(task.ID), r.dump())
	return db.IssueConveyorEvent{}
}

// dump renders the journal the way an operator would read it, for failures.
func (r *conveyorRig) dump() string {
	var b strings.Builder
	for _, e := range r.events() {
		fmt.Fprintf(&b, "\n  #%d %s action=%s state=%s reason=%q covered_by=%q", e.Seq.Int64, e.Kind, e.Action.String, e.State.String, e.Reason.String, e.CoveredBy.String)
	}
	return b.String()
}

// everyInputDecided is the diagnosis rule: no run that reached the route is
// left without an outcome in the journal.
func (r *conveyorRig) everyInputDecided() {
	r.t.Helper()
	decided := map[pgtype.UUID]bool{}
	for _, d := range r.eventsOf(conveyorEventDecision) {
		decided[d.SourceTaskID] = true
	}
	for _, in := range r.eventsOf(conveyorEventInput) {
		if !decided[in.SourceTaskID] {
			r.t.Fatalf("input of run %s has no decision:%s", util.UUIDToString(in.SourceTaskID), r.dump())
		}
	}
}

func (r *conveyorRig) metadataStep() string {
	var step string
	r.f.QueryRow(r.t, "SELECT COALESCE(metadata->>'step','') FROM issue WHERE id=$1", r.issue).Scan(&step)
	return step
}

func TestConveyorHappyPath(t *testing.T) {
	r := newConveyorRig(t)
	view := r.enable()
	if view.Status != ConveyorActive || view.Step != "dev" {
		t.Fatalf("enable: status %s step %s", view.Status, view.Step)
	}
	if got := r.loadIssue().Status; got != "in_progress" {
		t.Fatalf("dev issue status = %s, want in_progress", got)
	}
	dev := r.only("dev")
	if !dev.ConveyorActionID.Valid || !dev.OriginatorUserID.Valid || dev.OriginatorUserID != r.owner {
		t.Fatalf("first run is not the route's run for the person who enabled it: %+v", dev)
	}
	if !dev.HandoffNote.Valid || !strings.Contains(dev.HandoffNote.String, "conveyor result") {
		t.Fatalf("first run does not tell the executor how to hand its step in: %q", dev.HandoffNote.String)
	}
	const commit = "abcdef1234567"
	r.pass("dev", commit)
	if got := len(r.tasksOf("lead")); got != 0 {
		t.Fatalf("lead was woken after dev (%d runs)", got)
	}
	r.only("qa")
	if got := r.loadIssue().Status; got != "qa" {
		t.Fatalf("qa issue status = %s, want qa", got)
	}
	r.pass("qa", commit)
	if got := len(r.tasksOf("lead")); got != 0 {
		t.Fatalf("lead was woken after qa (%d runs)", got)
	}
	r.only("review")
	if got := r.loadIssue().Status; got != "review" {
		t.Fatalf("review issue status = %s, want review", got)
	}
	r.pass("review", commit)

	lead := r.only("lead")
	if !strings.Contains(lead.HandoffNote.String, "Все шаги пройдены") {
		t.Fatalf("final coordinator run does not say the route is done: %q", lead.HandoffNote.String)
	}
	for _, name := range []string{"dev", "qa", "review"} {
		r.only(name)
	}
	view = r.view()
	if view.Status != ConveyorCompleted || r.metadataStep() != "close" {
		t.Fatalf("end state: status %s, metadata.step %q", view.Status, r.metadataStep())
	}
	if status := r.loadIssue().Status; status != "in_review" {
		t.Fatalf("final issue status = %s, want in_review", status)
	}
	if got := len(r.eventsOf(conveyorEventInput)); got != 3 {
		t.Fatalf("journal has %d inputs, want 3:%s", got, r.dump())
	}
	r.everyInputDecided()
}

func TestConveyorFailureReasonOwnsQuotaDecision(t *testing.T) {
	cases := []struct {
		name, reason, message string
		wait                  bool
	}{
		{"test 429", "agent_error", "tests failed: expected status 200, got 429", false},
		{"disk quota", "agent_error", "write /tmp/x: disk quota exceeded", false},
		{"rate limit test", "agent_error", "TestRateLimitMiddleware failed: rate limit not applied", false},
		{"explicit unknown", taskfailure.ReasonAgentUnknown.String(), "quota", false},
		{"unrecognized reason", "disk_quota_exceeded", "quota", false},
		{"provider quota", taskfailure.ReasonAgentProviderQuotaLimit.String(), "provider stopped", true},
		{"provider capacity", taskfailure.ReasonAgentProviderCapacityOrRateLimit.String(), "provider stopped", true},
		{"legacy quota", "", "usage limit reached", true},
		{"legacy crash", "", "agent crashed", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newConveyorRig(t)
			r.enable()
			task := r.start(r.only("dev"))
			r.fail(task, tc.reason, tc.message)
			want, leads := ConveyorAwaitingCoordinator, 1
			if tc.wait {
				want, leads = ConveyorWaitingProvider, 0
			}
			if got := r.view().Status; got != want {
				t.Fatalf("route status = %s, want %s", got, want)
			}
			if got := len(r.tasksOf("lead")); got != leads {
				t.Fatalf("coordinator runs = %d, want %d", got, leads)
			}
			r.everyInputDecided()
		})
	}
}

func TestConveyorEnableValidation(t *testing.T) {
	r := newConveyorRig(t)
	route := func(spec string) error {
		raw, _ := json.Marshal(spec)
		_, err := r.apply(ConveyorCommand{Action: ConveyorCmdEnable, Route: raw})
		return err
	}
	a := func(step string) string { return step + ":" + util.UUIDToString(r.agents[routeAgent(step)]) }
	for name, tc := range map[string]struct {
		spec string
		want error
	}{
		"no coordinator":      {a("dev") + "," + a("qa"), ErrConveyorInvalid},
		"dev without qa":      {a("dev") + "," + a("close"), ErrConveyorInvalid},
		"unknown agent":       {"dev:nobody," + a("qa") + "," + a("close"), ErrConveyorInvalid},
		"close in the middle": {a("dev") + "," + a("close") + "," + a("qa") + "," + a("close"), ErrConveyorInvalid},
	} {
		if err := route(tc.spec); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", name, err, tc.want)
		}
	}
	r.f.Exec(t, `UPDATE issue SET metadata='{"risky":true}'::jsonb WHERE id=$1`, r.issue)
	if err := route(a("dev") + "," + a("qa") + "," + a("close")); !errors.Is(err, ErrConveyorInvalid) {
		t.Errorf("risky issue without review: %v", err)
	}
	// A person who may not invoke an agent cannot put it on a route.
	stranger := parseTestUUID(t, r.f.member(t, "stranger"))
	raw, _ := json.Marshal(a("dev") + "," + a("qa") + "," + a("review") + "," + a("close"))
	if _, err := r.svc.Apply(r.ctx, r.loadIssue(), stranger, ConveyorCommand{Action: ConveyorCmdEnable, Route: raw}); !errors.Is(err, ErrConveyorForbidden) {
		t.Errorf("enable by a person without invoke rights: %v", err)
	}
	if got := len(r.tasksOf("dev")); got != 0 {
		t.Fatalf("a refused enable started %d runs", got)
	}
	// metadata.route is the source when the request names none.
	r.f.Exec(t, `UPDATE issue SET metadata=jsonb_build_object('risky',true,'route',$2::text) WHERE id=$1`, r.issue, r.route("dev", "qa", "review", "close"))
	if view := r.mustApply(ConveyorCommand{Action: ConveyorCmdEnable}); len(view.Route) != 4 {
		t.Fatalf("route from metadata has %d nodes", len(view.Route))
	}
	// The same step can appear twice; positions are by index.
	if _, err := r.apply(ConveyorCommand{Action: ConveyorCmdEnable, ExpectedRevision: r.revision()}); !errors.Is(err, ErrConveyorConflict) {
		t.Errorf("enable over a live route: %v", err)
	}
}

func TestConveyorEscalation(t *testing.T) {
	const commit, other = "abcdef1234567", "1234567abcdef"
	type setup func(r *conveyorRig)
	cases := map[string]struct {
		play   setup
		reason string
	}{
		"changes_requested": {func(r *conveyorRig) {
			r.pass("dev", commit)
			task := r.start(r.only("qa"))
			r.mustSubmit(task, r.result(ConveyorOutcomeChangesRequested, commit))
			r.complete(task)
		}, "changes_requested"},
		"blocked": {func(r *conveyorRig) {
			task := r.start(r.only("dev"))
			r.mustSubmit(task, r.result(ConveyorOutcomeBlocked, ""))
			r.complete(task)
		}, "blocked"},
		"needs_coordinator": {func(r *conveyorRig) {
			task := r.start(r.only("dev"))
			r.mustSubmit(task, r.result(ConveyorOutcomeNeedsCoordinator, ""))
			r.complete(task)
		}, "needs_coordinator"},
		"completed without a result": {func(r *conveyorRig) {
			r.complete(r.start(r.only("dev")))
		}, "without handing in a result"},
		"qa judged another commit": {func(r *conveyorRig) {
			r.pass("dev", commit)
			task := r.start(r.only("qa"))
			r.mustSubmit(task, r.result(ConveyorOutcomePass, other))
			r.complete(task)
		}, "accepted one"},
		"failed run": {func(r *conveyorRig) {
			r.fail(r.start(r.only("dev")), "agent_error", "the agent crashed")
		}, "the agent crashed"},
		"pass from a run that then failed": {func(r *conveyorRig) {
			task := r.start(r.only("dev"))
			r.mustSubmit(task, r.result(ConveyorOutcomePass, commit))
			r.fail(task, "agent_error", "crashed after handing in")
		}, "crashed after handing in"},
		"failed check": {func(r *conveyorRig) {
			task := r.start(r.only("dev"))
			in := r.result(ConveyorOutcomePass, commit)
			in.Checks = []ConveyorCheck{{Name: "lint", OK: false}}
			r.mustSubmit(task, in)
			r.complete(task)
		}, `check "lint" failed`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := newConveyorRig(t)
			r.enable()
			tc.play(r)
			view := r.view()
			if view.Status != ConveyorAwaitingCoordinator {
				t.Fatalf("status %s, want awaiting_coordinator; journal:%s", view.Status, r.dump())
			}
			r.everyInputDecided()
			lead := r.only("lead")
			if !strings.Contains(lead.HandoffNote.String, tc.reason) {
				t.Fatalf("coordinator handoff does not carry the reason %q:\n%s", tc.reason, lead.HandoffNote.String)
			}
			// Nothing else runs until the coordinator decides.
			before := len(r.tasksOf("qa")) + len(r.tasksOf("review"))
			r.svc.DeliverAllPending(r.ctx)
			if after := len(r.tasksOf("qa")) + len(r.tasksOf("review")) + len(r.tasksOf("lead")); after != before+1 {
				t.Fatalf("an escalated route kept starting runs")
			}

			// The coordinator sends it back: a new round at dev.
			rev := r.revision()
			view = r.mustApply(ConveyorCommand{Action: ConveyorCmdResume, FromStep: "dev", ExpectedRevision: rev})
			if view.Round != 2 || view.Status != ConveyorActive || view.Step != "dev" {
				t.Fatalf("resume: %+v", view)
			}
			if status := r.loadIssue().Status; status != "in_progress" {
				t.Fatalf("resume issue status = %s, want in_progress", status)
			}
			if got := len(r.tasksOf("dev")); got < 2 {
				t.Fatalf("resume at dev started no new dev run (%d)", got)
			}
		})
	}
}

func TestConveyorResultIdempotent(t *testing.T) {
	r := newConveyorRig(t)
	r.enable()
	task := r.start(r.only("dev"))
	in := r.result(ConveyorOutcomePass, "abcdef1234567")

	first := r.mustSubmit(task, in)
	if !first.Created || first.Comment == nil {
		t.Fatal("first hand-in did not post its report")
	}
	if len(first.Comment.SuppressedAgentIds) != 4 {
		t.Fatalf("report suppresses %d agents, want every agent of the route", len(first.Comment.SuppressedAgentIds))
	}
	again := r.mustSubmit(task, in)
	if again.Created || again.Event.ID != first.Event.ID {
		t.Fatalf("exact repeat: created=%v id=%s want %s", again.Created, util.UUIDToString(again.Event.ID), util.UUIDToString(first.Event.ID))
	}
	if n := r.f.Count(t, "SELECT count(*) FROM comment WHERE issue_id=$1 AND source_task_id=$2", r.issue, task.ID); n != 1 {
		t.Fatalf("repeat posted the report again (%d comments)", n)
	}
	changed := in
	changed.Report = "something else"
	if _, err := r.submit(task, changed); !errors.Is(err, ErrConveyorConflict) {
		t.Fatalf("different payload: %v", err)
	}

	// Terminal first, result after: the end was already judged.
	r.complete(task)
	late := r.result(ConveyorOutcomePass, "abcdef1234567")
	late.Report = "too late"
	if _, err := r.submit(task, late); !errors.Is(err, ErrConveyorConflict) {
		t.Fatalf("result after the run ended: %v", err)
	}
	if same, err := r.submit(task, in); err != nil || same.Created {
		t.Fatalf("exact repeat after the end: %v created=%v", err, same.Created)
	}
}

func TestConveyorResultPermissions(t *testing.T) {
	r := newConveyorRig(t)
	r.enable()
	dev := r.start(r.only("dev"))
	in := r.result(ConveyorOutcomePass, "abcdef1234567")

	// Another agent cannot hand in for the run.
	if _, err := r.svc.SubmitResult(r.ctx, r.loadIssue(), dev.ID, r.agents["qa"], in); !errors.Is(err, ErrConveyorForbidden) {
		t.Errorf("result for another agent's run: %v", err)
	}
	// A run that is not a step of the route cannot.
	plain := parseTestUUID(t, r.f.Task(t, util.UUIDToString(r.agents["dev"]), testutil.Cols{"issue_id": util.UUIDToString(r.issue), "status": "running",
		"runtime_id": testutil.Raw("(SELECT runtime_id FROM agent WHERE id='" + util.UUIDToString(r.agents["dev"]) + "')")}))
	if _, err := r.svc.SubmitResult(r.ctx, r.loadIssue(), plain, r.agents["dev"], in); !errors.Is(err, ErrConveyorForbidden) {
		t.Errorf("result for a plain run: %v", err)
	}
	// Nor a run of another issue's route.
	other := parseTestUUID(t, r.f.Issue(t, "Another issue"))
	r.f.Cleanup(t, "DELETE FROM agent_task_queue WHERE issue_id=$1", other)
	if _, err := r.svc.SubmitResult(r.ctx, db.Issue{ID: other, WorkspaceID: r.loadIssue().WorkspaceID}, dev.ID, r.agents["dev"], in); !errors.Is(err, ErrConveyorForbidden) {
		t.Errorf("result for the run through another issue: %v", err)
	}
	// Malformed results are refused before anything is written.
	for name, bad := range map[string]ConveyorResultInput{
		"schema":  {SchemaVersion: 2, Outcome: "pass", Report: "x"},
		"outcome": {SchemaVersion: 1, Outcome: "done", Report: "x"},
		"report":  {SchemaVersion: 1, Outcome: "pass"},
		"commit":  {SchemaVersion: 1, Outcome: "pass", Report: "x", Artifact: ConveyorArtifact{Commit: "not a sha"}},
	} {
		if _, err := r.submit(dev, bad); !errors.Is(err, ErrConveyorInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n := len(r.eventsOf(conveyorEventResult)); n != 0 {
		t.Fatalf("refused results left %d journal rows", n)
	}
	// The refusals of a run that may not hand in are in the journal, once each.
	if _, err := r.svc.SubmitResult(r.ctx, r.loadIssue(), dev.ID, r.agents["qa"], in); !errors.Is(err, ErrConveyorForbidden) {
		t.Errorf("repeat: %v", err)
	}
	refused := r.eventsOf(conveyorEventRejected)
	if len(refused) != 2 || refused[0].Reason.String == "" {
		t.Errorf("journal shows %d refusals, want one for each run:%s", len(refused), r.dump())
	}

	// A paused route and a disabled one take no results.
	r.mustApply(ConveyorCommand{Action: ConveyorCmdPause, ExpectedRevision: r.revision()})
	if _, err := r.submit(dev, in); !errors.Is(err, ErrConveyorConflict) {
		t.Errorf("result on a paused route: %v", err)
	}
	r.mustApply(ConveyorCommand{Action: ConveyorCmdDisable, ExpectedRevision: r.revision()})
	if _, err := r.submit(dev, in); !errors.Is(err, ErrConveyorConflict) {
		t.Errorf("result on a disabled route: %v", err)
	}
}

// A run left behind by a new round cannot hand in, and does not move the route.
func TestConveyorResultFromAnOldRound(t *testing.T) {
	r := newConveyorRig(t)
	r.enable()
	const commit = "abcdef1234567"
	r.pass("dev", commit)
	qa := r.start(r.only("qa"))
	// The coordinator sends the route back to dev while qa is still executing.
	r.f.Exec(t, "UPDATE agent_task_queue SET status='queued' WHERE id=$1", qa.ID)
	r.f.Exec(t, "UPDATE agent_task_queue SET status='running' WHERE id=$1", qa.ID)
	r.mustApply(ConveyorCommand{Action: ConveyorCmdPause, ExpectedRevision: r.revision()})
	r.mustApply(ConveyorCommand{Action: ConveyorCmdResume, FromStep: "dev", ExpectedRevision: r.revision()})
	if _, err := r.submit(qa, r.result(ConveyorOutcomePass, commit)); !errors.Is(err, ErrConveyorConflict) {
		t.Fatalf("result from a run of the previous round: %v", err)
	}
	r.complete(qa)
	if d := r.decisionFor(qa); d.Action.String != conveyorDecisionStale {
		t.Fatalf("the end of an old-round run was decided as %s", d.Action.String)
	}
	r.everyInputDecided()
	if view := r.view(); view.Status != ConveyorActive || view.Step != "dev" || view.Round != 2 {
		t.Fatalf("an old-round run moved the route: %+v", view)
	}
	if len(r.tasksOf("lead")) != 0 || len(r.tasksOf("review")) != 0 {
		t.Fatal("an old-round run started runs")
	}
}

func mustJSON(v any) json.RawMessage {
	raw, _ := json.Marshal(v)
	return raw
}

func TestConveyorQuotaSingleRetry(t *testing.T) {
	r := newConveyorRig(t)
	r.enable()
	const commit = "abcdef1234567"
	dev := r.start(r.only("dev"))
	r.fail(dev, taskfailure.ReasonAgentProviderQuotaLimit.String(), "You have hit your usage limit")

	view := r.view()
	if view.Status != ConveyorWaitingProvider || !strings.Contains(view.WaitingReason, "provider limit") {
		t.Fatalf("after a quota failure: %+v", view)
	}
	if got := len(r.tasksOf("lead")); got != 0 {
		t.Fatalf("a provider limit woke the coordinator (%d runs)", got)
	}
	failed := r.task(util.UUIDToString(dev.ID))

	// The epic loop asks without naming the run, the provider lane names it.
	issue := r.f.svc.TaskSvc
	first, err := issue.RerunIssue(r.ctx, r.issue, pgtype.UUID{}, pgtype.UUID{}, r.owner, nil)
	if err != nil {
		t.Fatalf("rerun without --task: %v", err)
	}
	second, err := issue.RerunIssue(r.ctx, r.issue, failed.ID, pgtype.UUID{}, r.owner, nil)
	if err != nil {
		t.Fatalf("rerun with --task: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("two reruns made two runs: %s and %s", util.UUIDToString(first.ID), util.UUIDToString(second.ID))
	}
	if got := len(r.tasksOf("dev")); got != 2 {
		t.Fatalf("dev has %d runs, want the failed one and one successor", got)
	}
	if got := len(r.tasksOf("lead")); got != 0 {
		t.Fatalf("a rerun woke the coordinator")
	}
	succ := r.task(util.UUIDToString(first.ID))
	if succ.AgentID != r.agents["dev"] || succ.ConveyorActionID != failed.ConveyorActionID || !strings.Contains(succ.HandoffNote.String, "conveyor result") {
		t.Fatalf("successor is not the route's run: %+v", succ)
	}
	if r.view().Status != ConveyorActive {
		t.Fatalf("route did not resume with the successor: %s", r.view().Status)
	}

	// Its pass moves the route on.
	succ = r.start(succ)
	r.mustSubmit(succ, r.result(ConveyorOutcomePass, commit))
	r.complete(succ)
	r.only("qa")
	if got := len(r.tasksOf("lead")); got != 0 {
		t.Fatalf("coordinator woken on the way")
	}
	r.everyInputDecided()
	// A late rerun of the already-passed step is not the route's business.
	if again, err := issue.RerunIssue(r.ctx, r.issue, failed.ID, pgtype.UUID{}, r.owner, nil); err != nil || again.AgentID == r.agents["dev"] && again.ConveyorActionID.Valid {
		t.Fatalf("rerun of a finished step: %v %+v", err, again)
	}
}

func TestConveyorStaleWorkNeverStarts(t *testing.T) {
	for name, stale := range map[string]func(r *conveyorRig){
		"pause": func(r *conveyorRig) {
			r.mustApply(ConveyorCommand{Action: ConveyorCmdPause, ExpectedRevision: r.revision()})
		},
		"disable": func(r *conveyorRig) {
			r.mustApply(ConveyorCommand{Action: ConveyorCmdDisable, ExpectedRevision: r.revision()})
		},
		"issue closed": func(r *conveyorRig) {
			r.f.Exec(r.t, "UPDATE issue SET status='done' WHERE id=$1", r.issue)
		},
		"issue cancelled": func(r *conveyorRig) {
			r.f.Exec(r.t, "UPDATE issue SET status='cancelled' WHERE id=$1", r.issue)
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newConveyorRig(t)
			r.enable()
			dev := r.only("dev")
			// Claimed by the daemon, then the route moves before it starts.
			r.f.Exec(t, "UPDATE agent_task_queue SET status='dispatched',dispatched_at=clock_timestamp() WHERE id=$1", dev.ID)
			stale(r)
			claimed := r.task(util.UUIDToString(dev.ID))
			claim := db.LockAgentTaskStartClaimParams{ID: claimed.ID, RuntimeID: claimed.RuntimeID, DispatchedAt: claimed.DispatchedAt}
			if _, err := r.f.svc.TaskSvc.StartTaskForClaim(r.ctx, claim); !errors.Is(err, ErrConveyorStale) {
				t.Fatalf("start of a stale run: %v", err)
			}
			if got := r.task(util.UUIDToString(dev.ID)).Status; got != "cancelled" {
				t.Fatalf("stale run is %s, want cancelled", got)
			}
			rejected := r.eventsOf(conveyorEventRejectedStale)
			if len(rejected) != 1 || rejected[0].SourceTaskID != dev.ID || rejected[0].Reason.String == "" {
				t.Fatalf("journal does not say why the run never started:%s", r.dump())
			}
			if name == "pause" || name == "disable" {
				if got := len(r.tasksOf("qa")); got != 0 {
					t.Fatalf("a refused run advanced the route")
				}
			}
		})
	}
}

func TestConveyorWithdrawsUnstartedRunsOnDisable(t *testing.T) {
	r := newConveyorRig(t)
	r.enable()
	dev := r.only("dev")
	r.mustApply(ConveyorCommand{Action: ConveyorCmdDisable, ExpectedRevision: r.revision()})
	if got := r.task(util.UUIDToString(dev.ID)).Status; got != "cancelled" {
		t.Fatalf("queued run of a disabled route is %s", got)
	}
}

func TestConveyorPausedRouteHoldsTheEndOfARunningStep(t *testing.T) {
	r := newConveyorRig(t)
	r.enable()
	dev := r.start(r.only("dev"))
	r.mustSubmit(dev, r.result(ConveyorOutcomePass, "abcdef1234567"))
	r.mustApply(ConveyorCommand{Action: ConveyorCmdPause, ExpectedRevision: r.revision()})
	r.complete(dev)
	if d := r.decisionFor(dev); d.Action.String != conveyorDecisionHeld {
		t.Fatalf("decision %s, want held", d.Action.String)
	}
	if len(r.tasksOf("qa")) != 0 || len(r.tasksOf("lead")) != 0 {
		t.Fatal("a paused route started a run")
	}
	// Resuming restarts the step the route stopped on.
	view := r.mustApply(ConveyorCommand{Action: ConveyorCmdResume, ExpectedRevision: r.revision()})
	if view.Status != ConveyorActive || view.Round != 2 || view.Step != "dev" {
		t.Fatalf("resume: %+v", view)
	}
	if got := len(r.tasksOf("dev")); got != 2 {
		t.Fatalf("resume made %d dev runs in total, want 2", got)
	}
	if _, err := r.apply(ConveyorCommand{Action: ConveyorCmdPause, ExpectedRevision: 1}); !errors.Is(err, ErrConveyorConflict) {
		t.Fatalf("pause with a stale revision: %v", err)
	}
}

// Rolling back the transaction that ends a run leaves neither a decision nor a
// run; running it again afterwards decides exactly once.
func TestConveyorTerminalRollback(t *testing.T) {
	r := newConveyorRig(t)
	r.enable()
	dev := r.start(r.only("dev"))
	r.mustSubmit(dev, r.result(ConveyorOutcomePass, "abcdef1234567"))
	r.f.Exec(t, "UPDATE agent_task_queue SET status='completed',completed_at=now() WHERE id=$1", dev.ID)
	done := r.task(util.UUIDToString(dev.ID))

	tx, err := r.f.Pool.Begin(r.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.svc.OnTaskTerminal(r.ctx, r.f.q.WithTx(tx), done); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(r.ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(r.eventsOf(conveyorEventDecision)) + len(r.eventsOf(conveyorEventAction)) - 1; n != 0 {
		t.Fatalf("a rolled-back decision left %d rows:%s", n, r.dump())
	}
	if len(r.tasksOf("qa")) != 0 || r.view().Step != "dev" {
		t.Fatal("a rolled-back decision moved the route")
	}
	r.terminal(done)
	r.terminal(done)
	r.svc.DeliverPending(r.ctx, r.issue)
	r.only("qa")
	if n := len(r.eventsOf(conveyorEventDecision)); n != 1 {
		t.Fatalf("decisions: %d", n)
	}
}

// terminal runs the deciding step in its own transaction, as the status write does.
func (r *conveyorRig) terminal(task db.AgentTaskQueue) {
	r.t.Helper()
	tx, err := r.f.Pool.Begin(r.ctx)
	if err != nil {
		r.t.Fatal(err)
	}
	if err = r.svc.OnTaskTerminal(r.ctx, r.f.q.WithTx(tx), task); err != nil {
		tx.Rollback(r.ctx)
		r.t.Fatal(err)
	}
	if err = tx.Commit(r.ctx); err != nil {
		r.t.Fatal(err)
	}
}

// Many writers at once - the terminal path replayed, two delivery ticks, the
// post-commit delivery - leave one decision, one action and one run.
func TestConveyorConcurrentTerminal(t *testing.T) {
	r := newConveyorRig(t)
	r.enable()
	dev := r.start(r.only("dev"))
	// The lead's own wakeup on the end of the run races the route's decision.
	leadID := util.UUIDToString(r.agents["lead"])
	leadRun := parseTestUUID(t, r.f.Task(t, leadID, testutil.Cols{"issue_id": util.UUIDToString(r.issue), "status": "running",
		"runtime_id": testutil.Raw("(SELECT runtime_id FROM agent WHERE id='" + leadID + "')")}))
	wakeups := &IssueWakeupService{Tasks: r.f.svc.TaskSvc}
	w, err := wakeups.Create(r.ctx, r.issue, r.owner, leadRun, WakeupInput{AgentID: leadID, Kind: "event", Mode: "once",
		EventTypes: []string{"task.completed"}, FilterTaskID: util.UUIDToString(dev.ID), Instruction: "check the dev run"})
	if err != nil {
		t.Fatal(err)
	}
	r.mustSubmit(dev, r.result(ConveyorOutcomePass, "abcdef1234567"))
	r.f.Exec(t, "UPDATE agent_task_queue SET status='completed',completed_at=now() WHERE id=$1", dev.ID)
	r.f.Exec(t, "UPDATE agent_task_queue SET status='completed',completed_at=now() WHERE id=$1", leadRun)
	done := r.task(util.UUIDToString(dev.ID))

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	run := func(fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(); err != nil {
				errs <- err
			}
		}()
	}
	for i := 0; i < 4; i++ {
		run(func() error {
			tx, err := r.f.Pool.Begin(r.ctx)
			if err != nil {
				return err
			}
			defer tx.Rollback(r.ctx)
			if err = r.svc.OnTaskTerminal(r.ctx, r.f.q.WithTx(tx), done); err != nil {
				return err
			}
			return tx.Commit(r.ctx)
		})
		run(func() error { return r.svc.DeliverAllPending(r.ctx) })
		run(func() error { r.svc.DeliverPending(r.ctx, r.issue); return nil })
		run(func() error {
			// A busy rule row is held to the next tick, not an error of the route.
			var pgErr *pgconn.PgError
			if err := wakeups.dispatch(r.ctx, w); errors.As(err, &pgErr) && pgErr.Code == "55P03" {
				return nil
			} else {
				return err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent writer failed (a deadlock or a lost race): %v", err)
	}
	r.svc.DeliverPending(r.ctx, r.issue)
	if err := wakeups.dispatch(r.ctx, w); err != nil {
		t.Fatal(err)
	}
	if got := len(r.tasksOf("lead")); got != 1 {
		t.Fatalf("lead runs: %d, want only the registering one", got)
	}
	if n := len(r.eventsOf(conveyorEventDecision)); n != 1 {
		t.Fatalf("decisions: %d, want 1:%s", n, r.dump())
	}
	if n := len(r.eventsOf(conveyorEventInput)); n != 1 {
		t.Fatalf("inputs: %d, want 1", n)
	}
	actions := 0
	for _, a := range r.eventsOf(conveyorEventAction) {
		if a.Idx.Int32 == 1 {
			actions++
		}
	}
	if actions != 1 {
		t.Fatalf("actions for the qa step: %d, want 1", actions)
	}
	if got := len(r.tasksOf("qa")); got != 1 {
		t.Fatalf("qa runs: %d, want 1", got)
	}
}

// An action whose run cannot be created stays owed; a fresh service instance
// (a restart) delivers it exactly once.
func TestConveyorPendingDelivery(t *testing.T) {
	r := newConveyorRig(t)
	r.enable()
	// Remove the qa agent's runtime: the run cannot be created now.
	qa := util.UUIDToString(r.agents["qa"])
	r.f.Exec(t, "UPDATE agent SET runtime_id=NULL WHERE id=$1", qa)
	r.pass("dev", "abcdef1234567")
	if len(r.tasksOf("qa")) != 0 {
		t.Fatal("run created for an agent without a runtime")
	}
	pending := r.pending()
	if len(pending) != 1 || pending[0].Reason.String == "" {
		t.Fatalf("the owed run is not visible with its reason:%s", r.dump())
	}
	if view := r.view(); view.PendingActions != 1 || view.Step != "qa" || view.Status != ConveyorActive {
		t.Fatalf("view while owed: %+v", view)
	}
	if r.loadIssue().Status != "in_progress" || r.metadataStep() != "dev" {
		t.Fatal("undelivered qa action moved the issue projection")
	}

	r.f.Exec(t, "UPDATE agent SET runtime_id=(SELECT runtime_id FROM agent WHERE id=$2) WHERE id=$1", qa, r.agents["dev"])
	restarted := &IssueConveyorService{Tasks: &TaskService{Queries: r.f.q, TxStarter: r.f.Pool, Bus: r.f.svc.TaskSvc.Bus}}
	if err := restarted.DeliverAllPending(r.ctx); err != nil {
		t.Fatal(err)
	}
	if err := restarted.DeliverAllPending(r.ctx); err != nil {
		t.Fatal(err)
	}
	r.only("qa")
	if len(r.pending()) != 0 {
		t.Fatal("a delivered action is still owed")
	}
	if r.loadIssue().Status != "qa" || r.metadataStep() != "qa" {
		t.Fatal("recovered delivery did not update the issue projection")
	}
}

func (r *conveyorRig) pending() []db.IssueConveyorEvent {
	var out []db.IssueConveyorEvent
	for _, a := range r.eventsOf(conveyorEventAction) {
		if a.State.String == "pending" {
			out = append(out, a)
		}
	}
	return out
}

// A route that says "a run is working" while no run exists and none is owed
// hands itself to the coordinator.
func TestConveyorStalledRouteGoesToTheCoordinator(t *testing.T) {
	r := newConveyorRig(t)
	r.enable()
	dev := r.only("dev")
	// The run vanished without the route hearing of it: a sweeper failed it.
	r.f.Exec(t, "UPDATE agent_task_queue SET status='failed',completed_at=now(),error='runtime offline' WHERE id=$1", dev.ID)
	r.f.Exec(t, "UPDATE issue_conveyor SET updated_at = now() - interval '10 minutes' WHERE issue_id=$1", r.issue)
	if err := r.svc.DeliverAllPending(r.ctx); err != nil {
		t.Fatal(err)
	}
	if view := r.view(); view.Status != ConveyorAwaitingCoordinator {
		t.Fatalf("status %s:%s", view.Status, r.dump())
	}
	lead := r.only("lead")
	if !strings.Contains(lead.HandoffNote.String, "no run is working") {
		t.Fatalf("coordinator handoff: %q", lead.HandoffNote.String)
	}
	// Not a second time.
	if err := r.svc.DeliverAllPending(r.ctx); err != nil {
		t.Fatal(err)
	}
	r.only("lead")
}

// A run that is not part of a route, and rules that people made, behave as before.
func TestConveyorLeavesOrdinaryWorkAlone(t *testing.T) {
	r := newConveyorRig(t)
	other := parseTestUUID(t, r.f.Issue(t, "Plain issue"))
	r.f.Cleanup(t, "DELETE FROM agent_task_queue WHERE issue_id=$1", other)
	r.f.Cleanup(t, "DELETE FROM issue_wakeup WHERE issue_id=$1", other)
	r.f.Cleanup(t, "DELETE FROM issue_wakeup_receipt WHERE wakeup_id IN (SELECT id FROM issue_wakeup WHERE issue_id=$1)", other)
	r.enable()

	devID := util.UUIDToString(r.agents["dev"])
	plain := parseTestUUID(t, r.f.Task(t, devID, testutil.Cols{"issue_id": util.UUIDToString(other), "status": "running",
		"runtime_id": testutil.Raw("(SELECT runtime_id FROM agent WHERE id='" + devID + "')")}))
	if _, err := r.f.svc.TaskSvc.CompleteTask(r.ctx, plain, []byte(`{"output":"done"}`), "", "", "", false, "", ""); err != nil {
		t.Fatal(err)
	}
	if n := r.f.Count(t, "SELECT count(*) FROM issue_conveyor_event WHERE issue_id=$1", other); n != 0 {
		t.Fatalf("an unrouted issue got %d journal rows", n)
	}

	// A person's wakeup on the route's own run fires as always.
	routed := r.start(r.only("dev"))
	wakeups := &IssueWakeupService{Tasks: r.f.svc.TaskSvc}
	w, err := wakeups.Create(r.ctx, r.issue, r.owner, pgtype.UUID{}, WakeupInput{AgentID: util.UUIDToString(r.agents["lead"]), Kind: "event", Mode: "once",
		EventTypes: []string{"task.completed"}, FilterTaskID: util.UUIDToString(routed.ID), Instruction: "look at the result"})
	if err != nil {
		t.Fatal(err)
	}
	r.mustSubmit(routed, r.result(ConveyorOutcomePass, "abcdef1234567"))
	r.complete(routed)
	if err := wakeups.dispatch(r.ctx, w); err != nil {
		t.Fatal(err)
	}
	if got := len(r.tasksOf("lead")); got != 1 {
		t.Fatalf("a person's wakeup started %d lead runs, want 1", got)
	}
	if n := len(r.eventsOf(conveyorEventSuperseded)); n != 0 {
		t.Fatalf("a person's wakeup was superseded:%s", r.dump())
	}
}

// The incident this feature closes: the executor hands in and mentions the lead
// in the report, and the lead has an own wakeup on the end of that run. After the run ends the lead has
// no run of its own and the wakeup says why.
func TestConveyorSupersedesOwnWakeup(t *testing.T) {
	r := newConveyorRig(t)
	r.enable()
	leadID := util.UUIDToString(r.agents["lead"])
	// The lead's own run, from which it registered the wakeup.
	leadRun := parseTestUUID(t, r.f.Task(t, leadID, testutil.Cols{"issue_id": util.UUIDToString(r.issue), "status": "running",
		"runtime_id": testutil.Raw("(SELECT runtime_id FROM agent WHERE id='" + leadID + "')")}))
	dev := r.start(r.only("dev"))
	wakeups := &IssueWakeupService{Tasks: r.f.svc.TaskSvc}
	w, err := wakeups.Create(r.ctx, r.issue, r.owner, leadRun, WakeupInput{AgentID: leadID, Kind: "event", Mode: "once",
		EventTypes: []string{"task.completed", "task.failed", "task.cancelled"}, FilterTaskID: util.UUIDToString(dev.ID), Instruction: "check the dev run"})
	if err != nil {
		t.Fatal(err)
	}
	in := r.result(ConveyorOutcomePass, "abcdef1234567")
	in.Report = "Done. @lead please take over."
	r.mustSubmit(dev, in)
	r.complete(dev)
	r.f.Exec(t, "UPDATE agent_task_queue SET status='completed',completed_at=now() WHERE id=$1", leadRun)

	if err := wakeups.dispatch(r.ctx, w); err != nil {
		t.Fatal(err)
	}
	if got := len(r.tasksOf("lead")); got != 1 {
		t.Fatalf("the wakeup started a run for the lead (%d runs, one is the registering run)", got)
	}
	superseded := r.eventsOf(conveyorEventSuperseded)
	if len(superseded) != 1 || !strings.HasPrefix(superseded[0].CoveredBy.String, "conveyor_action:") || superseded[0].Reason.String != "own_wakeup" {
		t.Fatalf("journal does not show the wakeup as superseded:%s", r.dump())
	}
	// The covering action is the one that started the qa run.
	qa := r.only("qa")
	if superseded[0].CoveredBy.String != "conveyor_action:"+util.UUIDToString(qa.ConveyorActionID) {
		t.Fatalf("covered_by %s does not name the qa action %s", superseded[0].CoveredBy.String, util.UUIDToString(qa.ConveyorActionID))
	}
	var enabled bool
	r.f.QueryRow(t, "SELECT enabled FROM issue_wakeup WHERE id=$1", w.ID).Scan(&enabled)
	if enabled || r.f.Count(t, "SELECT count(*) FROM issue_wakeup_receipt WHERE wakeup_id=$1 AND processed_at IS NULL", w.ID) != 0 {
		t.Fatal("the wakeup kept an unprocessed input")
	}
	var outcome string
	r.f.QueryRow(t, `SELECT COALESCE(details->>'outcome','') FROM activity_log WHERE issue_id=$1 AND action=$2 AND details->'wakeup'->>'id'=$3 ORDER BY created_at DESC LIMIT 1`, r.issue, wakeupActivityTriggered, util.UUIDToString(w.ID)).Scan(&outcome)
	if outcome != wakeupOutcomeSuperseded {
		t.Fatalf("wakeup timeline outcome %q", outcome)
	}
}

// A failed or cancelled end of the run still wakes the lead when nobody else
// has: here the end was not judged by the route (its decision is stale).
func TestConveyorDoesNotSupersedeWhatItDidNotDecide(t *testing.T) {
	r := newConveyorRig(t)
	r.enable()
	leadID := util.UUIDToString(r.agents["lead"])
	leadRun := parseTestUUID(t, r.f.Task(t, leadID, testutil.Cols{"issue_id": util.UUIDToString(r.issue), "status": "running",
		"runtime_id": testutil.Raw("(SELECT runtime_id FROM agent WHERE id='" + leadID + "')")}))
	dev := r.start(r.only("dev"))
	// The route is disabled while the run executes: its end is nobody's step.
	r.mustApply(ConveyorCommand{Action: ConveyorCmdDisable, ExpectedRevision: r.revision()})
	wakeups := &IssueWakeupService{Tasks: r.f.svc.TaskSvc}
	w, err := wakeups.Create(r.ctx, r.issue, r.owner, leadRun, WakeupInput{AgentID: leadID, Kind: "event", Mode: "once",
		EventTypes: []string{"task.completed", "task.failed", "task.cancelled"}, FilterTaskID: util.UUIDToString(dev.ID), Instruction: "check"})
	if err != nil {
		t.Fatal(err)
	}
	r.f.Exec(t, "UPDATE agent_task_queue SET status='completed',completed_at=now() WHERE id=$1", leadRun)
	r.fail(dev, "agent_error", "crashed")
	if err := wakeups.dispatch(r.ctx, w); err != nil {
		t.Fatal(err)
	}
	if got := len(r.tasksOf("lead")); got != 2 {
		t.Fatalf("the lead has %d runs, want the registering one and the wakeup's", got)
	}
}

// orderProbe records, per transaction, the named statements in the order they run.
type orderProbe struct {
	TxStarter
	mu   sync.Mutex
	logs [][]string
}

func (p *orderProbe) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := p.TxStarter.Begin(ctx)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.logs = append(p.logs, nil)
	idx := len(p.logs) - 1
	p.mu.Unlock()
	return orderProbeTx{Tx: tx, p: p, idx: idx}, nil
}

type orderProbeTx struct {
	pgx.Tx
	p   *orderProbe
	idx int
}

func (tx orderProbeTx) note(sql string) {
	if i := strings.Index(sql, "-- name: "); i >= 0 {
		name := strings.Fields(sql[i+len("-- name: "):])[0]
		tx.p.mu.Lock()
		tx.p.logs[tx.idx] = append(tx.p.logs[tx.idx], name)
		tx.p.mu.Unlock()
	}
}

func (tx orderProbeTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx.note(sql)
	return tx.Tx.Exec(ctx, sql, args...)
}

func (tx orderProbeTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	tx.note(sql)
	return tx.Tx.Query(ctx, sql, args...)
}

func (tx orderProbeTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	tx.note(sql)
	return tx.Tx.QueryRow(ctx, sql, args...)
}

// The deciding transaction takes the run's own row first and the route row
// second, once; no other writer takes them the other way round.
func TestConveyorLockOrderInTheTerminalTransaction(t *testing.T) {
	r := newConveyorRig(t)
	r.enable()
	dev := r.start(r.only("dev"))
	r.mustSubmit(dev, r.result(ConveyorOutcomePass, "abcdef1234567"))

	probe := &orderProbe{TxStarter: r.f.Pool}
	r.f.svc.TaskSvc.TxStarter = probe
	defer func() { r.f.svc.TaskSvc.TxStarter = r.f.Pool }()
	r.complete(dev)

	var terminal []string
	for _, log := range probe.logs {
		for _, name := range log {
			if name == "CompleteAgentTask" {
				terminal = log
			}
		}
	}
	if terminal == nil {
		t.Fatalf("no transaction wrote the terminal status: %v", probe.logs)
	}
	at := func(name string) int {
		for i, n := range terminal {
			if n == name {
				return i
			}
		}
		return -1
	}
	task, route := at("CompleteAgentTask"), at("LockIssueConveyor")
	if task < 0 || route < 0 || task > route {
		t.Fatalf("lock order is %v: the run's row must come before the route's", terminal)
	}
	locks := 0
	for _, n := range terminal {
		if n == "LockIssueConveyor" {
			locks++
		}
	}
	if locks != 1 {
		t.Fatalf("the terminal transaction took the route row %d times: %v", locks, terminal)
	}
}

// A route started at qa has no dev step to name the commit: the first verifier
// pins it, and the next one must judge the same.
func TestConveyorRouteStartedAtQAPinsTheFirstCommit(t *testing.T) {
	r := newConveyorRig(t)
	raw, _ := json.Marshal(r.route("dev", "qa", "review", "close"))
	view := r.mustApply(ConveyorCommand{Action: ConveyorCmdEnable, Route: raw, FromStep: "qa"})
	if view.Step != "qa" || len(view.Route) != 3 {
		t.Fatalf("route started at qa: %+v", view)
	}
	r.pass("qa", "abcdef1234567")
	if view := r.view(); view.AcceptedCommit != "abcdef1234567" {
		t.Fatalf("accepted commit %q", view.AcceptedCommit)
	}
	task := r.start(r.only("review"))
	r.mustSubmit(task, r.result(ConveyorOutcomePass, "1234567abcdef"))
	r.complete(task)
	if view := r.view(); view.Status != ConveyorAwaitingCoordinator {
		t.Fatalf("review of another commit: %s", view.Status)
	}
}
