package handler

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
)

// processChildEvents runs the sub-issue rules of these parents (the child_done
// system rule and people's sub-issue conditions) right after a write. The
// write recorded its sub-issue changes in its own transaction; a failure here
// is retried by the scheduler sweep.
func (h *Handler) processChildEvents(ctx context.Context, parentIDs ...pgtype.UUID) {
	_ = (&service.IssueWakeupService{Tasks: h.TaskService}).ProcessChildEvents(ctx, parentIDs...)
	// A closed sub-issue can open the next stage of an epic the server
	// dispatches; the scheduler retries a pass that fails here.
	dispatch := &service.StageDispatchService{Issues: h.IssueService}
	for _, id := range parentIDs {
		if id.Valid {
			if _, err := dispatch.Dispatch(ctx, id); err != nil {
				slog.Warn("stage dispatch after a sub-issue change failed; the sweep retries", "parent_id", uuidToString(id), "error", err)
			}
		}
	}
}
