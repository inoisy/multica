import type { Pool, PoolAccount, RuntimeProfile, Subscription, SubscriptionList } from "@multica/core/types";

/** Shared fixtures of the accounts screens: accounts, pools and the lists the server returns. */
export function sub(key: string, extra: Partial<Subscription> = {}): Subscription {
  return {
    id: `id-${key}`, key, provider: "claude", family: "claude", title: key, owner_note: "", auth: "claude_oauth",
    credential_path: `~/.config/${key}/token`, windows: ["5h", "week"], caps: {}, effective_caps: {},
    account_group: "", disabled_until: null, disabled: false, usage: [], used_by: [], hosts: [], login_state: "logged_in",
    login: null, created_at: "", updated_at: "", ...extra,
  };
}

export function entry(key: string, position: number, extra: Partial<PoolAccount> = {}): PoolAccount {
  return {
    id: `id-${key}`, key, title: key, provider: "claude", account_group: "", position, blocked: false, usable: true,
    reason_code: null, until: null, until_estimated: false, ...extra,
  };
}

export function pool(id: string, accounts: PoolAccount[], extra: Partial<Pool> = {}): Pool {
  return {
    id, name: id, runtime_type: "claude", description: "", spend_order: "first", all_workspaces: false, revision: 12,
    subscriptions: accounts, workspace_ids: ["ws-1"],
    environments: [{ runtime_profile_id: `env-${id}`, workspace_id: "ws-1", display_name: `${id} env`, enabled: true }],
    availability: { blocked: false, usable: true, reason_code: null, until: null, until_estimated: false },
    created_at: "", updated_at: "", ...extra,
  };
}

export const usedBy = (position: number) => ({ workspace_id: "ws-1", runtime_profile_id: "env-claude", display_name: "Claude env", position });
export const limitedVerdict = { blocked: true, usable: false, reason_code: "agent_cap" as const, until: "2026-10-24T17:40:00Z", until_estimated: false };

export function subscriptionList(canManage: boolean): SubscriptionList {
  return {
    can_manage: canManage,
    subscriptions: [
      sub("acc-a", {
        title: "Plan A", caps: { "5h": 70 }, effective_caps: { "5h": 70 }, used_by: [usedBy(1)],
        usage: [{ window: "5h", used_percent: 75, resets_at: null, limit_reached: false, source: "daemon", observed_at: new Date().toISOString() }],
      }),
      sub("acc-b", { title: "Plan B", used_by: [usedBy(2)] }),
      sub("cx-a", { title: "Codex plan", provider: "codex", family: "codex", auth: "codex_auth" }),
      sub("loose", { title: "Loose key", auth: "api_key", provider: "zai" }),
    ],
  };
}

export function poolList(canManage = true) {
  return {
    can_manage: canManage,
    pools: [
      pool("claude", [entry("acc-a", 1, limitedVerdict), entry("acc-b", 2)], { all_workspaces: true }),
      // acc-b is in a second pool as well: the same account, counted once.
      pool("video", [entry("acc-b", 1)], { revision: 3 }),
      pool("codex", [entry("cx-a", 1)], { runtime_type: "codex" }),
    ],
  };
}

export function profile(id: string, extra: Partial<RuntimeProfile> = {}): RuntimeProfile {
  return {
    id, workspace_id: "ws-1", display_name: id, protocol_family: "claude", runtime_type: "claude", command_name: "claude",
    description: null, fixed_args: [], visibility: "workspace", created_by: null, enabled: true, created_at: "", updated_at: "",
    subscriptions: [], spend_order: "first", share_key: null, all_workspaces: false, pool_id: null, ...extra,
  };
}
