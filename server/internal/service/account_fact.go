package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// The accounts of a pool are chosen by the daemon, after the claim. The server
// hands the pool over with the claim and writes down what it handed over
// (AccountOffer); the daemon then says which account it took, and only a
// confirmation that fits the offer becomes the fact of the run (AccountFact).

// OfferedAccount is an account in an offer. It carries no credential path.
type OfferedAccount struct {
	SubscriptionID  string `json:"subscription_id"`
	SubscriptionKey string `json:"subscription_key"`
	AccountGroup    string `json:"account_group"`
}

// AccountOffer is what the server handed to the daemon at claim.
type AccountOffer struct {
	OfferID       string           `json:"offer_id"`
	DaemonID      string           `json:"daemon_id"`
	SpendOrder    string           `json:"spend_order"`
	PoolID        string           `json:"pool_id"`
	PoolRevision  int32            `json:"pool_revision"`
	Subscriptions []OfferedAccount `json:"subscriptions"`
}

// AccountFact is the account the daemon confirmed for a run. Everything but
// the choice itself is taken from the offer and from the authenticated daemon.
type AccountFact struct {
	SubscriptionID  string    `json:"subscription_id"`
	SubscriptionKey string    `json:"subscription_key"`
	AccountGroup    string    `json:"account_group"`
	PoolID          string    `json:"pool_id"`
	PoolRevision    int32     `json:"pool_revision"`
	DaemonID        string    `json:"daemon_id"`
	ConfirmedAt     time.Time `json:"confirmed_at"`
}

// AccountConfirmation is what the daemon sends.
type AccountConfirmation struct {
	OfferID        string `json:"offer_id"`
	SubscriptionID string `json:"subscription_id"`
	PoolID         string `json:"pool_id"`
	PoolRevision   int32  `json:"pool_revision"`
}

// Why a confirmation is refused. The handler turns them into status codes.
var (
	ErrAccountNoOffer      = errors.New("the run was handed no pool of accounts")
	ErrAccountStaleOffer   = errors.New("the confirmation belongs to an earlier assignment of the run")
	ErrAccountNotOffered   = errors.New("the account, pool or revision is not part of the offer")
	ErrAccountTaskState    = errors.New("the run is not dispatched or running")
	ErrAccountConflict     = errors.New("another account is already confirmed for the run")
	ErrAccountWrongRuntime = errors.New("the run is on another runtime")
)

// ParseAccountOffer reads a stored offer; ok is false when there is none.
func ParseAccountOffer(raw []byte) (AccountOffer, bool) {
	var o AccountOffer
	if len(raw) == 0 || json.Unmarshal(raw, &o) != nil || o.OfferID == "" {
		return AccountOffer{}, false
	}
	return o, true
}

// ParseAccountFact reads a stored fact; ok is false when there is none.
func ParseAccountFact(raw []byte) (AccountFact, bool) {
	var f AccountFact
	if len(raw) == 0 || json.Unmarshal(raw, &f) != nil || f.SubscriptionID == "" {
		return AccountFact{}, false
	}
	return f, true
}

// claimAccountOffer runs in the claim transaction. A run on an environment
// that spends a pool is handed the pool as it is now; the offer is stored with
// the assignment so what the daemon was given and what the server remembers
// cannot differ. A run on another kind of environment keeps no offer, and an
// offer or fact left by an earlier assignment is dropped either way.
func claimAccountOffer(ctx context.Context, q *db.Queries, task *db.AgentTaskQueue, runtimeID pgtype.UUID) error {
	offer, err := BuildAccountOffer(ctx, q, runtimeID)
	if err != nil {
		return err
	}
	if offer == nil && task.AccountOffer == nil && task.AccountFact == nil {
		return nil
	}
	var raw []byte
	if offer != nil {
		if raw, err = json.Marshal(offer); err != nil {
			return err
		}
	}
	if err := q.SetClaimedTaskAccountOffer(ctx, db.SetClaimedTaskAccountOfferParams{ID: task.ID, AccountOffer: raw}); err != nil {
		return err
	}
	task.AccountOffer, task.AccountFact = raw, nil
	return nil
}

// BuildAccountOffer is the pool an environment would be handed now, nil when it spends none.
func BuildAccountOffer(ctx context.Context, q *db.Queries, runtimeID pgtype.UUID) (*AccountOffer, error) {
	rt, err := q.GetAgentRuntime(ctx, runtimeID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !rt.ProfileID.Valid) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	profile, err := q.GetRuntimeProfile(ctx, rt.ProfileID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !profile.PoolID.Valid) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	pool, err := q.LockSubscriptionPoolShared(ctx, profile.PoolID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	members, err := q.ListSubscriptionPoolMembersByPools(ctx, []pgtype.UUID{pool.ID})
	if err != nil {
		return nil, err
	}
	ids := make([]pgtype.UUID, len(members))
	for i, m := range members {
		ids[i] = m.SubscriptionID
	}
	subs, err := q.ListSubscriptionsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := map[string]db.Subscription{}
	for _, s := range subs {
		byID[util.UUIDToString(s.ID)] = s
	}
	offer := &AccountOffer{OfferID: util.UUIDToString(dbid.NewV7()), DaemonID: rt.DaemonID.String, SpendOrder: pool.SpendOrder, PoolID: util.UUIDToString(pool.ID), PoolRevision: pool.Revision}
	for _, m := range members {
		s, ok := byID[util.UUIDToString(m.SubscriptionID)]
		if !ok {
			continue
		}
		offer.Subscriptions = append(offer.Subscriptions, OfferedAccount{SubscriptionID: util.UUIDToString(s.ID), SubscriptionKey: s.Key, AccountGroup: s.AccountGroup})
	}
	if len(offer.Subscriptions) == 0 {
		return nil, nil
	}
	return offer, nil
}

// ConfirmTaskAccount records the account the daemon took for a run.
// daemonID is the authenticated daemon of the run's runtime. The confirmation
// is checked against the offer stored at claim, never against the pool as it
// is now, so a pool edited after the claim does not break or rewrite a run.
//
// A repeat of the confirmed account is answered with the stored fact, even
// after the run ended; a first confirmation for a finished run is refused, and
// a different account never replaces the first.
func (s *TaskService) ConfirmTaskAccount(ctx context.Context, taskID, runtimeID pgtype.UUID, daemonID string, req AccountConfirmation, now time.Time) (AccountFact, error) {
	var fact AccountFact
	err := s.runInTx(ctx, func(q *db.Queries) error {
		task, err := q.LockAgentTaskForAccount(ctx, taskID)
		if err != nil {
			return err
		}
		if task.RuntimeID != runtimeID {
			return ErrAccountWrongRuntime
		}
		offer, ok := ParseAccountOffer(task.AccountOffer)
		if !ok {
			return ErrAccountNoOffer
		}
		if offer.DaemonID != daemonID {
			return ErrAccountWrongRuntime
		}
		if offer.OfferID != req.OfferID {
			return ErrAccountStaleOffer
		}
		var chosen *OfferedAccount
		for i := range offer.Subscriptions {
			if offer.Subscriptions[i].SubscriptionID == req.SubscriptionID {
				chosen = &offer.Subscriptions[i]
			}
		}
		if chosen == nil || offer.PoolID != req.PoolID || offer.PoolRevision != req.PoolRevision {
			return ErrAccountNotOffered
		}
		if stored, ok := ParseAccountFact(task.AccountFact); ok {
			if stored.DaemonID != daemonID {
				return ErrAccountWrongRuntime
			}
			if stored.SubscriptionID != req.SubscriptionID {
				return ErrAccountConflict
			}
			fact = stored
			return nil
		}
		if task.Status != "dispatched" && task.Status != "running" {
			return ErrAccountTaskState
		}
		fact = AccountFact{
			SubscriptionID: chosen.SubscriptionID, SubscriptionKey: chosen.SubscriptionKey, AccountGroup: chosen.AccountGroup,
			PoolID: offer.PoolID, PoolRevision: offer.PoolRevision, DaemonID: daemonID, ConfirmedAt: now.UTC(),
		}
		raw, err := json.Marshal(fact)
		if err != nil {
			return err
		}
		return q.SetTaskAccountFact(ctx, db.SetTaskAccountFactParams{ID: taskID, AccountFact: raw})
	})
	return fact, err
}
