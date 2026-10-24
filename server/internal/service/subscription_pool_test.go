package service

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type poolRig struct {
	*modelClaimRig
	pools *PoolService
	ws2   pgtype.UUID
	keys  []string
}

func newPoolRig(t *testing.T) *poolRig {
	t.Helper()
	r := newModelClaimRig(t)
	pr := &poolRig{modelClaimRig: r, pools: &PoolService{Queries: r.q, TxStarter: r.fx.Pool}}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	boot := r.fx
	ws2 := boot.Workspace(t, "pool-two-"+suffix, "pool-two-"+suffix, map[string]any{"issue_prefix": "PTW"})
	pr.ws2 = util.MustParseUUID(ws2)
	t.Cleanup(func() {
		ctx := r.ctx
		pool := r.fx.Pool
		pool.Exec(ctx, `DELETE FROM runtime_profile WHERE pool_id IN (SELECT id FROM subscription_pool WHERE name LIKE 'ptest %')`)
		pool.Exec(ctx, `DELETE FROM subscription_pool_grant WHERE pool_id IN (SELECT id FROM subscription_pool WHERE name LIKE 'ptest %')`)
		pool.Exec(ctx, `DELETE FROM subscription_pool_member WHERE pool_id IN (SELECT id FROM subscription_pool WHERE name LIKE 'ptest %')`)
		pool.Exec(ctx, `DELETE FROM subscription_pool WHERE name LIKE 'ptest %'`)
		pool.Exec(ctx, `DELETE FROM subscription WHERE key LIKE 'ptest-%'`)
	})
	return pr
}

func (pr *poolRig) account(name, provider string) string {
	pr.t.Helper()
	key := fmt.Sprintf("ptest-%s-%d", name, time.Now().UnixNano())
	auth := "claude_oauth"
	if provider == "codex" {
		auth = "codex_auth"
	}
	pr.fx.Exec(pr.t, `INSERT INTO subscription (key, provider, auth) VALUES ($1, $2, $3)`, key, provider, auth)
	pr.keys = append(pr.keys, key)
	return key
}

func (pr *poolRig) members(pool pgtype.UUID) string {
	pr.t.Helper()
	var out string
	pr.fx.QueryRow(pr.t, `SELECT coalesce(string_agg(split_part(s.key, '-', 2) || '@' || m.position, ',' ORDER BY m.position), '') FROM subscription_pool_member m JOIN subscription s ON s.id = m.subscription_id WHERE m.pool_id = $1`, pool).Scan(&out)
	return out
}

func TestPoolIsCreatedWithItsAccountsGrantsAndEnvironments(t *testing.T) {
	pr := newPoolRig(t)
	a, b := pr.account("a", "codex"), pr.account("b", "codex")
	res, err := pr.pools.Create(pr.ctx, pr.owner, PoolInput{
		Name: "ptest one", RuntimeType: "codex", Subscriptions: []string{a, b}, Workspaces: []pgtype.UUID{pr.ws, pr.ws2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pool.Revision != 1 || res.Pool.SpendOrder != "first" || pr.members(res.Pool.ID) != "a@1,b@2" {
		t.Fatalf("pool: %+v members %s", res.Pool, pr.members(res.Pool.ID))
	}
	if len(res.Grants) != 2 || len(res.Profiles) != 2 {
		t.Fatalf("grants %d environments %d", len(res.Grants), len(res.Profiles))
	}
	for _, p := range res.Profiles {
		if p.DisplayName != "ptest one" || p.ProtocolFamily != "codex" || p.CommandName != "codex" || !p.Enabled || p.PoolID != res.Pool.ID {
			t.Fatalf("environment: %+v", p)
		}
	}
}

func TestPoolRefusesWrongFamilyDuplicatesAndTakenNames(t *testing.T) {
	pr := newPoolRig(t)
	claude, codex := pr.account("c", "claude"), pr.account("x", "codex")
	for name, in := range map[string]PoolInput{
		"wrong family": {Name: "ptest bad", RuntimeType: "claude", Subscriptions: []string{claude, codex}},
		"twice":        {Name: "ptest bad", RuntimeType: "claude", Subscriptions: []string{claude, claude}},
		"unknown":      {Name: "ptest bad", RuntimeType: "claude", Subscriptions: []string{"ptest-nope"}},
		"no name":      {Name: " ", RuntimeType: "claude"},
		"family":       {Name: "ptest bad", RuntimeType: "gemini"},
		"spend order":  {Name: "ptest bad", RuntimeType: "claude", SpendOrder: "random"},
	} {
		if _, err := pr.pools.Create(pr.ctx, pr.owner, in); !errors.Is(err, ErrPoolInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The name of the environment is taken by another profile there.
	pr.fx.Exec(t, `INSERT INTO runtime_profile (workspace_id, display_name, protocol_family, command_name) VALUES ($1, 'ptest taken', 'claude', 'claude')`, pr.ws)
	_, err := pr.pools.Create(pr.ctx, pr.owner, PoolInput{Name: "ptest taken", RuntimeType: "claude", Workspaces: []pgtype.UUID{pr.ws}})
	if !errors.Is(err, ErrPoolNameTaken) {
		t.Fatalf("taken name: %v", err)
	}
	if n := pr.count(`SELECT count(*) FROM subscription_pool WHERE name = 'ptest taken'`); n != 0 {
		t.Fatalf("a refused create left %d pools", n)
	}
	pr.fx.Exec(t, `DELETE FROM runtime_profile WHERE display_name = 'ptest taken'`)
}

func TestPoolRevisionRisesOnAccountsOrderAndSpendOrderOnly(t *testing.T) {
	pr := newPoolRig(t)
	a, b, c := pr.account("a", "claude"), pr.account("b", "claude"), pr.account("c", "claude")
	res, err := pr.pools.Create(pr.ctx, pr.owner, PoolInput{Name: "ptest rev", RuntimeType: "claude", Subscriptions: []string{a, b}, Workspaces: []pgtype.UUID{pr.ws}})
	if err != nil {
		t.Fatal(err)
	}
	id := res.Pool.ID
	rev := func() int32 {
		p, _ := pr.q.GetSubscriptionPool(pr.ctx, id)
		return p.Revision
	}
	update := func(expected int32, p PoolPatch) error {
		_, err := pr.pools.Update(pr.ctx, id, pr.owner, &expected, p)
		return err
	}
	desc, order := "words", "balanced"
	name := "ptest renamed"
	if err := update(1, PoolPatch{Name: &name, Description: &desc}); err != nil || rev() != 1 {
		t.Fatalf("a rename must not raise the revision: %v rev %d", err, rev())
	}
	if n := pr.count(`SELECT count(*) FROM runtime_profile WHERE pool_id=$1 AND display_name='ptest renamed' AND description='words'`, id); n != 1 {
		t.Fatal("the environment did not follow the pool's name and description")
	}
	same := []string{a, b}
	if err := update(1, PoolPatch{Subscriptions: &same}); err != nil || rev() != 1 {
		t.Fatalf("the same accounts in the same order change nothing: %v rev %d", err, rev())
	}
	reordered := []string{b, a, c}
	if err := update(1, PoolPatch{Subscriptions: &reordered}); err != nil || rev() != 2 || pr.members(id) != "b@1,a@2,c@3" {
		t.Fatalf("reorder: %v rev %d members %s", err, rev(), pr.members(id))
	}
	if err := update(2, PoolPatch{SpendOrder: &order}); err != nil || rev() != 3 {
		t.Fatalf("spend order: %v rev %d", err, rev())
	}

	// A client that read an older revision is refused and told the current one.
	err = update(2, PoolPatch{Subscriptions: &same})
	var conflict *PoolRevisionError
	if !errors.As(err, &conflict) || conflict.Current != 3 || !errors.Is(err, ErrPoolRevisionConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	if pr.members(id) != "b@1,a@2,c@3" {
		t.Fatalf("a refused write changed the members: %s", pr.members(id))
	}
}

func TestPoolGrantsMakeAndSwitchOffEnvironments(t *testing.T) {
	pr := newPoolRig(t)
	res, err := pr.pools.Create(pr.ctx, pr.owner, PoolInput{Name: "ptest grants", RuntimeType: "claude", Workspaces: []pgtype.UUID{pr.ws}})
	if err != nil {
		t.Fatal(err)
	}
	id := res.Pool.ID
	enabled := func(ws pgtype.UUID) string {
		var out string
		pr.fx.QueryRow(t, `SELECT coalesce((SELECT enabled::text FROM runtime_profile WHERE pool_id=$1 AND workspace_id=$2), 'none')`, id, ws).Scan(&out)
		return out
	}
	if _, err := pr.pools.SetGrants(pr.ctx, id, pr.owner, nil, []pgtype.UUID{pr.ws, pr.ws2}); err != nil {
		t.Fatal(err)
	}
	if enabled(pr.ws) != "true" || enabled(pr.ws2) != "true" {
		t.Fatalf("environments: %s %s", enabled(pr.ws), enabled(pr.ws2))
	}
	// Taking a grant back switches the environment off; it does not delete it.
	if _, err := pr.pools.SetGrants(pr.ctx, id, pr.owner, nil, []pgtype.UUID{pr.ws}); err != nil {
		t.Fatal(err)
	}
	if enabled(pr.ws2) != "false" || enabled(pr.ws) != "true" {
		t.Fatalf("after taking back: %s %s", enabled(pr.ws), enabled(pr.ws2))
	}
	if n := pr.count(`SELECT count(*) FROM subscription_pool_grant WHERE pool_id=$1`, id); n != 1 {
		t.Fatalf("grants: %d", n)
	}
	// Switching the pool off and on applies to granted workspaces only.
	if _, err := pr.pools.SetEnabled(pr.ctx, id, false); err != nil {
		t.Fatal(err)
	}
	if _, err := pr.pools.SetEnabled(pr.ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if enabled(pr.ws) != "true" || enabled(pr.ws2) != "false" {
		t.Fatalf("a revoked environment came back: %s %s", enabled(pr.ws), enabled(pr.ws2))
	}
	// Granted again, it is switched on, not duplicated.
	if _, err := pr.pools.SetGrants(pr.ctx, id, pr.owner, nil, []pgtype.UUID{pr.ws, pr.ws2}); err != nil {
		t.Fatal(err)
	}
	if enabled(pr.ws2) != "true" || pr.count(`SELECT count(*) FROM runtime_profile WHERE pool_id=$1`, id) != 2 {
		t.Fatal("regrant")
	}
	// The grants do not move the revision: a run already handed the pool keeps it.
	if p, _ := pr.q.GetSubscriptionPool(pr.ctx, id); p.Revision != 1 {
		t.Fatalf("revision %d", p.Revision)
	}
}

func TestPoolIsDeletedOnlyWithoutEnvironments(t *testing.T) {
	pr := newPoolRig(t)
	a := pr.account("a", "claude")
	res, err := pr.pools.Create(pr.ctx, pr.owner, PoolInput{Name: "ptest delete", RuntimeType: "claude", Subscriptions: []string{a}, Workspaces: []pgtype.UUID{pr.ws}})
	if err != nil {
		t.Fatal(err)
	}
	if err := pr.pools.Delete(pr.ctx, res.Pool.ID); !errors.Is(err, ErrPoolInUse) {
		t.Fatalf("pool with an environment: %v", err)
	}
	pr.fx.Exec(t, `DELETE FROM runtime_profile WHERE pool_id=$1`, res.Pool.ID)
	if err := pr.pools.Delete(pr.ctx, res.Pool.ID); err != nil {
		t.Fatal(err)
	}
	if n := pr.count(`SELECT (SELECT count(*) FROM subscription_pool WHERE id=$1) + (SELECT count(*) FROM subscription_pool_member WHERE pool_id=$1) + (SELECT count(*) FROM subscription_pool_grant WHERE pool_id=$1)`, res.Pool.ID); n != 0 {
		t.Fatalf("leftovers: %d", n)
	}
	if err := pr.pools.Delete(pr.ctx, res.Pool.ID); !errors.Is(err, ErrPoolNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestDeletedAccountLeavesEveryPoolAndClosesTheGap(t *testing.T) {
	pr := newPoolRig(t)
	a, b, c := pr.account("a", "claude"), pr.account("b", "claude"), pr.account("c", "claude")
	one, err := pr.pools.Create(pr.ctx, pr.owner, PoolInput{Name: "ptest gap one", RuntimeType: "claude", Subscriptions: []string{a, b, c}})
	if err != nil {
		t.Fatal(err)
	}
	two, err := pr.pools.Create(pr.ctx, pr.owner, PoolInput{Name: "ptest gap two", RuntimeType: "claude", Subscriptions: []string{b}})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := pr.q.GetSubscriptionByKey(pr.ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := RemoveAccount(pr.ctx, pr.q, sub.ID)
	if err != nil || len(changed) != 2 {
		t.Fatalf("pools changed: %v %v", changed, err)
	}
	if pr.members(one.Pool.ID) != "a@1,c@2" || pr.members(two.Pool.ID) != "" {
		t.Fatalf("members: %q / %q", pr.members(one.Pool.ID), pr.members(two.Pool.ID))
	}
	if p, _ := pr.q.GetSubscriptionPool(pr.ctx, one.Pool.ID); p.Revision != 2 {
		t.Fatalf("revision %d", p.Revision)
	}
}

func TestPoolReachingAllWorkspacesFollowsNewOnes(t *testing.T) {
	pr := newPoolRig(t)
	res, err := pr.pools.Create(pr.ctx, pr.owner, PoolInput{Name: "ptest everywhere", RuntimeType: "claude", AllWorkspaces: true})
	if err != nil {
		t.Fatal(err)
	}
	fresh := pr.fx.Workspace(t, "pool-fresh", fmt.Sprintf("pool-fresh-%d", time.Now().UnixNano()), map[string]any{"issue_prefix": "PFR"})
	ws := util.MustParseUUID(fresh)
	if err := EnsureAllWorkspacePools(pr.ctx, pr.q, ws, pr.owner); err != nil {
		t.Fatal(err)
	}
	if err := EnsureAllWorkspacePools(pr.ctx, pr.q, ws, pr.owner); err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if n := pr.count(`SELECT count(*) FROM runtime_profile WHERE pool_id=$1 AND workspace_id=$2 AND enabled`, res.Pool.ID, ws); n != 1 {
		t.Fatalf("environments of the new workspace: %d", n)
	}
	if n := pr.count(`SELECT count(*) FROM subscription_pool_grant WHERE pool_id=$1 AND workspace_id=$2`, res.Pool.ID, ws); n != 1 {
		t.Fatalf("grant of the new workspace: %d", n)
	}
	pr.fx.Exec(t, `DELETE FROM runtime_profile WHERE workspace_id=$1`, ws)
	pr.fx.Exec(t, `DELETE FROM subscription_pool_grant WHERE workspace_id=$1`, ws)
	if !strings.Contains(fresh, "-") {
		t.Fatal("unreachable")
	}
	_ = db.Subscription{}
}
