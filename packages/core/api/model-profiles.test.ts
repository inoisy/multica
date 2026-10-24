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
