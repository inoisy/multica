// @vitest-environment node
import { afterEach, expect, it, vi } from "vitest";
import { poolRevisionConflict } from "../subscriptions";
import { ApiClient } from "./client";

afterEach(() => vi.unstubAllGlobals());
const client = new ApiClient("https://api.example.test");
const respond = (body: unknown, status = 200) => {
  const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify(body), { status }));
  vi.stubGlobal("fetch", fetch);
  return fetch;
};

const minimal = { id: "s1", key: "claude-3", provider: "zai" };

it("fills defaults of a stage A subscription without hosts or sign-in state", async () => {
  respond({ subscriptions: [minimal] });
  const list = await client.listSubscriptions();
  expect(list.can_manage).toBe(false);
  expect(list.subscriptions[0]).toMatchObject({
    family: "claude",
    windows: [],
    caps: {},
    effective_caps: {},
    account_group: "",
    disabled: false,
    disabled_until: null,
    usage: [],
    used_by: [],
    hosts: [],
    login_state: null,
    login: null,
  });
});

it("keeps a newer server's states readable", async () => {
  respond({
    can_manage: true,
    subscriptions: [
      {
        ...minimal,
        provider: "codex",
        family: "something-new",
        login_state: "locked",
        hosts: [{ daemon_id: "d1", device_name: "vps", credential_state: "quarantined" }],
        login: { id: "l1", state: "paused", url: "https://auth.test", code: "AB-12" },
        usage: [{ window: "5h", used_percent: "41", observed_at: "2026-10-20T10:00:00Z" }],
      },
    ],
  });
  const [sub] = (await client.listSubscriptions()).subscriptions;
  expect(sub?.family).toBe("codex");
  expect(sub?.login_state).toBe("unchecked");
  expect(sub?.hosts[0]?.credential_state).toBe("unknown");
  expect(sub?.login).toMatchObject({ state: "failed", url: "https://auth.test", code: "AB-12", message: "" });
  expect(sub?.usage[0]).toMatchObject({ used_percent: null, limit_reached: false, resets_at: null });
});

it("does not present a malformed list as no subscriptions", async () => {
  respond({ subscriptions: [{ key: 5 }] });
  await expect(client.listSubscriptions()).rejects.toThrow("Could not load subscriptions");
  respond([]);
  await expect(client.listSubscriptions()).rejects.toThrow("Could not load subscriptions");
});

it("sends the token only in the sign-in request body", async () => {
  const fetch = respond({ login: { id: "l1", state: "waiting" } }, 202);
  const session = await client.startSubscriptionLogin("claude-3", { token: "sk-ant-oat01-x" });
  expect(session).toMatchObject({ id: "l1", state: "waiting", url: null, code: null });
  const [url, init] = fetch.mock.calls[0] as [string, RequestInit];
  expect(url).toBe("https://api.example.test/api/subscriptions/claude-3/login");
  expect(init.method).toBe("POST");
  expect(JSON.parse(String(init.body))).toEqual({ token: "sk-ant-oat01-x" });
});

it("reads no sign-in as null and rejects a malformed one", async () => {
  respond({ login: null });
  await expect(client.getSubscriptionLogin("codex-3")).resolves.toBeNull();
  respond({ login: "waiting" });
  await expect(client.getSubscriptionLogin("codex-3")).rejects.toThrow("Could not read the sign-in state");
});

it("reads discrepancies and keeps unknown kinds", async () => {
  respond({ items: [{ kind: "credential_missing", device_name: "vps" }, { kind: "brand_new", message: "x" }] });
  const items = await client.listSubscriptionDiscrepancies();
  expect(items[0]).toMatchObject({ kind: "credential_missing", message: "", device_name: "vps", subscription_key: null });
  expect(items[1]?.kind).toBe("brand_new");
  respond({ items: "nope" });
  await expect(client.listSubscriptionDiscrepancies()).rejects.toThrow("Could not load discrepancies");
});

it("reads a pool's subscriptions with the profile's pool fields", async () => {
  respond({
    runtime_profile: {
      id: "p1", workspace_id: "w1", display_name: "Claude · подписки", protocol_family: "claude",
      command_name: "subs-run", description: null, fixed_args: ["claude"], visibility: "workspace", created_by: null,
      enabled: true, subscriptions: ["claude-3"], share_key: "pool-1",
    },
    subscriptions: [minimal],
  });
  const pool = await client.getRuntimeProfileSubscriptions("w1", "p1");
  expect(pool.spend_order).toBe("first");
  expect(pool.missing).toEqual([]);
  expect(pool.agents).toEqual([]);
  expect(pool.pool_id).toBeNull();
  expect(pool.runtime_profile).toMatchObject({ subscriptions: ["claude-3"], share_key: "pool-1", all_workspaces: false, spend_order: "first", pool_id: null });
  respond({ runtime_profile: null, subscriptions: [] });
  await expect(client.getRuntimeProfileSubscriptions("w1", "p1")).rejects.toThrow("Could not load the runtime's subscriptions");
});

it("defaults pool fields of an older runtime profile", async () => {
  respond({
    runtime_profiles: [
      { id: "p1", workspace_id: "w1", display_name: "Pi", protocol_family: "pi", command_name: "pi", description: null, fixed_args: [], visibility: "workspace", created_by: null, enabled: true },
    ],
  });
  const [profile] = await client.listRuntimeProfiles("w1");
  expect(profile).toMatchObject({ subscriptions: [], spend_order: "first", share_key: null, all_workspaces: false });
});

const account = {
  id: "s1", key: "claude-3", title: "Claude 3", provider: "claude", account_group: "g1", position: 1,
  blocked: true, usable: false, reason_code: "agent_cap", until: "2026-10-24T17:40:00Z", until_estimated: true,
};
const poolBody = {
  id: "pool1", name: "Claude", runtime_type: "claude", description: "", spend_order: "balanced", all_workspaces: false,
  revision: 12, subscriptions: [account], workspace_ids: ["w1"],
  environments: [{ runtime_profile_id: "p1", workspace_id: "w1", display_name: "Claude · подписки", enabled: true }],
  availability: { blocked: true, usable: false, reason_code: "agent_cap", until: "2026-10-24T17:40:00Z", until_estimated: true },
  created_at: "2026-10-24T10:00:00Z", updated_at: "2026-10-24T11:00:00Z",
};

it("reads pools with their ordered accounts, revision and one availability time", async () => {
  respond({ pools: [poolBody], can_manage: true });
  const list = await client.listPools();
  expect(list.can_manage).toBe(true);
  expect(list.pools[0]).toMatchObject({ revision: 12, spend_order: "balanced", runtime_type: "claude" });
  expect(list.pools[0]?.subscriptions[0]).toMatchObject({ key: "claude-3", reason_code: "agent_cap", until_estimated: true });
  expect(list.pools[0]?.availability).toMatchObject({ blocked: true, until: "2026-10-24T17:40:00Z", until_estimated: true });
});

it("falls back safely on an unknown pool order, family and reason code", async () => {
  respond({
    pools: [{
      id: "pool1", name: "New", runtime_type: "gemini", spend_order: "most_used",
      subscriptions: [{ id: "s1", key: "k", reason_code: "future_reason", blocked: true }],
    }],
  });
  const [pool] = (await client.listPools()).pools;
  expect(pool).toMatchObject({ runtime_type: "claude", spend_order: "first", revision: 0, workspace_ids: [], environments: [] });
  expect(pool?.availability).toEqual({ blocked: false, usable: false, reason_code: null, until: null, until_estimated: false });
  expect(pool?.subscriptions[0]).toMatchObject({ reason_code: "future_reason", blocked: true, usable: false, until: null });
});

it("does not present a malformed pool list as no pools", async () => {
  respond({ pools: [{ name: 5 }] });
  await expect(client.listPools()).rejects.toThrow("Could not load pools");
  respond([]);
  await expect(client.listPools()).rejects.toThrow("Could not load pools");
});

it("sends the read revision with a pool edit and returns the updated pool", async () => {
  const fetch = respond({ ...poolBody, revision: 13 });
  const pool = await client.updatePool("pool1", { revision: 12, subscriptions: ["claude-3"], spend_order: "first" });
  expect(pool.revision).toBe(13);
  const [url, init] = fetch.mock.calls[0] as [string, RequestInit];
  expect(url).toBe("https://api.example.test/api/pools/pool1");
  expect(init.method).toBe("PATCH");
  expect(JSON.parse(String(init.body))).toEqual({ revision: 12, subscriptions: ["claude-3"], spend_order: "first" });
});

it("reads the current revision from a stale pool edit", async () => {
  respond({ error: "pool changed", code: "pool_revision_conflict", current_revision: 14 }, 409);
  const error = await client.updatePool("pool1", { revision: 12 }).catch((e: unknown) => e);
  expect(poolRevisionConflict(error)).toBe(14);
  respond({ error: "pool is in use" }, 409);
  expect(poolRevisionConflict(await client.deletePool("pool1").catch((e: unknown) => e))).toBeNull();
  expect(poolRevisionConflict(new Error("x"))).toBeNull();
});

it("defaults the pool of a runtime profile", async () => {
  respond({
    runtime_profiles: [
      { id: "p1", workspace_id: "w1", display_name: "Pi", protocol_family: "pi", command_name: "pi", description: null, fixed_args: [], visibility: "workspace", created_by: null, enabled: true },
      { id: "p2", workspace_id: "w1", display_name: "Pool", protocol_family: "claude", command_name: "x", description: null, fixed_args: [], visibility: "workspace", created_by: null, enabled: true, pool_id: "pool1" },
    ],
  });
  const profiles = await client.listRuntimeProfiles("w1");
  expect(profiles.map((p) => p.pool_id)).toEqual([null, "pool1"]);
});

it("surfaces a refused write", async () => {
  respond({ error: "instance admin only" }, 403);
  await expect(client.updateSubscription("claude-3", { caps: { "5h": 10 } })).rejects.toThrow();
});

it("reads a pool's agents and drops malformed ones", async () => {
  const runtime_profile = {
    id: "p1", workspace_id: "w1", display_name: "Pool", protocol_family: "claude", command_name: "claude",
    description: null, fixed_args: [], visibility: "workspace", created_by: null, enabled: true,
  };
  respond({ runtime_profile, subscriptions: [], agents: [{ id: "a1", name: "Writer" }, { id: 5 }, "x", { id: "a2" }] });
  const pool = await client.getRuntimeProfileSubscriptions("w1", "p1");
  expect(pool.agents).toEqual([{ id: "a1", name: "Writer" }, { id: "a2", name: "" }]);
  respond({ runtime_profile, subscriptions: [], agents: "nope" });
  await expect(client.getRuntimeProfileSubscriptions("w1", "p1")).resolves.toMatchObject({ agents: [] });
});
