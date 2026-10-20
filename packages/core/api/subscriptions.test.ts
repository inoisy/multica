// @vitest-environment node
import { afterEach, expect, it, vi } from "vitest";
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
    reserve: 0,
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
  expect(pool.runtime_profile).toMatchObject({ subscriptions: ["claude-3"], share_key: "pool-1", all_workspaces: false, spend_order: "first" });
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

it("reads the new pool and the workspaces it skipped", async () => {
  respond({ share_key: "pool-1", runtime_profiles: [] }, 201);
  await expect(
    client.createRuntimePool({ display_name: "Claude · подписки", runtime_type: "claude", subscriptions: [], spend_order: "first", all_workspaces: true }),
  ).resolves.toEqual({ share_key: "pool-1", runtime_profiles: [], skipped_workspace_ids: [] });
});

it("surfaces a refused write", async () => {
  respond({ error: "instance admin only" }, 403);
  await expect(client.updateSubscription("claude-3", { reserve: 10 })).rejects.toThrow();
});
