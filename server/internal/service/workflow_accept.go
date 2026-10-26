package service

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// acceptSystemKey marks the hidden agent that carries accept steps; it is
// also the provider of the daemon runtime that runs them.
const acceptSystemKey = "accept"

// WorkflowAcceptanceAgent returns the model-free agent that runs a Workflow
// acceptance step. Its task follows the project's ordinary workflow handoff.
func WorkflowAcceptanceAgent(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID) (*db.Agent, error) {
	agents, err := q.ListAllAgentsAnyKind(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	a, _, err := acceptAgent(ctx, q, workspaceID, agents)
	return a, err
}

func IsWorkflowAcceptanceAgent(a db.Agent) bool { return isAcceptCarrier(a) }

// isAcceptCarrier reports whether the agent is the server's hidden accept agent.
func isAcceptCarrier(a db.Agent) bool {
	return a.Kind == "system" && a.SystemKey.Valid && a.SystemKey.String == acceptSystemKey
}

// acceptAgent returns the workspace's hidden accept agent: the one on an
// online accept runtime first, created there when that runtime has none yet,
// else any existing one. nil when no daemon of the workspace offers accept.
func acceptAgent(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID, agents []db.Agent) (*db.Agent, bool, error) {
	runtimes, err := q.ListAgentRuntimes(ctx, workspaceID)
	if err != nil {
		return nil, false, err
	}
	var accept []db.AgentRuntime
	for _, r := range runtimes {
		if r.Provider == acceptSystemKey {
			accept = append(accept, r)
		}
	}
	if len(accept) == 0 {
		return nil, false, nil
	}
	carrierOn := func(runtimeID pgtype.UUID) *db.Agent {
		for i := range agents {
			if isAcceptCarrier(agents[i]) && !agents[i].ArchivedAt.Valid && agents[i].RuntimeID == runtimeID {
				return &agents[i]
			}
		}
		return nil
	}
	var fallback *db.Agent
	for _, r := range accept {
		if a := carrierOn(r.ID); a != nil {
			if r.Status == "online" {
				return a, false, nil
			}
			if fallback == nil {
				fallback = a
			}
		}
	}
	for _, r := range accept {
		if r.Status != "online" {
			continue
		}
		created, err := q.CreateSystemAcceptAgent(ctx, db.CreateSystemAcceptAgentParams{
			WorkspaceID: workspaceID, Name: "Приёмка", RuntimeMode: r.RuntimeMode, RuntimeID: r.ID, OwnerID: r.OwnerID,
		})
		var unique *pgconn.PgError
		if errors.As(err, &unique) && unique.Code == "23505" {
			// Another acceptance created it at the same moment: read it back.
			again, lerr := q.ListAllAgentsAnyKind(ctx, workspaceID)
			if lerr != nil {
				return nil, false, lerr
			}
			for i := range again {
				if isAcceptCarrier(again[i]) && !again[i].ArchivedAt.Valid && again[i].RuntimeID == r.ID {
					return &again[i], true, nil
				}
			}
		}
		if err != nil {
			return nil, false, err
		}
		return &created, true, nil
	}
	return fallback, false, nil
}
