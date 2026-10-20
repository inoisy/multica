import type { RuntimeProfile } from "../types/agent";
import type { Subscription, SubscriptionFamily, SubscriptionUsage } from "../types/subscription";

/**
 * Pure rules of subscriptions and the runtime pools that spend them: which
 * profiles are pools, which subscriptions share one account card, and how a
 * pool's order changes when a subscription is added or moved.
 */

/** A profile is a subscription pool when it spends subscriptions or is shared across workspaces. */
export function isSubscriptionPool(profile: RuntimeProfile): boolean {
  return (profile.subscriptions?.length ?? 0) > 0 || Boolean(profile.share_key);
}

/** The CLI family a pool runs on; null for a profile of any other CLI. */
export function poolFamily(profile: RuntimeProfile): SubscriptionFamily | null {
  const type = profile.runtime_type || profile.protocol_family;
  return type === "claude" || type === "codex" ? type : null;
}

/** Pools of the workspace that can spend a subscription of `family`. */
export function poolsOfFamily(profiles: RuntimeProfile[], family: SubscriptionFamily): RuntimeProfile[] {
  return profiles.filter((profile) => isSubscriptionPool(profile) && poolFamily(profile) === family);
}

/** One card on the page: subscriptions of one provider account, which share one load. */
export interface SubscriptionAccount {
  id: string;
  subscriptions: Subscription[];
  /** Latest snapshot of every window across the account's subscriptions. */
  usage: SubscriptionUsage[];
}

/** Groups subscriptions by `account_group` (a subscription without one is its own account), keeping list order. */
export function groupByAccount(subscriptions: Subscription[]): SubscriptionAccount[] {
  const accounts: SubscriptionAccount[] = [];
  const byId = new Map<string, SubscriptionAccount>();
  for (const sub of subscriptions) {
    const id = sub.account_group || sub.key;
    let account = byId.get(id);
    if (!account) {
      account = { id, subscriptions: [], usage: [] };
      byId.set(id, account);
      accounts.push(account);
    }
    account.subscriptions.push(sub);
  }
  for (const account of accounts) account.usage = latestUsage(account.subscriptions);
  return accounts;
}

/** The newest snapshot of each window, in the order windows first appear. */
export function latestUsage(subscriptions: Subscription[]): SubscriptionUsage[] {
  const byWindow = new Map<string, SubscriptionUsage>();
  for (const sub of subscriptions) {
    for (const snapshot of sub.usage) {
      const current = byWindow.get(snapshot.window);
      if (!current || observedAt(snapshot) > observedAt(current)) byWindow.set(snapshot.window, snapshot);
    }
  }
  return [...byWindow.values()];
}

function observedAt(snapshot: SubscriptionUsage): number {
  const time = Date.parse(snapshot.observed_at);
  return Number.isNaN(time) ? 0 : time;
}

/** Whether agents may no longer spend this window: the provider says so or the load reached the agents' cap. */
export function windowBlocked(snapshot: SubscriptionUsage, cap: number | undefined): boolean {
  if (snapshot.limit_reached) return true;
  return cap !== undefined && snapshot.used_percent !== null && snapshot.used_percent >= cap;
}

export type Placement = "first" | "last";

/** The order with `key` added at `placement`; a key already there keeps its place. */
export function withSubscription(order: string[], key: string, placement: Placement): string[] {
  if (order.includes(key)) return order;
  return placement === "first" ? [key, ...order] : [...order, key];
}

/** The list with the item at `index` moved by `delta` (−1 up, +1 down); out of range leaves it as is. */
export function moveItem<T>(items: T[], index: number, delta: -1 | 1): T[] {
  const target = index + delta;
  if (index < 0 || index >= items.length || target < 0 || target >= items.length) return items;
  const next = [...items];
  const [item] = next.splice(index, 1);
  next.splice(target, 0, item as T);
  return next;
}
