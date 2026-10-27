import type { Subscription, SubscriptionFamily } from "@multica/core/types";

/**
 * View rules of the Subscriptions page: what the wizard offers and how a
 * card is grouped. Keys, credential paths and default windows are the
 * server's; the wizard only names the provider.
 */

export type SubscriptionKind = "claude" | "codex" | "api";

/** Key providers the wizard offers; `windows` are the quota windows the provider has. */
export const API_PROVIDERS = [
  { provider: "zai", windows: ["5h", "week"] },
  { provider: "minimax", windows: ["5h", "week"] },
  { provider: "openrouter", windows: [] as string[] },
  { provider: "loodsen-ai", windows: [] as string[] },
] as const;

export type ApiProvider = (typeof API_PROVIDERS)[number]["provider"];

/** Windows of plan subscriptions (Claude, Codex). */
export const PLAN_WINDOWS = ["5h", "week"];

export function providerOf(kind: SubscriptionKind, apiProvider: ApiProvider): string {
  return kind === "api" ? apiProvider : kind;
}

export function familyOf(kind: SubscriptionKind): SubscriptionFamily {
  return kind === "codex" ? "codex" : "claude";
}

export function windowsOf(kind: SubscriptionKind, apiProvider: ApiProvider): string[] {
  if (kind !== "api") return PLAN_WINDOWS;
  return [...(API_PROVIDERS.find((p) => p.provider === apiProvider)?.windows ?? [])];
}

/** How the person signs in: device link (Codex), pasted token or key, or not from the web. */
export type LoginMethod = "device" | "token" | "key" | null;

export function loginMethod(auth: string): LoginMethod {
  if (auth === "codex_auth") return "device";
  if (auth === "claude_oauth") return "token";
  if (auth === "api_key") return "key";
  return null;
}

/** Card group on the page. */
export function groupOf(sub: Pick<Subscription, "auth" | "family">): "claude" | "codex" | "api" {
  if (sub.auth === "api_key") return "api";
  return sub.family;
}

/** Parses a whole percent 0–100; null when it is not one. */
export function parsePercent(value: string): number | null {
  const trimmed = value.trim();
  if (!/^\d{1,3}$/.test(trimmed)) return null;
  const number = Number(trimmed);
  return number <= 100 ? number : null;
}
