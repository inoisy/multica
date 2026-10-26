package main

import (
	"strings"
	"testing"
)

// Migration 616 removes the schema the single model source and the pools
// replaced. It runs on the schema of 614 with the leftovers it has to clear.

const dropLegacyMigration = 616

const (
	legacyTables  = "agent_model_option,subscription_limit_backup,model_truth_backup"
	legacyColumns = "subscription.reserve,runtime_profile.subscriptions,runtime_profile.spend_order,runtime_profile.share_key,runtime_profile.all_workspaces,subscription_pool.legacy_share_key"
)

func (r *modelAccountsRig) hasTable(name string) bool {
	return r.count(`SELECT count(*) FROM information_schema.tables WHERE table_schema = $1 AND table_name = $2`, r.schema, name) == 1
}

func (r *modelAccountsRig) hasColumn(ref string) bool {
	table, column, _ := strings.Cut(ref, ".")
	return r.count(`SELECT count(*) FROM information_schema.columns WHERE table_schema = $1 AND table_name = $2 AND column_name = $3`, r.schema, table, column) == 1
}

func (r *modelAccountsRig) hasIndex(name string) bool {
	return r.count(`SELECT count(*) FROM pg_indexes WHERE schemaname = $1 AND indexname = $2`, r.schema, name) == 1
}

func seedLegacyDrop(t *testing.T) *modelAccountsRig {
	t.Helper()
	r := newModelAccountsRig(t)
	seedModelTruth(t, r)
	if err := r.run("up", firstModelTruthMigration, lastModelTruthMigration); err != nil {
		t.Fatal(err)
	}
	r.exec(`INSERT INTO subscription (key, provider, auth, account_group) VALUES ('late-solo', 'claude', 'claude_oauth', 'late')`)
	for _, code := range []string{"pool_verdict", "", "provider_limit", "agent_cap_refusal", "disabled_refusal", "unknown", "made_up"} {
		r.exec(`INSERT INTO runtime_unavailable (workspace_id, runtime_id, until, reason, reason_code) VALUES ($1, $2, now() + interval '1 hour', 'x', $3)`, r.ws1, r.rt, code)
	}
	return r
}

func TestDropLegacyMigrationRemovesTheOldSchema(t *testing.T) {
	r := seedLegacyDrop(t)
	for _, table := range strings.Split(legacyTables, ",") {
		if !r.hasTable(table) {
			t.Fatalf("%s must exist before 616", table)
		}
	}
	if err := r.run("up", dropLegacyMigration, dropLegacyMigration); err != nil {
		t.Fatal(err)
	}
	for _, table := range strings.Split(legacyTables, ",") {
		if r.hasTable(table) {
			t.Errorf("table %s survived 616", table)
		}
	}
	for _, column := range strings.Split(legacyColumns, ",") {
		if r.hasColumn(column) {
			t.Errorf("column %s survived 616", column)
		}
	}
	if r.hasIndex("runtime_profile_workspace_share_key_uidx") {
		t.Error("the share_key index survived 616")
	}
	// What the pools stand on stays.
	for _, table := range []string{"subscription_pool", "subscription_pool_member", "subscription_pool_grant"} {
		if !r.hasTable(table) {
			t.Errorf("table %s is gone", table)
		}
	}
	for _, column := range []string{"subscription_pool.all_workspaces", "runtime_profile.pool_id", "subscription.caps", "subscription.account_group"} {
		if !r.hasColumn(column) {
			t.Errorf("column %s is gone", column)
		}
	}

	// Pool verdicts go, the rest of the codes are the closed set.
	if got := r.text(`SELECT string_agg(reason_code, ',' ORDER BY reason_code) FROM runtime_unavailable WHERE reason = 'x'`); got != "agent_cap_refusal,disabled_refusal,provider_limit,unknown,unknown,unknown" {
		t.Errorf("codes = %s", got)
	}
	r.insertRefusal(t, "pool_verdict", false)
	r.insertRefusal(t, "", false)
	r.insertRefusal(t, "provider_limit", true)
	// The default is an unknown refusal.
	r.exec(`INSERT INTO runtime_unavailable (workspace_id, runtime_id, until, reason) VALUES ($1, $2, now() + interval '1 hour', 'x')`, r.ws1, r.rt)
	if got := r.text(`SELECT reason_code FROM runtime_unavailable ORDER BY created_at DESC, id LIMIT 1`); got != "unknown" {
		t.Errorf("default code = %s", got)
	}

	// A group left with one account is cleared, a group of two stays.
	if got := r.text(`SELECT account_group FROM subscription WHERE key = 'late-solo'`); got != "" {
		t.Errorf("late single group = %q", got)
	}
	if n := r.count(`SELECT count(*) FROM subscription WHERE account_group = 'pair'`); n != 2 {
		t.Errorf("a group of two accounts must stay, %d kept", n)
	}
}

func (r *modelAccountsRig) insertRefusal(t *testing.T, code string, wantOK bool) {
	t.Helper()
	_, err := r.pool.Exec(r.ctx, `INSERT INTO runtime_unavailable (workspace_id, runtime_id, until, reason, reason_code) VALUES ($1, $2, now() + interval '1 hour', 'x', $3)`, r.ws1, r.rt, code)
	if wantOK && err != nil {
		t.Errorf("code %q refused: %v", code, err)
	}
	if !wantOK && err == nil {
		t.Errorf("code %q accepted", code)
	}
}

func TestDropLegacyMigrationRoundTrip(t *testing.T) {
	r := seedLegacyDrop(t)
	if err := r.run("up", dropLegacyMigration, dropLegacyMigration); err != nil {
		t.Fatal(err)
	}
	if err := r.run("down", dropLegacyMigration, dropLegacyMigration); err != nil {
		t.Fatalf("down: %v", err)
	}
	// The schema is back, without the data.
	for _, table := range []string{"agent_model_option", "subscription_limit_backup"} {
		if !r.hasTable(table) {
			t.Errorf("table %s was not restored", table)
		}
	}
	if r.hasTable("model_truth_backup") {
		t.Error("model_truth_backup comes back only from a database backup")
	}
	for _, column := range strings.Split(legacyColumns, ",") {
		if !r.hasColumn(column) {
			t.Errorf("column %s was not restored", column)
		}
	}
	if !r.hasIndex("agent_model_option_position_uidx") || !r.hasIndex("runtime_profile_workspace_share_key_uidx") {
		t.Error("the indexes were not restored")
	}
	if n := r.count(`SELECT count(*) FROM agent_model_option`); n != 0 {
		t.Errorf("agent_model_option has %d rows after the rollback", n)
	}
	// The closed set of codes is open again for the code that wrote ''.
	r.exec(`INSERT INTO runtime_unavailable (workspace_id, runtime_id, until, reason, reason_code) VALUES ($1, $2, now() + interval '1 hour', 'x', '')`, r.ws1, r.rt)
	if err := r.run("up", dropLegacyMigration, dropLegacyMigration); err != nil {
		t.Fatalf("up again: %v", err)
	}
}
