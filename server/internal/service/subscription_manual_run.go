package service

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ManualRunRetention is how long the journal of manual runs is kept.
const ManualRunRetention = 90 * 24 * time.Hour

// ManualRunPruneBatch bounds one delete so the cleanup never holds a long lock.
const ManualRunPruneBatch = 500

// manualRunPruneMaxBatches bounds one cleanup round; what is left waits for
// the next one.
const manualRunPruneMaxBatches = 20

// PruneManualRuns removes the journal rows older than the retention, finished
// or not, in bounded batches, and reports how many it removed.
func PruneManualRuns(ctx context.Context, q *db.Queries, now time.Time) (int64, error) {
	var total int64
	for i := 0; i < manualRunPruneMaxBatches; i++ {
		n, err := q.DeleteExpiredSubscriptionManualRuns(ctx, db.DeleteExpiredSubscriptionManualRunsParams{
			Before:    pgtype.Timestamptz{Time: now.Add(-ManualRunRetention), Valid: true},
			BatchSize: ManualRunPruneBatch,
		})
		total += n
		if err != nil || n < ManualRunPruneBatch {
			return total, err
		}
	}
	return total, nil
}

// ManualRunCommands are the only commands the journal records, and only by
// name: what the admin typed after it is never seen by the server.
var ManualRunCommands = map[string]bool{"claude": true, "codex": true}

// OpenManualRun records that an admin started a command on an account. It
// notes whether the account was already over the agents' limit; that never
// refuses the run.
func OpenManualRun(ctx context.Context, q *db.Queries, sub db.Subscription, userID pgtype.UUID, daemonID, command string, now time.Time) (db.SubscriptionManualRun, error) {
	accounts, err := LoadAccounts(ctx, q, []pgtype.UUID{sub.ID}, now)
	if err != nil {
		return db.SubscriptionManualRun{}, err
	}
	over := false
	if a, ok := accounts[util.UUIDToString(sub.ID)]; ok {
		over = OverAgentCap(a, now)
	}
	return q.CreateSubscriptionManualRun(ctx, db.CreateSubscriptionManualRunParams{
		SubscriptionID:   sub.ID,
		UserID:           userID,
		DaemonID:         daemonID,
		Command:          command,
		AgentCapExceeded: over,
	})
}

// FinishManualRun closes a record once. A repeat of the close, which a daemon
// retries after a lost answer, returns the record as it was closed; ok is
// false when there is no such record on the account.
func FinishManualRun(ctx context.Context, q *db.Queries, subscriptionID, runID pgtype.UUID, exitCode int32) (run db.SubscriptionManualRun, ok bool, err error) {
	run, err = q.CloseSubscriptionManualRun(ctx, db.CloseSubscriptionManualRunParams{
		ID: runID, SubscriptionID: subscriptionID, ExitCode: pgtype.Int4{Int32: exitCode, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		run, err = q.GetSubscriptionManualRun(ctx, db.GetSubscriptionManualRunParams{ID: runID, SubscriptionID: subscriptionID})
		if errors.Is(err, pgx.ErrNoRows) {
			return run, false, nil
		}
	}
	return run, err == nil, err
}
