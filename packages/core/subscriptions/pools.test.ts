// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { RuntimeProfile } from "../types/agent";
import type { Subscription } from "../types/subscription";
import { groupByAccount, isSubscriptionPool, moveItem, poolsOfFamily, windowBlocked, withSubscription } from "./pools";

function sub(key: string, extra: Partial<Subscription> = {}): Subscription {
  return {
    id: key, key, provider: "claude", family: "claude", title: key, owner_note: "", auth: "claude_oauth",
    credential_path: "", windows: ["5h"], caps: {}, reserve: 0, effective_caps: {}, account_group: "",
    disabled_until: null, disabled: false, usage: [], used_by: [], hosts: [], login_state: null, login: null,
    created_at: "", updated_at: "", ...extra,
  };
}

function profile(id: string, extra: Partial<RuntimeProfile> = {}): RuntimeProfile {
  return {
    id, workspace_id: "w1", display_name: id, protocol_family: "claude", runtime_type: "claude", command_name: "x",
    description: null, fixed_args: [], visibility: "workspace", created_by: null, enabled: true, created_at: "",
    updated_at: "", subscriptions: [], share_key: null, ...extra,
  };
}

describe("pools", () => {
  it("treats profiles with subscriptions or a share key as pools of their family", () => {
    const profiles = [
      profile("plain"),
      profile("shared", { share_key: "pool-1" }),
      profile("filled", { subscriptions: ["a"] }),
      profile("codex", { runtime_type: "codex", protocol_family: "codex", subscriptions: ["b"] }),
    ];
    expect(isSubscriptionPool(profiles[0]!)).toBe(false);
    expect(poolsOfFamily(profiles, "claude").map((p) => p.id)).toEqual(["shared", "filled"]);
    expect(poolsOfFamily(profiles, "codex").map((p) => p.id)).toEqual(["codex"]);
  });

  it("groups one account's subscriptions and keeps the newest window snapshot", () => {
    const accounts = groupByAccount([
      sub("a", { account_group: "acc", usage: [{ window: "5h", used_percent: 10, resets_at: null, limit_reached: false, source: "daemon", observed_at: "2026-10-20T10:00:00Z" }] }),
      sub("b"),
      sub("c", { account_group: "acc", usage: [{ window: "5h", used_percent: 30, resets_at: null, limit_reached: false, source: "daemon", observed_at: "2026-10-20T11:00:00Z" }] }),
    ]);
    expect(accounts.map((a) => [a.id, a.subscriptions.map((s) => s.key)])).toEqual([["acc", ["a", "c"]], ["b", ["b"]]]);
    expect(accounts[0]?.usage.map((u) => u.used_percent)).toEqual([30]);
  });

  it("blocks a window at the provider's limit or the agents' cap", () => {
    const snapshot = { window: "5h", used_percent: 70, resets_at: null, limit_reached: false, source: "", observed_at: "" };
    expect(windowBlocked(snapshot, undefined)).toBe(false);
    expect(windowBlocked(snapshot, 70)).toBe(true);
    expect(windowBlocked({ ...snapshot, limit_reached: true }, undefined)).toBe(true);
    expect(windowBlocked({ ...snapshot, used_percent: null }, 10)).toBe(false);
  });

  it("places a new key first or last and keeps an existing one", () => {
    expect(withSubscription(["a", "b"], "c", "first")).toEqual(["c", "a", "b"]);
    expect(withSubscription(["a", "b"], "c", "last")).toEqual(["a", "b", "c"]);
    expect(withSubscription(["a", "b"], "b", "first")).toEqual(["a", "b"]);
  });

  it("moves within bounds only", () => {
    expect(moveItem(["a", "b", "c"], 1, -1)).toEqual(["b", "a", "c"]);
    expect(moveItem(["a", "b"], 0, -1)).toEqual(["a", "b"]);
  });
});
