package scheduler

import (
	"context"
	"time"
)

// IssueConveyorDelivery creates the runs a route owes and hands routes that
// lost their current run to their coordinator.
type IssueConveyorDelivery interface {
	DeliverAllPending(context.Context) error
}

// A decision commits with its owed action, so the run missing right after a
// crash or an unreachable agent is always findable from the action row; a
// missed bucket cannot lose it.
func IssueConveyorJob(delivery IssueConveyorDelivery) JobSpec {
	return JobSpec{
		Name: "issue_conveyor_delivery", Cadence: 30 * time.Second, CatchUpMode: CatchUpLatestOnly, CatchUpWindow: time.Hour,
		RunTimeout: 45 * time.Second, StaleTimeout: time.Minute, HeartbeatInterval: 10 * time.Second,
		AllowStaleReentry: true, MaxAttempts: 1, Scopes: StaticScopes(ScopeGlobal),
		Handler: func(ctx context.Context, _ HandlerInput) (HandlerResult, error) {
			return HandlerResult{}, delivery.DeliverAllPending(ctx)
		},
	}
}
