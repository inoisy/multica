package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// recomputePoolVerdicts exists in the build of the original algorithm, where
// the pool's verdict is a stored row that can be rewritten at T. This build
// computes the verdict from the accounts whenever it is asked, so there is
// nothing to rewrite and the flag is refused.
func recomputePoolVerdicts(_ context.Context, _ *pgxpool.Pool, _ time.Time) error {
	return errors.New("--recompute-pool-verdicts belongs to the snapshot of the original algorithm: this build computes pool verdicts from the accounts at T")
}
