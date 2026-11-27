package protocol

// DaemonHeartbeatAckFork is the fork's part of a heartbeat ack: subscription
// sign-ins and probes the server asks the daemon to run.
type DaemonHeartbeatAckFork struct {
	// PendingSubscriptionLogin asks the daemon to sign a subscription in and
	// write its credential file. Token is set only for pasted secrets.
	PendingSubscriptionLogin *DaemonHeartbeatPendingSubscriptionLogin `json:"pending_subscription_login,omitempty"`
	// PendingSubscriptionProbe asks the daemon to measure one subscription now
	// and report the snapshot tagged with the probe id.
	PendingSubscriptionProbe *DaemonHeartbeatPendingSubscriptionProbe `json:"pending_subscription_probe,omitempty"`
}

// DaemonHeartbeatPendingSubscriptionLogin describes a subscription sign-in for
// the daemon: a Codex device login, or a pasted Claude token / API key to write
// atomically to credential_path. The token is never logged by either side.
type DaemonHeartbeatPendingSubscriptionLogin struct {
	ID             string `json:"id"`
	Key            string `json:"key"`
	Provider       string `json:"provider"`
	Auth           string `json:"auth"`
	CredentialPath string `json:"credential_path"`
	Token          string `json:"token,omitempty"`
}

// DaemonHeartbeatPendingSubscriptionProbe is a forced measurement of one
// subscription in the workspace that asked for it.
type DaemonHeartbeatPendingSubscriptionProbe struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	WorkspaceID string `json:"workspace_id"`
}
