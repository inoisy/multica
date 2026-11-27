package service

import "context"

// SweepForkOrphans is the scheduler job that drops fork rows whose agent,
// issue, run, project, runtime profile or workspace was deleted.
func (s *TaskService) SweepForkOrphans(ctx context.Context) error {
	return s.Queries.DeleteForkOrphans(ctx)
}
