package service

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestSweepForkOrphansDropsRowsOfDeletedOwners(t *testing.T) {
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

	goneWS := "00000000-0000-4000-8000-00000000beef"
	r.fx.Exec(t, `INSERT INTO issue_workflow_artifact (issue_id, workspace_id, project_id, workflow_id, artifact) VALUES ($1, $2, $1, $1, '{}')`, gone, r.ws)
	r.fx.Exec(t, `INSERT INTO model_profile (id, workspace_id, name, owner_agent_id) VALUES ($1, $2, 'sweep personal', $1)`, gone, r.ws)
	r.fx.Exec(t, `INSERT INTO runtime_unavailable (workspace_id, runtime_id, until, reason) VALUES ($1, $2, now() + interval '1 hour', 'sweep')`, goneWS, r.rt["codex"])

	if err := r.svc.SweepForkOrphans(r.ctx); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"agent_model_settings WHERE agent_id", "task_model_choice WHERE task_id", "issue_model_profile WHERE issue_id", "runtime_profile_pool WHERE runtime_profile_id", "issue_workflow_artifact WHERE issue_id", "model_profile WHERE id"} {
		if n := r.count("SELECT count(*) FROM "+table+"=$1", gone); n != 0 {
			t.Fatalf("%s: orphan survived", table)
		}
	}
	if n := r.count("SELECT count(*) FROM runtime_unavailable WHERE workspace_id=$1", goneWS); n != 0 {
		t.Fatal("rows of a deleted workspace survived")
	}
	if r.profileOf(agent) != r.strong || r.choice(task.ID).ModelChoice == nil {
		t.Fatal("the sweep removed rows of live owners")
	}
}
