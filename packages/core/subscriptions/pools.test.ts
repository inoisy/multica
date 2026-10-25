// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { Pool, PoolAccount, Subscription } from "../types/subscription";
import {
  accountLimit,
  agentLimit,
  groupByAccount,
  moveItem,
  overviewByPool,
  parseAgentLimit,
  peopleShare,
  poolOfEnvironment,
  poolsOfFamily,
  windowBlocked,
  withSubscription,
} from "./pools";

function sub(key: string, extra: Partial<Subscription> = {}): Subscription {
  return {
    id: key, key, provider: "claude", family: "claude", title: key, owner_note: "", auth: "claude_oauth",
    credential_path: "", windows: ["5h"], caps: {}, effective_caps: {}, account_group: "",
    disabled_until: null, disabled: false, usage: [], used_by: [], hosts: [], login_state: null, login: null,
    created_at: "", updated_at: "", ...extra,
  };
}

function entry(key: string, position: number): PoolAccount {
  return {
    id: key, key, title: key, provider: "claude", account_group: "", position, blocked: false, usable: true,
    reason_code: null, until: null, until_estimated: false,
  };
}

function pool(id: string, keys: string[], extra: Partial<Pool> = {}): Pool {
  return {
    id, name: id, runtime_type: "claude", description: "", spend_order: "first", all_workspaces: false, revision: 1,
    subscriptions: keys.map((key, i) => entry(key, i + 1)), workspace_ids: [], environments: [],
    availability: { blocked: false, usable: true, reason_code: null, until: null, until_estimated: false },
    created_at: "", updated_at: "", ...extra,
  };
}

describe("pools", () => {
  it("offers an account only the pools of its family", () => {
    const pools = [pool("a", []), pool("b", [], { runtime_type: "codex" })];
    expect(poolsOfFamily(pools, "claude").map((p) => p.id)).toEqual(["a"]);
    expect(poolsOfFamily(pools, "codex").map((p) => p.id)).toEqual(["b"]);
  });

  it("finds the pool an environment spends, or none", () => {
    const pools = [pool("a", [])];
    expect(poolOfEnvironment(pools, "a")?.id).toBe("a");
    expect(poolOfEnvironment(pools, null)).toBeNull();
    expect(poolOfEnvironment(pools, "hidden")).toBeNull();
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

  it("blocks a window at the provider's limit or the agents' limit", () => {
    const snapshot = { window: "5h", used_percent: 70, resets_at: null, limit_reached: false, source: "", observed_at: "" };
    expect(windowBlocked(snapshot, undefined)).toBe(false);
    expect(windowBlocked(snapshot, 70)).toBe(true);
    expect(windowBlocked({ ...snapshot, limit_reached: true }, undefined)).toBe(true);
    expect(windowBlocked({ ...snapshot, used_percent: null }, 10)).toBe(false);
  });

  it("derives the people's share from the one agents' limit", () => {
    expect(agentLimit(sub("a", { caps: { "5h": 70 } }), "5h")).toBe(70);
    expect(agentLimit(sub("a"), "week")).toBe(100);
    expect(peopleShare(70)).toBe(30);
    expect(peopleShare(0)).toBe(100);
    expect(peopleShare(100)).toBe(0);
  });

  it("takes the strictest limit of an account's subscriptions", () => {
    expect(accountLimit([sub("a", { caps: { "5h": 80 } }), sub("b", { caps: { "5h": 60 } })], "5h")).toBe(60);
    expect(accountLimit([sub("a")], "5h")).toBeUndefined();
  });

  it("accepts a whole limit 0…100 and refuses anything else", () => {
    expect(parseAgentLimit(" 70 ")).toBe(70);
    expect(parseAgentLimit("0")).toBe(0);
    expect(parseAgentLimit("100")).toBe(100);
    expect(parseAgentLimit("101")).toBeNull();
    expect(parseAgentLimit("-1")).toBeNull();
    expect(parseAgentLimit("7.5")).toBeNull();
    expect(parseAgentLimit("")).toBeNull();
  });

  it("counts an account of several pools once and lists the ones without a pool", () => {
    const subs = [sub("a"), sub("b"), sub("c")];
    const overview = overviewByPool([pool("p1", ["a", "b"]), pool("p2", ["b"])], subs);
    expect(overview.groups.map((g) => g.accounts.map((s) => s.key))).toEqual([["a", "b"], ["b"]]);
    expect(overview.unpooled.map((s) => s.key)).toEqual(["c"]);
    expect(overview.totalAccounts).toBe(3);
    expect([...overview.sharedKeys]).toEqual(["b"]);
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
