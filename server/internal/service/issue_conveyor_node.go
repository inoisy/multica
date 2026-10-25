package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// A route node may carry options after its agent, so one route serves cases
// that differ in more than who does what:
//
//	build:Монтажёр[profile=Сильная],critic:Критик[cross=build;back=build;rounds=2],close:Продюсер
//
// profile — the model profile of this step's runs (wins over the issue's);
// cross   — run on another model family than the run that passed that step;
// back    — changes_requested opens a new round at that step by itself; a
//
//	verifier (qa, review, accept) without it goes back to the closest
//	earlier work step, back=none sends its changes to the coordinator;
//
// rounds  — how many rounds the route may reach through back (default 2);
// retries — how many times a run cut short by infrastructure (the daemon
//
//	stopped, the runtime went offline, the provider failed) starts
//	again by itself before the coordinator is woken (default 2, 0..5).
const (
	conveyorOptProfile = "profile"
	conveyorOptCross   = "cross"
	conveyorOptBack    = "back"
	conveyorOptRounds  = "rounds"
	conveyorOptRetries = "retries"

	conveyorBackNone = "none"

	conveyorDefaultBackRounds = 2
	conveyorMaxBackRounds     = 5
	conveyorDefaultRetries    = 2
	conveyorMaxRetries        = 5
)

// splitNodeOptions parses "Agent[k=v;k=v]" into the agent and its options.
func splitNodeOptions(agent string) (string, ConveyorRouteEntry, error) {
	var e ConveyorRouteEntry
	open := strings.Index(agent, "[")
	if open < 0 {
		return strings.TrimSpace(agent), e, nil
	}
	if !strings.HasSuffix(agent, "]") {
		return "", e, conveyorErr(ErrConveyorInvalid, "route node %q: options must end with ]", agent)
	}
	name := strings.TrimSpace(agent[:open])
	for _, kv := range strings.Split(agent[open+1:len(agent)-1], ";") {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		k, v, ok := strings.Cut(kv, "=")
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		if !ok || v == "" {
			return "", e, conveyorErr(ErrConveyorInvalid, "route node %q: option %q must look like key=value", name, kv)
		}
		switch k {
		case conveyorOptProfile:
			e.Profile = v
		case conveyorOptCross:
			e.Cross = strings.ToLower(v)
		case conveyorOptBack:
			e.Back = strings.ToLower(v)
		case conveyorOptRounds:
			n, err := strconv.Atoi(v)
			if err != nil {
				return "", e, conveyorErr(ErrConveyorInvalid, "route node %q: rounds must be a number", name)
			}
			e.Rounds = int32(n)
		case conveyorOptRetries:
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 || n > conveyorMaxRetries {
				return "", e, conveyorErr(ErrConveyorInvalid, "route node %q: retries must be 0..%d", name, conveyorMaxRetries)
			}
			// 0 is stored as -1: the zero value means "default".
			e.Retries = int32(n)
			if n == 0 {
				e.Retries = -1
			}
		default:
			return "", e, conveyorErr(ErrConveyorInvalid, "route node %q: unknown option %q (profile, cross, back, rounds, retries)", name, k)
		}
	}
	return name, e, nil
}

// spec writes the entry back in the metadata form.
func (e ConveyorRouteEntry) spec() string {
	var opts []string
	if e.Profile != "" {
		opts = append(opts, conveyorOptProfile+"="+e.Profile)
	}
	if e.Cross != "" {
		opts = append(opts, conveyorOptCross+"="+e.Cross)
	}
	if e.Back != "" {
		opts = append(opts, conveyorOptBack+"="+e.Back)
	}
	if e.Rounds != 0 {
		opts = append(opts, conveyorOptRounds+"="+strconv.Itoa(int(e.Rounds)))
	}
	if e.Retries != 0 {
		opts = append(opts, conveyorOptRetries+"="+strconv.Itoa(max(int(e.Retries), 0)))
	}
	if len(opts) == 0 {
		return e.Step + ":" + e.Agent
	}
	return e.Step + ":" + e.Agent + "[" + strings.Join(opts, ";") + "]"
}

// validateNodeRefs checks what options say about the route's shape: cross and
// back name a step that comes earlier, rounds goes with back.
func validateNodeRefs(entries []ConveyorRouteEntry) error {
	for i, e := range entries {
		earlier := func(step string) bool {
			for _, p := range entries[:i] {
				if p.Step == step {
					return true
				}
			}
			return false
		}
		if e.Step == conveyorStepClose && (e.Profile != "" || e.Cross != "" || e.Back != "" || e.Rounds != 0 || e.Retries != 0) {
			return conveyorErr(ErrConveyorInvalid, "close takes no options")
		}
		if e.Cross != "" && !earlier(e.Cross) {
			return conveyorErr(ErrConveyorInvalid, "step %s: cross=%s must name an earlier step", e.Step, e.Cross)
		}
		if e.Back != "" && e.Back != conveyorBackNone && !earlier(e.Back) {
			return conveyorErr(ErrConveyorInvalid, "step %s: back=%s must name an earlier step", e.Step, e.Back)
		}
		if e.Rounds != 0 && (e.Back == conveyorBackNone || (e.Back == "" && !isConveyorVerifier(e.Step))) {
			return conveyorErr(ErrConveyorInvalid, "step %s: rounds needs back", e.Step)
		}
		if e.Rounds < 0 || e.Rounds > conveyorMaxBackRounds || (e.Back != "" && e.Rounds == 1) {
			return conveyorErr(ErrConveyorInvalid, "step %s: rounds must be 2..%d", e.Step, conveyorMaxBackRounds)
		}
	}
	return nil
}

// resolveNodeProfile finds the profile an option names, by name or id.
func resolveNodeProfile(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID, ref string) (db.ModelProfile, error) {
	if id, err := util.ParseUUID(ref); err == nil {
		p, err := q.GetSharedModelProfileInWorkspace(ctx, db.GetSharedModelProfileInWorkspaceParams{ID: id, WorkspaceID: workspaceID})
		if err == nil || !errors.Is(err, pgx.ErrNoRows) {
			return p, err
		}
	}
	p, err := q.GetModelProfileByNameInWorkspace(ctx, db.GetModelProfileByNameInWorkspaceParams{WorkspaceID: workspaceID, Name: ref})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && p.OwnerAgentID.Valid) {
		// A personal profile belongs to its agent; a route step cannot name it.
		return db.ModelProfile{}, conveyorErr(ErrConveyorInvalid, "model profile %q not found", ref)
	}
	return p, err
}

// isConveyorVerifier reports whether the step judges work an earlier step did.
func isConveyorVerifier(step string) bool {
	return step == conveyorStepQA || step == conveyorStepReview || step == conveyorStepAccept
}

// defaultBack is where the node's changes_requested goes: the back it names,
// else, for a verifier, the closest earlier step that is not a verifier
// (dev,qa,review,accept: dev). "" sends changes to the coordinator.
func defaultBack(entries []ConveyorRouteEntry, i int) string {
	e := entries[i]
	switch {
	case e.Back == conveyorBackNone:
		return ""
	case e.Back != "" || !isConveyorVerifier(e.Step):
		return e.Back
	}
	for j := i - 1; j >= 0; j-- {
		if !isConveyorVerifier(entries[j].Step) {
			return entries[j].Step
		}
	}
	return ""
}

// retries is how many times the route starts a run of this node again after
// infrastructure cut it short.
func (n ConveyorNode) retries() int32 {
	switch {
	case n.Retries < 0:
		return 0
	case n.Retries > 0:
		return n.Retries
	}
	return conveyorDefaultRetries
}

// stepIdxBefore is the index of the last node named step before idx, -1 if none.
func stepIdxBefore(route []ConveyorNode, step string, idx int32) int32 {
	for i := idx - 1; i >= 0; i-- {
		if node := conveyorNodeAt(route, i); node != nil && node.Step == step {
			return i
		}
	}
	return -1
}

// backRounds is how many rounds a node's back may take the route to.
func (n ConveyorNode) backRounds() int32 {
	if n.Rounds > 0 {
		return n.Rounds
	}
	return conveyorDefaultBackRounds
}

// conveyorRunModel is what a run of a route step must use: the node's profile
// and the family it must avoid. Zero values mean the usual choice.
type conveyorRunModel struct {
	ProfileID     pgtype.UUID
	ExcludeFamily string
	// Node is the step the run serves; nil for a run outside a route.
	Node *ConveyorNode
}

// RunModel reads the options of the node a run serves. The family to avoid is
// the one the newest run of the cross step chose, in this round or the
// latest earlier round that ran it.
func (s *IssueConveyorService) RunModel(ctx context.Context, q *db.Queries, task db.AgentTaskQueue) (conveyorRunModel, error) {
	var out conveyorRunModel
	if !task.ConveyorActionID.Valid || !task.IssueID.Valid {
		return out, nil
	}
	action, err := q.GetIssueConveyorEvent(ctx, task.ConveyorActionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if action.Action.String != conveyorActionRunStep {
		return out, nil
	}
	cv, err := q.GetIssueConveyor(ctx, task.IssueID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && cv.ID != action.ConveyorID) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	route, err := conveyorRoute(cv)
	if err != nil {
		return out, err
	}
	node := conveyorNodeAt(route, action.Idx.Int32)
	if node == nil {
		return out, nil
	}
	return s.nodeRunModel(ctx, q, cv, route, node, action.Round.Int32)
}

// nodeRunModel is the model setting of a run of node in the given round.
func (s *IssueConveyorService) nodeRunModel(ctx context.Context, q *db.Queries, cv db.IssueConveyor, route []ConveyorNode, node *ConveyorNode, round int32) (conveyorRunModel, error) {
	out := conveyorRunModel{Node: node}
	if node.ProfileID != "" {
		out.ProfileID, _ = util.ParseUUID(node.ProfileID)
	}
	if node.Cross == "" {
		return out, nil
	}
	crossIdx := stepIdxBefore(route, node.Cross, node.Idx)
	if crossIdx < 0 {
		return out, nil
	}
	for ; round >= 1; round-- {
		prev, err := q.GetLatestConveyorStepTask(ctx, db.GetLatestConveyorStepTaskParams{
			ConveyorID: cv.ID, Idx: pgtype.Int4{Int32: crossIdx, Valid: true}, Round: pgtype.Int4{Int32: round, Valid: true},
		})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return out, err
		}
		out.ExcludeFamily, err = s.taskFamily(ctx, q, prev)
		return out, err
	}
	return out, nil
}

// taskFamily is the model family a finished run used: its recorded choice,
// else its runtime's provider and the agent's model.
func (s *IssueConveyorService) taskFamily(ctx context.Context, q *db.Queries, task db.AgentTaskQueue) (string, error) {
	var c ModelChoice
	if len(task.ModelChoice) > 0 && json.Unmarshal(task.ModelChoice, &c) == nil && c.Chosen != nil {
		return ModelFamily(c.Provider, c.Chosen.Model), nil
	}
	rt, err := q.GetAgentRuntime(ctx, task.RuntimeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	model := ""
	if agent, err := q.GetAgent(ctx, task.AgentID); err == nil {
		model = agent.Model.String
	}
	return ModelFamily(rt.Provider, model), nil
}

// PreviewRouteStepChoice is the choice for a run of route step idx in the
// conveyor's current round: the node's profile and cross family apply as they
// do for a queued run of that step.
func PreviewRouteStepChoice(ctx context.Context, q *db.Queries, cv db.IssueConveyor, idx int32, agent db.Agent, issue *db.Issue, now time.Time) (ModelChoice, error) {
	route, err := conveyorRoute(cv)
	if err != nil {
		return ModelChoice{}, err
	}
	node := conveyorNodeAt(route, idx)
	if node == nil {
		return ModelChoice{}, fmt.Errorf("conveyor %s has no step %d", util.UUIDToString(cv.ID), idx)
	}
	routeModel, err := (&IssueConveyorService{}).nodeRunModel(ctx, q, cv, route, node, cv.Round)
	if err != nil {
		return ModelChoice{}, err
	}
	return previewChoice(ctx, q, agent, issue, routeModel, now)
}
