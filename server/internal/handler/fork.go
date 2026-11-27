package handler

import (
	"net/http"

	"github.com/multica-ai/multica/server/internal/integrations/express"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ForkConfig is the fork's part of Config.
type ForkConfig struct {
	// InstanceAdmins are the emails (MULTICA_INSTANCE_ADMINS, comma separated)
	// allowed to change instance-level subscriptions and their runtime pools.
	InstanceAdmins []string
}

// ForkState is the fork's part of Handler.
type ForkState struct {
	// SubscriptionLogins holds pending subscription sign-ins in memory only.
	SubscriptionLogins *SubscriptionLoginStore
	SubscriptionProbes *SubscriptionProbeStore

	// ExpressInstall owns the eXpress (BotX) bot install lifecycle and the
	// at-rest encryption of each bot's secret key. ExpressBindingTokens backs
	// the "link your eXpress account" prompt; ExpressWebhook is the public
	// endpoint CTS calls. All nil unless MULTICA_EXPRESS_SECRET_KEY is set.
	ExpressInstall       *express.InstallService
	ExpressBindingTokens *express.BindingTokenService
	ExpressWebhook       *express.Webhook
}

func newForkState() ForkState {
	return ForkState{
		SubscriptionLogins: NewSubscriptionLoginStore(),
		SubscriptionProbes: NewSubscriptionProbeStore(),
	}
}

// loadAgentForView is loadAgentForUser for read-only views: it also returns the
// workspace's hidden agent of the removed acceptance step (kind = 'system',
// system_key = 'accept', STT-429), so an archived run it carried shows its name
// instead of "Unknown Agent". Mutations keep using
// loadAgentForUser, which still answers 404 for every system agent.
func (h *Handler) loadAgentForView(w http.ResponseWriter, r *http.Request, agentID string) (db.Agent, bool) {
	if agentUUID, err := util.ParseUUID(agentID); err == nil {
		if wsUUID, err := util.ParseUUID(h.resolveWorkspaceID(r)); err == nil {
			if a, err := h.Queries.GetAgent(r.Context(), agentUUID); err == nil && a.WorkspaceID == wsUUID &&
				a.Kind == "system" && a.SystemKey.Valid && a.SystemKey.String == "accept" && !a.ArchivedAt.Valid {
				if _, ok := requireUserID(w, r); !ok {
					return db.Agent{}, false
				}
				return a, true
			}
		}
	}
	return h.loadAgentForUser(w, r, agentID)
}
