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
