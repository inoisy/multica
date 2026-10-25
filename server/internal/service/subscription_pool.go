package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// Errors of the pool service; the handler maps them to status codes.
var (
	ErrPoolInvalid          = errors.New("invalid subscription pool")
	ErrPoolNotFound         = errors.New("subscription pool not found")
	ErrPoolRevisionConflict = errors.New("subscription pool changed")
	ErrPoolInUse            = errors.New("subscription pool still has environments")
	ErrPoolNameTaken        = errors.New("a runtime profile with the pool's name already exists in the workspace")
)

// subscriptionProviderFamily maps an account's provider to the CLI family it
// runs on: every provider but codex speaks the Anthropic API through Claude
// Code.
var subscriptionProviderFamily = map[string]string{
	"claude":     "claude",
	"zai":        "claude",
	"minimax":    "claude",
	"loodsen-ai": "claude",
	"openrouter": "claude",
	"codex":      "codex",
}

// SubscriptionFamily is the CLI family an account's provider runs on, "" for
// an unknown provider.
func SubscriptionFamily(provider string) string { return subscriptionProviderFamily[provider] }

// SpendOrders are the ways a pool spends its accounts.
var SpendOrders = map[string]bool{"first": true, "balanced": true}

// PoolService owns pools: their name, accounts and order, the workspaces they
// are granted to, and the runtime profile each of those workspaces keeps for
// them. Nothing else writes any of the three; the pieces change together in
// one transaction.
type PoolService struct {
	Queries   *db.Queries
	TxStarter TxStarter
}

func (s *PoolService) withTx(ctx context.Context, fn func(q *db.Queries) error) error {
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(s.Queries.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func poolInvalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrPoolInvalid, fmt.Sprintf(format, args...))
}

// PoolRevisionError tells the client which revision the pool has now.
type PoolRevisionError struct{ Current int32 }

func (e *PoolRevisionError) Error() string {
	return fmt.Sprintf("%v: it is at revision %d now; read it again", ErrPoolRevisionConflict, e.Current)
}
func (e *PoolRevisionError) Unwrap() error { return ErrPoolRevisionConflict }

// PoolInput creates a pool. Subscriptions are account keys in spend order.
// Workspaces are the workspaces it is granted to; AllWorkspaces grants it to
// every workspace, present and future.
type PoolInput struct {
	Name          string
	RuntimeType   string
	Description   string
	SpendOrder    string
	Subscriptions []string
	Workspaces    []pgtype.UUID
	AllWorkspaces bool
}

// PoolPatch changes a pool; nil fields are kept. Subscriptions replaces the
// accounts and their order as a whole.
type PoolPatch struct {
	Name          *string
	Description   *string
	SpendOrder    *string
	Subscriptions *[]string
	AllWorkspaces *bool
}

// PoolResult is a pool after a write, with the environments the write touched.
type PoolResult struct {
	Pool     db.SubscriptionPool
	Members  []db.SubscriptionPoolMember
	Grants   []db.SubscriptionPoolGrant
	Profiles []db.RuntimeProfile
	// Skipped are the workspaces where the environment could not be made
	// because a runtime profile with the pool's name is already there.
	Skipped []pgtype.UUID
}

func (s *PoolService) validateAccounts(ctx context.Context, q *db.Queries, family string, keys []string) ([]db.Subscription, error) {
	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k] {
			return nil, poolInvalid("subscription %s is listed twice", k)
		}
		seen[k] = true
	}
	if len(keys) == 0 {
		return nil, nil
	}
	subs, err := q.ListSubscriptionsByKeys(ctx, keys)
	if err != nil {
		return nil, err
	}
	byKey := map[string]db.Subscription{}
	for _, sub := range subs {
		byKey[sub.Key] = sub
	}
	ordered := make([]db.Subscription, 0, len(keys))
	for _, k := range keys {
		sub, ok := byKey[k]
		if !ok {
			return nil, poolInvalid("subscription %s not found", k)
		}
		if SubscriptionFamily(sub.Provider) != family {
			return nil, poolInvalid("subscription %s (%s) does not run on a %s runtime", k, sub.Provider, family)
		}
		ordered = append(ordered, sub)
	}
	return ordered, nil
}

func validatePoolName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return "", poolInvalid("name must be 1 to 100 characters")
	}
	return name, nil
}

func validatePoolDescription(d string) (string, error) {
	d = strings.TrimSpace(d)
	if utf8.RuneCountInString(d) > 500 {
		return "", poolInvalid("description must be 500 characters or fewer")
	}
	return d, nil
}

func replaceMembers(ctx context.Context, q *db.Queries, poolID pgtype.UUID, subs []db.Subscription) error {
	if err := q.DeleteSubscriptionPoolMembers(ctx, poolID); err != nil {
		return err
	}
	for i, sub := range subs {
		if err := q.InsertSubscriptionPoolMember(ctx, db.InsertSubscriptionPoolMemberParams{PoolID: poolID, SubscriptionID: sub.ID, Position: int32(i + 1)}); err != nil {
			return err
		}
	}
	return nil
}

// ensureEnvironment gives the workspace the runtime profile of the pool, or
// switches the one it has back on. ok is false when the pool's name is taken
// by another profile there.
func ensureEnvironment(ctx context.Context, q *db.Queries, pool db.SubscriptionPool, workspaceID, actor pgtype.UUID) (db.RuntimeProfile, bool, error) {
	existing, err := q.GetPoolRuntimeProfileInWorkspace(ctx, db.GetPoolRuntimeProfileInWorkspaceParams{PoolID: pool.ID, WorkspaceID: workspaceID})
	if err == nil {
		if existing.Enabled {
			return existing, true, nil
		}
		enabled, err := q.UpdateRuntimeProfile(ctx, db.UpdateRuntimeProfileParams{ID: existing.ID, WorkspaceID: workspaceID, Enabled: pgtype.Bool{Bool: true, Valid: true}})
		return enabled, true, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return db.RuntimeProfile{}, false, err
	}
	p, err := q.CreatePoolRuntimeProfile(ctx, db.CreatePoolRuntimeProfileParams{
		WorkspaceID: workspaceID, DisplayName: pool.Name, ProtocolFamily: pool.RuntimeType,
		Description: pgtype.Text{String: pool.Description, Valid: pool.Description != ""}, CreatedBy: actor, PoolID: pool.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.RuntimeProfile{}, false, nil
	}
	return p, err == nil, err
}

func (s *PoolService) result(ctx context.Context, q *db.Queries, pool db.SubscriptionPool, profiles []db.RuntimeProfile, skipped []pgtype.UUID) (PoolResult, error) {
	members, err := q.ListSubscriptionPoolMembersByPools(ctx, []pgtype.UUID{pool.ID})
	if err != nil {
		return PoolResult{}, err
	}
	grants, err := q.ListSubscriptionPoolGrantsByPool(ctx, pool.ID)
	if err != nil {
		return PoolResult{}, err
	}
	return PoolResult{Pool: pool, Members: members, Grants: grants, Profiles: profiles, Skipped: skipped}, nil
}

// grantTo records the grants and makes the environments in the workspaces.
func grantTo(ctx context.Context, q *db.Queries, pool db.SubscriptionPool, workspaces []pgtype.UUID, actor pgtype.UUID) (profiles []db.RuntimeProfile, skipped []pgtype.UUID, err error) {
	for _, ws := range workspaces {
		if _, err := q.GetWorkspace(ctx, ws); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, nil, poolInvalid("workspace %s not found", util.UUIDToString(ws))
			}
			return nil, nil, err
		}
		if err := q.InsertSubscriptionPoolGrant(ctx, db.InsertSubscriptionPoolGrantParams{PoolID: pool.ID, WorkspaceID: ws}); err != nil {
			return nil, nil, err
		}
		p, ok, err := ensureEnvironment(ctx, q, pool, ws, actor)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			skipped = append(skipped, ws)
			continue
		}
		profiles = append(profiles, p)
	}
	return profiles, skipped, nil
}

// Create makes a pool, its accounts, its grants and the environments.
func (s *PoolService) Create(ctx context.Context, actor pgtype.UUID, in PoolInput) (PoolResult, error) {
	name, err := validatePoolName(in.Name)
	if err != nil {
		return PoolResult{}, err
	}
	description, err := validatePoolDescription(in.Description)
	if err != nil {
		return PoolResult{}, err
	}
	family := strings.TrimSpace(in.RuntimeType)
	if family != "claude" && family != "codex" {
		return PoolResult{}, poolInvalid("runtime_type must be claude or codex")
	}
	order := strings.TrimSpace(in.SpendOrder)
	if order == "" {
		order = "first"
	}
	if !SpendOrders[order] {
		return PoolResult{}, poolInvalid("spend_order must be first or balanced")
	}
	var out PoolResult
	err = s.withTx(ctx, func(q *db.Queries) error {
		subs, err := s.validateAccounts(ctx, q, family, in.Subscriptions)
		if err != nil {
			return err
		}
		pool, err := q.CreateSubscriptionPool(ctx, db.CreateSubscriptionPoolParams{
			ID: dbid.NewV7(), Name: name, RuntimeType: family, Description: description, SpendOrder: order,
			AllWorkspaces: in.AllWorkspaces, CreatedBy: actor,
		})
		if err != nil {
			return err
		}
		if err := replaceMembers(ctx, q, pool.ID, subs); err != nil {
			return err
		}
		targets := in.Workspaces
		if in.AllWorkspaces {
			if targets, err = q.ListAllWorkspaceIDs(ctx); err != nil {
				return err
			}
		}
		profiles, skipped, err := grantTo(ctx, q, pool, targets, actor)
		if err != nil {
			return err
		}
		if len(targets) > 0 && len(profiles) == 0 {
			return fmt.Errorf("%w: %s", ErrPoolNameTaken, name)
		}
		out, err = s.result(ctx, q, pool, profiles, skipped)
		return err
	})
	return out, err
}

// lockPool takes the pool for writing and checks the revision the client read.
func lockPool(ctx context.Context, q *db.Queries, id pgtype.UUID, expected *int32) (db.SubscriptionPool, error) {
	pool, err := q.LockSubscriptionPool(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return pool, ErrPoolNotFound
	}
	if err != nil {
		return pool, err
	}
	if expected != nil && *expected != pool.Revision {
		return pool, &PoolRevisionError{Current: pool.Revision}
	}
	return pool, nil
}

// Update changes a pool. expected, when set, is the revision the client read:
// a pool that has moved on is refused with PoolRevisionError. The revision
// rises when the accounts, their order or the spend order change.
func (s *PoolService) Update(ctx context.Context, id, actor pgtype.UUID, expected *int32, patch PoolPatch) (PoolResult, error) {
	var out PoolResult
	err := s.withTx(ctx, func(q *db.Queries) error {
		pool, err := lockPool(ctx, q, id, expected)
		if err != nil {
			return err
		}
		name, description, order, all := pool.Name, pool.Description, pool.SpendOrder, pool.AllWorkspaces
		step := int32(0)
		if patch.Name != nil {
			if name, err = validatePoolName(*patch.Name); err != nil {
				return err
			}
		}
		if patch.Description != nil {
			if description, err = validatePoolDescription(*patch.Description); err != nil {
				return err
			}
		}
		if patch.SpendOrder != nil {
			order = strings.TrimSpace(*patch.SpendOrder)
			if !SpendOrders[order] {
				return poolInvalid("spend_order must be first or balanced")
			}
			if order != pool.SpendOrder {
				step = 1
			}
		}
		if patch.AllWorkspaces != nil {
			all = *patch.AllWorkspaces
		}
		if patch.Subscriptions != nil {
			subs, err := s.validateAccounts(ctx, q, pool.RuntimeType, *patch.Subscriptions)
			if err != nil {
				return err
			}
			current, err := q.ListSubscriptionPoolMembersByPools(ctx, []pgtype.UUID{pool.ID})
			if err != nil {
				return err
			}
			if !sameMembers(current, subs) {
				step = 1
				if err := replaceMembers(ctx, q, pool.ID, subs); err != nil {
					return err
				}
			}
		}
		updated, err := q.UpdateSubscriptionPool(ctx, db.UpdateSubscriptionPoolParams{
			ID: pool.ID, Name: name, Description: description, SpendOrder: order, AllWorkspaces: all, RevisionStep: step,
		})
		if err != nil {
			return err
		}
		synced, err := q.SyncPoolRuntimeProfiles(ctx, db.SyncPoolRuntimeProfilesParams{
			PoolID: pool.ID, DisplayName: name, Description: pgtype.Text{String: description, Valid: description != ""},
		})
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: %s", ErrPoolNameTaken, name)
		}
		if err != nil {
			return err
		}
		profiles := synced
		var skipped []pgtype.UUID
		if all && !pool.AllWorkspaces {
			ids, err := q.ListAllWorkspaceIDs(ctx)
			if err != nil {
				return err
			}
			made, skip, err := grantTo(ctx, q, updated, ids, actor)
			if err != nil {
				return err
			}
			profiles, skipped = append(profiles, made...), skip
		}
		out, err = s.result(ctx, q, updated, profiles, skipped)
		return err
	})
	return out, err
}

func sameMembers(current []db.SubscriptionPoolMember, next []db.Subscription) bool {
	if len(current) != len(next) {
		return false
	}
	for i, m := range current {
		if m.SubscriptionID != next[i].ID || int(m.Position) != i+1 {
			return false
		}
	}
	return true
}

// SetGrants makes the pool's grants exactly the given workspaces: environments
// appear where it is newly granted, and are switched off where it is taken
// back. It changes nothing for a run that was already handed the pool.
func (s *PoolService) SetGrants(ctx context.Context, id, actor pgtype.UUID, expected *int32, workspaces []pgtype.UUID) (PoolResult, error) {
	var out PoolResult
	err := s.withTx(ctx, func(q *db.Queries) error {
		pool, err := lockPool(ctx, q, id, expected)
		if err != nil {
			return err
		}
		want := map[string]bool{}
		for _, ws := range workspaces {
			want[util.UUIDToString(ws)] = true
		}
		current, err := q.ListSubscriptionPoolGrantsByPool(ctx, pool.ID)
		if err != nil {
			return err
		}
		var profiles []db.RuntimeProfile
		for _, g := range current {
			if want[util.UUIDToString(g.WorkspaceID)] {
				continue
			}
			if err := q.DeleteSubscriptionPoolGrant(ctx, db.DeleteSubscriptionPoolGrantParams{PoolID: pool.ID, WorkspaceID: g.WorkspaceID}); err != nil {
				return err
			}
			off, err := q.DisablePoolRuntimeProfileInWorkspace(ctx, db.DisablePoolRuntimeProfileInWorkspaceParams{PoolID: pool.ID, WorkspaceID: g.WorkspaceID})
			if err != nil {
				return err
			}
			profiles = append(profiles, off...)
		}
		made, skipped, err := grantTo(ctx, q, pool, workspaces, actor)
		if err != nil {
			return err
		}
		out, err = s.result(ctx, q, pool, append(profiles, made...), skipped)
		return err
	})
	return out, err
}

// SetEnabled switches the environments of a pool, in the workspaces it is
// granted to, on or off together.
func (s *PoolService) SetEnabled(ctx context.Context, id pgtype.UUID, enabled bool) ([]db.RuntimeProfile, error) {
	var out []db.RuntimeProfile
	err := s.withTx(ctx, func(q *db.Queries) error {
		if _, err := lockPool(ctx, q, id, nil); err != nil {
			return err
		}
		var err error
		out, err = q.SetPoolRuntimeProfilesEnabled(ctx, db.SetPoolRuntimeProfilesEnabledParams{PoolID: id, Enabled: enabled})
		return err
	})
	return out, err
}

// Delete removes a pool nobody spends: it has no environment left. Delete the
// environments first, as any runtime profile.
func (s *PoolService) Delete(ctx context.Context, id pgtype.UUID) error {
	return s.withTx(ctx, func(q *db.Queries) error {
		if _, err := lockPool(ctx, q, id, nil); err != nil {
			return err
		}
		profiles, err := q.ListPoolRuntimeProfiles(ctx, id)
		if err != nil {
			return err
		}
		if len(profiles) > 0 {
			return fmt.Errorf("%w: %d", ErrPoolInUse, len(profiles))
		}
		if err := q.DeleteSubscriptionPoolMembers(ctx, id); err != nil {
			return err
		}
		grants, err := q.ListSubscriptionPoolGrantsByPool(ctx, id)
		if err != nil {
			return err
		}
		for _, g := range grants {
			if err := q.DeleteSubscriptionPoolGrant(ctx, db.DeleteSubscriptionPoolGrantParams{PoolID: id, WorkspaceID: g.WorkspaceID}); err != nil {
				return err
			}
		}
		return q.DeleteSubscriptionPool(ctx, id)
	})
}

// RemoveAccount takes a deleted account out of every pool and raises their
// revision, in the caller's transaction. It returns the pools that changed.
func RemoveAccount(ctx context.Context, q *db.Queries, subscriptionID pgtype.UUID) ([]pgtype.UUID, error) {
	pools, err := q.DeleteSubscriptionFromPools(ctx, subscriptionID)
	if err != nil {
		return nil, err
	}
	for _, id := range pools {
		pool, err := q.LockSubscriptionPool(ctx, id)
		if err != nil {
			return nil, err
		}
		members, err := q.ListSubscriptionPoolMembersByPools(ctx, []pgtype.UUID{id})
		if err != nil {
			return nil, err
		}
		// Close the gap the account left, keeping the order of the rest.
		if err := q.DeleteSubscriptionPoolMembers(ctx, id); err != nil {
			return nil, err
		}
		for i, m := range members {
			if err := q.InsertSubscriptionPoolMember(ctx, db.InsertSubscriptionPoolMemberParams{PoolID: id, SubscriptionID: m.SubscriptionID, Position: int32(i + 1)}); err != nil {
				return nil, err
			}
		}
		if _, err := q.UpdateSubscriptionPool(ctx, db.UpdateSubscriptionPoolParams{
			ID: id, Name: pool.Name, Description: pool.Description, SpendOrder: pool.SpendOrder, AllWorkspaces: pool.AllWorkspaces, RevisionStep: 1,
		}); err != nil {
			return nil, err
		}
	}
	return pools, nil
}

// EnsureAllWorkspacePools gives a new workspace the environment of every pool
// that is granted to all workspaces. A name already taken is skipped.
func EnsureAllWorkspacePools(ctx context.Context, q *db.Queries, workspaceID, actor pgtype.UUID) error {
	pools, err := q.ListAllWorkspacePools(ctx)
	if err != nil {
		return err
	}
	for _, pool := range pools {
		if err := q.InsertSubscriptionPoolGrant(ctx, db.InsertSubscriptionPoolGrantParams{PoolID: pool.ID, WorkspaceID: workspaceID}); err != nil {
			return err
		}
		if _, _, err := ensureEnvironment(ctx, q, pool, workspaceID, actor); err != nil {
			return err
		}
	}
	return nil
}
