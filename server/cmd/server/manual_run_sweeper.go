package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	// The journal's 90-day retention does not need a fine tick; an hourly
	// round keeps it off the liveness path.
	manualRunSweepInterval = time.Hour
	manualRunSweepBudget   = 30 * time.Second
)

// runManualRunSweeper removes manual-run journal rows past their retention.
func runManualRunSweeper(ctx context.Context, queries *db.Queries) {
	runPeriodicSweep(ctx, manualRunSweepInterval, func() {
		sweepManualRuns(ctx, queries, time.Now())
	})
}

func sweepManualRuns(ctx context.Context, queries *db.Queries, now time.Time) {
	sweepCtx, cancel := context.WithTimeout(ctx, manualRunSweepBudget)
	defer cancel()
	removed, err := service.PruneManualRuns(sweepCtx, queries, now)
	if err != nil {
		slog.Warn("manual run journal cleanup failed", "removed", removed, "error", err)
		return
	}
	if removed > 0 {
		slog.Info("manual run journal cleanup removed expired rows", "count", removed)
	}
}
