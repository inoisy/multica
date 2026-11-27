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

// ForkOrphans removes fork rows whose owner is gone.
type ForkOrphans interface {
	SweepForkOrphans(context.Context) error
}

// ForkOrphansJob cleans up after upstream deletes, which do not know the fork
// tables.
func ForkOrphansJob(rows ForkOrphans) JobSpec {
	return JobSpec{
		Name: "fork_orphans", Cadence: time.Hour, CatchUpMode: CatchUpLatestOnly, CatchUpWindow: 24 * time.Hour,
		RunTimeout: 5 * time.Minute, StaleTimeout: 10 * time.Minute, HeartbeatInterval: 30 * time.Second,
		AllowStaleReentry: true, MaxAttempts: 1, Scopes: StaticScopes(ScopeGlobal),
		Handler: func(ctx context.Context, _ HandlerInput) (HandlerResult, error) {
			return HandlerResult{}, rows.SweepForkOrphans(ctx)
		},
	}
}
