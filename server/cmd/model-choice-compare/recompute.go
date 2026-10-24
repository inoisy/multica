package main

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/handler"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// recomputePoolVerdicts rewrites the pool verdict rows of runtime_unavailable
// the way the server does after every usage report, judged at now. It changes
// the copy: it is the second snapshot of the old algorithm, taken only when
// the stored rows are older than the data they were computed from.
func recomputePoolVerdicts(ctx context.Context, pool *pgxpool.Pool, now time.Time) error {
	h := &handler.Handler{Queries: db.New(pool)}
	h.RefreshSubscriptionAvailabilityAt(ctx, now)
	return nil
}
