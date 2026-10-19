package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// IssueConveyorService owns the opt-in linear route of one issue. An executor
// hands in a structured result; in the transaction that ends its run the
// server decides the next move and records it, so the next run of the route
// exists without the coordinator being woken in between. Everything that
// concerns "does the route already cover this?" (wakeups, mentions, reruns,
// stale starts) asks this type, so the rule lives in one place.
type IssueConveyorService struct{ Tasks *TaskService }

// Route statuses. paused is held by an operator; awaiting_coordinator and
// waiting_provider are the two reasons the route stops by itself.
const (
	ConveyorActive              = "active"
	ConveyorPaused              = "paused"
	ConveyorAwaitingCoordinator = "awaiting_coordinator"
	ConveyorWaitingProvider     = "waiting_provider"
	ConveyorCompleted           = "completed"
	ConveyorDisabled            = "disabled"
)

// Operator commands of PUT /conveyor.
const (
	ConveyorCmdEnable  = "enable"
	ConveyorCmdPause   = "pause"
	ConveyorCmdResume  = "resume"
	ConveyorCmdDisable = "disable"
)

// Journal kinds (issue_conveyor_event.kind).
const (
	conveyorEventInput         = "input"
	conveyorEventResult        = "result"
	conveyorEventDecision      = "decision"
	conveyorEventAction        = "action"
	conveyorEventState         = "state"
	conveyorEventSuperseded    = "superseded"
	conveyorEventRejected      = "rejected"
	conveyorEventRejectedStale = "rejected_stale"
)

// What an action asks for, and what a decision decided.
const (
	conveyorActionRunStep           = "run_step"
	conveyorActionNotifyCoordinator = "notify_coordinator"

	conveyorDecisionAdvance      = "advance"
	conveyorDecisionComplete     = "complete"
	conveyorDecisionEscalate     = "escalate"
	conveyorDecisionWaitProvider = "wait_provider"
	conveyorDecisionStale        = "stale"
	conveyorDecisionHeld         = "held"
)

const (
	conveyorStepClose  = "close"
	conveyorStepDev    = "dev"
	conveyorStepQA     = "qa"
	conveyorStepReview = "review"
	// conveyorStepAccept checks the commit the route accepted on top of the
	// base branch and merges it; a daemon runs it without a model.
	conveyorStepAccept = "accept"
)

// Errors map to HTTP in the handler: invalid 400, forbidden 403, not found
// 404, conflict 409. Stale means the caller's run is no longer the route's
// current assignment.
var (
	ErrConveyorInvalid   = errors.New("conveyor: invalid request")
	ErrConveyorForbidden = errors.New("conveyor: forbidden")
	ErrConveyorNotFound  = errors.New("conveyor: not found")
	ErrConveyorConflict  = errors.New("conveyor: conflict")
	ErrConveyorStale     = errors.New("conveyor: run is not the current assignment")
)

func conveyorErr(kind error, format string, args ...any) error {
	return fmt.Errorf("%w: %s", kind, fmt.Sprintf(format, args...))
}

// ConveyorNode is one position of the route snapshot taken when it was enabled.
type ConveyorNode struct {
	Idx     int32  `json:"idx"`
	Step    string `json:"step"`
	AgentID string `json:"agent_id"`
}

// ConveyorRouteEntry is a route position as an operator writes it; Agent is a
// workspace agent name or id.
type ConveyorRouteEntry struct {
	Step  string `json:"step"`
	Agent string `json:"agent"`
}

// ConveyorCommand is the body of PUT /api/issues/{id}/conveyor. Route is
// either the metadata form ("dev:Name,qa:Name,close:Name") or a list of
// entries; without it the issue's metadata.route is the source.
type ConveyorCommand struct {
	Action           string          `json:"action"`
	Route            json.RawMessage `json:"route,omitempty"`
	FromStep         string          `json:"from_step,omitempty"`
	ExpectedRevision int64           `json:"expected_revision"`
}

// ConveyorView is what GET /conveyor shows: the state, the current assignment
// and the run on it, and why the route waits.
type ConveyorView struct {
	ID                 string         `json:"id"`
	IssueID            string         `json:"issue_id"`
	Status             string         `json:"status"`
	Revision           int64          `json:"revision"`
	Round              int32          `json:"round"`
	CurrentIdx         int32          `json:"current_idx"`
	Step               string         `json:"step"`
	Route              []ConveyorNode `json:"route"`
	CoordinatorAgentID string         `json:"coordinator_agent_id"`
	CurrentTaskID      string         `json:"current_task_id,omitempty"`
	CurrentTaskStatus  string         `json:"current_task_status,omitempty"`
	AcceptedCommit     string         `json:"accepted_commit,omitempty"`
	WaitingReason      string         `json:"waiting_reason,omitempty"`
	PendingActions     int            `json:"pending_actions"`
	UpdatedAt          string         `json:"updated_at"`
}

func (s *IssueConveyorService) q() *db.Queries { return s.Tasks.Queries }

func conveyorRoute(cv db.IssueConveyor) ([]ConveyorNode, error) {
	var route []ConveyorNode
	if err := json.Unmarshal(cv.Route, &route); err != nil || len(route) < 2 {
		return nil, fmt.Errorf("conveyor %s: unreadable route snapshot", util.UUIDToString(cv.ID))
	}
	return route, nil
}

// conveyorNodeAt is the route node at idx, nil when the snapshot has none.
func conveyorNodeAt(route []ConveyorNode, idx int32) *ConveyorNode {
	if idx < 0 || int(idx) >= len(route) {
		return nil
	}
	return &route[idx]
}

// ParseConveyorRouteSpec reads the metadata form "step:Agent,step:Agent".
func ParseConveyorRouteSpec(spec string) ([]ConveyorRouteEntry, error) {
	var out []ConveyorRouteEntry
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		step, agent, ok := strings.Cut(part, ":")
		step, agent = strings.ToLower(strings.TrimSpace(step)), strings.TrimSpace(agent)
		if !ok || step == "" || agent == "" {
			return nil, conveyorErr(ErrConveyorInvalid, "route position %q must look like step:Agent", part)
		}
		out = append(out, ConveyorRouteEntry{Step: step, Agent: agent})
	}
	return out, nil
}

func (cmd ConveyorCommand) routeEntries(issue db.Issue) ([]ConveyorRouteEntry, error) {
	raw := strings.TrimSpace(string(cmd.Route))
	switch {
	case raw == "" || raw == "null":
		var meta map[string]any
		_ = json.Unmarshal(issue.Metadata, &meta)
		spec, _ := meta["route"].(string)
		if strings.TrimSpace(spec) == "" {
			return nil, conveyorErr(ErrConveyorInvalid, "route is required: pass it or set metadata.route")
		}
		return ParseConveyorRouteSpec(spec)
	case strings.HasPrefix(raw, `"`):
		var spec string
		if err := json.Unmarshal(cmd.Route, &spec); err != nil {
			return nil, conveyorErr(ErrConveyorInvalid, "route: %v", err)
		}
		return ParseConveyorRouteSpec(spec)
	default:
		var entries []ConveyorRouteEntry
		if err := json.Unmarshal(cmd.Route, &entries); err != nil {
			return nil, conveyorErr(ErrConveyorInvalid, "route: %v", err)
		}
		for i := range entries {
			entries[i].Step = strings.ToLower(strings.TrimSpace(entries[i].Step))
			entries[i].Agent = strings.TrimSpace(entries[i].Agent)
		}
		return entries, nil
	}
}

// resolveRoute turns the entries into a validated snapshot: agents exist, run
// somewhere and may be invoked by the person enabling the route, the last
// node is the coordinator, work is followed by QA, and a risky issue is
// reviewed.
func (s *IssueConveyorService) resolveRoute(ctx context.Context, q *db.Queries, issue db.Issue, entries []ConveyorRouteEntry, fromStep string, invoker pgtype.UUID) ([]ConveyorNode, error) {
	if len(entries) < 2 {
		return nil, conveyorErr(ErrConveyorInvalid, "route needs at least one step and a close:<coordinator> node")
	}
	if entries[len(entries)-1].Step != conveyorStepClose {
		return nil, conveyorErr(ErrConveyorInvalid, "the last route node must be close:<coordinator>")
	}
	if fromStep = strings.ToLower(strings.TrimSpace(fromStep)); fromStep != "" {
		start := -1
		for i, e := range entries[:len(entries)-1] {
			if e.Step == fromStep {
				start = i
				break
			}
		}
		if start < 0 {
			return nil, conveyorErr(ErrConveyorInvalid, "from_step %q is not a step of the route", fromStep)
		}
		entries = entries[start:]
	}
	agents, err := q.ListAgents(ctx, issue.WorkspaceID)
	if err != nil {
		return nil, err
	}
	route := make([]ConveyorNode, 0, len(entries))
	lastDev, lastQA := -1, -1
	hasReview := false
	for i, e := range entries {
		if e.Step == conveyorStepClose && i != len(entries)-1 {
			return nil, conveyorErr(ErrConveyorInvalid, "close is only allowed as the last node")
		}
		agent, err := pickConveyorAgent(agents, e.Agent)
		if err != nil {
			return nil, err
		}
		if agent.ArchivedAt.Valid || !agent.RuntimeID.Valid {
			return nil, conveyorErr(ErrConveyorInvalid, "agent %q is archived or has no runtime", agent.Name)
		}
		if !CanMemberInvokeAgent(ctx, q, agent, invoker, issue.WorkspaceID) {
			return nil, conveyorErr(ErrConveyorForbidden, "you may not invoke agent %q", agent.Name)
		}
		route = append(route, ConveyorNode{Idx: int32(i), Step: e.Step, AgentID: util.UUIDToString(agent.ID)})
		switch e.Step {
		case conveyorStepDev:
			lastDev = i
		case conveyorStepQA:
			lastQA = i
		case conveyorStepReview:
			hasReview = true
		}
	}
	if lastDev >= 0 && lastQA < lastDev {
		return nil, conveyorErr(ErrConveyorInvalid, "a qa step must follow dev")
	}
	if err := acceptPlacementError(entries); err != nil {
		return nil, err
	}
	var meta map[string]any
	_ = json.Unmarshal(issue.Metadata, &meta)
	if risky, _ := meta["risky"].(bool); risky && !hasReview {
		return nil, conveyorErr(ErrConveyorInvalid, "metadata.risky is true: the route needs a review step")
	}
	return route, nil
}

func pickConveyorAgent(agents []db.Agent, ref string) (db.Agent, error) {
	var found []db.Agent
	for _, a := range agents {
		if util.UUIDToString(a.ID) == ref || (strings.EqualFold(a.Name, ref) && !a.ArchivedAt.Valid) {
			found = append(found, a)
		}
	}
	switch len(found) {
	case 0:
		return db.Agent{}, conveyorErr(ErrConveyorInvalid, "agent %q not found", ref)
	case 1:
		return found[0], nil
	}
	return db.Agent{}, conveyorErr(ErrConveyorInvalid, "agent name %q is ambiguous; use the agent id", ref)
}

// Apply runs one operator command. The coordinator's person (invoker) is the
// human the new runs answer to; pause and disable do not need one.
func (s *IssueConveyorService) Apply(ctx context.Context, issue db.Issue, invoker pgtype.UUID, cmd ConveyorCommand) (ConveyorView, error) {
	switch cmd.Action {
	case ConveyorCmdEnable, ConveyorCmdResume:
		if !invoker.Valid {
			return ConveyorView{}, conveyorErr(ErrConveyorForbidden, "a human originator is required")
		}
	case ConveyorCmdPause, ConveyorCmdDisable:
	default:
		return ConveyorView{}, conveyorErr(ErrConveyorInvalid, "action must be enable, pause, resume or disable")
	}
	tx, err := s.Tasks.TxStarter.Begin(ctx)
	if err != nil {
		return ConveyorView{}, err
	}
	defer tx.Rollback(ctx)
	q := s.Tasks.Queries.WithTx(tx)

	// Enable may add route statuses. Take the catalog before the route, as
	// delivery does, to serialize creation and respect concurrent archiving.
	if cmd.Action == ConveyorCmdEnable {
		if err = q.LockIssueStatusCatalog(ctx, issue.WorkspaceID); err != nil {
			return ConveyorView{}, err
		}
	}
	cv, err := q.LockIssueConveyor(ctx, issue.ID)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ConveyorView{}, err
	}
	// A new revision orphans the actions of the previous one: those not yet
	// delivered are withdrawn before the command owes its own. Runs already
	// created for them are refused at claim.
	if exists {
		if _, err = q.CancelPendingIssueConveyorActions(ctx, db.CancelPendingIssueConveyorActionsParams{ConveyorID: cv.ID, Reason: pgtype.Text{String: "superseded by " + cmd.Action, Valid: true}}); err != nil {
			return ConveyorView{}, err
		}
	}
	switch cmd.Action {
	case ConveyorCmdEnable:
		cv, err = s.enable(ctx, q, issue, invoker, cv, exists, cmd)
	default:
		if !exists {
			return ConveyorView{}, conveyorErr(ErrConveyorNotFound, "no route on this issue")
		}
		if cmd.ExpectedRevision != cv.Revision {
			return ConveyorView{}, conveyorErr(ErrConveyorConflict, "revision is %d, not %d", cv.Revision, cmd.ExpectedRevision)
		}
		switch cmd.Action {
		case ConveyorCmdPause:
			cv, err = s.pause(ctx, q, cv)
		case ConveyorCmdResume:
			cv, err = s.resume(ctx, q, issue, invoker, cv, cmd)
		default:
			cv, err = s.disable(ctx, q, cv)
		}
	}
	if err != nil {
		return ConveyorView{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ConveyorView{}, err
	}
	if cmd.Action == ConveyorCmdEnable && s.Tasks.Bus != nil {
		s.Tasks.Bus.Publish(events.Event{Type: protocol.EventIssueStatusChanged,
			WorkspaceID: util.UUIDToString(issue.WorkspaceID), ActorType: "member", ActorID: util.UUIDToString(invoker),
			Payload: map[string]any{"action": "conveyor_enable"}})
	}
	s.withdrawStaleTasks(ctx, cv)
	// Delivery failures are the job's to retry; the command itself has landed.
	s.DeliverPending(ctx, issue.ID)
	return s.View(ctx, issue.ID)
}

func (s *IssueConveyorService) record(ctx context.Context, q *db.Queries, cv db.IssueConveyor, ev db.InsertIssueConveyorEventParams) (db.IssueConveyorEvent, error) {
	ev.ID = dbid.NewV7()
	ev.ConveyorID, ev.WorkspaceID, ev.IssueID = cv.ID, cv.WorkspaceID, cv.IssueID
	return q.InsertIssueConveyorEvent(ctx, ev)
}

// recordState journals an operator command.
func (s *IssueConveyorService) recordState(ctx context.Context, q *db.Queries, cv db.IssueConveyor, action string, details map[string]any) error {
	raw, _ := json.Marshal(details)
	_, err := s.record(ctx, q, cv, db.InsertIssueConveyorEventParams{
		Kind:     conveyorEventState,
		Revision: pgtype.Int8{Int64: cv.Revision, Valid: true},
		Action:   pgtype.Text{String: action, Valid: true},
		Details:  raw,
	})
	return err
}

func (s *IssueConveyorService) enable(ctx context.Context, q *db.Queries, issue db.Issue, invoker pgtype.UUID, cv db.IssueConveyor, exists bool, cmd ConveyorCommand) (db.IssueConveyor, error) {
	if exists && cv.Status != ConveyorDisabled && cv.Status != ConveyorCompleted {
		return cv, conveyorErr(ErrConveyorConflict, "the issue already has a route (%s)", cv.Status)
	}
	if exists && cmd.ExpectedRevision != cv.Revision {
		return cv, conveyorErr(ErrConveyorConflict, "revision is %d, not %d", cv.Revision, cmd.ExpectedRevision)
	}
	if !exists && cmd.ExpectedRevision != 0 {
		return cv, conveyorErr(ErrConveyorConflict, "the issue has no route yet: expected_revision must be 0")
	}
	if active, err := wakeupIssueActive(ctx, q, issue); err != nil {
		return cv, err
	} else if !active {
		return cv, conveyorErr(ErrConveyorConflict, "the issue is closed")
	}
	entries, err := cmd.routeEntries(issue)
	if err != nil {
		return cv, err
	}
	route, err := s.resolveRoute(ctx, q, issue, entries, cmd.FromStep, invoker)
	if err != nil {
		return cv, err
	}
	if err = s.requireNoLiveRun(ctx, q, issue.ID, route[0].AgentID); err != nil {
		return cv, err
	}
	if err = ensureConveyorIssueStatuses(ctx, q, issue.WorkspaceID, invoker, route); err != nil {
		return cv, err
	}
	raw, _ := json.Marshal(route)
	coordinator, _ := util.ParseUUID(route[len(route)-1].AgentID)
	if !exists {
		cv, err = q.InsertIssueConveyor(ctx, db.InsertIssueConveyorParams{
			ID: dbid.NewV7(), WorkspaceID: issue.WorkspaceID, IssueID: issue.ID, Route: raw,
			CurIdx: 0, CoordinatorAgentID: coordinator, EnabledBy: invoker,
		})
	} else {
		cv.Route, cv.CurIdx, cv.Round, cv.Revision = raw, 0, cv.Round+1, cv.Revision+1
		cv.CurTaskID, cv.Status, cv.CoordinatorAgentID, cv.EnabledBy = pgtype.UUID{}, ConveyorActive, coordinator, invoker
		cv.AcceptedCommit, cv.WaitingReason = pgtype.Text{}, pgtype.Text{}
		cv, err = q.SaveIssueConveyor(ctx, saveParams(cv))
	}
	if err != nil {
		return cv, err
	}
	if err = s.recordState(ctx, q, cv, ConveyorCmdEnable, map[string]any{"route": route, "enabled_by": util.UUIDToString(invoker)}); err != nil {
		return cv, err
	}
	_, err = s.owe(ctx, q, cv, conveyorActionRunStep, 0, "enabled", nil)
	return cv, err
}

// Route steps use ordinary custom statuses. Existing catalog entries keep
// their names and colors; archived or terminal keys cannot be repurposed.
func ensureConveyorIssueStatuses(ctx context.Context, q *db.Queries, workspaceID, invoker pgtype.UUID, route []ConveyorNode) error {
	for _, node := range route {
		if node.Step != conveyorStepQA && node.Step != conveyorStepReview {
			continue
		}
		_, err := q.GetIssueStatusEntryByKey(ctx, db.GetIssueStatusEntryByKeyParams{WorkspaceID: workspaceID, Key: node.Step})
		if errors.Is(err, pgx.ErrNoRows) {
			member, merr := q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{WorkspaceID: workspaceID, UserID: invoker})
			if merr != nil && !errors.Is(merr, pgx.ErrNoRows) {
				return merr
			}
			if merr != nil || (member.Role != "owner" && member.Role != "admin") {
				return conveyorErr(ErrConveyorForbidden, "an owner or admin must create the missing route status %q", node.Step)
			}
			name := "QA"
			if node.Step == conveyorStepReview {
				name = "Review"
			}
			_, err = q.CreateIssueStatusEntry(ctx, db.CreateIssueStatusEntryParams{
				WorkspaceID: workspaceID, Key: node.Step, Name: name,
				Category: issuestatus.CategoryStarted, Color: "#f59e0b",
			})
			var conflict *pgconn.PgError
			if errors.As(err, &conflict) && conflict.Code == "23505" {
				return conveyorErr(ErrConveyorConflict, "cannot create route status %q: name %q is already in use; configure the required key in the status catalog", node.Step, name)
			}
		}
		if err != nil {
			return err
		}
		if err = validateConveyorIssueStatus(ctx, q, workspaceID, node.Step); err != nil {
			return conveyorErr(ErrConveyorConflict, "%s", err)
		}
	}
	return nil
}

func saveParams(cv db.IssueConveyor) db.SaveIssueConveyorParams {
	return db.SaveIssueConveyorParams{
		ID: cv.ID, Route: cv.Route, Revision: cv.Revision, CurIdx: cv.CurIdx, Round: cv.Round,
		CurTaskID: cv.CurTaskID, Status: cv.Status, CoordinatorAgentID: cv.CoordinatorAgentID,
		EnabledBy: cv.EnabledBy, AcceptedCommit: cv.AcceptedCommit, WaitingReason: cv.WaitingReason,
	}
}

func (s *IssueConveyorService) requireNoLiveRun(ctx context.Context, q *db.Queries, issueID pgtype.UUID, agentID string) error {
	id, err := util.ParseUUID(agentID)
	if err != nil {
		return err
	}
	live, err := q.CountLiveIssueRunsOfAgent(ctx, db.CountLiveIssueRunsOfAgentParams{IssueID: issueID, AgentID: id})
	if err != nil {
		return err
	}
	if live > 0 {
		return conveyorErr(ErrConveyorConflict, "agent %s has an unfinished run on this issue", agentID)
	}
	return nil
}

func (s *IssueConveyorService) pause(ctx context.Context, q *db.Queries, cv db.IssueConveyor) (db.IssueConveyor, error) {
	if cv.Status != ConveyorActive && cv.Status != ConveyorWaitingProvider {
		return cv, conveyorErr(ErrConveyorConflict, "a route in status %s cannot be paused", cv.Status)
	}
	cv.Status, cv.Revision = ConveyorPaused, cv.Revision+1
	cv.WaitingReason = pgtype.Text{String: "paused by an operator", Valid: true}
	cv, err := q.SaveIssueConveyor(ctx, saveParams(cv))
	if err != nil {
		return cv, err
	}
	return cv, s.recordState(ctx, q, cv, ConveyorCmdPause, map[string]any{})
}

func (s *IssueConveyorService) disable(ctx context.Context, q *db.Queries, cv db.IssueConveyor) (db.IssueConveyor, error) {
	if cv.Status == ConveyorDisabled {
		return cv, conveyorErr(ErrConveyorConflict, "the route is already disabled")
	}
	cv.Status, cv.Revision = ConveyorDisabled, cv.Revision+1
	cv.WaitingReason = pgtype.Text{}
	cv, err := q.SaveIssueConveyor(ctx, saveParams(cv))
	if err != nil {
		return cv, err
	}
	return cv, s.recordState(ctx, q, cv, ConveyorCmdDisable, map[string]any{})
}

// resume opens a new round at from_step (default: the step the route stopped
// on). A step before QA starts a fresh candidate, so the accepted commit is
// dropped; qa and review keep judging the same one.
func (s *IssueConveyorService) resume(ctx context.Context, q *db.Queries, issue db.Issue, invoker pgtype.UUID, cv db.IssueConveyor, cmd ConveyorCommand) (db.IssueConveyor, error) {
	switch cv.Status {
	case ConveyorPaused, ConveyorAwaitingCoordinator, ConveyorWaitingProvider:
	default:
		return cv, conveyorErr(ErrConveyorConflict, "a route in status %s cannot be resumed", cv.Status)
	}
	route, err := conveyorRoute(cv)
	if err != nil {
		return cv, err
	}
	idx := cv.CurIdx
	if step := strings.ToLower(strings.TrimSpace(cmd.FromStep)); step != "" {
		idx = -1
		for _, n := range route[:len(route)-1] {
			if n.Step == step {
				idx = n.Idx
				break
			}
		}
		if idx < 0 {
			return cv, conveyorErr(ErrConveyorInvalid, "from_step %q is not a step of the route", step)
		}
	}
	node := conveyorNodeAt(route[:len(route)-1], idx)
	if node == nil {
		return cv, conveyorErr(ErrConveyorInvalid, "the route has no step to resume at")
	}
	if active, err := wakeupIssueActive(ctx, q, issue); err != nil {
		return cv, err
	} else if !active {
		return cv, conveyorErr(ErrConveyorConflict, "the issue is closed")
	}
	// The runs of the round being left must not overlap the new one. Withdrawal
	// of those not yet started happens after commit; one executing blocks.
	if err = s.requireNoLiveRun(ctx, q, issue.ID, node.AgentID); err != nil {
		return cv, err
	}
	// The invoker may differ from the person who enabled the route: the new
	// round runs on their authority.
	agent, err := s.loadRouteAgent(ctx, q, node.AgentID)
	if err != nil {
		return cv, err
	}
	if !CanMemberInvokeAgent(ctx, q, agent, invoker, issue.WorkspaceID) {
		return cv, conveyorErr(ErrConveyorForbidden, "you may not invoke agent %q", agent.Name)
	}
	cv.CurIdx, cv.Round, cv.Revision = idx, cv.Round+1, cv.Revision+1
	cv.Status, cv.CurTaskID, cv.WaitingReason, cv.EnabledBy = ConveyorActive, pgtype.UUID{}, pgtype.Text{}, invoker
	if node.Step != conveyorStepQA && node.Step != conveyorStepReview {
		cv.AcceptedCommit = pgtype.Text{}
	}
	cv, err = q.SaveIssueConveyor(ctx, saveParams(cv))
	if err != nil {
		return cv, err
	}
	if err = s.recordState(ctx, q, cv, ConveyorCmdResume, map[string]any{"step": node.Step, "idx": node.Idx}); err != nil {
		return cv, err
	}
	_, err = s.owe(ctx, q, cv, conveyorActionRunStep, idx, "resumed", nil)
	return cv, err
}

func (s *IssueConveyorService) loadRouteAgent(ctx context.Context, q *db.Queries, agentID string) (db.Agent, error) {
	id, err := util.ParseUUID(agentID)
	if err != nil {
		return db.Agent{}, err
	}
	return q.GetAgent(ctx, id)
}

// owe journals a run the route owes at idx; delivery creates the run. A
// second writer for the same position finds the unique key taken and owes
// nothing more: the zero event is returned.
func (s *IssueConveyorService) owe(ctx context.Context, q *db.Queries, cv db.IssueConveyor, action string, idx int32, reason string, extra map[string]any) (db.IssueConveyorEvent, error) {
	route, err := conveyorRoute(cv)
	if err != nil {
		return db.IssueConveyorEvent{}, err
	}
	node := conveyorNodeAt(route, idx)
	if node == nil {
		return db.IssueConveyorEvent{}, fmt.Errorf("conveyor %s: no node at %d", util.UUIDToString(cv.ID), idx)
	}
	agentID := node.AgentID
	if action == conveyorActionNotifyCoordinator {
		agentID = util.UUIDToString(cv.CoordinatorAgentID)
	}
	d := map[string]any{"step": node.Step, "agent_id": agentID, "why": reason}
	for k, v := range extra {
		d[k] = v
	}
	details, _ := json.Marshal(d)
	ev, err := s.record(ctx, q, cv, db.InsertIssueConveyorEventParams{
		Kind:     conveyorEventAction,
		Revision: pgtype.Int8{Int64: cv.Revision, Valid: true},
		Idx:      pgtype.Int4{Int32: idx, Valid: true},
		Round:    pgtype.Int4{Int32: cv.Round, Valid: true},
		Action:   pgtype.Text{String: action, Valid: true},
		State:    pgtype.Text{String: "pending", Valid: true},
		Reason:   pgtype.Text{String: reason, Valid: true},
		Details:  details,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.IssueConveyorEvent{}, nil
	}
	return ev, err
}

// View reads the state of the issue's route.
func (s *IssueConveyorService) View(ctx context.Context, issueID pgtype.UUID) (ConveyorView, error) {
	cv, err := s.q().GetIssueConveyor(ctx, issueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ConveyorView{}, conveyorErr(ErrConveyorNotFound, "no route on this issue")
	}
	if err != nil {
		return ConveyorView{}, err
	}
	route, err := conveyorRoute(cv)
	if err != nil {
		return ConveyorView{}, err
	}
	view := ConveyorView{
		ID: util.UUIDToString(cv.ID), IssueID: util.UUIDToString(cv.IssueID), Status: cv.Status,
		Revision: cv.Revision, Round: cv.Round, CurrentIdx: cv.CurIdx, Route: route,
		CoordinatorAgentID: util.UUIDToString(cv.CoordinatorAgentID), AcceptedCommit: cv.AcceptedCommit.String,
		WaitingReason: cv.WaitingReason.String, UpdatedAt: cv.UpdatedAt.Time.UTC().Format("2006-01-02T15:04:05Z"),
	}
	if node := conveyorNodeAt(route, cv.CurIdx); node != nil {
		view.Step = node.Step
	}
	// While a step runs, its newest run is the current one (a retry or a rerun
	// replaces the run the route first created); otherwise the last run the
	// route created, a coordinator's included.
	current := cv.CurTaskID
	if cv.Status == ConveyorActive || cv.Status == ConveyorWaitingProvider {
		if latest, lerr := s.q().GetLatestConveyorStepTask(ctx, db.GetLatestConveyorStepTaskParams{
			ConveyorID: cv.ID, Idx: pgtype.Int4{Int32: cv.CurIdx, Valid: true}, Round: pgtype.Int4{Int32: cv.Round, Valid: true},
		}); lerr == nil {
			current = latest.ID
		}
	}
	if current.Valid {
		view.CurrentTaskID = util.UUIDToString(current)
		if task, terr := s.q().GetAgentTask(ctx, current); terr == nil {
			view.CurrentTaskStatus = task.Status
		}
	}
	pending, err := s.q().ListPendingIssueConveyorActions(ctx, db.ListPendingIssueConveyorActionsParams{IssueID: issueID, PageSize: 50})
	if err != nil {
		return ConveyorView{}, err
	}
	view.PendingActions = len(pending)
	return view, nil
}

// Events lists the journal after a cursor (the seq of the last event seen).
func (s *IssueConveyorService) Events(ctx context.Context, issueID pgtype.UUID, after int64, limit int32) ([]db.IssueConveyorEvent, error) {
	cv, err := s.q().GetIssueConveyor(ctx, issueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, conveyorErr(ErrConveyorNotFound, "no route on this issue")
	}
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	return s.q().ListIssueConveyorEvents(ctx, db.ListIssueConveyorEventsParams{ConveyorID: cv.ID, AfterSeq: pgtype.Int8{Int64: after, Valid: true}, PageSize: limit})
}

// acceptPlacementError enforces where the accept step may stand: at most once,
// right before close, after a review of the same route. It merges what the
// route accepted, so nothing may change the commit after it and a review must
// have judged that commit first.
func acceptPlacementError(entries []ConveyorRouteEntry) error {
	review := false
	for i, e := range entries {
		switch e.Step {
		case conveyorStepReview:
			review = true
		case conveyorStepAccept:
			if i != len(entries)-2 {
				return conveyorErr(ErrConveyorInvalid, "accept is only allowed once, right before close")
			}
			if !review {
				return conveyorErr(ErrConveyorInvalid, "accept needs a review step before it")
			}
		}
	}
	return nil
}
