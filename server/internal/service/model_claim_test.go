package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// modelClaimRig is a workspace with an owner, four public online runtimes
// (as if on separate daemons), a Strong profile codex·sol → claude·opus →
// glm·glm and a Mid profile with one row on the spare runtime.
type modelClaimRig struct {
	t        *testing.T
	ctx      context.Context
	fx       *testutil.Fixture
	q        *db.Queries
	svc      *TaskService
	profiles *ModelProfileService
	ws       pgtype.UUID
	owner    pgtype.UUID
	rt       map[string]string
	strong   pgtype.UUID
	mid      pgtype.UUID
}

func newModelClaimRig(t *testing.T) *modelClaimRig {
	t.Helper()
	pool := sharedTestPool(t)
	bootstrap := testutil.New(pool, "", "")
	suffix := time.Now().UnixNano()
	owner := bootstrap.User(t, fmt.Sprintf("models-owner-%d", suffix), fmt.Sprintf("models-owner-%d@example.com", suffix))
	ws := bootstrap.Workspace(t, fmt.Sprintf("models-%d", suffix), fmt.Sprintf("models-%d", suffix), testutil.Cols{"issue_prefix": "MOD"})
	fx := testutil.New(pool, ws, owner)
	fx.Member(t, ws, owner, "owner")
	r := &modelClaimRig{t: t, ctx: context.Background(), fx: fx, q: db.New(pool), ws: util.MustParseUUID(ws), owner: util.MustParseUUID(owner), rt: map[string]string{}}
	r.svc = &TaskService{Queries: r.q, TxStarter: pool, Bus: events.New()}
	r.profiles = &ModelProfileService{Queries: r.q, TxStarter: pool}
	for _, name := range []string{"codex", "claude", "glm", "spare"} {
		provider := name
		if name == "glm" || name == "spare" {
			provider = "claude"
		}
		r.rt[name] = fx.Runtime(t, "models-"+name, testutil.Cols{"provider": provider, "visibility": "public"})
	}
	for _, table := range []string{"runtime_unavailable", "model_profile_option", "model_profile", "agent_model_option", "model_idle_episode", "inbox_item"} {
		fx.Cleanup(t, "DELETE FROM "+table+" WHERE workspace_id=$1", ws)
	}
	fx.Cleanup(t, "DELETE FROM agent_task_queue WHERE agent_id IN (SELECT id FROM agent WHERE workspace_id=$1)", ws)
	r.mid = r.saveProfile(pgtype.UUID{}, "Mid", "", "spare:mini")
	r.strong = r.saveProfile(pgtype.UUID{}, "Strong", "", "codex:sol:high", "claude:opus", "glm:glm")
	return r
}

// saveProfile writes rows given as runtime:model[:thinking].
func (r *modelClaimRig) saveProfile(id pgtype.UUID, name, exhausted string, rows ...string) pgtype.UUID {
	r.t.Helper()
	in := ModelProfileInput{Name: name, Options: r.rows(rows...)}
	if exhausted != "" {
		in.WhenExhaustedProfileID = &exhausted
	}
	p, err := r.profiles.SaveProfile(r.ctx, r.ws, id, r.owner, in)
	if err != nil {
		r.t.Fatalf("save profile %s: %v", name, err)
	}
	return p.ID
}

func (r *modelClaimRig) rows(rows ...string) []ModelOptionInput {
	out := make([]ModelOptionInput, 0, len(rows))
	for _, row := range rows {
		var rt, model, thinking string
		parts := splitN(row, 3)
		rt, model = parts[0], parts[1]
		thinking = parts[2]
		out = append(out, ModelOptionInput{RuntimeID: r.rt[rt], Model: model, ThinkingLevel: thinking})
	}
	return out
}

func splitN(s string, n int) []string {
	out := make([]string, n)
	i := 0
	for _, c := range s {
		if c == ':' && i < n-1 {
			i++
			continue
		}
		out[i] += string(c)
	}
	return out
}

// agent creates an agent bound to the codex runtime, on profile (may be
// invalid for none).
func (r *modelClaimRig) agent(name string, profile pgtype.UUID) db.Agent {
	r.t.Helper()
	id := r.fx.Agent(r.t, name, r.rt["codex"], testutil.Cols{"max_concurrent_tasks": 5, "model": "plain"})
	agent, err := r.q.GetAgent(r.ctx, util.MustParseUUID(id))
	if err != nil {
		r.t.Fatal(err)
	}
	if profile.Valid {
		pid := util.UUIDToString(profile)
		if agent, err = r.profiles.SaveAgentModels(r.ctx, agent, AgentModelsInput{ModelProfileID: &pid}); err != nil {
			r.t.Fatal(err)
		}
	}
	return agent
}

// enqueue queues a run of the agent on a new issue and lets the enqueue hook
// route it, as every enqueue path does.
func (r *modelClaimRig) enqueue(agent db.Agent, issueProfile pgtype.UUID) db.AgentTaskQueue {
	r.t.Helper()
	cols := testutil.Cols{}
	if issueProfile.Valid {
		cols["model_profile_id"] = util.UUIDToString(issueProfile)
	}
	issue := r.fx.Issue(r.t, "models issue", cols)
	id := r.fx.Task(r.t, util.UUIDToString(agent.ID), testutil.Cols{"runtime_id": util.UUIDToString(agent.RuntimeID), "issue_id": issue})
	task, err := r.q.GetAgentTask(r.ctx, util.MustParseUUID(id))
	if err != nil {
		r.t.Fatal(err)
	}
	r.svc.NotifyTaskEnqueued(r.ctx, task)
	return r.task(task.ID)
}

func (r *modelClaimRig) task(id pgtype.UUID) db.AgentTaskQueue {
	r.t.Helper()
	task, err := r.q.GetAgentTask(r.ctx, id)
	if err != nil {
		r.t.Fatal(err)
	}
	return task
}

func (r *modelClaimRig) claim(runtime string) *db.AgentTaskQueue {
	r.t.Helper()
	task, err := r.svc.ClaimTaskForRuntime(r.ctx, util.MustParseUUID(r.rt[runtime]))
	if err != nil {
		r.t.Fatalf("claim on %s: %v", runtime, err)
	}
	return task
}

func (r *modelClaimRig) limit(runtime string, until time.Time) {
	r.fx.Exec(r.t, "INSERT INTO runtime_unavailable (workspace_id, runtime_id, until, reason) VALUES ($1,$2,$3,'test')", r.ws, r.rt[runtime], until)
}

func (r *modelClaimRig) runtimeOf(task db.AgentTaskQueue) string {
	for name, id := range r.rt {
		if id == util.UUIDToString(task.RuntimeID) {
			return name
		}
	}
	return util.UUIDToString(task.RuntimeID)
}

func choiceOf(t *testing.T, task db.AgentTaskQueue) ModelChoice {
	t.Helper()
	var c ModelChoice
	if task.ModelChoice == nil {
		t.Fatalf("task %s has no model choice", util.UUIDToString(task.ID))
	}
	if err := json.Unmarshal(task.ModelChoice, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func (r *modelClaimRig) count(sql string, args ...any) int {
	return r.fx.Count(r.t, sql, args...)
}

func TestModelOptionClaimTakesFirstAvailableRow(t *testing.T) {
	r := newModelClaimRig(t)
	agent := r.agent("models first", r.strong)

	task := r.enqueue(agent, pgtype.UUID{})
	if r.runtimeOf(task) != "codex" || choiceOf(t, task).Position != 1 {
		t.Fatalf("routed to %s, %+v", r.runtimeOf(task), choiceOf(t, task))
	}
	// A daemon that serves another runtime of the list does not take it.
	if got := r.claim("claude"); got != nil {
		t.Fatalf("claude took the run of codex: %v", got.ID)
	}
	claimed := r.claim("codex")
	if claimed == nil || claimed.ID != task.ID || !claimed.ModelPosition.Valid || claimed.ModelPosition.Int32 != 1 {
		t.Fatalf("codex claim: %+v", claimed)
	}
	if c := choiceOf(t, r.task(task.ID)); c.Chosen == nil || c.Chosen.Model != "sol" || c.Chosen.ThinkingLevel != "high" || c.Reason != ModelReasonPrimary {
		t.Fatalf("recorded choice: %+v", c)
	}
}

func TestModelOptionClaimSkipsUnavailableAndRehomes(t *testing.T) {
	r := newModelClaimRig(t)
	agent := r.agent("models skip", r.strong)
	// The first row's runtime is offline: the run goes to row 2 at enqueue.
	r.fx.Exec(t, "UPDATE agent_runtime SET status='offline' WHERE id=$1", r.rt["codex"])
	task := r.enqueue(agent, pgtype.UUID{})
	if r.runtimeOf(task) != "claude" || choiceOf(t, task).Reason != ModelReasonFallbackRow {
		t.Fatalf("offline row 1: %s %+v", r.runtimeOf(task), choiceOf(t, task))
	}
	// Row 1 comes back before anyone claimed: claude does not take a run
	// whose list now chooses codex; the run moves there instead.
	r.fx.Exec(t, "UPDATE agent_runtime SET status='online', last_seen_at=now() WHERE id=$1", r.rt["codex"])
	if got := r.claim("claude"); got != nil {
		t.Fatalf("claude claimed a run that belongs to codex now")
	}
	if moved := r.task(task.ID); r.runtimeOf(moved) != "codex" || moved.Status != "queued" {
		t.Fatalf("run not re-homed: %s %s", r.runtimeOf(moved), moved.Status)
	}
	if got := r.claim("codex"); got == nil || got.ModelPosition.Int32 != 1 {
		t.Fatalf("codex claim after re-home: %+v", got)
	}
}

func TestQuotaFailureMarksRuntimeAndRetriesOnceOnNextRow(t *testing.T) {
	r := newModelClaimRig(t)
	agent := r.agent("models quota", r.strong)
	task := r.enqueue(agent, pgtype.UUID{})
	if r.claim("codex") == nil {
		t.Fatal("no claim")
	}
	now := time.Now().UTC()
	errText := fmt.Sprintf("You've hit your usage limit. Try again in 2h 5m.")
	failed, err := r.svc.FailTask(r.ctx, task.ID, errText, "", "", "", "", false, "", "")
	if err != nil || failed.Status != "failed" {
		t.Fatalf("fail: %v %+v", err, failed)
	}
	var until time.Time
	r.fx.QueryRow(t, "SELECT until FROM runtime_unavailable WHERE runtime_id=$1", r.rt["codex"]).Scan(&until)
	if d := until.Sub(now); d < 2*time.Hour || d > 2*time.Hour+6*time.Minute+30*time.Second {
		t.Fatalf("until %v is not now+2h5m", until)
	}
	children := r.count("SELECT count(*) FROM agent_task_queue WHERE retry_of_task_id=$1", task.ID)
	if children != 1 {
		t.Fatalf("quota retries: %d", children)
	}
	// A replay of the same failure writes nothing new.
	if _, err := r.svc.FailTask(r.ctx, task.ID, errText, "", "", "", "", false, "", ""); err != nil {
		t.Fatal(err)
	}
	if n := r.count("SELECT count(*) FROM agent_task_queue WHERE retry_of_task_id=$1", task.ID); n != 1 {
		t.Fatalf("replay created more retries: %d", n)
	}
	var childID pgtype.UUID
	r.fx.QueryRow(t, "SELECT id FROM agent_task_queue WHERE retry_of_task_id=$1", task.ID).Scan(&childID)
	child := r.task(childID)
	if r.runtimeOf(child) != "claude" || child.Status != "queued" {
		t.Fatalf("retry routed to %s (%s)", r.runtimeOf(child), child.Status)
	}
	got := r.claim("claude")
	if got == nil || got.ID != child.ID || got.ModelPosition.Int32 != 2 {
		t.Fatalf("retry claim: %+v", got)
	}
	if c := choiceOf(t, r.task(child.ID)); c.Reason != ModelReasonFallbackRow || c.Chosen.Model != "opus" {
		t.Fatalf("retry choice: %+v", c)
	}
}

func TestQuotaSubsPickRefusalCountsAsQuota(t *testing.T) {
	r := newModelClaimRig(t)
	agent := r.agent("models subs-pick", r.strong)
	task := r.enqueue(agent, pgtype.UUID{})
	r.claim("codex")
	// The daemon labels the refusal as a plain agent error; it is a quota.
	if _, err := r.svc.FailTask(r.ctx, task.ID, "subs-pick: подписки runtime выше лимита агентам", "", "", "", "agent_error.unknown", false, "", ""); err != nil {
		t.Fatal(err)
	}
	if n := r.count("SELECT count(*) FROM runtime_unavailable WHERE runtime_id=$1", r.rt["codex"]); n != 1 {
		t.Fatalf("subs-pick refusal did not mark the runtime: %d", n)
	}
	if n := r.count("SELECT count(*) FROM agent_task_queue WHERE retry_of_task_id=$1", task.ID); n != 1 {
		t.Fatalf("subs-pick refusal retries: %d", n)
	}
}

func TestQuotaWordsWithExplicitOtherReasonAreNotQuota(t *testing.T) {
	r := newModelClaimRig(t)
	agent := r.agent("models not quota", r.strong)
	task := r.enqueue(agent, pgtype.UUID{})
	r.claim("codex")
	if _, err := r.svc.FailTask(r.ctx, task.ID, "go test: expected 429 quota exceeded in fixture", "", "", "", "agent_error.unknown", false, "", ""); err != nil {
		t.Fatal(err)
	}
	if n := r.count("SELECT count(*) FROM runtime_unavailable WHERE workspace_id=$1", r.ws); n != 0 {
		t.Fatalf("a non-quota failure marked a runtime")
	}
	if n := r.count("SELECT count(*) FROM agent_task_queue WHERE retry_of_task_id=$1", task.ID); n != 0 {
		t.Fatalf("a non-quota failure was retried")
	}
}

func TestIdleAllUnavailableWaitsNotifiesOnceAndSweepReleases(t *testing.T) {
	r := newModelClaimRig(t)
	agent := r.agent("models idle", r.strong)
	soon := time.Now().Add(time.Hour)
	for _, rt := range []string{"codex", "claude", "glm"} {
		r.limit(rt, soon)
	}
	task := r.enqueue(agent, pgtype.UUID{})
	c := choiceOf(t, task)
	if c.Reason != ModelReasonAllUnavailable || c.WaitUntil == nil {
		t.Fatalf("waiting choice: %+v", c)
	}
	for _, rt := range []string{"codex", "claude", "glm"} {
		if got := r.claim(rt); got != nil {
			t.Fatalf("%s claimed a waiting run", rt)
		}
	}
	exhausted := "SELECT count(*) FROM inbox_item WHERE workspace_id=$1 AND type='agent_models_exhausted' AND recipient_id=$2"
	restored := "SELECT count(*) FROM inbox_item WHERE workspace_id=$1 AND type='agent_models_restored'"
	if n := r.count(exhausted, r.ws, r.owner); n != 1 {
		t.Fatalf("idle notices: %d", n)
	}
	// Another waiting run of the same agent in the same episode: no second notice.
	r.enqueue(agent, pgtype.UUID{})
	if _, err := r.svc.SweepModelRouting(r.ctx); err != nil {
		t.Fatal(err)
	}
	if n := r.count(exhausted, r.ws, r.owner); n != 1 {
		t.Fatalf("idle notices within one episode: %d", n)
	}
	// Two sweeps with nothing new change nothing.
	if n, err := r.svc.SweepModelRouting(r.ctx); err != nil || n != 0 {
		t.Fatalf("idle sweep changed %d (%v)", n, err)
	}

	// The limits lift: the sweep routes the run back without anyone asking,
	// to the primary row.
	r.fx.Exec(t, "UPDATE runtime_unavailable SET until=now()-interval '1 second' WHERE workspace_id=$1", r.ws)
	if n, err := r.svc.SweepModelRouting(r.ctx); err != nil || n == 0 {
		t.Fatalf("sweep after reset changed %d (%v)", n, err)
	}
	if n, err := r.svc.SweepModelRouting(r.ctx); err != nil || n != 0 {
		t.Fatalf("second sweep changed %d (%v)", n, err)
	}
	got := r.claim("codex")
	if got == nil || got.ModelPosition.Int32 != 1 {
		t.Fatalf("after the reset the primary row runs: %+v", got)
	}
	if n := r.count(restored, r.ws); n != 1 {
		t.Fatalf("restored notices: %d", n)
	}
	if n := r.count("SELECT count(*) FROM model_idle_episode WHERE agent_id=$1 AND ended_at IS NULL", agent.ID); n != 0 {
		t.Fatalf("episode still open")
	}

	// A new stand-still is a new episode with a new notice.
	for _, rt := range []string{"codex", "claude", "glm"} {
		r.limit(rt, soon)
	}
	r.enqueue(agent, pgtype.UUID{})
	if n := r.count(exhausted, r.ws, r.owner); n != 2 {
		t.Fatalf("second episode notices: %d", n)
	}
	_ = task
}

func TestModelOptionExhaustedProfileUsedAtClaim(t *testing.T) {
	r := newModelClaimRig(t)
	r.saveProfile(r.strong, "Strong", util.UUIDToString(r.mid), "codex:sol:high", "claude:opus", "glm:glm")
	agent := r.agent("models exhausted", r.strong)
	for _, rt := range []string{"codex", "claude", "glm"} {
		r.limit(rt, time.Now().Add(time.Hour))
	}
	task := r.enqueue(agent, pgtype.UUID{})
	if r.runtimeOf(task) != "spare" {
		t.Fatalf("exhausted profile not used: %s", r.runtimeOf(task))
	}
	got := r.claim("spare")
	if got == nil {
		t.Fatal("spare did not claim")
	}
	if c := choiceOf(t, r.task(task.ID)); c.Reason != ModelReasonFallbackProfile || c.ProfileName != "Mid" || len(c.ExhaustedProfiles) != 1 {
		t.Fatalf("choice: %+v", c)
	}
	if n := r.count("SELECT count(*) FROM inbox_item WHERE workspace_id=$1", r.ws); n != 0 {
		t.Fatalf("an exhausted profile is not a stand-still: %d notices", n)
	}
}

func TestModelProfileEditReachesNextClaimOfEveryAgent(t *testing.T) {
	r := newModelClaimRig(t)
	a := r.agent("models edit a", r.strong)
	b := r.agent("models edit b", r.strong)
	own := r.agent("models edit own", r.strong)
	if _, err := r.profiles.SaveAgentModels(r.ctx, own, AgentModelsInput{Options: ptr(r.rows("glm:glm"))}); err != nil {
		t.Fatal(err)
	}
	r.saveProfile(r.strong, "Strong", "", "claude:opus:max", "codex:sol")
	for _, agent := range []db.Agent{a, b} {
		task := r.enqueue(agent, pgtype.UUID{})
		if r.runtimeOf(task) != "claude" {
			t.Fatalf("%s did not follow the edit: %s", agent.Name, r.runtimeOf(task))
		}
	}
	if task := r.enqueue(own, pgtype.UUID{}); r.runtimeOf(task) != "glm" {
		t.Fatalf("own list changed with the profile: %s", r.runtimeOf(task))
	}
}

func ptr[T any](v T) *T { return &v }

func TestModelOptionIssueProfileOnlyForItsRuns(t *testing.T) {
	r := newModelClaimRig(t)
	agent := r.agent("models issue", r.strong)
	withProfile := r.enqueue(agent, r.mid)
	plain := r.enqueue(agent, pgtype.UUID{})
	if r.runtimeOf(withProfile) != "spare" || choiceOf(t, withProfile).Source != ModelSourceIssueProfile {
		t.Fatalf("issue profile: %s %+v", r.runtimeOf(withProfile), choiceOf(t, withProfile))
	}
	if r.runtimeOf(plain) != "codex" || choiceOf(t, plain).Source != ModelSourceAgentProfile {
		t.Fatalf("other issue: %s", r.runtimeOf(plain))
	}
}

func TestModelOptionRunningRunUntouchedByListChange(t *testing.T) {
	r := newModelClaimRig(t)
	agent := r.agent("models running", r.strong)
	task := r.enqueue(agent, pgtype.UUID{})
	r.claim("codex")
	before := r.task(task.ID)
	r.saveProfile(r.strong, "Strong", "", "glm:glm")
	if _, err := r.svc.SweepModelRouting(r.ctx); err != nil {
		t.Fatal(err)
	}
	after := r.task(task.ID)
	if after.RuntimeID != before.RuntimeID || string(after.ModelChoice) != string(before.ModelChoice) || after.Status != "dispatched" {
		t.Fatalf("a dispatched run changed: %s → %s", r.runtimeOf(before), r.runtimeOf(after))
	}
}

func TestModelOptionPlainAgentAndKillSwitchUnchanged(t *testing.T) {
	r := newModelClaimRig(t)
	plain := r.agent("models plain", pgtype.UUID{})
	task := r.enqueue(plain, pgtype.UUID{})
	if task.ModelChoice != nil || r.runtimeOf(task) != "codex" {
		t.Fatalf("a plain agent's run was routed: %s", task.ModelChoice)
	}
	if got := r.claim("codex"); got == nil || got.ModelPosition.Valid {
		t.Fatalf("plain claim: %+v", got)
	}

	t.Setenv("MULTICA_MODEL_CHOICE", "off")
	agent := r.agent("models switch off", r.strong)
	r.fx.Exec(t, "UPDATE agent_runtime SET status='offline' WHERE id=$1", r.rt["codex"])
	if task := r.enqueue(agent, pgtype.UUID{}); task.ModelChoice != nil || r.runtimeOf(task) != "codex" {
		t.Fatalf("switched off, the run was still routed")
	}
}

// A route step that hits a quota is repeated by the backend on the next model:
// one successor under the same route action, and the route keeps going
// instead of waiting for the provider.
func TestQuotaOnRouteStepRetriesUnderTheSameAction(t *testing.T) {
	r := newConveyorRig(t)
	dev, err := r.f.q.GetAgent(r.ctx, r.agents["dev"])
	if err != nil {
		t.Fatal(err)
	}
	spare := r.f.Runtime(t, "models route spare", testutil.Cols{"provider": "claude", "visibility": "public"})
	r.f.Cleanup(t, "DELETE FROM runtime_unavailable WHERE workspace_id=$1", r.f.WorkspaceID)
	r.f.Cleanup(t, "DELETE FROM agent_model_option WHERE agent_id=$1", dev.ID)
	profiles := &ModelProfileService{Queries: r.f.q, TxStarter: r.f.Pool}
	rows := []ModelOptionInput{{RuntimeID: util.UUIDToString(dev.RuntimeID), Model: "sol"}, {RuntimeID: spare, Model: "opus"}}
	if _, err := profiles.SaveAgentModels(r.ctx, dev, AgentModelsInput{Options: &rows}); err != nil {
		t.Fatal(err)
	}
	r.enable()
	step := r.start(r.only("dev"))
	if step.ModelChoice == nil {
		t.Fatal("the route's run was not routed by the model list")
	}
	r.fail(step, "", "You've hit your limit · resets 3pm (UTC)")

	runs := r.tasksOf("dev")
	if len(runs) != 2 {
		t.Fatalf("dev has %d runs, want the failed one and one repeat", len(runs))
	}
	repeat := runs[1]
	if repeat.ConveyorActionID != step.ConveyorActionID || !repeat.RetryOfTaskID.Valid || repeat.RetryOfTaskID != step.ID {
		t.Fatalf("repeat is not the route's run: %+v", repeat)
	}
	if util.UUIDToString(repeat.RuntimeID) != spare {
		t.Fatalf("repeat not on the next model's runtime")
	}
	if view := r.view(); view.Status != ConveyorActive {
		t.Fatalf("route waits for the provider instead of continuing: %s", view.Status)
	}
	if got := len(r.tasksOf("lead")); got != 0 {
		t.Fatalf("the coordinator was woken (%d runs)", got)
	}
	// A manual rerun meanwhile does not add a second successor.
	if again, err := r.f.svc.TaskSvc.RerunIssue(r.ctx, r.issue, step.ID, pgtype.UUID{}, r.owner, nil); err != nil || again.ID != repeat.ID {
		t.Fatalf("rerun next to the repeat: %v %v", err, again)
	}
}

// The daemon's reported reset parks the runtime, not a time read back from
// the refusal text; without one, a reset that has just passed holds the
// runtime minutes, not until the same clock tomorrow (TOON-274).
func TestQuotaRefusalParksUntilReportedReset(t *testing.T) {
	r := newModelClaimRig(t)
	agent := r.agent("models reported reset", r.strong)
	now := time.Now().UTC()
	past := now.Add(-time.Minute).Format("2006-01-02 15:04")
	text := "usage limit: подписки runtime выше лимита агентам (остаток — резерв людям): a неделя 54% ≥ 50%; b 5ч 97% ≥ 75%, reset at " + past + " UTC"
	until := func() time.Time {
		var u time.Time
		r.fx.QueryRow(t, "SELECT until FROM runtime_unavailable WHERE runtime_id=$1 ORDER BY created_at DESC LIMIT 1", r.rt["codex"]).Scan(&u)
		return u
	}

	task := r.enqueue(agent, pgtype.UUID{})
	if r.claim("codex") == nil {
		t.Fatal("no claim")
	}
	reported := now.Add(25 * time.Minute).Truncate(time.Second)
	if _, _, err := r.svc.FailTaskWithQuotaReset(r.ctx, task.ID, text, "", "", "", "", false, "", "", reported); err != nil {
		t.Fatal(err)
	}
	if got := until(); !got.Equal(reported) {
		t.Fatalf("until %v, want reported %v", got, reported)
	}

	r.fx.Exec(t, "DELETE FROM runtime_unavailable WHERE workspace_id=$1", r.ws)
	r.fx.Exec(t, "UPDATE agent_task_queue SET status='cancelled' WHERE agent_id=$1 AND status IN ('queued','dispatched','running')", agent.ID)
	task = r.enqueue(agent, pgtype.UUID{})
	if r.claim("codex") == nil {
		t.Fatal("no second claim")
	}
	if _, err := r.svc.FailTask(r.ctx, task.ID, text, "", "", "", "", false, "", ""); err != nil {
		t.Fatal(err)
	}
	if d := until().Sub(now); d < 4*time.Minute || d > 6*time.Minute {
		t.Fatalf("past reset in the text: parked %v, want ~5m", d)
	}
}
