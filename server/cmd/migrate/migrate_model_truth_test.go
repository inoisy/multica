package main

import (
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// Migrations 613 and 614 run on the schema of 612 with the shapes they have
// to handle: agents on a profile with fields that drifted, a model agent
// without a profile, the hidden accept carrier, single and shared account
// groups, an environment of a pool with an empty runtime_type.

const (
	firstModelTruthMigration = 613
	lastModelTruthMigration  = 614
)

type modelTruthIDs struct {
	drifted, bench, accept, profile, benchProfile string
	loneAccount, pairA, pairB                     string
}

func seedModelTruth(t *testing.T, r *modelAccountsRig) modelTruthIDs {
	t.Helper()
	var ids modelTruthIDs
	r.seed()
	if err := r.run("up", firstModelAccountsMigration, lastModelAccountsMigration); err != nil {
		t.Fatal(err)
	}
	// An agent on a shared profile whose fields drifted from row 1.
	ids.profile = r.text(`INSERT INTO model_profile (workspace_id, name) VALUES ($1, 'Truth') RETURNING id::text`, r.ws1)
	r.exec(`INSERT INTO model_profile_option (workspace_id, profile_id, position, runtime_id, model, thinking_level) VALUES ($1, $2, 1, $3, 'opus', 'high'), ($1, $2, 2, $4, 'gpt', '')`,
		r.ws1, ids.profile, r.rt, r.rtCodex)
	ids.drifted = r.fx.Agent(t, "Drifted", r.rtCodex, testutil.Cols{"model_profile_id": ids.profile, "model": "gpt", "thinking_level": "low"})
	// A model agent on fields only, with custom args.
	ids.bench = r.fx.Agent(t, "Bench", r.rtCodex, testutil.Cols{"model": "glm-5", "thinking_level": "max", "custom_args": testutil.Raw(`'["--x"]'::jsonb`)})
	ids.accept = r.fx.Agent(t, "Accept", r.rt, testutil.Cols{"kind": "system", "system_key": "accept", "model": "keep"})
	r.exec(`INSERT INTO subscription (key, provider, auth, account_group) VALUES ('lone', 'claude', 'claude_oauth', 'solo'), ('pair-a', 'claude', 'claude_oauth', 'pair'), ('pair-b', 'claude', 'claude_oauth', 'pair')`)
	ids.loneAccount = r.text(`SELECT id::text FROM subscription WHERE key = 'lone'`)
	ids.pairA = r.text(`SELECT id::text FROM subscription WHERE key = 'pair-a'`)
	ids.pairB = r.text(`SELECT id::text FROM subscription WHERE key = 'pair-b'`)
	return ids
}

func TestModelTruthMigrationsProjectProfilesAndNormalizeGroups(t *testing.T) {
	r := newModelAccountsRig(t)
	ids := seedModelTruth(t, r)
	if err := r.run("up", firstModelTruthMigration, lastModelTruthMigration); err != nil {
		t.Fatal(err)
	}

	// The drifted agent carries row 1 of its profile.
	if got := r.text(`SELECT runtime_id::text || '|' || model || '|' || thinking_level FROM agent WHERE id = $1`, ids.drifted); got != r.rt+"|opus|high" {
		t.Errorf("drifted agent fields = %s", got)
	}
	// The agent on fields got a personal profile of one row with its args, and
	// still has the same fields.
	if got := r.text(`SELECT jsonb_agg(jsonb_build_array(o.position, o.runtime_id, o.model, o.thinking_level, o.custom_args))::text
		FROM agent a JOIN model_profile p ON p.id = a.model_profile_id AND p.owner_agent_id = a.id
		JOIN model_profile_option o ON o.profile_id = p.id WHERE a.id = $1`, ids.bench); got != `[[1, "`+r.rtCodex+`", "glm-5", "max", ["--x"]]]` {
		t.Errorf("bench profile row = %s", got)
	}
	if got := r.text(`SELECT model || '|' || thinking_level FROM agent WHERE id = $1`, ids.bench); got != "glm-5|max" {
		t.Errorf("bench fields = %s", got)
	}
	// The agent without any list (Bare) is a model agent too.
	if n := r.count(`SELECT count(*) FROM agent WHERE model_profile_id IS NULL AND NOT (kind = 'system' AND system_key = 'accept')`); n != 0 {
		t.Errorf("%d model agents stay without a profile", n)
	}
	// The accept carrier is untouched.
	if got := r.text(`SELECT coalesce(model_profile_id::text, '-') || '|' || model FROM agent WHERE id = $1`, ids.accept); got != "-|keep" {
		t.Errorf("accept carrier changed: %s", got)
	}
	// A single group is cleared, a group of two stays.
	if got := r.text(`SELECT account_group FROM subscription WHERE id = $1`, ids.loneAccount); got != "" {
		t.Errorf("single group = %q", got)
	}
	if n := r.count(`SELECT count(*) FROM subscription WHERE id IN ($1, $2) AND account_group = 'pair'`, ids.pairA, ids.pairB); n != 2 {
		t.Errorf("a group of two accounts must stay, %d kept", n)
	}

	// Repeating changes nothing.
	snapshot := func() string {
		return r.text(`SELECT jsonb_build_object(
			'agents', (SELECT jsonb_agg(to_jsonb(a) - 'updated_at' ORDER BY a.id) FROM agent a),
			'profiles', (SELECT count(*) FROM model_profile),
			'rows', (SELECT count(*) FROM model_profile_option),
			'backup', (SELECT count(*) FROM model_truth_backup))::text`)
	}
	before := snapshot()
	r.exec(readMigration(t, "613_migrate_model_truth"))
	if after := snapshot(); after != before {
		t.Error("613 is not repeatable")
	}
}

func TestModelTruthMigrationsRoundTrip(t *testing.T) {
	r := newModelAccountsRig(t)
	ids := seedModelTruth(t, r)
	shape := func() string {
		return r.text(`SELECT jsonb_pretty(jsonb_build_object(
			'agents', (SELECT jsonb_agg(jsonb_build_object('id', a.id, 'rt', a.runtime_id, 'mode', a.runtime_mode, 'model', a.model, 'thinking', a.thinking_level, 'profile', a.model_profile_id) ORDER BY a.id) FROM agent a),
			'profiles', (SELECT jsonb_agg(p.id ORDER BY p.id) FROM model_profile p),
			'groups', (SELECT jsonb_agg(jsonb_build_array(s.key, s.account_group) ORDER BY s.key) FROM subscription s)))`)
	}
	before := shape()
	if err := r.run("up", firstModelTruthMigration, lastModelTruthMigration); err != nil {
		t.Fatal(err)
	}
	if shape() == before {
		t.Fatal("613 changed nothing")
	}
	if err := r.run("down", firstModelTruthMigration, lastModelTruthMigration); err != nil {
		t.Fatal(err)
	}
	if after := shape(); after != before {
		t.Fatalf("the data changed over up/down:\n%s", firstDifferences(before, after))
	}
	if n := r.count(`SELECT count(*) FROM information_schema.tables WHERE table_schema = $1 AND table_name = 'model_truth_backup'`, r.schema); n != 0 {
		t.Error("the backup table survived the rollback")
	}
	if n := r.count(`SELECT count(*) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace
		WHERE n.nspname = $1 AND c.conname = 'runtime_profile_pool_command_check'`, r.schema); n != 0 {
		t.Error("the check survived the rollback")
	}
	if err := r.run("up", firstModelTruthMigration, lastModelTruthMigration); err != nil {
		t.Fatalf("up again: %v", err)
	}
	_ = ids
}

func TestModelTruthMigrationForbidsWrappersInPoolEnvironments(t *testing.T) {
	r := newModelAccountsRig(t)
	seedModelTruth(t, r)
	pools := map[string]string{}
	for _, name := range []string{"legacy", "wrapper", "fresh"} {
		pools[name] = r.text(`INSERT INTO subscription_pool (name, runtime_type) VALUES ($1, 'claude') RETURNING id::text`, "truth "+name)
	}
	// A pool environment written before runtime_type existed.
	r.exec(`INSERT INTO runtime_profile (workspace_id, display_name, protocol_family, command_name, runtime_type, pool_id)
		VALUES ($1, 'legacy pool env', 'claude', 'claude', '', $2)`, r.ws1, pools["legacy"])
	// And a wrapper that was already there: it stays, the check is NOT VALID.
	r.exec(`INSERT INTO runtime_profile (workspace_id, display_name, protocol_family, command_name, runtime_type, pool_id)
		VALUES ($1, 'old wrapper', 'claude', 'claude-direct', 'claude', $2)`, r.ws1, pools["wrapper"])
	if err := r.run("up", firstModelTruthMigration, lastModelTruthMigration); err != nil {
		t.Fatal(err)
	}
	if got := r.text(`SELECT runtime_type FROM runtime_profile WHERE display_name = 'legacy pool env'`); got != "claude" {
		t.Errorf("runtime_type of the legacy environment = %q", got)
	}
	if n := r.count(`SELECT count(*) FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace
		WHERE n.nspname = $1 AND c.conname = 'runtime_profile_pool_command_check' AND NOT c.convalidated`, r.schema); n != 1 {
		t.Error("the check must exist and be NOT VALID")
	}
	_, err := r.pool.Exec(r.ctx, `INSERT INTO runtime_profile (workspace_id, display_name, protocol_family, command_name, runtime_type, pool_id)
		VALUES ($1, 'new wrapper', 'claude', 'claude-direct', 'claude', $2)`, r.ws1, pools["fresh"])
	if err == nil || !strings.Contains(err.Error(), "runtime_profile_pool_command_check") {
		t.Errorf("a new wrapper in a pool environment must be refused, got %v", err)
	}
	// Without a pool any command is fine.
	r.exec(`INSERT INTO runtime_profile (workspace_id, display_name, protocol_family, command_name, runtime_type)
		VALUES ($1, 'own login', 'claude', 'claude-direct', 'claude')`, r.ws1)
}
