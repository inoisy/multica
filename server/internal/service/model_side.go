package service

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Model and subscription data of agents, issues, runs and runtime profiles
// lives in side tables keyed by the upstream row (agent_model_settings,
// issue_model_profile, task_model_choice, runtime_profile_pool). A missing
// side row means the defaults: no profile, no disallowed tools, no recorded
// choice, no pool.

// AgentModelSettings is the agent's side row, or its defaults.
func AgentModelSettings(ctx context.Context, q *db.Queries, agentID pgtype.UUID) (db.AgentModelSetting, error) {
	s, err := q.GetAgentModelSettings(ctx, agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.AgentModelSetting{AgentID: agentID, DisallowedTools: []byte("[]")}, nil
	}
	return s, err
}

// AgentModelProfileID is the shared or personal profile the agent runs on.
func AgentModelProfileID(ctx context.Context, q *db.Queries, agentID pgtype.UUID) (pgtype.UUID, error) {
	s, err := AgentModelSettings(ctx, q, agentID)
	return s.ModelProfileID, err
}

// AgentModelSettingsByAgent maps agent id to its side row for the given agents.
func AgentModelSettingsByAgent(ctx context.Context, q *db.Queries, agentIDs []pgtype.UUID) (map[pgtype.UUID]db.AgentModelSetting, error) {
	out := make(map[pgtype.UUID]db.AgentModelSetting, len(agentIDs))
	if len(agentIDs) == 0 {
		return out, nil
	}
	rows, err := q.ListAgentModelSettingsByAgents(ctx, agentIDs)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.AgentID] = r
	}
	return out, nil
}

// IssueModelProfileID is the profile set on the issue, invalid when none is.
func IssueModelProfileID(ctx context.Context, q *db.Queries, issueID pgtype.UUID) (pgtype.UUID, error) {
	id, err := q.GetIssueModelProfileID(ctx, issueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return pgtype.UUID{}, nil
	}
	return id, err
}

// TaskModelChoice is what the server recorded about the run's model and
// account; empty for a run that has none.
func TaskModelChoice(ctx context.Context, q *db.Queries, taskID pgtype.UUID) (db.TaskModelChoice, error) {
	c, err := q.GetTaskModelChoice(ctx, taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.TaskModelChoice{TaskID: taskID}, nil
	}
	return c, err
}

// RuntimeProfilePoolID is the subscription pool the environment spends,
// invalid for an environment outside any pool.
func RuntimeProfilePoolID(ctx context.Context, q *db.Queries, profileID pgtype.UUID) (pgtype.UUID, error) {
	id, err := q.GetRuntimeProfilePoolID(ctx, profileID)
	if errors.Is(err, pgx.ErrNoRows) {
		return pgtype.UUID{}, nil
	}
	return id, err
}

// RuntimeProfilePools maps environment id to the pool it spends.
func RuntimeProfilePools(ctx context.Context, q *db.Queries, profileIDs []pgtype.UUID) (map[pgtype.UUID]pgtype.UUID, error) {
	out := make(map[pgtype.UUID]pgtype.UUID, len(profileIDs))
	if len(profileIDs) == 0 {
		return out, nil
	}
	rows, err := q.ListRuntimeProfilePools(ctx, profileIDs)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.RuntimeProfileID] = r.PoolID
	}
	return out, nil
}

// taskHasModelChoice reports whether the server routed the run by a model
// list; a lookup error counts as no.
func taskHasModelChoice(ctx context.Context, q *db.Queries, taskID pgtype.UUID) bool {
	c, err := TaskModelChoice(ctx, q, taskID)
	return err == nil && c.ModelChoice != nil
}

// SweepModelSideRows is the scheduler job that drops side rows of deleted
// agents, issues, runs and runtime profiles.
func (s *TaskService) SweepModelSideRows(ctx context.Context) error {
	return s.Queries.DeleteOrphanModelSideRows(ctx)
}
