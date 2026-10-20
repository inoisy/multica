import { describe, expect, it } from "vitest";
import {
  compatibleRuntimes,
  moveItem,
  nextSubscriptionId,
  planSubscription,
  runtimesWithout,
  withSubscription,
  type RuntimeRow,
} from "./subscription-plan";

const subs = [
  { id: "claude-main", provider: "claude", title: "Claude main" },
  { id: "claude-second", provider: "claude", title: "Claude second" },
  { id: "codex-main", provider: "codex", title: "Codex main" },
  { id: "zai", provider: "zai", title: "z.ai" },
];
const runtimes: RuntimeRow[] = [
  { name: "Claude · 2", command: "claude-reserve", subscriptions: ["claude-second", "claude-main"], order: "first" },
  { name: "Codex · 2", command: "codex-reserve", subscriptions: ["codex-main"], order: "balanced" },
  { name: "z.ai GLM", command: "claude-zai-pro", subscriptions: ["zai"], order: "first" },
  { name: "Empty", command: "x", subscriptions: [], order: "first" },
];

describe("subscription plan", () => {
  it("names a new subscription with the first free number", () => {
    expect(nextSubscriptionId("claude", ["claude-main", "claude-2"])).toBe("claude-3");
    expect(nextSubscriptionId("zai", ["zai"])).toBe("zai-2");
  });

  it("plans a Claude account as a token file, never the secret", () => {
    const plan = planSubscription("claude", "zai", "  Claude, third  ", subs.map((s) => s.id), ["5h", "week", "fable"]);
    expect(plan).toEqual({
      id: "claude-2",
      row: {
        provider: "claude", title: "Claude, third", quota_row: "Claude, third", windows: ["5h", "week"],
        auth: "claude_oauth", credential: "~agents/.config/claude-2/token", cli: "",
      },
    });
  });

  it("keeps Codex in its own CODEX_HOME and a key under .config", () => {
    expect(planSubscription("codex", "zai", "C", [], ["5h", "week"]).row.credential).toBe("~agents/.codex-2/auth.json");
    const key = planSubscription("api", "openrouter", "OR", [], ["5h", "week"]);
    expect(key.id).toBe("openrouter-2");
    expect(key.row).toMatchObject({ provider: "openrouter", auth: "api_key", credential: "~agents/.config/openrouter-2/key", windows: [] });
    expect(planSubscription("api", "loodsen-ai", "L", [], []).id).toBe("locia-2");
  });

  it("drops windows the quota table does not know", () => {
    expect(planSubscription("api", "zai", "z", [], ["week"]).row.windows).toEqual(["week"]);
  });

  it("offers only runtimes of the same provider, plus empty ones", () => {
    expect(compatibleRuntimes(runtimes, subs, "claude").map((r) => r.name)).toEqual(["Claude · 2", "Empty"]);
    expect(compatibleRuntimes(runtimes, subs, "zai").map((r) => r.name)).toEqual(["z.ai GLM", "Empty"]);
  });

  it("puts a subscription first or last once", () => {
    const runtime = runtimes[0]!;
    expect(withSubscription(runtime, "claude-2", "last").subscriptions).toEqual(["claude-second", "claude-main", "claude-2"]);
    expect(withSubscription(runtime, "claude-2", "first").subscriptions).toEqual(["claude-2", "claude-second", "claude-main"]);
    expect(withSubscription(runtime, "claude-main", "first")).toBe(runtime);
  });

  it("takes a subscription out of every runtime that spends it", () => {
    expect(runtimesWithout(runtimes, "claude-main")).toEqual([
      { name: "Claude · 2", command: "claude-reserve", subscriptions: ["claude-second"], order: "first" },
    ]);
    expect(runtimesWithout(runtimes, "nobody")).toEqual([]);
  });

  it("moves an item within bounds", () => {
    expect(moveItem(["a", "b", "c"], 2, -1)).toEqual(["a", "c", "b"]);
    expect(moveItem(["a", "b"], 0, -1)).toEqual(["a", "b"]);
  });
});
