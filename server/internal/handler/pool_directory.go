package handler

import (
	"context"
	"crypto/md5"
	"encoding/hex"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// poolDirectory is the pools, their accounts, grants and environments, read
// once for a response that mentions several of them.
type poolDirectory struct {
	pools    map[string]db.SubscriptionPool
	order    []string
	members  map[string][]db.SubscriptionPoolMember
	accounts map[string]db.Subscription
	grants   map[string][]db.SubscriptionPoolGrant
	envs     map[string][]db.RuntimeProfile
}

func (h *Handler) loadPoolDirectory(ctx context.Context) (*poolDirectory, error) {
	pools, err := h.Queries.ListSubscriptionPools(ctx)
	if err != nil {
		return nil, err
	}
	members, err := h.Queries.ListSubscriptionPoolMembers(ctx)
	if err != nil {
		return nil, err
	}
	grants, err := h.Queries.ListSubscriptionPoolGrants(ctx)
	if err != nil {
		return nil, err
	}
	envs, err := h.Queries.ListPoolRuntimeProfilesAll(ctx)
	if err != nil {
		return nil, err
	}
	accounts, err := h.Queries.ListSubscriptions(ctx)
	if err != nil {
		return nil, err
	}
	d := &poolDirectory{
		pools: map[string]db.SubscriptionPool{}, members: map[string][]db.SubscriptionPoolMember{}, accounts: map[string]db.Subscription{},
		grants: map[string][]db.SubscriptionPoolGrant{}, envs: map[string][]db.RuntimeProfile{},
	}
	for _, p := range pools {
		id := uuidToString(p.ID)
		d.pools[id] = p
		d.order = append(d.order, id)
	}
	for _, m := range members {
		d.members[uuidToString(m.PoolID)] = append(d.members[uuidToString(m.PoolID)], m)
	}
	for _, g := range grants {
		d.grants[uuidToString(g.PoolID)] = append(d.grants[uuidToString(g.PoolID)], g)
	}
	for _, e := range envs {
		d.envs[uuidToString(e.PoolID)] = append(d.envs[uuidToString(e.PoolID)], e)
	}
	for _, a := range accounts {
		d.accounts[uuidToString(a.ID)] = a
	}
	return d, nil
}

// keys are the account keys of a pool in spend order; a member whose account
// is gone is left out.
func (d *poolDirectory) keys(poolID string) []string {
	out := []string{}
	for _, m := range d.members[poolID] {
		if a, ok := d.accounts[uuidToString(m.SubscriptionID)]; ok {
			out = append(out, a.Key)
		}
	}
	return out
}

// legacyShareKey is the share_key older clients know a pool by: the key it was
// moved from, else a stable one made from its id when it reaches several
// workspaces, else none.
func legacyShareKey(p db.SubscriptionPool, grants int) *string {
	if p.LegacyShareKey.Valid && p.LegacyShareKey.String != "" {
		key := p.LegacyShareKey.String
		return &key
	}
	if p.AllWorkspaces || grants > 1 {
		sum := md5.Sum([]byte(uuidToString(p.ID)))
		key := "pool-" + hex.EncodeToString(sum[:])[:16]
		return &key
	}
	return nil
}

// compat fills the pool fields older clients read from a runtime profile.
func (d *poolDirectory) compat(resp *RuntimeProfileResponse, p db.RuntimeProfile) {
	resp.Subscriptions = []string{}
	if !p.PoolID.Valid {
		resp.SpendOrder = "first"
		return
	}
	id := uuidToString(p.PoolID)
	pool, ok := d.pools[id]
	if !ok {
		resp.SpendOrder = "first"
		return
	}
	resp.PoolID = &id
	resp.Subscriptions = d.keys(id)
	resp.SpendOrder = pool.SpendOrder
	resp.ShareKey = legacyShareKey(pool, len(d.grants[id]))
	resp.AllWorkspaces = pool.AllWorkspaces
}

// runtimeProfileResponse is a runtime profile with the pool fields of its pool.
func (h *Handler) runtimeProfileResponse(ctx context.Context, p db.RuntimeProfile) RuntimeProfileResponse {
	resp := runtimeProfileToResponse(p)
	if !p.PoolID.Valid {
		return resp
	}
	if d, err := h.loadPoolDirectory(ctx); err == nil {
		d.compat(&resp, p)
	}
	return resp
}

// runtimeProfileResponses is runtimeProfileResponse for a list, reading the
// pools once.
func (h *Handler) runtimeProfileResponses(ctx context.Context, profiles []db.RuntimeProfile) []RuntimeProfileResponse {
	out := make([]RuntimeProfileResponse, len(profiles))
	var d *poolDirectory
	for i, p := range profiles {
		out[i] = runtimeProfileToResponse(p)
		if !p.PoolID.Valid {
			continue
		}
		if d == nil {
			var err error
			if d, err = h.loadPoolDirectory(ctx); err != nil {
				continue
			}
		}
		d.compat(&out[i], p)
	}
	return out
}

// poolAccountIDs lists the account ids of a pool in spend order.
func (d *poolDirectory) accountIDs(poolID string) []pgtype.UUID {
	var out []pgtype.UUID
	for _, m := range d.members[poolID] {
		out = append(out, m.SubscriptionID)
	}
	return out
}
