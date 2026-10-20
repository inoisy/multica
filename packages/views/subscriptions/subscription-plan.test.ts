// @vitest-environment node
import { describe, expect, it } from "vitest";
import { familyOf, groupOf, loginMethod, parsePercent, providerOf, windowsOf } from "./subscription-plan";

describe("subscription plan", () => {
  it("maps a wizard kind to provider, family and windows", () => {
    expect([providerOf("claude", "zai"), familyOf("claude"), windowsOf("claude", "zai")]).toEqual(["claude", "claude", ["5h", "week"]]);
    expect([providerOf("codex", "zai"), familyOf("codex")]).toEqual(["codex", "codex"]);
    expect([providerOf("api", "openrouter"), familyOf("api"), windowsOf("api", "openrouter")]).toEqual(["openrouter", "claude", []]);
  });

  it("signs in by device link, token or key, and not from the web otherwise", () => {
    expect(loginMethod("codex_auth")).toBe("device");
    expect(loginMethod("claude_oauth")).toBe("token");
    expect(loginMethod("api_key")).toBe("key");
    expect(loginMethod("claude_login")).toBeNull();
  });

  it("groups key subscriptions apart from plans of their family", () => {
    expect(groupOf({ auth: "api_key", family: "claude" })).toBe("api");
    expect(groupOf({ auth: "claude_login", family: "claude" })).toBe("claude");
    expect(groupOf({ auth: "codex_auth", family: "codex" })).toBe("codex");
  });

  it("accepts whole percents only", () => {
    expect(parsePercent(" 70 ")).toBe(70);
    expect(parsePercent("100")).toBe(100);
    expect(parsePercent("101")).toBeNull();
    expect(parsePercent("7.5")).toBeNull();
    expect(parsePercent("")).toBeNull();
  });
});
