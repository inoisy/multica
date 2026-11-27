package service

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestSweepModelSideRowsDropsRowsOfDeletedOwners(t *testing.T) {
	r := newModelClaimRig(t)
	agent := r.agent("side sweep", r.strong)
	task := r.enqueue(agent, pgtype.UUID{})
	if r.choice(task.ID).ModelChoice == nil {
		t.Fatal("routed run has no side row")
	}
	gone := "00000000-0000-4000-8000-00000000dead"
	r.fx.Exec(t, `INSERT INTO agent_model_settings (agent_id, workspace_id, disallowed_tools) VALUES ($1, $2, '["x"]')`, gone, r.ws)
	r.fx.Exec(t, `INSERT INTO task_model_choice (task_id, model_choice) VALUES ($1, '{}')`, gone)
	r.fx.Exec(t, `INSERT INTO issue_model_profile (issue_id, workspace_id, model_profile_id) VALUES ($1, $2, $3)`, gone, r.ws, r.strong)
	r.fx.Exec(t, `INSERT INTO runtime_profile_pool (runtime_profile_id, workspace_id, pool_id) VALUES ($1, $2, $1)`, gone, r.ws)

	if err := r.svc.SweepModelSideRows(r.ctx); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"agent_model_settings WHERE agent_id", "task_model_choice WHERE task_id", "issue_model_profile WHERE issue_id", "runtime_profile_pool WHERE runtime_profile_id"} {
		if n := r.count("SELECT count(*) FROM "+table+"=$1", gone); n != 0 {
			t.Fatalf("%s: orphan survived", table)
		}
	}
	if r.profileOf(agent) != r.strong || r.choice(task.ID).ModelChoice == nil {
		t.Fatal("the sweep removed rows of live owners")
	}
}
