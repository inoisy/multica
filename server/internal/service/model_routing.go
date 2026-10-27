package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// A run is model-routed when its model comes from a list (an issue profile,
// an agent profile or an agent's own list). Its runtime is then the one the
// list chooses, re-chosen at every claim; it carries model_choice from the
// moment it is routed. Runs of agents on their plain fields stay exactly as
// before: no model_choice, the agent's runtime, the agent's model.

// ModelChoiceEnabled is the kill switch of server-side model choice:
// MULTICA_MODEL_CHOICE=off brings back the agent's own runtime and model for
// every new claim without a deploy.
func ModelChoiceEnabled() bool {
	return !strings.EqualFold(strings.TrimSpace(os.Getenv("MULTICA_MODEL_CHOICE")), "off")
}

// Inbox types of an agent standing still for want of a model.
const (
	InboxTypeAgentModelsExhausted = "agent_models_exhausted"
	InboxTypeAgentModelsRestored  = "agent_models_restored"
)

// errClaimNotThisRuntime rolls back a claim the model list sends elsewhere.
var errClaimNotThisRuntime = errors.New("model choice: run belongs to another runtime now")

// RunChoice is the choice a queued run would get if it were claimed now.
func RunChoice(ctx context.Context, q *db.Queries, task db.AgentTaskQueue, now time.Time) (ModelChoice, error) {
	choice, _, err := choiceForTask(ctx, q, task, now)
	return choice, err
}

// choiceForTask is the choice for one run, read with q.
func choiceForTask(ctx context.Context, q *db.Queries, task db.AgentTaskQueue, now time.Time) (ModelChoice, db.Agent, error) {
	agent, err := q.GetAgent(ctx, task.AgentID)
	if err != nil {
		return ModelChoice{}, agent, err
	}
	var issue *db.Issue
	if task.IssueID.Valid {
		if i, err := q.GetIssue(ctx, task.IssueID); err == nil {
			issue = &i
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return ModelChoice{}, agent, err
		}
	}
	choice, err := PreviewChoice(ctx, q, agent, issue, now)
	return choice, agent, err
}

// isModelRouted reports whether the run's model comes from a list.
func isModelRouted(task db.AgentTaskQueue, choice ModelChoice) bool {
	return task.ModelChoice != nil || choice.Source != ModelSourceAgentFields
}

// routeKey is what makes two routes of one run the same; equal keys mean the
// sweep has nothing to write.
func routeKey(runtime pgtype.UUID, c ModelChoice) string {
	wait := ""
	if c.WaitUntil != nil {
		wait = c.WaitUntil.UTC().Format(time.RFC3339)
	}
	return strings.Join([]string{util.UUIDToString(runtime), c.Source, c.ProfileID, c.Reason, fmt.Sprint(c.Position), wait}, "|")
}

func storedRouteKey(task db.AgentTaskQueue) string {
	if task.ModelChoice == nil {
		return ""
	}
	var c ModelChoice
	if json.Unmarshal(task.ModelChoice, &c) != nil {
		return ""
	}
	return routeKey(task.RuntimeID, c)
}

// idleNotice is an inbox item to publish after its transaction commits.
type idleNotice struct {
	item  db.InboxItem
	agent pgtype.UUID
}

// routeTask places an unclaimed model-routed run on the runtime its list
// chooses now. A run whose every model is unavailable keeps its runtime and
// records why it waits; the agent's idle episode opens in the same
// transaction. Returns the run as written and whether anything changed.
func (s *TaskService) routeTask(ctx context.Context, q *db.Queries, task db.AgentTaskQueue, now time.Time) (db.AgentTaskQueue, ModelChoice, bool, []idleNotice, error) {
	choice, agent, err := choiceForTask(ctx, q, task, now)
	if err != nil {
		return task, choice, false, nil, err
	}
	if !isModelRouted(task, choice) {
		return task, choice, false, nil, nil
	}
	runtime := task.RuntimeID
	if id := choice.RuntimeID(); id != "" {
		runtime = util.MustParseUUID(id)
	}
	var notices []idleNotice
	if choice.Waiting() && choice.Reason == ModelReasonAllUnavailable {
		if notices, err = s.openIdleEpisode(ctx, q, agent, task, choice); err != nil {
			return task, choice, false, nil, err
		}
	}
	if routeKey(runtime, choice) == storedRouteKey(task) {
		return task, choice, false, notices, nil
	}
	raw, _ := json.Marshal(choice)
	routed, err := q.SetTaskModelRoute(ctx, db.SetTaskModelRouteParams{ID: task.ID, RuntimeID: runtime, ModelChoice: raw})
	if errors.Is(err, pgx.ErrNoRows) {
		return task, choice, false, notices, nil
	}
	if err != nil {
		return task, choice, false, nil, err
	}
	return routed, choice, true, notices, nil
}

// RouteQueuedTask routes one run in its own transaction and wakes the runtime
// it now belongs to. Called after every enqueue; a failure leaves the run where
// it was for the sweep.
func (s *TaskService) RouteQueuedTask(ctx context.Context, task db.AgentTaskQueue) db.AgentTaskQueue {
	if !ModelChoiceEnabled() || task.Status != "queued" && task.Status != "deferred" {
		return task
	}
	var routed db.AgentTaskQueue
	var changed bool
	var notices []idleNotice
	err := s.runInTx(ctx, func(qtx *db.Queries) error {
		var err error
		routed, _, changed, notices, err = s.routeTask(ctx, qtx, task, time.Now())
		return err
	})
	if err != nil {
		slog.Warn("model choice: route queued run failed", "task_id", util.UUIDToString(task.ID), "error", err)
		return task
	}
	s.publishIdleNotices(notices)
	if changed && routed.RuntimeID != task.RuntimeID {
		slog.Info("model choice: run routed", "task_id", util.UUIDToString(task.ID),
			"from_runtime", util.UUIDToString(task.RuntimeID), "to_runtime", util.UUIDToString(routed.RuntimeID))
	}
	return routed
}

// claimModelChoice runs inside the claim transaction for a run the SQL just
// moved to dispatched. A model-routed run must run on the runtime its list
// chooses at this moment: when that is this runtime the choice is recorded
// on the run and the agent's idle episode ends; otherwise the claim is rolled
// back (errClaimNotThisRuntime) and the caller re-routes the run.
func (s *TaskService) claimModelChoice(ctx context.Context, q *db.Queries, task *db.AgentTaskQueue, runtimeID pgtype.UUID) ([]idleNotice, error) {
	if !ModelChoiceEnabled() {
		return nil, nil
	}
	choice, agent, err := choiceForTask(ctx, q, *task, time.Now())
	if err != nil {
		return nil, err
	}
	if !isModelRouted(*task, choice) {
		return nil, nil
	}
	if choice.Waiting() || choice.RuntimeID() != util.UUIDToString(runtimeID) {
		return nil, errClaimNotThisRuntime
	}
	raw, _ := json.Marshal(choice)
	if err := q.SetClaimedTaskModelChoice(ctx, db.SetClaimedTaskModelChoiceParams{
		ID: task.ID, ModelPosition: pgtype.Int4{Int32: choice.Position, Valid: true}, ModelChoice: raw,
	}); err != nil {
		return nil, err
	}
	task.ModelPosition = pgtype.Int4{Int32: choice.Position, Valid: true}
	task.ModelChoice = raw
	return s.closeIdleEpisode(ctx, q, agent, *task, choice)
}

func (s *TaskService) ownerNotices(ctx context.Context, q *db.Queries, agent db.Agent, task db.AgentTaskQueue, kind, severity, body string, details map[string]any) ([]idleNotice, error) {
	owners, err := q.ListWorkspaceOwnerUserIDs(ctx, agent.WorkspaceID)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(details)
	var notices []idleNotice
	for _, owner := range owners {
		item, err := q.CreateInboxItem(ctx, db.CreateInboxItemParams{
			ID: dbid.NewV7(), WorkspaceID: agent.WorkspaceID, RecipientType: "member", RecipientID: owner,
			Type: kind, Severity: severity, IssueID: task.IssueID, Title: agent.Name,
			Body:      pgtype.Text{String: body, Valid: true},
			ActorType: pgtype.Text{String: "system", Valid: true}, Details: raw,
		})
		if err != nil {
			return nil, err
		}
		notices = append(notices, idleNotice{item: item, agent: agent.ID})
	}
	return notices, nil
}

// openIdleEpisode tells the workspace owners once that the agent stands
// still: which run, until when, which runtimes are on a limit. A second call
// while the episode is open writes nothing.
func (s *TaskService) openIdleEpisode(ctx context.Context, q *db.Queries, agent db.Agent, task db.AgentTaskQueue, choice ModelChoice) ([]idleNotice, error) {
	limited := choice.LimitedRuntimes()
	runtimes, _ := json.Marshal(limited)
	var until pgtype.Timestamptz
	if choice.WaitUntil != nil {
		until = pgtype.Timestamptz{Time: *choice.WaitUntil, Valid: true}
	}
	episode, err := q.OpenModelIdleEpisode(ctx, db.OpenModelIdleEpisodeParams{
		WorkspaceID: agent.WorkspaceID, AgentID: agent.ID, TaskID: task.ID, Until: until, Runtimes: runtimes,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	body := "Every model of the agent's list is on a limit"
	if choice.WaitUntil != nil {
		body += " until " + choice.WaitUntil.UTC().Format("2006-01-02 15:04 UTC")
	}
	if len(limited) > 0 {
		body += " (" + strings.Join(limited, ", ") + ")"
	}
	body += "; the run waits."
	details := map[string]any{
		"episode_id": util.UUIDToString(episode.ID), "agent_id": util.UUIDToString(agent.ID), "task_id": util.UUIDToString(task.ID),
		"runtimes": limited, "source": choice.Source, "profile_name": choice.ProfileName,
	}
	if choice.WaitUntil != nil {
		details["until"] = choice.WaitUntil.UTC().Format(time.RFC3339)
	}
	return s.ownerNotices(ctx, q, agent, task, InboxTypeAgentModelsExhausted, "attention", body, details)
}

// closeIdleEpisode ends the agent's open episode, if any, with one short
// "working again" notice.
func (s *TaskService) closeIdleEpisode(ctx context.Context, q *db.Queries, agent db.Agent, task db.AgentTaskQueue, choice ModelChoice) ([]idleNotice, error) {
	episode, err := q.CloseModelIdleEpisode(ctx, agent.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	model := ""
	if choice.Chosen != nil {
		model = strings.Join(nonEmpty(choice.RuntimeName, choice.Chosen.Model, choice.Chosen.ThinkingLevel), "·")
	}
	details := map[string]any{
		"episode_id": util.UUIDToString(episode.ID), "agent_id": util.UUIDToString(agent.ID), "task_id": util.UUIDToString(task.ID),
		"model": model, "position": choice.Position,
	}
	return s.ownerNotices(ctx, q, agent, task, InboxTypeAgentModelsRestored, "info", "The agent is working again on "+model+".", details)
}

func (s *TaskService) publishIdleNotices(notices []idleNotice) {
	if s.Bus == nil {
		return
	}
	for _, n := range notices {
		item := n.item
		s.Bus.Publish(events.Event{
			Type: protocol.EventInboxNew, WorkspaceID: util.UUIDToString(item.WorkspaceID), ActorType: "system",
			Payload: map[string]any{"item": map[string]any{
				"id": util.UUIDToString(item.ID), "workspace_id": util.UUIDToString(item.WorkspaceID),
				"recipient_type": item.RecipientType, "recipient_id": util.UUIDToString(item.RecipientID),
				"type": item.Type, "severity": item.Severity, "issue_id": util.UUIDToPtr(item.IssueID),
				"title": item.Title, "body": util.TextToPtr(item.Body), "read": item.Read, "archived": item.Archived,
				"created_at": util.TimestampToString(item.CreatedAt), "actor_type": util.TextToPtr(item.ActorType),
				"actor_id": util.UUIDToPtr(item.ActorID), "details": json.RawMessage(item.Details),
			}},
		})
	}
}

// SweepModelRouting is the scheduler job: lifts expired runtime limits, the
// daemon's refusals a usable account has made stale, and
// re-routes every unclaimed model-routed run whose choice changed, so a run
// that waited is claimed by itself once a model is free, and a run on an
// offline runtime moves to the next row. Two runs in a row with nothing new
// change nothing.
func (s *TaskService) SweepModelRouting(ctx context.Context) (int, error) {
	if !ModelChoiceEnabled() {
		return 0, nil
	}
	expired, err := s.Queries.DeleteExpiredRuntimeUnavailable(ctx)
	if err != nil {
		return 0, err
	}
	for _, rec := range expired {
		slog.Info("model choice: runtime limit lifted", "runtime_id", util.UUIDToString(rec.RuntimeID), "until", rec.Until.Time)
	}
	if _, err := LiftStaleRefusals(ctx, s.Queries, time.Now()); err != nil {
		slog.Warn("model choice: lift stale refusals failed", "error", err)
	}
	tasks, err := s.Queries.ListQueuedTasksForModelRouting(ctx, 500)
	if err != nil {
		return 0, err
	}
	changed := 0
	var errs []error
	for _, t := range tasks {
		var routed db.AgentTaskQueue
		var moved bool
		var notices []idleNotice
		err := s.runInTx(ctx, func(qtx *db.Queries) error {
			var err error
			routed, _, moved, notices, err = s.routeTask(ctx, qtx, t, time.Now())
			return err
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("task %s: %w", util.UUIDToString(t.ID), err))
			continue
		}
		s.publishIdleNotices(notices)
		if moved {
			changed++
			s.notifyTaskAvailable(routed)
		}
	}
	return changed, errors.Join(errs...)
}

// LiftStaleRefusals deletes the daemon's own refusals (on the agent limit, on
// disabled accounts) of every runtime whose pool has a usable account at now:
// the snapshot that shows the account usable is newer than the refusal. A
// provider's refusal stays until its reset. It returns what it removed.
func LiftStaleRefusals(ctx context.Context, q *db.Queries, now time.Time) ([]db.RuntimeUnavailable, error) {
	rows, err := q.ListActiveLocalRefusals(ctx, pgtype.Timestamptz{Time: now, Valid: true})
	if err != nil {
		return nil, err
	}
	byRuntime := map[string][]db.RuntimeUnavailable{}
	var order []string
	for _, rec := range rows {
		if code := StoredRefusalCode(rec); code != UnavailableAgentCapRefusal && code != UnavailableDisabledRefusal {
			continue
		}
		key := util.UUIDToString(rec.RuntimeID)
		if _, seen := byRuntime[key]; !seen {
			order = append(order, key)
		}
		byRuntime[key] = append(byRuntime[key], rec)
	}
	var lifted []db.RuntimeUnavailable
	for _, key := range order {
		verdict, err := runtimePoolVerdict(ctx, q, byRuntime[key][0].RuntimeID, now)
		if err != nil {
			return lifted, err
		}
		if verdict == nil || !verdict.Usable {
			continue
		}
		ids := make([]pgtype.UUID, len(byRuntime[key]))
		for i, rec := range byRuntime[key] {
			ids[i] = rec.ID
		}
		deleted, err := q.DeleteRuntimeUnavailableByIDs(ctx, ids)
		if err != nil {
			return lifted, err
		}
		for _, row := range deleted {
			slog.Info("model choice: stale refusal lifted", "runtime_id", key, "until", row.Until.Time, "reason_code", StoredRefusalCode(row))
		}
		lifted = append(lifted, deleted...)
	}
	return lifted, nil
}

// runtimePoolVerdict is the verdict on the pool a runtime spends, nil when it
// spends none.
func runtimePoolVerdict(ctx context.Context, q *db.Queries, runtimeID pgtype.UUID, now time.Time) (*PoolVerdict, error) {
	rt, err := q.GetAgentRuntime(ctx, runtimeID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !rt.ProfileID.Valid) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	profile, err := q.GetRuntimeProfile(ctx, rt.ProfileID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !profile.PoolID.Valid) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	poolID := util.UUIDToString(profile.PoolID)
	accounts, members, err := poolAccounts(ctx, q, map[string]string{util.UUIDToString(profile.ID): poolID}, now)
	if err != nil {
		return nil, err
	}
	v := EvaluatePool(poolAccountList(accounts, members[poolID]), rt.DaemonID.String, now)
	return &v, nil
}
