package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/migrations"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// The migrations 597-612 of the model/accounts move (personal profiles, pools,
// one limit, reason codes) are tested on a scratch schema at the schema of the
// day before them (596) with data in the old shape.

const (
	firstModelAccountsMigration = 597
	lastModelAccountsMigration  = 612
)

type modelAccountsRig struct {
	t       *testing.T
	ctx     context.Context
	pool    *pgxpool.Pool
	schema  string
	lockKey int64
	fx      *testutil.Fixture
	ws1     string
	ws2     string
	ws3     string
	user    string
	rt      string
	rtCodex string
	agents  map[string]string
}

func migrationNumber(path string) int {
	n, _ := strconv.Atoi(strings.SplitN(filepath.Base(path), "_", 2)[0])
	return n
}

func (r *modelAccountsRig) files(direction string, from, to int) []string {
	r.t.Helper()
	all, err := migrations.Files(direction)
	if err != nil {
		r.t.Fatal(err)
	}
	var out []string
	for _, f := range all {
		if n := migrationNumber(f); n >= from && n <= to {
			out = append(out, f)
		}
	}
	return out
}

func (r *modelAccountsRig) run(direction string, from, to int) error {
	r.t.Helper()
	return runMigrations(r.ctx, r.pool, runOptions{
		Direction:             direction,
		Files:                 r.files(direction, from, to),
		SchemaMigrationsTable: r.schema + ".schema_migrations",
		AdvisoryLockKey:       r.lockKey,
		Hooks:                 hooksForDirection(direction),
		Conditions:            conditionsForDirection(direction),
	})
}

func (r *modelAccountsRig) exec(sql string, args ...any) {
	r.t.Helper()
	if _, err := r.pool.Exec(r.ctx, sql, args...); err != nil {
		r.t.Fatalf("%v\n%s", err, sql)
	}
}

func (r *modelAccountsRig) text(sql string, args ...any) string {
	r.t.Helper()
	var out *string
	if err := r.pool.QueryRow(r.ctx, sql, args...).Scan(&out); err != nil {
		r.t.Fatalf("%v\n%s", err, sql)
	}
	if out == nil {
		return ""
	}
	return *out
}

func (r *modelAccountsRig) count(sql string, args ...any) int {
	r.t.Helper()
	var n int
	if err := r.pool.QueryRow(r.ctx, sql, args...).Scan(&n); err != nil {
		r.t.Fatalf("%v\n%s", err, sql)
	}
	return n
}

// newModelAccountsRig builds the schema as of migration 596 and fills it with
// the shapes the move has to handle.
func newModelAccountsRig(t *testing.T) *modelAccountsRig {
	t.Helper()
	base := openTestPool(t)
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	t.Cleanup(cancel)
	schema := createScratchSchema(t, ctx, base, "model_accounts_")
	pool := openTestPoolWithSearchPath(t, schema+", public")
	r := &modelAccountsRig{
		t: t, ctx: ctx, pool: pool, schema: schema,
		lockKey: int64(rand.Uint64()&0x7fffffffffffffff) | 1,
		agents:  map[string]string{},
	}
	if err := r.run("up", 0, firstModelAccountsMigration-1); err != nil {
		t.Fatalf("apply the schema up to %d: %v", firstModelAccountsMigration-1, err)
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	boot := testutil.New(pool, "", "")
	r.user = boot.User(t, "owner"+suffix, "owner"+suffix+"@example.com")
	r.ws1 = boot.Workspace(t, "STT"+suffix, "stt"+suffix, testutil.Cols{"issue_prefix": "STT"})
	r.ws2 = boot.Workspace(t, "Two"+suffix, "two"+suffix, testutil.Cols{"issue_prefix": "TWO"})
	r.ws3 = boot.Workspace(t, "Three"+suffix, "three"+suffix, testutil.Cols{"issue_prefix": "THR"})
	r.fx = testutil.New(pool, r.ws1, r.user)
	r.rt = r.fx.Runtime(t, "claude-runtime", testutil.Cols{"provider": "claude", "visibility": "public"})
	r.rtCodex = r.fx.Runtime(t, "codex-runtime", testutil.Cols{"provider": "codex", "visibility": "public"})
	return r
}

func (r *modelAccountsRig) agent(name string, profile string, rows ...string) string {
	r.t.Helper()
	cols := testutil.Cols{}
	if profile != "" {
		cols["model_profile_id"] = profile
	}
	id := r.fx.Agent(r.t, name, r.rt, cols)
	r.agents[name] = id
	for i, row := range rows {
		// model|thinking|custom_args as JSON; the last two are optional.
		parts := strings.SplitN(row, "|", 3)
		for len(parts) < 3 {
			parts = append(parts, "")
		}
		args := "NULL"
		if parts[2] != "" {
			args = "'" + parts[2] + "'::jsonb"
		}
		r.exec(fmt.Sprintf(`INSERT INTO agent_model_option (workspace_id, agent_id, position, runtime_id, model, thinking_level, custom_args)
			VALUES ($1, $2, $3, $4, $5, $6, %s)`, args), r.ws1, id, i+1, r.rt, parts[0], parts[1])
	}
	return id
}

func (r *modelAccountsRig) profile(name string, rows ...string) string {
	r.t.Helper()
	id := r.text(`INSERT INTO model_profile (workspace_id, name) VALUES ($1, $2) RETURNING id::text`, r.ws1, name)
	for i, row := range rows {
		parts := strings.SplitN(row, ":", 2)
		model := parts[0]
		thinking := ""
		if len(parts) == 2 {
			thinking = parts[1]
		}
		r.exec(`INSERT INTO model_profile_option (workspace_id, profile_id, position, runtime_id, model, thinking_level) VALUES ($1, $2, $3, $4, $5, $6)`,
			r.ws1, id, i+1, r.rt, model, thinking)
	}
	return id
}

func (r *modelAccountsRig) subscription(key, provider string, windows, caps string, reserve int) string {
	r.t.Helper()
	auth := "claude_oauth"
	if provider == "codex" {
		auth = "codex_auth"
	}
	return r.text(`INSERT INTO subscription (key, provider, auth, windows, caps, reserve) VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, $6) RETURNING id::text`,
		key, provider, auth, windows, caps, reserve)
}

func (r *modelAccountsRig) runtimeProfile(ws, name, family, subs, order, shareKey string, all bool) string {
	r.t.Helper()
	var key any
	if shareKey != "<null>" {
		key = shareKey
	}
	return r.text(`INSERT INTO runtime_profile (workspace_id, display_name, protocol_family, command_name, subscriptions, spend_order, share_key, all_workspaces)
		VALUES ($1, $2, $3, $3, $4::jsonb, $5, $6, $7) RETURNING id::text`, ws, name, family, subs, order, key, all)
}

// oldShape is every business fact the move touches, in the shape the old
// schema stores it. It reads only columns that exist before 597 and after the
// rollback, so the same query runs before and after.
func (r *modelAccountsRig) oldShape() string {
	r.t.Helper()
	return r.text(`SELECT jsonb_pretty(jsonb_build_object(
		'agents', (SELECT coalesce(jsonb_agg(jsonb_build_object('id', a.id, 'profile', a.model_profile_id,
			'own', (SELECT coalesce(jsonb_agg(jsonb_build_array(o.position, o.runtime_id, o.model, o.thinking_level, o.custom_args) ORDER BY o.position), '[]'::jsonb)
				FROM agent_model_option o WHERE o.agent_id = a.id)) ORDER BY a.id), '[]'::jsonb) FROM agent a),
		'profiles', (SELECT coalesce(jsonb_agg(jsonb_build_object('id', p.id, 'name', p.name, 'exhausted', p.when_exhausted_profile_id,
			'rows', (SELECT coalesce(jsonb_agg(jsonb_build_array(o.position, o.runtime_id, o.model, o.thinking_level, o.custom_args) ORDER BY o.position), '[]'::jsonb)
				FROM model_profile_option o WHERE o.profile_id = p.id)) ORDER BY p.id), '[]'::jsonb) FROM model_profile p),
		'runtime_profiles', (SELECT coalesce(jsonb_agg(jsonb_build_object('id', rp.id, 'ws', rp.workspace_id, 'name', rp.display_name,
			'subs', rp.subscriptions, 'order', rp.spend_order, 'share_key', rp.share_key, 'all', rp.all_workspaces) ORDER BY rp.id), '[]'::jsonb) FROM runtime_profile rp),
		'subscriptions', (SELECT coalesce(jsonb_agg(jsonb_build_object('key', s.key, 'caps', s.caps, 'reserve', s.reserve, 'windows', s.windows) ORDER BY s.key), '[]'::jsonb) FROM subscription s),
		'unavailable', (SELECT coalesce(jsonb_agg(jsonb_build_object('id', u.id, 'runtime', u.runtime_id, 'until', u.until, 'reason', u.reason, 'task', u.task_id) ORDER BY u.id), '[]'::jsonb) FROM runtime_unavailable u)
	))`)
}

// seed writes the data of the move: two agents named like a shared profile
// differing only in case, an agent whose own list equals its profile, agents
// without lists, a shared pool of three workspaces, single-workspace pools,
// two profiles with an empty share_key, accounts with and without a reserve.
func (r *modelAccountsRig) seed() { r.seedWith(true) }

// seedWith(false) leaves out what no rollback can bring back: a profile
// reference to a profile that no longer exists, an account key nobody owns,
// and an empty share_key (it comes back as none).
func (r *modelAccountsRig) seedWith(lossy bool) {
	r.t.Helper()
	strong := r.profile("Strong", "opus:high", "sonnet")
	r.profile("Claude", "haiku")
	r.agent("Claude", strong, "opus|high", "sonnet||[]")
	r.agent("claude", strong, "haiku", "sonnet||[\"--x\"]")
	r.agent("Same", strong, "opus|high", "sonnet")
	r.agent("Plain", strong)
	r.agent("Bare", "")
	if lossy {
		r.agent("Dangling", "00000000-0000-4000-8000-0000000000aa", "opus")
	} else {
		r.agent("Dangling", strong, "opus")
	}

	r.subscription("claude-a", "claude", `["5h","week"]`, `{"5h":80}`, 0)
	r.subscription("claude-b", "claude", `["5h","week"]`, `{}`, 10)
	r.subscription("codex-main", "codex", `["5h","week"]`, `{"5h":99,"week":90}`, 5)
	r.subscription("codex-second", "codex", `["5h","week"]`, `{"5h":88,"week":86}`, 15)
	r.subscription("keyless", "claude", `[]`, `{"5h":100}`, 100)

	r.runtimeProfile(r.ws1, "claude толя", "claude", `["claude-a","claude-b"]`, "first", "pool-shared", true)
	r.runtimeProfile(r.ws2, "claude толя", "claude", `["claude-a","claude-b"]`, "first", "pool-shared", true)
	r.runtimeProfile(r.ws3, "claude толя", "claude", `["claude-a","claude-b"]`, "first", "pool-shared", true)
	codexKeys := `["codex-main","codex-second"]`
	if lossy {
		codexKeys = `["codex-main","ghost","codex-second"]`
	}
	r.runtimeProfile(r.ws1, "codex ws1", "codex", codexKeys, "balanced", "<null>", false)
	emptyKey := "<null>"
	if lossy {
		emptyKey = ""
	}
	r.runtimeProfile(r.ws1, "claude ws1", "claude", `["claude-a"]`, "first", emptyKey, false)
	r.runtimeProfile(r.ws2, "claude ws2", "claude", `["claude-a"]`, "first", emptyKey, false)
	r.runtimeProfile(r.ws1, "plain cli", "claude", `[]`, "first", "<null>", false)

	task := r.fx.Task(r.t, r.agents["Plain"], testutil.Cols{"runtime_id": r.rt})
	future := "now() + interval '2 hours'"
	for _, row := range []struct{ reason, task string }{
		{"subscriptions: usage limit: подписки runtime выше лимита агентам", "NULL"},
		{"usage limit: подписки runtime выше лимита агентам (остаток — резерв людям)", "'" + task + "'"},
		{"usage limit: подписки среды отключены до 2026-10-24", "'" + task + "'"},
		{"agent_provider_quota_limit: You've hit your usage limit", "'" + task + "'"},
		{"manual note", "NULL"},
	} {
		r.exec(fmt.Sprintf(`INSERT INTO runtime_unavailable (workspace_id, runtime_id, until, reason, task_id) VALUES ($1, $2, %s, $3, %s)`, future, row.task), r.ws1, r.rt, row.reason)
	}
}

func TestModelAccountsMigrationsMovePersonalProfiles(t *testing.T) {
	r := newModelAccountsRig(t)
	r.seed()
	if err := r.run("up", firstModelAccountsMigration, lastModelAccountsMigration); err != nil {
		t.Fatal(err)
	}

	if n := r.count(`SELECT count(*) FROM model_profile WHERE owner_agent_id IS NOT NULL`); n != 4 {
		t.Fatalf("personal profiles = %d, want 4 (the agents with an own list)", n)
	}
	for _, name := range []string{"Bare", "Plain"} {
		if got := r.text(`SELECT model_profile_id::text FROM agent WHERE id = $1`, r.agents[name]); name == "Bare" && got != "" {
			t.Errorf("%s has no own list and must stay without a profile, got %s", name, got)
		}
	}

	// Case-insensitive names: the shared profile took "Claude", so both agents
	// named Claude get their id prefix. Names carry no language-specific word.
	names := r.text(`SELECT string_agg(name, '|' ORDER BY name) FROM model_profile WHERE owner_agent_id IS NOT NULL`)
	for _, want := range []string{"Same", "Dangling"} {
		if !strings.Contains("|"+names+"|", "|"+want+"|") {
			t.Errorf("personal profile names %q lack %q", names, want)
		}
	}
	if strings.ContainsFunc(names, func(c rune) bool { return c >= 'А' && c <= 'я' }) {
		t.Errorf("personal profile names %q carry a Russian word", names)
	}
	if dup := r.count(`SELECT count(*) FROM (SELECT lower(name) FROM model_profile GROUP BY workspace_id, lower(name) HAVING count(*) > 1) d`); dup != 0 {
		t.Errorf("%d profile names collide case-insensitively", dup)
	}
	for _, name := range []string{"Claude", "claude"} {
		got := r.text(`SELECT p.name FROM agent a JOIN model_profile p ON p.id = a.model_profile_id WHERE a.id = $1`, r.agents[name])
		if want := name + " · " + r.agents[name][:8]; got != want {
			t.Errorf("agent %s points at %q, want %q", name, got, want)
		}
	}

	// based_on: the agent's shared profile; a dangling one becomes NULL.
	strong := r.text(`SELECT id::text FROM model_profile WHERE name = 'Strong'`)
	if got := r.text(`SELECT based_on_profile_id::text FROM model_profile WHERE owner_agent_id = $1`, r.agents["Same"]); got != strong {
		t.Errorf("based_on of Same = %q, want %q", got, strong)
	}
	if n := r.count(`SELECT count(*) FROM model_profile WHERE owner_agent_id = $1 AND based_on_profile_id IS NULL`, r.agents["Dangling"]); n != 1 {
		t.Error("a dangling profile reference must become based_on NULL")
	}
	if n := r.count(`SELECT count(*) FROM model_profile WHERE owner_agent_id IS NOT NULL AND when_exhausted_profile_id IS NOT NULL`); n != 0 {
		t.Error("migrated personal profiles have no when_exhausted profile")
	}

	// Rows are copied with positions; NULL args stay NULL and [] stays [].
	rows := r.text(`SELECT jsonb_agg(jsonb_build_array(o.position, o.model, o.thinking_level, o.custom_args) ORDER BY o.position)::text
		FROM model_profile p JOIN model_profile_option o ON o.profile_id = p.id WHERE p.owner_agent_id = $1`, r.agents["Claude"])
	if want := `[[1, "opus", "high", null], [2, "sonnet", "", []]]`; rows != want {
		t.Errorf("rows of Claude = %s, want %s", rows, want)
	}
	// The old table is kept untouched.
	if n := r.count(`SELECT count(*) FROM agent_model_option`); n != 7 {
		t.Errorf("agent_model_option has %d rows, want the 7 it had", n)
	}

	// Repeating the migration changes nothing.
	before := r.text(`SELECT jsonb_agg(to_jsonb(p) - 'id' - 'created_at' - 'updated_at' ORDER BY p.name)::text FROM model_profile p`)
	r.exec(readMigration(t, "609_migrate_personal_model_profiles"))
	if after := r.text(`SELECT jsonb_agg(to_jsonb(p) - 'id' - 'created_at' - 'updated_at' ORDER BY p.name)::text FROM model_profile p`); after != before {
		t.Error("609 is not repeatable")
	}
}

func TestModelAccountsMigrationsMovePools(t *testing.T) {
	r := newModelAccountsRig(t)
	r.seed()
	if err := r.run("up", firstModelAccountsMigration, lastModelAccountsMigration); err != nil {
		t.Fatal(err)
	}

	// The shared key, codex, and two profiles with an empty key that stay apart; "plain cli" has no accounts.
	if n := r.count(`SELECT count(*) FROM subscription_pool`); n != 4 {
		t.Fatalf("pools = %d, want 4", n)
	}
	shared := r.text(`SELECT id::text FROM subscription_pool WHERE legacy_share_key = 'pool-shared'`)
	if shared == "" {
		t.Fatal("no pool for the shared key")
	}
	if got := r.text(`SELECT count(*)::text FROM subscription_pool_grant WHERE pool_id = $1`, shared); got != "3" {
		t.Errorf("shared pool grants = %s, want 3", got)
	}
	if got := r.text(`SELECT all_workspaces::text FROM subscription_pool WHERE id = $1`, shared); got != "true" {
		t.Errorf("all_workspaces = %s", got)
	}
	if got := r.text(`SELECT count(*)::text FROM runtime_profile WHERE pool_id = $1`, shared); got != "3" {
		t.Errorf("runtime profiles of the shared pool = %s", got)
	}
	members := r.text(`SELECT string_agg(s.key || '@' || m.position, ',' ORDER BY m.position) FROM subscription_pool_member m JOIN subscription s ON s.id = m.subscription_id WHERE m.pool_id = $1`, shared)
	if members != "claude-a@1,claude-b@2" {
		t.Errorf("members = %s", members)
	}
	// An account missing from subscription is skipped and the rest renumbered.
	codexPool := r.text(`SELECT pool_id::text FROM runtime_profile WHERE display_name = 'codex ws1'`)
	members = r.text(`SELECT string_agg(s.key || '@' || m.position, ',' ORDER BY m.position) FROM subscription_pool_member m JOIN subscription s ON s.id = m.subscription_id WHERE m.pool_id = $1`, codexPool)
	if members != "codex-main@1,codex-second@2" {
		t.Errorf("codex members = %s", members)
	}
	if got := r.text(`SELECT spend_order FROM subscription_pool WHERE id = $1`, codexPool); got != "balanced" {
		t.Errorf("spend order = %s", got)
	}
	// Two independent single-workspace profiles with an empty key stay two pools; the account is in both.
	if got := r.text(`SELECT count(DISTINCT pool_id)::text FROM runtime_profile WHERE display_name IN ('claude ws1', 'claude ws2')`); got != "2" {
		t.Errorf("empty share_key joined independent profiles: %s pools", got)
	}
	if n := r.count(`SELECT count(*) FROM runtime_profile WHERE display_name = 'plain cli' AND pool_id IS NOT NULL`); n != 0 {
		t.Error("a profile without accounts or key is not a pool")
	}
	if n := r.count(`SELECT count(*) FROM subscription_pool_member m JOIN subscription s ON s.id = m.subscription_id WHERE s.key = 'claude-a'`); n != 3 {
		t.Errorf("claude-a is in %d pools, want 3 (it stays in several)", n)
	}

	// Repeat: nothing changes; a copy added later joins the pool made for its key.
	r.exec(readMigration(t, "610_migrate_subscription_pools"))
	if n := r.count(`SELECT count(*) FROM subscription_pool`); n != 4 {
		t.Errorf("610 is not repeatable: %d pools", n)
	}
	r.exec(`INSERT INTO workspace (name, slug, issue_prefix) VALUES ('Four', 'four-x', 'FOU')`)
	four := r.text(`SELECT id::text FROM workspace WHERE slug = 'four-x'`)
	r.runtimeProfile(four, "claude толя", "claude", `["claude-a","claude-b"]`, "first", "pool-shared", true)
	r.exec(readMigration(t, "610_migrate_subscription_pools"))
	if got := r.text(`SELECT count(*)::text FROM subscription_pool_grant WHERE pool_id = $1`, shared); got != "4" {
		t.Errorf("late copy was not granted: %s", got)
	}
}

func TestModelAccountsMigrationsRefuseDisagreeingCopies(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(r *modelAccountsRig)
		want  string
	}{
		{"subscriptions", func(r *modelAccountsRig) {
			r.subscription("a", "claude", `[]`, `{}`, 0)
			r.subscription("b", "claude", `[]`, `{}`, 0)
			r.runtimeProfile(r.ws1, "p", "claude", `["a"]`, "first", "k1", false)
			r.runtimeProfile(r.ws2, "p", "claude", `["b"]`, "first", "k1", false)
		}, "k1"},
		{"spend order", func(r *modelAccountsRig) {
			r.subscription("a", "claude", `[]`, `{}`, 0)
			r.runtimeProfile(r.ws1, "p", "claude", `["a"]`, "first", "k2", false)
			r.runtimeProfile(r.ws2, "p", "claude", `["a"]`, "balanced", "k2", false)
		}, "k2"},
		{"duplicate account", func(r *modelAccountsRig) {
			r.subscription("a", "claude", `[]`, `{}`, 0)
			r.runtimeProfile(r.ws1, "p", "claude", `["a","a"]`, "first", "<null>", false)
		}, "twice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newModelAccountsRig(t)
			tc.setup(r)
			err := r.run("up", firstModelAccountsMigration, lastModelAccountsMigration)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("migration error = %v, want it to name %q", err, tc.want)
			}
			if n := r.count(`SELECT count(*) FROM subscription_pool`); n != 0 {
				t.Errorf("a refused migration left %d pools", n)
			}
			if n := r.count(`SELECT count(*) FROM runtime_profile WHERE pool_id IS NOT NULL`); n != 0 {
				t.Errorf("a refused migration set pool_id on %d profiles", n)
			}
		})
	}
}

func TestModelAccountsMigrationsFoldReserveIntoCaps(t *testing.T) {
	r := newModelAccountsRig(t)
	r.seed()
	effective := func(key string) string {
		return r.text(`SELECT coalesce(jsonb_object_agg(k, lim), '{}'::jsonb)::text FROM (
			SELECT w.k, least(coalesce((s.caps ->> w.k)::int, 100), 100 - s.reserve) AS lim
			FROM subscription s,
				LATERAL (SELECT jsonb_array_elements_text(s.windows) AS k UNION SELECT jsonb_object_keys(s.caps) AS k) w
			WHERE s.key = $1) x WHERE lim < 100`, key)
	}
	want := map[string]string{}
	for _, key := range []string{"claude-a", "claude-b", "codex-main", "codex-second", "keyless"} {
		want[key] = effective(key)
	}
	if want["codex-main"] != `{"5h": 95, "week": 90}` || want["codex-second"] != `{"5h": 85, "week": 85}` {
		t.Fatalf("effective caps before the move: %v", want)
	}
	if err := r.run("up", firstModelAccountsMigration, lastModelAccountsMigration); err != nil {
		t.Fatal(err)
	}
	for key, w := range want {
		if got := effective(key); got != w {
			t.Errorf("%s: effective caps changed from %s to %s", key, w, got)
		}
		if res := r.text(`SELECT reserve::text FROM subscription WHERE key = $1`, key); res != "0" {
			t.Errorf("%s: reserve = %s", key, res)
		}
	}
	if got := r.text(`SELECT caps::text FROM subscription WHERE key = 'codex-second'`); got != `{"5h": 85, "week": 85}` {
		t.Errorf("codex-second caps = %s", got)
	}
	if n := r.count(`SELECT count(*) FROM subscription_limit_backup`); n != 4 {
		t.Errorf("backup rows = %d, want the 4 accounts that had a reserve", n)
	}
}

func TestModelAccountsMigrationsCodeTheRefusals(t *testing.T) {
	r := newModelAccountsRig(t)
	r.seed()
	if err := r.run("up", firstModelAccountsMigration, lastModelAccountsMigration); err != nil {
		t.Fatal(err)
	}
	got := r.text(`SELECT string_agg(reason_code, ',' ORDER BY reason_code) FROM runtime_unavailable`)
	if want := "agent_cap_refusal,disabled_refusal,pool_verdict,provider_limit,unknown"; got != want {
		t.Errorf("codes = %s, want %s", got, want)
	}
}

// The round trip: up, down to 596, and the old shape is what it was. Then the
// edits made after the move survive the rollback.
func TestModelAccountsMigrationsRoundTrip(t *testing.T) {
	r := newModelAccountsRig(t)
	r.seedWith(false)
	before := r.oldShape()

	if err := r.run("up", firstModelAccountsMigration, lastModelAccountsMigration); err != nil {
		t.Fatal(err)
	}
	if err := r.run("down", firstModelAccountsMigration, lastModelAccountsMigration); err != nil {
		t.Fatal(err)
	}
	after := r.oldShape()
	if before != after {
		t.Fatalf("the old shape changed over up/down:\n%s", firstDifferences(before, after))
	}
	if n := r.count(`SELECT count(*) FROM information_schema.tables WHERE table_schema = $1 AND table_name IN ('subscription_pool', 'subscription_pool_member', 'subscription_pool_grant', 'subscription_limit_backup')`, r.schema); n != 0 {
		t.Errorf("%d new tables survived the rollback", n)
	}
	if err := r.run("up", firstModelAccountsMigration, lastModelAccountsMigration); err != nil {
		t.Fatalf("up again: %v", err)
	}
}

func TestModelAccountsMigrationsRollbackKeepsLaterEdits(t *testing.T) {
	r := newModelAccountsRig(t)
	r.seed()
	if err := r.run("up", firstModelAccountsMigration, lastModelAccountsMigration); err != nil {
		t.Fatal(err)
	}

	// A personal profile edited after the move: a new second row.
	claude := r.agents["Claude"]
	personal := r.text(`SELECT model_profile_id::text FROM agent WHERE id = $1`, claude)
	r.exec(`DELETE FROM model_profile_option WHERE profile_id = $1 AND position = 2`, personal)
	r.exec(`UPDATE model_profile_option SET model = 'opus-edited' WHERE profile_id = $1 AND position = 1`, personal)
	// An agent returned to its shared profile: its personal profile is deleted by hand, as the service does.
	same := r.agents["Same"]
	samePersonal := r.text(`SELECT model_profile_id::text FROM agent WHERE id = $1`, same)
	strong := r.text(`SELECT id::text FROM model_profile WHERE name = 'Strong'`)
	r.exec(`UPDATE agent SET model_profile_id = $2 WHERE id = $1`, same, strong)
	r.exec(`DELETE FROM model_profile_option WHERE profile_id = $1`, samePersonal)
	r.exec(`DELETE FROM model_profile WHERE id = $1`, samePersonal)
	// Pool: reorder, add an account, change the order of spending.
	shared := r.text(`SELECT id::text FROM subscription_pool WHERE legacy_share_key = 'pool-shared'`)
	r.exec(`UPDATE subscription_pool_member SET position = position + 10 WHERE pool_id = $1`, shared)
	r.exec(`UPDATE subscription_pool_member SET position = CASE position WHEN 11 THEN 2 ELSE 1 END WHERE pool_id = $1`, shared)
	r.exec(`INSERT INTO subscription_pool_member (pool_id, subscription_id, position) SELECT $1, id, 3 FROM subscription WHERE key = 'keyless'`, shared)
	r.exec(`UPDATE subscription_pool SET spend_order = 'balanced' WHERE id = $1`, shared)
	// A pool made after the move and granted to two workspaces.
	fresh := r.text(`INSERT INTO subscription_pool (name, runtime_type) VALUES ('fresh', 'claude') RETURNING id::text`)
	r.exec(`INSERT INTO subscription_pool_member (pool_id, subscription_id, position) SELECT $1, id, 1 FROM subscription WHERE key = 'claude-b'`, fresh)
	for _, ws := range []string{r.ws1, r.ws2} {
		r.exec(`INSERT INTO subscription_pool_grant (pool_id, workspace_id) VALUES ($1, $2)`, fresh, ws)
		r.exec(`INSERT INTO runtime_profile (workspace_id, display_name, protocol_family, command_name, pool_id) VALUES ($1, 'fresh', 'claude', 'claude', $2)`, ws, fresh)
	}
	// A limit edited after the move, and one left alone.
	r.exec(`UPDATE subscription SET caps = '{"5h":50}'::jsonb WHERE key = 'codex-main'`)

	if err := r.run("down", firstModelAccountsMigration, lastModelAccountsMigration); err != nil {
		t.Fatal(err)
	}

	own := r.text(`SELECT jsonb_agg(jsonb_build_array(position, model) ORDER BY position)::text FROM agent_model_option WHERE agent_id = $1`, claude)
	if own != `[[1, "opus-edited"]]` {
		t.Errorf("own list after the rollback = %s", own)
	}
	if n := r.count(`SELECT count(*) FROM agent_model_option WHERE agent_id = $1`, same); n != 0 {
		t.Errorf("an agent returned to a shared profile kept %d own rows", n)
	}
	if got := r.text(`SELECT model_profile_id::text FROM agent WHERE id = $1`, same); got != strong {
		t.Errorf("agent Same points at %q, want its shared profile", got)
	}
	if got := r.text(`SELECT model_profile_id::text FROM agent WHERE id = $1`, claude); got != strong {
		t.Errorf("agent Claude points at %q, want the profile it was based on", got)
	}
	if n := r.count(`SELECT count(*) FROM model_profile`); n != 2 {
		t.Errorf("profiles after the rollback = %d, want the 2 shared ones", n)
	}

	subs := r.text(`SELECT subscriptions::text || '|' || spend_order || '|' || share_key || '|' || all_workspaces FROM runtime_profile WHERE workspace_id = $1 AND display_name = 'claude толя'`, r.ws1)
	if subs != `["claude-b", "claude-a", "keyless"]|balanced|pool-shared|true` {
		t.Errorf("shared pool after the rollback = %s", subs)
	}
	freshKey := r.text(`SELECT share_key FROM runtime_profile WHERE workspace_id = $1 AND display_name = 'fresh'`, r.ws1)
	freshKey2 := r.text(`SELECT share_key FROM runtime_profile WHERE workspace_id = $1 AND display_name = 'fresh'`, r.ws2)
	if !strings.HasPrefix(freshKey, "pool-") || freshKey != freshKey2 {
		t.Errorf("a pool granted to two workspaces must get one generated share_key, got %q and %q", freshKey, freshKey2)
	}
	if got := r.text(`SELECT share_key FROM runtime_profile WHERE display_name = 'claude ws1'`); got != "" {
		t.Errorf("a single profile must go back without a share_key, got %q", got)
	}

	if got := r.text(`SELECT caps::text || '|' || reserve FROM subscription WHERE key = 'codex-main'`); got != `{"5h": 50}|0` {
		t.Errorf("an edited limit must stay edited: %s", got)
	}
	if got := r.text(`SELECT caps::text || '|' || reserve FROM subscription WHERE key = 'codex-second'`); got != `{"5h": 88, "week": 86}|15` {
		t.Errorf("an untouched limit must get its caps and reserve back: %s", got)
	}
}

func readMigration(t *testing.T, version string) string {
	t.Helper()
	files, err := migrations.Files("up")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if migrations.ExtractVersion(f) == version {
			raw, err := readFile(f)
			if err != nil {
				t.Fatal(err)
			}
			return raw
		}
	}
	t.Fatalf("migration %s not found", version)
	return ""
}

func readFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	return string(raw), err
}

// firstDifferences lists the lines where two multi-line texts part, with the
// line before each for context.
func firstDifferences(before, after string) string {
	a, b := strings.Split(before, "\n"), strings.Split(after, "\n")
	var out []string
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y string
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			out = append(out, fmt.Sprintf("line %d: before %q after %q", i+1, x, y))
			if len(out) >= 12 {
				break
			}
		}
	}
	return strings.Join(out, "\n")
}
