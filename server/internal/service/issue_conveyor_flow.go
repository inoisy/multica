package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

func (s *TaskService) conveyor() *IssueConveyorService { return &IssueConveyorService{Tasks: s} }

// conveyorStallAfter is how long a route may say "a run is working" with no
// run and nothing owed before it is handed to its coordinator.
const conveyorStallAfter = 5 * time.Minute

func isQuotaFailure(task db.AgentTaskQueue) bool {
	// A persisted reason is authoritative: test output and filesystem errors
	// can contain provider vocabulary without being provider failures.
	reason := taskfailure.Reason(strings.TrimSpace(task.FailureReason.String))
	if reason == "" {
		reason = taskfailure.Classify(task.Error.String)
	}
	return reason == taskfailure.ReasonAgentProviderQuotaLimit || reason == taskfailure.ReasonAgentProviderCapacityOrRateLimit
}

// isInfraFailure reports whether the run was cut short by something other
// than its work: the daemon stopped or restarted (cancelled), the runtime went
// away, the provider failed on its side. Such a step is worth starting again;
// a person's cancel (user_cancelled) is not.
func isInfraFailure(task db.AgentTaskQueue) bool {
	switch taskfailure.Reason(strings.TrimSpace(task.FailureReason.String)) {
	case "cancelled", "idle_watchdog",
		taskfailure.ReasonRuntimeOffline, taskfailure.ReasonRuntimeReconnectTimeout, taskfailure.ReasonRuntimeRecovery,
		taskfailure.ReasonQueuedExpired, taskfailure.ReasonAgentProviderServerError, taskfailure.ReasonAgentProviderNetwork:
		return true
	}
	return false
}

// conveyorVerdict is what the route decides about a run that ended.
type conveyorVerdict struct {
	action string
	reason string
	report string
	commit string
}

// judge decides, from facts only, what the end of the current run means.
// A failed run never advances the route, whatever result it handed in.
func judge(cv db.IssueConveyor, route []ConveyorNode, node ConveyorNode, task db.AgentTaskQueue, res *ConveyorResultInput) conveyorVerdict {
	if task.Status == "failed" {
		if isQuotaFailure(task) {
			return conveyorVerdict{action: conveyorDecisionWaitProvider, reason: "provider limit: " + clip(task.FailureReason.String, 200)}
		}
		return conveyorVerdict{action: conveyorDecisionEscalate, reason: "the run failed (" + clip(task.FailureReason.String, 100) + "): " + clip(task.Error.String, 300)}
	}
	if task.Status != "completed" {
		return conveyorVerdict{action: conveyorDecisionEscalate, reason: "the run ended as " + task.Status}
	}
	if res == nil {
		return conveyorVerdict{action: conveyorDecisionEscalate, reason: "the run completed without handing in a result"}
	}
	if res.Outcome != ConveyorOutcomePass {
		return conveyorVerdict{action: conveyorDecisionEscalate, reason: "outcome " + res.Outcome, report: clip(res.Report, 1500)}
	}
	for _, c := range res.Checks {
		if !c.OK {
			return conveyorVerdict{action: conveyorDecisionEscalate, reason: fmt.Sprintf("check %q failed", c.Name), report: clip(res.Report, 1500)}
		}
	}
	commit := res.Artifact.Commit
	switch node.Step {
	case conveyorStepDev:
		if commit == "" {
			return conveyorVerdict{action: conveyorDecisionEscalate, reason: "dev passed without artifact.commit", report: clip(res.Report, 1500)}
		}
		return conveyorVerdict{action: conveyorDecisionAdvance, commit: commit}
	case conveyorStepQA, conveyorStepReview, conveyorStepAccept:
		accepted := cv.AcceptedCommit.String
		// A later verifier that fixed the work itself (the reviewer committed on
		// top of what QA passed) hands in its own commit: the earlier verifiers
		// judge it again, the coordinator is not needed for that.
		if accepted != "" && commit != "" && commit != accepted && node.Step != conveyorStepAccept && firstVerifierBefore(route, node.Idx) >= 0 {
			return conveyorVerdict{action: conveyorDecisionRecheck, reason: fmt.Sprintf("%s handed in its own commit %q instead of %q", node.Step, commit, accepted), report: clip(res.Report, 4000), commit: commit}
		}
		if accepted != "" && commit != accepted {
			return conveyorVerdict{action: conveyorDecisionEscalate, reason: fmt.Sprintf("%s judged commit %q, the accepted one is %q", node.Step, commit, accepted), report: clip(res.Report, 1500)}
		}
		// A route that starts at qa has no dev step to name the commit: the
		// first verifier's commit is the one the next verifier must judge.
		if accepted == "" && commit != "" {
			return conveyorVerdict{action: conveyorDecisionAdvance, commit: commit}
		}
	}
	return conveyorVerdict{action: conveyorDecisionAdvance}
}

func clip(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

// OnTaskTerminal is called in the transaction that moved a run to completed or
// failed, after the task row is written (lock order: task, then route). A run
// of the route's current step becomes an input; the decision and the run it
// owes are written in the same transaction, so a rollback leaves neither.
// Replays find the unique keys taken and change nothing.
func (s *IssueConveyorService) OnTaskTerminal(ctx context.Context, q *db.Queries, task db.AgentTaskQueue) error {
	if !task.ConveyorActionID.Valid || !task.IssueID.Valid {
		return nil
	}
	cv, err := q.LockIssueConveyor(ctx, task.IssueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	action, err := q.GetIssueConveyorEvent(ctx, task.ConveyorActionID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && action.ConveyorID != cv.ID) {
		return nil
	}
	if err != nil {
		return err
	}
	// A coordinator run is the end of a hand-over, not a step to judge.
	if action.Action.String != conveyorActionRunStep {
		return nil
	}
	inputDetails, _ := json.Marshal(map[string]any{"status": task.Status, "failure_reason": task.FailureReason.String})
	if _, err = s.record(ctx, q, cv, db.InsertIssueConveyorEventParams{
		Kind: conveyorEventInput, SourceTaskID: task.ID, Revision: action.Revision, Idx: action.Idx, Round: action.Round,
		Action: action.Action, TaskID: task.ID, Details: inputDetails,
	}); errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}

	decide := func(verdict, reason string, extra map[string]any) error {
		d := map[string]any{"status": task.Status}
		for k, v := range extra {
			d[k] = v
		}
		raw, _ := json.Marshal(d)
		_, err := s.record(ctx, q, cv, db.InsertIssueConveyorEventParams{
			Kind: conveyorEventDecision, SourceTaskID: task.ID, Revision: action.Revision, Idx: action.Idx, Round: action.Round,
			Action: pgtype.Text{String: verdict, Valid: true}, Reason: pgtype.Text{String: reason, Valid: reason != ""}, Details: raw,
		})
		return err
	}
	if !isCurrentStep(cv, action) {
		return decide(conveyorDecisionStale, "the run is not the route's current assignment", nil)
	}
	if cv.Status == ConveyorPaused {
		return decide(conveyorDecisionHeld, "the route is paused", nil)
	}
	if cv.Status != ConveyorActive && cv.Status != ConveyorWaitingProvider {
		return decide(conveyorDecisionStale, "the route is "+cv.Status, nil)
	}
	route, err := conveyorRoute(cv)
	if err != nil {
		return err
	}
	node := conveyorNodeAt(route, cv.CurIdx)
	if node == nil {
		return fmt.Errorf("conveyor %s: no node at %d", util.UUIDToString(cv.ID), cv.CurIdx)
	}
	var res *ConveyorResultInput
	if ev, rerr := q.GetIssueConveyorRunEvent(ctx, db.GetIssueConveyorRunEventParams{ConveyorID: cv.ID, SourceTaskID: task.ID, Kind: conveyorEventResult}); rerr == nil {
		var parsed ConveyorResultInput
		if json.Unmarshal(ev.Details, &parsed) == nil {
			res = &parsed
		}
	} else if !errors.Is(rerr, pgx.ErrNoRows) {
		return rerr
	}
	v := judge(cv, route, *node, task, res)

	// A run infrastructure cut short is started again by itself while the
	// node's retries last: a daemon restart must not cost a coordinator run.
	if v.action == conveyorDecisionEscalate && task.Status == "failed" && isInfraFailure(task) {
		retried, err := s.revisionDecisions(ctx, q, cv, conveyorDecisionRetry, node.Idx)
		if err != nil {
			return err
		}
		if int32(len(retried)) < node.retries() {
			cv.Round, cv.Status, cv.CurTaskID, cv.WaitingReason = cv.Round+1, ConveyorActive, pgtype.UUID{}, pgtype.Text{}
			if _, err = q.SaveIssueConveyor(ctx, saveParams(cv)); err != nil {
				return err
			}
			why := fmt.Sprintf("the run of %s was cut short (%s): round %d starts it again", node.Step, task.FailureReason.String, cv.Round)
			owed, err := s.owe(ctx, q, cv, conveyorActionRunStep, cv.CurIdx, why, map[string]any{"source_task_id": util.UUIDToString(task.ID)})
			if err != nil {
				return err
			}
			return decide(conveyorDecisionRetry, why, map[string]any{"action_id": util.UUIDToString(owed.ID), "to_idx": cv.CurIdx})
		}
		v.reason = fmt.Sprintf("%s; %d retr(ies) spent", v.reason, node.retries())
	}

	// A later verifier's own commit goes back to the first verifier; the
	// verifier that made it is passed over on the way (it judged its fix).
	if v.action == conveyorDecisionRecheck {
		rechecks, err := s.revisionDecisions(ctx, q, cv, conveyorDecisionRecheck, -1)
		if err != nil {
			return err
		}
		if len(rechecks) >= conveyorMaxRechecks {
			v.action, v.reason = conveyorDecisionEscalate, fmt.Sprintf("%s; %d re-check(s) spent", v.reason, conveyorMaxRechecks)
		} else {
			first := firstVerifierBefore(route, cv.CurIdx)
			author := cv.CurIdx
			cv.AcceptedCommit = pgtype.Text{String: v.commit, Valid: true}
			cv.CurIdx, cv.Round = first, cv.Round+1
			cv.Status, cv.CurTaskID, cv.WaitingReason = ConveyorActive, pgtype.UUID{}, pgtype.Text{}
			if _, err = q.SaveIssueConveyor(ctx, saveParams(cv)); err != nil {
				return err
			}
			why := fmt.Sprintf("step %s committed %s itself: round %d checks it again from %s", node.Step, clip(v.commit, 9), cv.Round, route[first].Step)
			owed, err := s.owe(ctx, q, cv, conveyorActionRunStep, first, why, map[string]any{
				"report": v.report, "recheck_by": node.Step, "source_task_id": util.UUIDToString(task.ID),
			})
			if err != nil {
				return err
			}
			return decide(conveyorDecisionRecheck, why, map[string]any{
				"action_id": util.UUIDToString(owed.ID), "to_idx": first, "author_idx": author, "commit": v.commit, "round": cv.Round,
			})
		}
	}

	// A verifier that asked for changes returns the work by itself when its
	// node says where to, while rounds are left; the coordinator is woken
	// only when they run out.
	if v.action == conveyorDecisionEscalate && res != nil && res.Outcome == ConveyorOutcomeChangesRequested && node.Back != "" {
		backs, err := q.CountIssueConveyorBacks(ctx, db.CountIssueConveyorBacksParams{ConveyorID: cv.ID, Revision: pgtype.Int8{Int64: cv.Revision, Valid: true}})
		if err != nil {
			return err
		}
		if backIdx := stepIdxBefore(route, node.Back, cv.CurIdx); backIdx >= 0 && int32(backs)+1 < node.backRounds() {
			backNode := route[backIdx]
			cv.CurIdx, cv.Round = backIdx, cv.Round+1
			cv.Status, cv.CurTaskID, cv.WaitingReason = ConveyorActive, pgtype.UUID{}, pgtype.Text{}
			if backNode.Step != conveyorStepQA && backNode.Step != conveyorStepReview && backNode.Step != conveyorStepAccept {
				cv.AcceptedCommit = pgtype.Text{}
			}
			if _, err = q.SaveIssueConveyor(ctx, saveParams(cv)); err != nil {
				return err
			}
			why := fmt.Sprintf("step %s requested changes: round %d starts at %s", node.Step, cv.Round, backNode.Step)
			owed, err := s.owe(ctx, q, cv, conveyorActionRunStep, backIdx, why, map[string]any{
				"report": clip(res.Report, 4000), "returned_by": node.Step, "source_task_id": util.UUIDToString(task.ID),
			})
			if err != nil {
				return err
			}
			return decide(conveyorDecisionBack, why, map[string]any{"action_id": util.UUIDToString(owed.ID), "to_idx": backIdx})
		}
		v.reason = fmt.Sprintf("%s; %d round(s) of back=%s spent", v.reason, node.backRounds(), node.Back)
	}

	switch v.action {
	case conveyorDecisionWaitProvider:
		cv.Status, cv.WaitingReason = ConveyorWaitingProvider, pgtype.Text{String: v.reason, Valid: true}
		if _, err = q.SaveIssueConveyor(ctx, saveParams(cv)); err != nil {
			return err
		}
		return decide(v.action, v.reason, nil)
	case conveyorDecisionEscalate:
		cv.Status, cv.WaitingReason = ConveyorAwaitingCoordinator, pgtype.Text{String: v.reason, Valid: true}
		if _, err = q.SaveIssueConveyor(ctx, saveParams(cv)); err != nil {
			return err
		}
		owed, err := s.owe(ctx, q, cv, conveyorActionNotifyCoordinator, cv.CurIdx, v.reason, map[string]any{"report": v.report, "source_task_id": util.UUIDToString(task.ID)})
		if err != nil {
			return err
		}
		return decide(v.action, v.reason, map[string]any{"action_id": util.UUIDToString(owed.ID)})
	}

	// advance
	if v.commit != "" {
		cv.AcceptedCommit = pgtype.Text{String: v.commit, Valid: true}
	}
	next := cv.CurIdx + 1
	verdict, owedAction, why := conveyorDecisionAdvance, conveyorActionRunStep, "step "+node.Step+" passed"
	// The verifier whose own commit this round re-checks judged it already.
	if author, ok, err := s.recheckAuthor(ctx, q, cv); err != nil {
		return err
	} else if ok && author == next && int(next) < len(route)-1 {
		next++
		why = fmt.Sprintf("step %s passed; %s made this commit and is passed over", node.Step, route[author].Step)
	}
	cv.CurIdx, cv.CurTaskID, cv.WaitingReason = next, pgtype.UUID{}, pgtype.Text{}
	if int(next) == len(route)-1 {
		verdict, owedAction, why = conveyorDecisionComplete, conveyorActionNotifyCoordinator, "every step passed"
		cv.Status = ConveyorCompleted
	}
	if _, err = q.SaveIssueConveyor(ctx, saveParams(cv)); err != nil {
		return err
	}
	owed, err := s.owe(ctx, q, cv, owedAction, next, why, map[string]any{"source_task_id": util.UUIDToString(task.ID)})
	if err != nil {
		return err
	}
	return decide(verdict, why, map[string]any{"action_id": util.UUIDToString(owed.ID), "to_idx": next})
}

// isCurrentStep reports whether the action is the route's present position.
// The revision is not compared: pausing and resuming must not orphan a run
// that is already executing.
func isCurrentStep(cv db.IssueConveyor, action db.IssueConveyorEvent) bool {
	return action.Idx.Valid && action.Round.Valid && action.Idx.Int32 == cv.CurIdx && action.Round.Int32 == cv.Round
}

// staleActionReason is why an action may no longer start a run; "" when it may.
func staleActionReason(cv db.IssueConveyor, action db.IssueConveyorEvent, issueOpen bool) string {
	switch {
	case !issueOpen:
		return "the issue is closed"
	case cv.Status == ConveyorDisabled:
		return "the route is disabled"
	case cv.Status == ConveyorPaused:
		return "the route is paused"
	case !action.Revision.Valid || action.Revision.Int64 != cv.Revision:
		return "the action belongs to an older revision"
	case !isCurrentStep(cv, action):
		return "the action is no longer the current position"
	}
	switch action.Action.String {
	case conveyorActionRunStep:
		if cv.Status != ConveyorActive {
			return "the route is " + cv.Status
		}
	case conveyorActionNotifyCoordinator:
		if cv.Status != ConveyorAwaitingCoordinator && cv.Status != ConveyorCompleted {
			return "the route is " + cv.Status
		}
	}
	return ""
}

func (s *IssueConveyorService) issueOpen(ctx context.Context, q *db.Queries, issueID pgtype.UUID) (bool, error) {
	issue, err := q.GetIssue(ctx, issueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return wakeupIssueActive(ctx, q, issue)
}

// StaleReason is why a run of the route must not start now; "" when it may,
// or when the run is not a route run at all.
func (s *IssueConveyorService) StaleReason(ctx context.Context, q *db.Queries, task db.AgentTaskQueue) (string, error) {
	if !task.ConveyorActionID.Valid || !task.IssueID.Valid {
		return "", nil
	}
	action, err := q.GetIssueConveyorEvent(ctx, task.ConveyorActionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "the route action was removed", nil
	}
	if err != nil {
		return "", err
	}
	cv, err := q.GetIssueConveyor(ctx, task.IssueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "the route was removed", nil
	}
	if err != nil {
		return "", err
	}
	open, err := s.issueOpen(ctx, q, task.IssueID)
	if err != nil {
		return "", err
	}
	return staleActionReason(cv, action, open), nil
}

// RejectStale withdraws a run that must not start and journals why.
func (s *IssueConveyorService) RejectStale(ctx context.Context, task db.AgentTaskQueue, reason string) {
	if cv, err := s.q().GetIssueConveyor(ctx, task.IssueID); err == nil {
		_, err = s.record(ctx, s.q(), cv, db.InsertIssueConveyorEventParams{
			Kind: conveyorEventRejectedStale, SourceTaskID: task.ID, TaskID: task.ID,
			Reason: pgtype.Text{String: reason, Valid: true},
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("conveyor: journal stale run failed", "task_id", util.UUIDToString(task.ID), "error", err)
		}
	}
	if _, err := s.Tasks.CancelTaskWithReason(ctx, task.ID, "The route moved on before this run started: "+reason, "conveyor_stale"); err != nil {
		slog.Warn("conveyor: cancel stale run failed", "task_id", util.UUIDToString(task.ID), "error", err)
	}
}

// withdrawStaleTasks cancels runs of the route that no executor has claimed
// and whose action is no longer current.
func (s *IssueConveyorService) withdrawStaleTasks(ctx context.Context, cv db.IssueConveyor) {
	tasks, err := s.q().ListUnstartedConveyorTasks(ctx, cv.ID)
	if err != nil {
		slog.Warn("conveyor: list unstarted runs failed", "conveyor_id", util.UUIDToString(cv.ID), "error", err)
		return
	}
	for _, t := range tasks {
		if reason, err := s.StaleReason(ctx, s.q(), t); err == nil && reason != "" {
			s.RejectStale(ctx, t, reason)
		}
	}
}

// DeliverPending creates the runs the route owes for one issue. Failures stay
// pending for the job, including failures to update the issue's status.
func (s *IssueConveyorService) DeliverPending(ctx context.Context, issueID pgtype.UUID) {
	pending, err := s.q().ListPendingIssueConveyorActions(ctx, db.ListPendingIssueConveyorActionsParams{IssueID: issueID, PageSize: 20})
	if err != nil {
		slog.Warn("conveyor: list pending actions failed", "issue_id", util.UUIDToString(issueID), "error", err)
		return
	}
	for _, a := range pending {
		if err := s.deliver(ctx, a.ID); err != nil {
			slog.Warn("conveyor: deliver action failed", "action_id", util.UUIDToString(a.ID), "error", err)
		}
	}
}

// DeliverAllPending is the job body: every owed run on any issue.
func (s *IssueConveyorService) DeliverAllPending(ctx context.Context) error {
	pending, err := s.q().ListPendingIssueConveyorActions(ctx, db.ListPendingIssueConveyorActionsParams{PageSize: 100})
	if err != nil {
		return err
	}
	var errs []error
	for _, a := range pending {
		if err := s.deliver(ctx, a.ID); err != nil {
			errs = append(errs, fmt.Errorf("action %s: %w", util.UUIDToString(a.ID), err))
		}
	}
	if stallErr := s.EscalateStalled(ctx); stallErr != nil {
		errs = append(errs, stallErr)
	}
	return errors.Join(errs...)
}

// deliver creates the run an action owes. The overlay is resolved before any
// lock is taken (it may call out); the action is re-checked under the route
// lock, so two deliverers create one run and a stale action creates none.
func (s *IssueConveyorService) deliver(ctx context.Context, actionID pgtype.UUID) error {
	action, err := s.q().GetIssueConveyorEvent(ctx, actionID)
	if err != nil {
		return err
	}
	if action.State.String != "pending" {
		return nil
	}
	var details struct {
		AgentID string `json:"agent_id"`
	}
	_ = json.Unmarshal(action.Details, &details)
	agentID, err := util.ParseUUID(details.AgentID)
	if err != nil {
		return fmt.Errorf("action %s: bad agent id: %w", util.UUIDToString(actionID), err)
	}
	issue, err := s.q().GetIssue(ctx, action.IssueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	preview, err := s.q().GetIssueConveyor(ctx, action.IssueID)
	if err != nil {
		return err
	}
	var overlay runtimeMCPOverlayData
	if agent, aerr := s.q().GetAgent(ctx, agentID); aerr == nil && !agent.ArchivedAt.Valid && agent.RuntimeID.Valid && !isAcceptCarrier(agent) && CanMemberInvokeAgent(ctx, s.q(), agent, preview.EnabledBy, issue.WorkspaceID) {
		overlay = s.Tasks.buildRuntimeMCPOverlay(ctx, preview.EnabledBy, agent)
	}

	tx, err := s.Tasks.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := s.Tasks.Queries.WithTx(tx)
	// Catalog -> issue -> route. Terminal writes hold task -> route and never
	// take the issue row. Delivery only inserts a new task, so these paths
	// cannot wait on each other in reverse order.
	if err = q.LockIssueStatusCatalogShared(ctx, issue.WorkspaceID); err != nil {
		return err
	}
	issue, err = q.LockIssueForConveyorDelivery(ctx, db.LockIssueForConveyorDeliveryParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	cv, err := q.LockIssueConveyor(ctx, action.IssueID)
	if err != nil {
		return err
	}
	action, err = q.LockIssueConveyorEvent(ctx, actionID)
	if err != nil {
		return err
	}
	if action.State.String != "pending" {
		return nil
	}
	open, err := s.issueOpen(ctx, q, action.IssueID)
	if err != nil {
		return err
	}
	if why := staleActionReason(cv, action, open); why != "" {
		if err = q.CancelIssueConveyorAction(ctx, db.CancelIssueConveyorActionParams{ID: action.ID, Reason: pgtype.Text{String: why, Valid: true}}); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	// A route that passed every step closes the issue itself: the coordinator
	// is woken only when something needs a decision, not to say "done".
	if action.Action.String == conveyorActionNotifyCoordinator && cv.Status == ConveyorCompleted {
		return s.autoClose(ctx, tx, q, issue, cv, action)
	}
	wait := func(reason string) error {
		if err := q.SetIssueConveyorActionReason(ctx, db.SetIssueConveyorActionReasonParams{ID: action.ID, Reason: pgtype.Text{String: reason, Valid: true}}); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	agent, err := q.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: agentID, WorkspaceID: cv.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		// GetAgentInWorkspace sees only kind = 'user'; the accept step runs on the
		// hidden system agent, so look it up by id and accept only that carrier.
		if carrier, cerr := q.GetAgent(ctx, agentID); cerr == nil && carrier.WorkspaceID == cv.WorkspaceID && isAcceptCarrier(carrier) {
			agent, err = carrier, nil
		}
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return wait("agent not found in the workspace")
	}
	if err != nil {
		return err
	}
	if agent.ArchivedAt.Valid || !agent.RuntimeID.Valid {
		return wait("agent is archived or has no runtime")
	}
	// The hidden accept agent belongs to no member: the route itself runs it.
	if !isAcceptCarrier(agent) && !CanMemberInvokeAgent(ctx, q, agent, cv.EnabledBy, cv.WorkspaceID) {
		return wait("the person who enabled the route may not invoke the agent")
	}
	route, err := conveyorRoute(cv)
	if err != nil {
		return err
	}
	node := conveyorNodeAt(route, cv.CurIdx)
	if node == nil {
		return fmt.Errorf("conveyor has no node at %d", cv.CurIdx)
	}
	status := conveyorIssueStatus(node.Step)
	if status != "" {
		if err = validateConveyorIssueStatus(ctx, q, cv.WorkspaceID, status); err != nil {
			return wait(err.Error())
		}
	}
	var squadID pgtype.UUID
	isLeader := false
	if issue.AssigneeType.String == "squad" && issue.AssigneeID.Valid {
		if squad, serr := q.GetSquad(ctx, issue.AssigneeID); serr == nil && squad.LeaderID == agent.ID {
			squadID, isLeader = squad.ID, true
		}
	}
	taskContext, _ := json.Marshal(map[string]any{"conveyor": conveyorTaskContext(cv, action)})
	task, err := q.CreateConveyorTask(ctx, db.CreateConveyorTaskParams{
		ID: dbid.NewV7(), AgentID: agent.ID, RuntimeID: agent.RuntimeID, IssueID: issue.ID,
		Priority:       priorityToInt(issue.Priority),
		TriggerSummary: pgtype.Text{String: conveyorSummary(action, route), Valid: true},
		IsLeaderTask:   isLeader, HandoffNote: pgtype.Text{String: conveyorHandoffNote(issue, cv, action, route), Valid: true},
		SquadID: squadID, TaskContext: taskContext, OriginatorUserID: cv.EnabledBy,
		RuntimeMcpOverlay: overlay.Overlay, RuntimeConnectedApps: overlay.ConnectedApps, ActionID: action.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return wait("the workspace is being removed")
	}
	if err != nil {
		return err
	}
	if err = q.SetIssueConveyorActionDelivered(ctx, db.SetIssueConveyorActionDeliveredParams{ID: action.ID, TaskID: task.ID}); err != nil {
		return err
	}
	cv.CurTaskID = task.ID
	if _, err = q.SaveIssueConveyor(ctx, saveParams(cv)); err != nil {
		return err
	}
	prevStatus := issue.Status
	if status != "" && status != issue.Status {
		issue, err = q.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID, Status: status})
		if err != nil {
			return err
		}
	}
	value, _ := json.Marshal(node.Step)
	_, err = q.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID, Key: "step", Value: value})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	issue, err = q.GetIssue(ctx, issue.ID)
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if s.Tasks.Bus != nil {
		s.Tasks.broadcastIssueUpdated(ctx, issue, prevStatus)
	}
	s.Tasks.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
	s.Tasks.NotifyTaskEnqueued(ctx, task)
	return nil
}

func conveyorIssueStatus(step string) string {
	switch step {
	case conveyorStepDev:
		return issuestatus.InProgress
	case conveyorStepQA, conveyorStepReview:
		return step
	case conveyorStepClose:
		return issuestatus.InReview
	default:
		return ""
	}
}

func validateConveyorIssueStatus(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID, status string) error {
	entry, err := issuestatus.Resolve(ctx, q, workspaceID, status)
	if err != nil {
		return fmt.Errorf("route status %q is unavailable: %w", status, err)
	}
	if entry.Category != issuestatus.CategoryStarted {
		return fmt.Errorf("route status %q must belong to the started category", status)
	}
	return nil
}

func conveyorTaskContext(cv db.IssueConveyor, action db.IssueConveyorEvent) map[string]any {
	return map[string]any{
		"conveyor_id": util.UUIDToString(cv.ID), "action_id": util.UUIDToString(action.ID), "kind": action.Action.String,
		"revision": action.Revision.Int64, "idx": action.Idx.Int32, "round": action.Round.Int32,
	}
}

func conveyorSummary(action db.IssueConveyorEvent, route []ConveyorNode) string {
	step := ""
	if node := conveyorNodeAt(route, action.Idx.Int32); node != nil {
		step = node.Step
	}
	if action.Action.String == conveyorActionNotifyCoordinator {
		return fmt.Sprintf("Route needs its coordinator (step %s, round %d)", step, action.Round.Int32)
	}
	return fmt.Sprintf("Route step %s (round %d)", step, action.Round.Int32)
}

// EscalateStalled hands routes whose current run vanished to their coordinator.
func (s *IssueConveyorService) EscalateStalled(ctx context.Context) error {
	before := pgtype.Timestamptz{Time: time.Now().Add(-conveyorStallAfter), Valid: true}
	stalled, err := s.q().ListStalledIssueConveyors(ctx, db.ListStalledIssueConveyorsParams{Before: before, PageSize: 50})
	if err != nil {
		return err
	}
	var errs []error
	for _, c := range stalled {
		if err := s.escalateStalled(ctx, c.IssueID, before); err != nil {
			errs = append(errs, fmt.Errorf("route %s: %w", util.UUIDToString(c.ID), err))
		}
	}
	for _, c := range stalled {
		s.DeliverPending(ctx, c.IssueID)
	}
	return errors.Join(errs...)
}

func (s *IssueConveyorService) escalateStalled(ctx context.Context, issueID pgtype.UUID, before pgtype.Timestamptz) error {
	tx, err := s.Tasks.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := s.Tasks.Queries.WithTx(tx)
	cv, err := q.LockIssueConveyor(ctx, issueID)
	if err != nil {
		return err
	}
	stalled, err := q.IsIssueConveyorStalled(ctx, db.IsIssueConveyorStalledParams{ID: cv.ID, Before: before})
	if err != nil || !stalled {
		return err
	}
	const reason = "no run is working on the current step and none is owed"
	cv.Status, cv.WaitingReason = ConveyorAwaitingCoordinator, pgtype.Text{String: reason, Valid: true}
	if _, err = q.SaveIssueConveyor(ctx, saveParams(cv)); err != nil {
		return err
	}
	owed, err := s.owe(ctx, q, cv, conveyorActionNotifyCoordinator, cv.CurIdx, reason, map[string]any{"last_task_id": util.UUIDToString(cv.CurTaskID)})
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(map[string]any{"action_id": util.UUIDToString(owed.ID), "last_task_id": util.UUIDToString(cv.CurTaskID)})
	if _, err = s.record(ctx, q, cv, db.InsertIssueConveyorEventParams{
		Kind: conveyorEventDecision, Round: pgtype.Int4{Int32: cv.Round, Valid: true}, Idx: pgtype.Int4{Int32: cv.CurIdx, Valid: true},
		Action: pgtype.Text{String: conveyorDecisionEscalate, Valid: true}, Reason: pgtype.Text{String: reason, Valid: true}, Details: raw,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CoversWakeup reports whether a wakeup firing is already answered by the
// route: the rule is the agent's own, and every input is the ending of a run
// whose end the route has decided. The firing then starts no run.
func (s *IssueConveyorService) CoversWakeup(ctx context.Context, q *db.Queries, w db.IssueWakeup, receipts []db.IssueWakeupReceipt) (coveredBy string, covered bool, err error) {
	if w.SystemRule.Valid || !w.SourceTaskID.Valid || len(receipts) == 0 {
		return "", false, nil
	}
	source, err := q.GetAgentTask(ctx, w.SourceTaskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if source.AgentID != w.AgentID {
		return "", false, nil
	}
	var ids []pgtype.UUID
	seen := map[pgtype.UUID]bool{}
	for _, r := range receipts {
		if !strings.HasPrefix(r.EventType, "task.") {
			return "", false, nil
		}
		var p struct {
			TaskID string `json:"task_id"`
		}
		if json.Unmarshal(r.Payload, &p) != nil {
			return "", false, nil
		}
		id, perr := util.ParseUUID(p.TaskID)
		if perr != nil {
			return "", false, nil
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	decisions, err := q.ListIssueConveyorDecisionsForTasks(ctx, db.ListIssueConveyorDecisionsForTasksParams{IssueID: w.IssueID, TaskIds: ids})
	if err != nil {
		return "", false, err
	}
	byTask := map[pgtype.UUID]db.IssueConveyorEvent{}
	for _, d := range decisions {
		byTask[d.SourceTaskID] = d
	}
	if len(byTask) != len(ids) {
		return "", false, nil
	}
	first := byTask[ids[0]]
	by := "conveyor_decision:" + util.UUIDToString(first.ID)
	var d struct {
		ActionID string `json:"action_id"`
	}
	if json.Unmarshal(first.Details, &d) == nil && d.ActionID != "" {
		by = "conveyor_action:" + d.ActionID
	}
	return by, true, nil
}

// NoteSuperseded journals that the route answered a wakeup or a mention.
func (s *IssueConveyorService) NoteSuperseded(ctx context.Context, q *db.Queries, issueID pgtype.UUID, sourceTaskID pgtype.UUID, coveredBy, reason string, details map[string]any) {
	cv, err := q.GetIssueConveyor(ctx, issueID)
	if err != nil {
		return
	}
	raw, _ := json.Marshal(details)
	if _, err = s.record(ctx, q, cv, db.InsertIssueConveyorEventParams{
		Kind: conveyorEventSuperseded, SourceTaskID: sourceTaskID,
		CoveredBy: pgtype.Text{String: coveredBy, Valid: coveredBy != ""}, Reason: pgtype.Text{String: reason, Valid: true}, Details: raw,
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("conveyor: journal superseded failed", "issue_id", util.UUIDToString(issueID), "error", err)
	}
}

// ManagedRouteAgents are the agents a comment of this run must not wake: the
// run serves a step of an active route, and the route wakes the next agent
// itself. Nil for any other run.
func (s *IssueConveyorService) ManagedRouteAgents(ctx context.Context, issueID, authoringTaskID pgtype.UUID) (map[pgtype.UUID]string, error) {
	if !authoringTaskID.Valid {
		return nil, nil
	}
	task, err := s.q().GetAgentTask(ctx, authoringTaskID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!task.ConveyorActionID.Valid || task.IssueID != issueID)) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	action, err := s.q().GetIssueConveyorEvent(ctx, task.ConveyorActionID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && action.Action.String != conveyorActionRunStep) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cv, err := s.q().GetIssueConveyor(ctx, issueID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && cv.Status != ConveyorActive && cv.Status != ConveyorWaitingProvider) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	route, err := conveyorRoute(cv)
	if err != nil {
		return nil, err
	}
	agents := make(map[pgtype.UUID]string, len(route))
	for _, n := range route {
		if id, perr := util.ParseUUID(n.AgentID); perr == nil {
			agents[id] = "conveyor_action:" + util.UUIDToString(task.ConveyorActionID)
		}
	}
	return agents, nil
}

// RouteAgentIDs lists every agent of the route; the result report suppresses
// all of them.
func (s *IssueConveyorService) routeAgentIDs(cv db.IssueConveyor) ([]pgtype.UUID, error) {
	route, err := conveyorRoute(cv)
	if err != nil {
		return nil, err
	}
	ids := make([]pgtype.UUID, 0, len(route))
	for _, n := range route {
		if id, perr := util.ParseUUID(n.AgentID); perr == nil {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

type conveyorRerunPlan struct {
	managed  bool
	failed   db.AgentTaskQueue
	existing *db.AgentTaskQueue
}

// planRerun decides whether a rerun on this issue belongs to the route: it
// does when it names the run serving the current step or names nothing and
// that run is the one that ended badly. The route's assignment is rerun
// exactly once.
func (s *IssueConveyorService) planRerun(ctx context.Context, issueID, sourceTaskID pgtype.UUID) (conveyorRerunPlan, error) {
	cv, err := s.q().GetIssueConveyor(ctx, issueID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && cv.Status != ConveyorActive && cv.Status != ConveyorWaitingProvider) {
		return conveyorRerunPlan{}, nil
	}
	if err != nil {
		return conveyorRerunPlan{}, err
	}
	latest, err := s.q().GetLatestConveyorStepTask(ctx, db.GetLatestConveyorStepTaskParams{ConveyorID: cv.ID, Idx: pgtype.Int4{Int32: cv.CurIdx, Valid: true}, Round: pgtype.Int4{Int32: cv.Round, Valid: true}})
	if errors.Is(err, pgx.ErrNoRows) {
		return conveyorRerunPlan{}, nil
	}
	if err != nil {
		return conveyorRerunPlan{}, err
	}
	if sourceTaskID.Valid && sourceTaskID != latest.ID {
		source, serr := s.q().GetAgentTask(ctx, sourceTaskID)
		if serr != nil || source.ConveyorActionID != latest.ConveyorActionID {
			return conveyorRerunPlan{}, nil
		}
	}
	switch latest.Status {
	case "queued", "deferred", "dispatched", "running", "waiting_local_directory":
		return conveyorRerunPlan{managed: true, failed: latest, existing: &latest}, nil
	case "completed":
		return conveyorRerunPlan{}, nil
	}
	if successor, serr := s.q().GetLiveRerunSuccessor(ctx, latest.ID); serr == nil {
		return conveyorRerunPlan{managed: true, failed: latest, existing: &successor}, nil
	} else if !errors.Is(serr, pgx.ErrNoRows) {
		return conveyorRerunPlan{}, serr
	}
	return conveyorRerunPlan{managed: true, failed: latest}, nil
}

// RerunGuard keeps a rerun on a routed issue to one successor of the run that
// serves the current step, whoever asks and whether or not they name it. rerun
// is the ordinary rerun, called with the run to repeat.
func (s *IssueConveyorService) RerunGuard(ctx context.Context, issueID, sourceTaskID pgtype.UUID, rerun func(source pgtype.UUID) (*db.AgentTaskQueue, error)) (*db.AgentTaskQueue, error) {
	plan, err := s.planRerun(ctx, issueID, sourceTaskID)
	if err != nil {
		return nil, err
	}
	if !plan.managed {
		return rerun(sourceTaskID)
	}
	if plan.existing != nil {
		return s.bindRerun(ctx, issueID, plan.failed, *plan.existing)
	}
	task, err := rerun(plan.failed.ID)
	if err != nil {
		return nil, err
	}
	return s.bindRerun(ctx, issueID, plan.failed, *task)
}

// bindRerun makes task the route's run for the position its predecessor held.
// If another live run already holds it (a concurrent rerun got there first),
// the surplus run is withdrawn and the holder returned.
func (s *IssueConveyorService) bindRerun(ctx context.Context, issueID pgtype.UUID, predecessor, task db.AgentTaskQueue) (*db.AgentTaskQueue, error) {
	tx, err := s.Tasks.TxStarter.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	q := s.Tasks.Queries.WithTx(tx)
	cv, err := q.LockIssueConveyor(ctx, issueID)
	if err != nil {
		return nil, err
	}
	action, err := q.GetIssueConveyorEvent(ctx, predecessor.ConveyorActionID)
	if err != nil {
		return nil, err
	}
	if cv.Status != ConveyorActive && cv.Status != ConveyorWaitingProvider || !isCurrentStep(cv, action) {
		return &task, nil
	}
	if task.ConveyorActionID.Valid {
		return &task, nil
	}
	latest, err := q.GetLatestConveyorStepTask(ctx, db.GetLatestConveyorStepTaskParams{ConveyorID: cv.ID, Idx: action.Idx, Round: action.Round})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err == nil && latest.ID != task.ID {
		switch latest.Status {
		case "queued", "deferred", "dispatched", "running", "waiting_local_directory":
			if err = tx.Commit(ctx); err != nil {
				return nil, err
			}
			if _, cerr := s.Tasks.CancelTaskWithReason(ctx, task.ID, "Another rerun already serves this route step", "conveyor_duplicate_rerun"); cerr != nil {
				slog.Warn("conveyor: cancel surplus rerun failed", "task_id", util.UUIDToString(task.ID), "error", cerr)
			}
			return &latest, nil
		}
	}
	route, err := conveyorRoute(cv)
	if err != nil {
		return nil, err
	}
	issue, err := q.GetIssue(ctx, issueID)
	if err != nil {
		return nil, err
	}
	taskContext, _ := json.Marshal(map[string]any{"conveyor": conveyorTaskContext(cv, action)})
	if err = q.SetTaskConveyorAction(ctx, db.SetTaskConveyorActionParams{
		ID: task.ID, ActionID: action.ID, TaskContext: taskContext,
		HandoffNote: pgtype.Text{String: conveyorHandoffNote(issue, cv, action, route), Valid: true},
	}); err != nil {
		return nil, err
	}
	cv.CurTaskID, cv.Status, cv.WaitingReason = task.ID, ConveyorActive, pgtype.Text{}
	if _, err = q.SaveIssueConveyor(ctx, saveParams(cv)); err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(map[string]any{"predecessor_task_id": util.UUIDToString(predecessor.ID)})
	if _, err = s.record(ctx, q, cv, db.InsertIssueConveyorEventParams{
		Kind: conveyorEventState, Revision: action.Revision, Idx: action.Idx, Round: action.Round,
		Action: pgtype.Text{String: "rerun_bound", Valid: true}, TaskID: task.ID, Details: raw,
	}); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	bound, err := s.q().GetAgentTask(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	return &bound, nil
}

// deliverConveyorAfterTerminal creates the run a decision just owed, right
// after the deciding transaction committed. A failure leaves the action
// pending for the delivery job.
func (s *TaskService) deliverConveyorAfterTerminal(ctx context.Context, task db.AgentTaskQueue) {
	if task.ConveyorActionID.Valid && task.IssueID.Valid {
		s.conveyor().DeliverPending(ctx, task.IssueID)
	}
}

// conveyorAutoCloseReason journals why a completed route woke nobody.
const conveyorAutoCloseReason = "every step passed: the server closed the issue"

// autoClose ends a route that passed every step: the issue goes to done, a
// system comment sums up what passed and what was merged, and the owed
// coordinator run is withdrawn. It commits the delivery transaction.
func (s *IssueConveyorService) autoClose(ctx context.Context, tx pgx.Tx, q *db.Queries, issue db.Issue, cv db.IssueConveyor, action db.IssueConveyorEvent) error {
	if err := q.CancelIssueConveyorAction(ctx, db.CancelIssueConveyorActionParams{ID: action.ID, Reason: pgtype.Text{String: conveyorAutoCloseReason, Valid: true}}); err != nil {
		return err
	}
	prevStatus := issue.Status
	var err error
	if prevStatus != issuestatus.Done {
		if issue, err = q.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID, Status: issuestatus.Done}); err != nil {
			return err
		}
	}
	value, _ := json.Marshal(conveyorStepClose)
	if _, err = q.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID, Key: "step", Value: value}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	summary, err := s.autoCloseSummary(ctx, q, cv)
	if err != nil {
		return err
	}
	if _, err = q.CreateComment(ctx, db.CreateCommentParams{
		ID: dbid.NewV7(), IssueID: issue.ID, WorkspaceID: issue.WorkspaceID,
		AuthorType: "system", AuthorID: pgtype.UUID{Valid: true}, Content: summary, Type: "comment",
	}); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if s.Tasks.Bus != nil {
		s.Tasks.broadcastIssueUpdated(ctx, issue, prevStatus)
	}
	return nil
}

// autoCloseSummary is the closing comment: the steps that passed with their
// executors, the branch and commit, and what acceptance did with them.
func (s *IssueConveyorService) autoCloseSummary(ctx context.Context, q *db.Queries, cv db.IssueConveyor) (string, error) {
	route, err := conveyorRoute(cv)
	if err != nil {
		return "", err
	}
	agents, err := q.ListAllAgentsAnyKind(ctx, cv.WorkspaceID)
	if err != nil {
		return "", err
	}
	names := make(map[string]string, len(agents))
	for _, a := range agents {
		names[util.UUIDToString(a.ID)] = a.Name
	}
	steps := make([]string, 0, len(route))
	for _, n := range route {
		if n.Step == conveyorStepClose {
			continue
		}
		if n.Step == conveyorStepAccept {
			steps = append(steps, "accept")
			continue
		}
		steps = append(steps, fmt.Sprintf("%s (%s)", n.Step, names[n.AgentID]))
	}
	events, err := q.ListIssueConveyorEvents(ctx, db.ListIssueConveyorEventsParams{ConveyorID: cv.ID, AfterSeq: pgtype.Int8{Int64: 0, Valid: true}, PageSize: 500})
	if err != nil {
		return "", err
	}
	var branch, acceptReport string
	for _, e := range events {
		if e.Kind != conveyorEventResult || e.Action.String != ConveyorOutcomePass {
			continue
		}
		var d struct {
			Artifact struct {
				Branch string `json:"branch"`
			} `json:"artifact"`
			Report string `json:"report"`
		}
		_ = json.Unmarshal(e.Details, &d)
		if d.Artifact.Branch != "" && branch == "" {
			branch = d.Artifact.Branch
		}
		if node := conveyorNodeAt(route, e.Idx.Int32); node != nil && node.Step == conveyorStepAccept {
			acceptReport = strings.SplitN(strings.TrimSpace(d.Report), "\n", 2)[0]
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Маршрут пройден: %s. Задача закрыта сервером.", strings.Join(steps, " → "))
	if cv.AcceptedCommit.Valid {
		commit := cv.AcceptedCommit.String
		if len(commit) > 9 {
			commit = commit[:9]
		}
		fmt.Fprintf(&b, "\n\nКоммит `%s`", commit)
		if branch != "" {
			fmt.Fprintf(&b, ", ветка `%s`", branch)
		}
		b.WriteString(".")
	}
	if acceptReport != "" {
		fmt.Fprintf(&b, "\n\n%s", acceptReport)
	}
	return b.String(), nil
}

// conveyorMaxRechecks bounds how many times one revision of a route sends a
// verifier's own commit back to the first verifier.
const conveyorMaxRechecks = 2

// firstVerifierBefore is the first of the verifiers (qa, review) that judge
// the same work before idx: the run of them that ends at idx, after the last
// work step. -1 when the node at idx is the first one.
func firstVerifierBefore(route []ConveyorNode, idx int32) int32 {
	first := int32(-1)
	for i := idx - 1; i >= 0; i-- {
		n := conveyorNodeAt(route, i)
		if n == nil || (n.Step != conveyorStepQA && n.Step != conveyorStepReview) {
			break
		}
		first = i
	}
	return first
}

// revisionDecisions lists the decisions of this kind in the route's current
// revision, at idx (any node when idx < 0).
func (s *IssueConveyorService) revisionDecisions(ctx context.Context, q *db.Queries, cv db.IssueConveyor, action string, idx int32) ([]db.IssueConveyorEvent, error) {
	var out []db.IssueConveyorEvent
	after := int64(0)
	for {
		page, err := q.ListIssueConveyorEvents(ctx, db.ListIssueConveyorEventsParams{ConveyorID: cv.ID, AfterSeq: pgtype.Int8{Int64: after, Valid: true}, PageSize: 500})
		if err != nil {
			return nil, err
		}
		for _, e := range page {
			after = max(after, e.Seq.Int64)
			if e.Kind == conveyorEventDecision && e.Action.String == action && e.Revision.Int64 == cv.Revision && (idx < 0 || e.Idx.Int32 == idx) {
				out = append(out, e)
			}
		}
		if len(page) < 500 {
			return out, nil
		}
	}
}

// recheckAuthor is the node whose own commit the current round re-checks, if
// the round was opened by a recheck and still judges that commit.
func (s *IssueConveyorService) recheckAuthor(ctx context.Context, q *db.Queries, cv db.IssueConveyor) (int32, bool, error) {
	rechecks, err := s.revisionDecisions(ctx, q, cv, conveyorDecisionRecheck, -1)
	if err != nil || len(rechecks) == 0 {
		return 0, false, err
	}
	var d struct {
		AuthorIdx int32  `json:"author_idx"`
		Commit    string `json:"commit"`
		Round     int32  `json:"round"`
	}
	if json.Unmarshal(rechecks[len(rechecks)-1].Details, &d) != nil || d.Round != cv.Round || d.Commit != cv.AcceptedCommit.String {
		return 0, false, nil
	}
	return d.AuthorIdx, true, nil
}
