package service

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The fields runtime_id/model/thinking_level of an agent are the stored
// projection of row 1 of its profile, kept for clients and daemons that read
// them. Only the profile decides what runs: these functions are the one place
// that writes the projection, in the transaction of the change that moves it.

// projectProfileOnAgents copies row 1 of the profile onto every agent on it.
func projectProfileOnAgents(ctx context.Context, q *db.Queries, workspaceID, profileID pgtype.UUID) error {
	_, err := q.ProjectModelProfileRow1OnAgents(ctx, db.ProjectModelProfileRow1OnAgentsParams{WorkspaceID: workspaceID, ProfileID: profileID})
	return err
}

// setAgentProfile points the agent at the profile and projects its row 1; the
// returned agent carries both.
func setAgentProfile(ctx context.Context, q *db.Queries, agent db.Agent, profileID pgtype.UUID) (db.Agent, error) {
	pointed, err := q.SetAgentModelProfile(ctx, db.SetAgentModelProfileParams{ID: agent.ID, WorkspaceID: agent.WorkspaceID, ModelProfileID: profileID})
	if err != nil {
		return agent, err
	}
	if !profileID.Valid {
		return pointed, nil
	}
	if err := projectProfileOnAgents(ctx, q, agent.WorkspaceID, profileID); err != nil {
		return agent, err
	}
	return q.GetAgent(ctx, agent.ID)
}

// profilesOfRuntime lists the profiles that have a row on the runtime, with
// the workspace each belongs to.
func profilesOfRuntime(ctx context.Context, q *db.Queries, runtimeID pgtype.UUID) (map[pgtype.UUID]pgtype.UUID, error) {
	rows, err := q.ListModelProfileOptionsOfRuntime(ctx, runtimeID)
	if err != nil {
		return nil, err
	}
	out := map[pgtype.UUID]pgtype.UUID{}
	for _, r := range rows {
		out[r.ProfileID] = r.WorkspaceID
	}
	return out, nil
}

// RemoveRuntimeModelRows drops the rows and unavailability of a runtime that
// is going away, closes the gaps in the profiles it leaves and projects their
// new row 1 onto the agents. A profile left without rows leaves its agents
// unable to run; they do not fall back to their old fields.
func RemoveRuntimeModelRows(ctx context.Context, q *db.Queries, runtimeID pgtype.UUID) error {
	profiles, err := profilesOfRuntime(ctx, q, runtimeID)
	if err != nil {
		return err
	}
	if err := q.DeleteRuntimeModelReferences(ctx, runtimeID); err != nil {
		return err
	}
	for profileID, workspaceID := range profiles {
		if err := q.ShiftModelProfileOptionPositions(ctx, profileID); err != nil {
			return err
		}
		if err := q.CompactModelProfileOptionPositions(ctx, profileID); err != nil {
			return err
		}
		if err := projectProfileOnAgents(ctx, q, workspaceID, profileID); err != nil {
			return err
		}
	}
	return nil
}

// MoveRuntimeModelRows hands the rows and unavailability of a merged runtime
// to its successor and refreshes the projection of the profiles they sit in.
func MoveRuntimeModelRows(ctx context.Context, q *db.Queries, oldRuntimeID, newRuntimeID pgtype.UUID) error {
	profiles, err := profilesOfRuntime(ctx, q, oldRuntimeID)
	if err != nil {
		return err
	}
	if err := q.ReassignRuntimeModelReferences(ctx, db.ReassignRuntimeModelReferencesParams{NewRuntimeID: newRuntimeID, OldRuntimeID: oldRuntimeID}); err != nil {
		return err
	}
	for profileID, workspaceID := range profiles {
		if err := projectProfileOnAgents(ctx, q, workspaceID, profileID); err != nil {
			return err
		}
	}
	return nil
}

// ProfileFromAgentFields gives the agent the personal profile of one row made
// from its runtime, model and thinking level, and points it there. Creation
// paths that take the three fields (the agent form, the builder, the workspace
// assistant) call it in their transaction, so no model agent exists without a
// profile; a runtime switch of a carrier calls it again to rewrite the row.
func ProfileFromAgentFields(ctx context.Context, q *db.Queries, agent db.Agent) (db.Agent, error) {
	if _, err := q.LockModelProfilesInWorkspace(ctx, agent.WorkspaceID); err != nil {
		return agent, err
	}
	args := decodeArgs(agent.CustomArgs)
	if args == nil {
		args = []string{}
	}
	row := ModelOptionInput{
		RuntimeID: util.UUIDToString(agent.RuntimeID), Model: agent.Model.String,
		ThinkingLevel: agent.ThinkingLevel.String, CustomArgs: &args,
	}
	return savePersonalProfile(ctx, q, agent, PersonalProfileInput{Options: []ModelOptionInput{row}})
}
