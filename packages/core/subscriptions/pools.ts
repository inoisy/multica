import type { Pool, Subscription, SubscriptionFamily, SubscriptionUsage } from "../types/subscription";

/**
 * Pure rules of accounts and the pools that spend them: which accounts share
 * one provider account, what limit agents have per window, and how accounts
 * map to pools. Availability itself (can it run, until when) is the server's
 * verdict and is never recomputed here.
 */

/** Pools of the CLI family an account can join. */
export function poolsOfFamily(pools: Pool[], family: SubscriptionFamily): Pool[] {
  return pools.filter((pool) => pool.runtime_type === family);
}

/** The pool an environment spends; null for a plain CLI environment or a pool the reader cannot see. */
export function poolOfEnvironment(pools: Pool[], poolId: string | null | undefined): Pool | null {
  return poolId ? (pools.find((pool) => pool.id === poolId) ?? null) : null;
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

/** Highest and lowest limit agents may be given on a window; 100 means no limit. */
export const AGENT_LIMIT_MAX = 100;

/** The limit agents have on a window, whole %. A window without an entry has no limit (100). */
export function agentLimit(sub: Pick<Subscription, "caps">, window: string): number {
  return sub.caps[window] ?? AGENT_LIMIT_MAX;
}

/** The share of a window kept for people: always 100 − the agents' limit, never a separate setting. */
export function peopleShare(limit: number): number {
  return AGENT_LIMIT_MAX - limit;
}

/** Parses the agents' limit typed by a person: a whole number 0…100, else null. */
export function parseAgentLimit(value: string): number | null {
  const trimmed = value.trim();
  if (!/^\d{1,3}$/.test(trimmed)) return null;
  const number = Number(trimmed);
  return number <= AGENT_LIMIT_MAX ? number : null;
}

/** Whether agents may no longer spend this window: the provider says so or the load reached the agents' limit. */
export function windowBlocked(snapshot: SubscriptionUsage, cap: number | undefined): boolean {
  if (snapshot.limit_reached) return true;
  return cap !== undefined && snapshot.used_percent !== null && snapshot.used_percent >= cap;
}

/** The strictest limit of an account's subscriptions on a window; undefined when none has one. */
export function accountLimit(subscriptions: Subscription[], window: string): number | undefined {
  const limits = subscriptions.map((sub) => sub.caps[window]).filter((limit): limit is number => limit !== undefined);
  return limits.length ? Math.min(...limits) : undefined;
}

/** A pool and the accounts it holds, for the grouped overview. */
export interface PoolGroup {
  pool: Pool;
  /** Accounts in the pool's order; an account listed first here is "primary", repeats elsewhere are references. */
  accounts: Subscription[];
}

/** Everything the grouped overview shows: pools with their accounts, and the accounts that are in none. */
export interface PoolOverview {
  groups: PoolGroup[];
  /** Accounts of no pool: still visible, still counted. */
  unpooled: Subscription[];
  /** Each account once, however many pools hold it. */
  totalAccounts: number;
  /** Keys of accounts that sit in more than one pool. */
  sharedKeys: Set<string>;
}

/**
 * Groups accounts by the pools that hold them. An account in several pools is
 * counted once in `totalAccounts`; it is listed under every pool that holds it
 * and `sharedKeys` lets the view mark the repeats as references to the same account.
 */
export function overviewByPool(pools: Pool[], subscriptions: Subscription[]): PoolOverview {
  const byKey = new Map(subscriptions.map((sub) => [sub.key, sub]));
  const seen = new Map<string, number>();
  const groups = pools.map((pool) => {
    const accounts = pool.subscriptions
      .map((entry) => byKey.get(entry.key))
      .filter((sub): sub is Subscription => sub !== undefined);
    for (const sub of accounts) seen.set(sub.key, (seen.get(sub.key) ?? 0) + 1);
    return { pool, accounts };
  });
  const unpooled = subscriptions.filter((sub) => !seen.has(sub.key));
  const sharedKeys = new Set([...seen].filter(([, count]) => count > 1).map(([key]) => key));
  return { groups, unpooled, totalAccounts: seen.size + unpooled.length, sharedKeys };
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
