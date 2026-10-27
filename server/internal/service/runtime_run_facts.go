package service

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// RunFactsWindow is the fixed window the environment page counts runs over.
const RunFactsWindow = 24 * time.Hour

// What the page says of a runtime's place in the choice. It is independent of
// the machine's heartbeat, which the page shows as its own state.
const (
	SelectionActive  = "active"  // runs were recorded in the window
	SelectionReserve = "reserve" // no runs, but profiles, agents or issues lead here
	SelectionUnused  = "unused"  // no runs and nothing leads here
	SelectionClosed  = "closed"  // selection is forbidden for the environment
)

// RunFacts is what was recorded for a runtime in the window: one count per
// task record, split by the account the daemon confirmed. Runs without a
// confirmation are counted apart, never assigned to an account by guess.
type RunFacts struct {
	Since, Until       time.Time
	Runs               int64
	UnknownAccountRuns int64
	Accounts           map[string]int64
}

// LoadRunFacts counts the task records started on the runtime in the last 24
// hours. Quota measurements are not task records and never add to the count.
func LoadRunFacts(ctx context.Context, q *db.Queries, runtimeID pgtype.UUID, now time.Time) (RunFacts, error) {
	facts := RunFacts{Since: now.Add(-RunFactsWindow), Until: now, Accounts: map[string]int64{}}
	rows, err := q.ListRunFacts24Hours(ctx, db.ListRunFacts24HoursParams{
		Since:     pgtype.Timestamptz{Time: facts.Since, Valid: true},
		Until:     pgtype.Timestamptz{Time: now, Valid: true},
		RuntimeID: util.UUIDToString(runtimeID),
	})
	if err != nil {
		return facts, err
	}
	for _, row := range rows {
		facts.Runs += row.Runs
		if row.SubscriptionID == "" {
			facts.UnknownAccountRuns += row.Runs
		} else {
			facts.Accounts[row.SubscriptionID] += row.Runs
		}
	}
	return facts, nil
}

// SelectionState names the runtime's place in the choice from the facts and
// the references. A forbidden selection wins over the facts.
func SelectionState(facts RunFacts, usage RuntimeModelUsage, closed bool) string {
	switch {
	case closed:
		return SelectionClosed
	case facts.Runs > 0:
		return SelectionActive
	case len(usage.SelectedIn) > 0 || len(usage.FallbackProfiles) > 0 || len(usage.Agents) > 0 || usage.OpenIssues > 0:
		return SelectionReserve
	}
	return SelectionUnused
}
