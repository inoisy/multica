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
