import type { RuntimeProfile } from "./agent";

// ---------------------------------------------------------------------------
// Instance subscriptions: provider accounts (a Claude or Codex plan, an API
// key) shared by the whole instance. Runtime profiles list the subscriptions
// they spend, in order. Secrets never pass through these shapes: a
// subscription only knows where its credential file lives on the daemon host.
// ---------------------------------------------------------------------------

/** The CLI a subscription runs on; every provider but codex speaks through Claude Code. */
export type SubscriptionFamily = "claude" | "codex";

/** Known sign-in kinds; a newer server may send others, kept as strings. */
export type SubscriptionAuth = "claude_login" | "claude_oauth" | "codex_auth" | "api_key";

export const SUBSCRIPTION_LOGIN_STATES = ["logged_in", "revoked", "never", "check_failed", "unchecked"] as const;
export type SubscriptionLoginState = (typeof SUBSCRIPTION_LOGIN_STATES)[number];

export const SUBSCRIPTION_CREDENTIAL_STATES = ["present", "missing", "revoked", "unreadable", "unknown"] as const;
export type SubscriptionCredentialState = (typeof SUBSCRIPTION_CREDENTIAL_STATES)[number];

export const SUBSCRIPTION_LOGIN_SESSION_STATES = ["waiting", "done", "failed", "cancelled", "expired"] as const;
export type SubscriptionLoginSessionState = (typeof SUBSCRIPTION_LOGIN_SESSION_STATES)[number];

export type SpendOrder = "first" | "balanced";

/** Latest snapshot of one quota window. */
export interface SubscriptionUsage {
  window: string;
  used_percent: number | null;
  resets_at: string | null;
  limit_reached: boolean;
  source: string;
  observed_at: string;
}

/** A runtime profile that spends the subscription, and the subscription's place in its order (1-based). */
export interface SubscriptionUser {
  workspace_id: string;
  runtime_profile_id: string;
  display_name: string;
  position: number;
}

/** What a daemon host reports about the credential file. */
export interface SubscriptionHost {
  daemon_id: string;
  device_name: string;
  credential_state: SubscriptionCredentialState;
  checked_at: string | null;
  error: string;
}

export interface SubscriptionLoginSession {
  id: string;
  state: SubscriptionLoginSessionState;
  /** Device-auth link (Codex). */
  url: string | null;
  /** Device-auth code (Codex). */
  code: string | null;
  message: string;
  started_at: string | null;
}

export interface Subscription {
  id: string;
  key: string;
  provider: string;
  family: SubscriptionFamily;
  title: string;
  owner_note: string;
  auth: string;
  /** Path of the credential file on the daemon host, never the credential. */
  credential_path: string;
  windows: string[];
  /** Agents' cap per window, %. */
  caps: Record<string, number>;
  /** Share of every window kept for people, %. */
  reserve: number;
  /** min(cap, 100 − reserve); windows without a limit are absent. */
  effective_caps: Record<string, number>;
  /** Subscriptions of one provider account share a key here and one load. */
  account_group: string;
  disabled_until: string | null;
  disabled: boolean;
  usage: SubscriptionUsage[];
  used_by: SubscriptionUser[];
  hosts: SubscriptionHost[];
  /** Null when the server does not report sign-in state (no daemon checks yet). */
  login_state: SubscriptionLoginState | null;
  login: SubscriptionLoginSession | null;
  created_at: string;
  updated_at: string;
}

export interface SubscriptionList {
  subscriptions: Subscription[];
  /** Whether the current user may change subscriptions and pools. */
  can_manage: boolean;
}

export interface CreateSubscriptionInput {
  provider: string;
  title?: string;
  owner_note?: string;
  windows?: string[];
  caps?: Record<string, number>;
  reserve?: number;
  account_group?: string;
}

export interface UpdateSubscriptionInput {
  title?: string;
  owner_note?: string;
  /** Merged per window on the server. */
  caps?: Record<string, number>;
  reserve?: number;
  /** RFC 3339 time; an empty string lifts the switch-off. */
  disabled_until?: string;
}

export interface DeleteSubscriptionResult {
  deleted: boolean;
  detached_runtime_profile_ids: string[];
}

export interface StartSubscriptionLoginInput {
  /** `claude setup-token` output or an API key; required for those sign-in kinds. */
  token?: string;
  runtime_id?: string;
}

export type SubscriptionDiscrepancyKind =
  | "credential_missing"
  | "unused_subscription"
  | "runtime_without_subscriptions"
  | "agent_on_disabled_runtime";

export interface SubscriptionDiscrepancy {
  /** One of SubscriptionDiscrepancyKind, or a newer kind kept as is. */
  kind: string;
  message: string;
  subscription_key: string | null;
  runtime_profile_id: string | null;
  runtime_name: string | null;
  workspace_id: string | null;
  agent_id: string | null;
  agent_name: string | null;
  device_name: string | null;
}

export interface RuntimeProfileSubscriptions {
  runtime_profile: RuntimeProfile;
  spend_order: string;
  subscriptions: Subscription[];
  /** Keys in the profile's order that no longer exist. */
  missing: string[];
}

export interface UpdateRuntimeProfileSubscriptionsInput {
  subscriptions?: string[];
  spend_order?: SpendOrder;
}

export interface CreateRuntimePoolInput {
  display_name: string;
  runtime_type: SubscriptionFamily;
  subscriptions: string[];
  spend_order: SpendOrder;
  workspace_ids?: string[];
  all_workspaces?: boolean;
  description?: string;
}

export interface CreateRuntimePoolResult {
  share_key: string;
  runtime_profiles: RuntimeProfile[];
  /** Workspaces that already had a runtime with this name. */
  skipped_workspace_ids: string[];
}
