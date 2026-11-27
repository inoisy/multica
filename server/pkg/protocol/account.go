package protocol

import "time"

// Account refusal codes are shared by daemon reports and server availability.
const (
	AccountRefusalAgentCap      = "agent_cap_refusal"
	AccountRefusalDisabled      = "disabled_refusal"
	AccountRefusalProviderLimit = "provider_limit"
)

func IsAccountRefusalCode(code string) bool {
	return code == AccountRefusalAgentCap || code == AccountRefusalDisabled || code == AccountRefusalProviderLimit
}

// AccountFact records the account confirmed by the daemon from the saved offer.
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

// TaskFailFork is the fork's part of a failure report: the account refusal
// that stopped the run and, for a subscription cap, when it frees up. Older
// daemons omit both.
type TaskFailFork struct {
	RefusalCode  string     `json:"refusal_code,omitempty"`
	QuotaResetAt *time.Time `json:"quota_reset_at,omitempty"`
}

// QuotaReset is the reported reset, or zero when none was reported.
func (f TaskFailFork) QuotaReset() time.Time {
	if f.QuotaResetAt == nil {
		return time.Time{}
	}
	return *f.QuotaResetAt
}
