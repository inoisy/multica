package scheduler

import (
	"context"
	"time"
)

// ModelRouting lifts expired runtime limits and re-routes unclaimed runs
// whose model list now chooses differently.
type ModelRouting interface {
	SweepModelRouting(context.Context) (int, error)
}

// ModelRoutingJob is what makes a run that waited for a limit start by
// itself: no external rerun and no alarm clock. Every pass re-reads the
// state, so a missed bucket only delays the next one.
func ModelRoutingJob(routing ModelRouting) JobSpec {
	return JobSpec{
		Name: "model_routing", Cadence: 30 * time.Second, CatchUpMode: CatchUpLatestOnly, CatchUpWindow: time.Hour,
		RunTimeout: 45 * time.Second, StaleTimeout: time.Minute, HeartbeatInterval: 10 * time.Second,
		AllowStaleReentry: true, MaxAttempts: 1, Scopes: StaticScopes(ScopeGlobal),
		Handler: func(ctx context.Context, _ HandlerInput) (HandlerResult, error) {
			_, err := routing.SweepModelRouting(ctx)
			return HandlerResult{}, err
		},
	}
}

// ModelSideRows removes side rows whose agent, issue, run or environment is gone.
type ModelSideRows interface {
	SweepModelSideRows(context.Context) error
}

// ModelSideRowsJob keeps the model side tables free of rows left by upstream
// deletes, which do not know these tables.
func ModelSideRowsJob(rows ModelSideRows) JobSpec {
	return JobSpec{
		Name: "model_side_rows", Cadence: time.Hour, CatchUpMode: CatchUpLatestOnly, CatchUpWindow: 24 * time.Hour,
		RunTimeout: 5 * time.Minute, StaleTimeout: 10 * time.Minute, HeartbeatInterval: 30 * time.Second,
		AllowStaleReentry: true, MaxAttempts: 1, Scopes: StaticScopes(ScopeGlobal),
		Handler: func(ctx context.Context, _ HandlerInput) (HandlerResult, error) {
			return HandlerResult{}, rows.SweepModelSideRows(ctx)
		},
	}
}
