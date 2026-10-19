package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// NormalizeSquadRouteTemplate checks a squad's default route as an operator
// writes it ("dev:Agent,qa:Agent,review:Agent,close:Agent") and returns it in
// canonical form; "" clears the template. The shape follows what enabling a
// route demands, and every agent must exist, be live and have a runtime, so a
// template that saves is one an assignment can start.
func NormalizeSquadRouteTemplate(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID, spec string) (string, error) {
	if strings.TrimSpace(spec) == "" {
		return "", nil
	}
	entries, err := ParseConveyorRouteSpec(spec)
	if err != nil {
		return "", err
	}
	if len(entries) < 2 || entries[len(entries)-1].Step != conveyorStepClose {
		return "", conveyorErr(ErrConveyorInvalid, "the route needs at least one step and must end with close:<coordinator>")
	}
	agents, err := q.ListAgents(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	lastDev, lastQA := -1, -1
	parts := make([]string, len(entries))
	for i, e := range entries {
		switch e.Step {
		case conveyorStepDev:
			lastDev = i
		case conveyorStepQA:
			lastQA = i
		case conveyorStepClose:
			if i != len(entries)-1 {
				return "", conveyorErr(ErrConveyorInvalid, "close is only allowed as the last node")
			}
		}
		agent, err := pickConveyorAgent(agents, e.Agent)
		if err != nil {
			return "", err
		}
		if agent.ArchivedAt.Valid || !agent.RuntimeID.Valid {
			return "", conveyorErr(ErrConveyorInvalid, "agent %q is archived or has no runtime", agent.Name)
		}
		parts[i] = e.Step + ":" + e.Agent
	}
	if lastDev >= 0 && lastQA < lastDev {
		return "", conveyorErr(ErrConveyorInvalid, "a qa step must follow dev")
	}
	if err := acceptPlacementError(entries); err != nil {
		return "", err
	}
	return strings.Join(parts, ","), nil
}

// StartSquadRoute runs an issue just assigned to a squad along the squad's
// default route instead of waking its leader. It reports whether the route
// now leads the issue: true when this call enabled it or a live route already
// did, false when the squad has no template or the operator disabled the
// issue's route. An error means the template could not start (the leader
// then takes the issue, told why). invoker is the human the route's runs
// answer to.
func (s *IssueConveyorService) StartSquadRoute(ctx context.Context, issue db.Issue, squad db.Squad, invoker pgtype.UUID) (bool, error) {
	template := strings.TrimSpace(squad.RouteTemplate)
	if template == "" {
		return false, nil
	}
	var revision int64
	cv, err := s.q().GetIssueConveyor(ctx, issue.ID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return false, err
	case cv.Status == ConveyorDisabled:
		// Turned off by hand: assignment does not override the operator.
		return false, nil
	case cv.Status != ConveyorCompleted:
		return true, nil
	default:
		revision = cv.Revision
	}
	raw, _ := json.Marshal(template)
	if _, err = s.Apply(ctx, issue, invoker, ConveyorCommand{Action: ConveyorCmdEnable, Route: raw, ExpectedRevision: revision}); err != nil {
		return false, err
	}
	return true, nil
}

// SquadRouteHandoffNote tells the leader why the squad's default route did
// not take an issue it was assigned.
func SquadRouteHandoffNote(err error) string {
	return "The squad's default route was not enabled (" + err.Error() + "); coordinate this issue yourself or enable a route with `multica issue conveyor enable`."
}

// RouteLeadsIssue reports whether a live route (active or waiting for a
// provider) carries the issue: the route wakes each next agent itself, so an
// agent's comment must not hand the issue back to the squad leader. coveredBy
// names the route for the superseded journal.
func (s *IssueConveyorService) RouteLeadsIssue(ctx context.Context, issueID pgtype.UUID) (string, bool, error) {
	cv, err := s.q().GetIssueConveyor(ctx, issueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if cv.Status != ConveyorActive && cv.Status != ConveyorWaitingProvider {
		return "", false, nil
	}
	return "conveyor:" + util.UUIDToString(cv.ID), true, nil
}
