// @vitest-environment node
import { afterEach, expect, it, vi } from "vitest";
import { ApiClient } from "./client";

afterEach(() => vi.unstubAllGlobals());
const client = new ApiClient("https://api.example.test");
const respond = (body: unknown, status = 200) =>
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(body), { status })));

const preview = { source: "agent_profile", reason: "primary", position: 1, rows: [] };

it("fills defaults of an older or partial profile list", async () => {
  respond([{ id: "p1", name: "Strong", options: [{ runtime_id: "rt", position: 1 }] }]);
  const [profile] = await client.listModelProfiles();
  expect(profile?.when_exhausted_profile_id).toBeNull();
  expect(profile?.agents).toEqual([]);
  expect(profile?.options[0]).toMatchObject({ model: "", thinking_level: "", custom_args: null, state: "available", chosen: false });
});

it("does not present a malformed profile list as no profiles", async () => {
  respond([{ id: 1, name: "Strong" }]);
  await expect(client.listModelProfiles()).rejects.toThrow("Could not load model profiles");
});

it("keeps unknown sources, reasons and row states from a newer server", async () => {
  respond({
    agent_id: "a1",
    preview: { ...preview, source: "squad_profile", reason: "budget", rows: [{ runtime_id: "rt", state: "paused" }] },
  });
  const models = await client.getAgentModels("a1");
  expect(models.preview.source).toBe("squad_profile");
  expect(models.preview.reason).toBe("budget");
  expect(models.preview.rows[0]?.state).toBe("paused");
  expect(models.journal).toEqual([]);
  expect(models.waiting).toEqual([]);
  expect(models.can_edit).toBe(false);
});

it("rejects agent models without a choice", async () => {
  respond({ agent_id: "a1" });
  await expect(client.getAgentModels("a1")).rejects.toThrow("Could not load the agent's models");
});

it("drops a malformed chosen row instead of rendering a half model", async () => {
  respond({ agent_id: "a1", preview: { ...preview, chosen: { model: 5 } } });
  const models = await client.getAgentModels("a1");
  expect(models.preview.chosen).toBeNull();
});

it("reads the issue profile and its absence", async () => {
  respond({ issue_id: "i1", model_profile_id: null });
  await expect(client.getIssueModelProfile("i1")).resolves.toEqual({ issue_id: "i1", model_profile_id: null, profile_name: "" });
});

it("surfaces a refused profile delete", async () => {
  respond({ error: "model profile is referenced" }, 409);
  await expect(client.deleteModelProfile("p1")).rejects.toThrow();
});

it("reads preview changes and tolerates an empty list", async () => {
  respond({ changes: [{ kind: "agent", ref: { id: "a1", name: "Dev" }, before: null, after: { runtime_id: "rt", position: 1 } }] });
  const changes = await client.previewModelProfile("p1", { name: "Strong", description: "", when_exhausted_profile_id: null, options: [] });
  expect(changes[0]?.after?.model).toBe("");
  respond({});
  await expect(client.previewModelProfile("p1", { name: "Strong", description: "", when_exhausted_profile_id: null, options: [] })).resolves.toEqual([]);
});

it("degrades a malformed unavailability list to empty", async () => {
  respond([{ runtime_id: 3 }]);
  await expect(client.listRuntimeUnavailable()).resolves.toEqual([]);
});

it("reads a personal profile's base, match and differing rows", async () => {
  respond([
    {
      id: "pp1", name: "Tester", personal: true, owner_agent: { id: "a1", name: "Tester" },
      based_on: { id: "p1", name: "Above average" }, matches_base: false, diff_rows: [1, 3], options: [],
    },
    { id: "p1", name: "Above average", personal_profiles: [{ profile_id: "pp1", agent: { id: "a1", name: "Tester" }, matches_base: false, diff_rows: [1] }] },
  ]);
  const [personal, shared] = await client.listModelProfiles("all");
  expect(personal).toMatchObject({ personal: true, matches_base: false, diff_rows: [1, 3], based_on: { id: "p1" }, owner_agent: { name: "Tester" } });
  expect(shared).toMatchObject({ personal: false, owner_agent: null, based_on: null, matches_base: null, diff_rows: [] });
  expect(shared?.personal_profiles[0]).toMatchObject({ profile_id: "pp1", matches_base: false, diff_rows: [1] });
});

it("asks for the profile kind and keeps the default to shared profiles", async () => {
  const fetch = vi.fn().mockImplementation(() => Promise.resolve(new Response("[]")));
  vi.stubGlobal("fetch", fetch);
  await client.listModelProfiles();
  await client.listModelProfiles("personal");
  expect((fetch.mock.calls[0] as [string])[0]).toBe("https://api.example.test/api/model-profiles");
  expect((fetch.mock.calls[1] as [string])[0]).toBe("https://api.example.test/api/model-profiles?kind=personal");
});

it("reads the agent's mode, base and differing rows, and falls back on an unknown mode", async () => {
  respond({
    agent_id: "a1", preview, mode: "personal", personal_profile_id: "pp1", base_profile_id: "p1", base_profile_name: "Above average",
    matches_base: true, diff_rows: [],
  });
  await expect(client.getAgentModels("a1")).resolves.toMatchObject({ mode: "personal", personal_profile_id: "pp1", matches_base: true, diff_rows: [] });
  respond({ agent_id: "a1", preview, mode: "squad" });
  await expect(client.getAgentModels("a1")).resolves.toMatchObject({ mode: "fields", personal_profile_id: null, matches_base: null });
});

it("keeps the reason codes of a waiting choice and the account of a run", async () => {
  const fact = { subscription_id: "s1", subscription_key: "claude-3", account_group: "g", pool_id: "pool1", pool_revision: 12, daemon_id: "d1", confirmed_at: "2026-10-24T15:31:00Z" };
  respond({
    agent_id: "a1",
    preview: { ...preview, reason: "all_unavailable", wait_reason_code: "agent_cap", wait_until_estimated: true, wait_until: "2026-10-24T17:40:00Z" },
    last: { task_id: "t1", account_fact: fact, choice: preview },
    journal: [{ task_id: "t0", choice: preview }],
    runtime_unavailable: [{ runtime_id: "rt", until: "2026-10-24T17:40:00Z", reason: "limit", reason_code: "provider_limit", until_estimated: true, computed: true }],
  });
  const models = await client.getAgentModels("a1");
  expect(models.preview).toMatchObject({ wait_reason_code: "agent_cap", wait_until_estimated: true });
  expect(models.last?.account_fact).toEqual(fact);
  expect(models.journal[0]?.account_fact).toBeNull();
  expect(models.runtime_unavailable[0]).toMatchObject({ reason_code: "provider_limit", until_estimated: true, computed: true });
});

it("sends the personal profile endpoints and reads the updated models", async () => {
  const fetch = vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify({ agent_id: "a1", preview, mode: "personal" }))));
  vi.stubGlobal("fetch", fetch);
  await client.createAgentPersonalProfile("a1", { from_profile_id: "p1" });
  await client.setAgentPersonalProfile("a1", { options: [{ runtime_id: "rt", model: "m", thinking_level: "high" }] });
  await client.setAgentSingleModel("a1", { runtime_id: "rt", model: "m" });
  await client.resetAgentPersonalProfile("a1");
  const calls = fetch.mock.calls as [string, RequestInit][];
  expect(calls.map(([url, init]) => `${init.method} ${url.replace("https://api.example.test", "")}`)).toEqual([
    "POST /api/agents/a1/models/personal",
    "PUT /api/agents/a1/models/personal",
    "PUT /api/agents/a1/models/single",
    "DELETE /api/agents/a1/models/personal",
  ]);
  expect(JSON.parse(String(calls[0]?.[1].body))).toEqual({ from_profile_id: "p1" });
  expect(calls[3]?.[1].body).toBeUndefined();
});

it("rejects a malformed answer to a personal profile write", async () => {
  respond({ agent_id: "a1" });
  await expect(client.resetAgentPersonalProfile("a1")).rejects.toThrow("Could not read the agent's models");
});

it("reads the confirmed account and the choice of a task, and unknown without them", async () => {
  respond([
    { id: "t1", model_choice: { ...preview, wait_reason_code: "login_required" }, account_fact: { subscription_id: "s1", subscription_key: "k" } },
    { id: "t2" },
    { id: "t3", account_fact: { subscription_key: 5 }, model_choice: "x" },
  ]);
  const tasks = await client.listTasksByIssue("i1");
  expect(tasks[0]?.account_fact).toMatchObject({ subscription_key: "k", pool_revision: 0 });
  expect(tasks[0]?.model_choice).toMatchObject({ wait_reason_code: "login_required" });
  expect(tasks[1]).toMatchObject({ model_choice: null, account_fact: null });
  expect(tasks[2]).toMatchObject({ model_choice: null, account_fact: null });
});
