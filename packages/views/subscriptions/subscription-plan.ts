/**
 * Pure rules of the Subscriptions page: what a new subscription is called on
 * the server, which runtimes can spend it, and how runtime rows change when
 * one is added, moved or removed. The page only sends the resulting rows to
 * the control panel (`subscriptions/tables`).
 */

export type SubscriptionKind = "claude" | "codex" | "api";

export interface SubscriptionRow {
  id: string;
  provider: string;
  title: string;
  auth?: string | null;
  credential?: string | null;
  windows?: string[];
}

export interface RuntimeRow {
  name: string;
  command: string;
  subscriptions: string[];
  order: string;
}

/** Key providers the wizard offers; `windows` are the quota windows the dashboard reads for them. */
export const API_PROVIDERS = [
  { provider: "zai", base: "zai", windows: ["5h", "week"] },
  { provider: "minimax", base: "minimax", windows: ["5h", "week"] },
  { provider: "openrouter", base: "openrouter", windows: [] as string[] },
  { provider: "loodsen-ai", base: "locia", windows: [] as string[] },
] as const;

export type ApiProvider = (typeof API_PROVIDERS)[number]["provider"];

export interface NewSubscription {
  id: string;
  row: {
    provider: string;
    title: string;
    quota_row: string;
    windows: string[];
    auth: string;
    credential: string;
    cli: string;
  };
}

/** The smallest `<base>-<n>` (n ≥ 2) no row uses yet. */
export function nextSubscriptionId(base: string, taken: Iterable<string>): string {
  const used = new Set(taken);
  for (let n = 2; ; n += 1) {
    const id = `${base}-${n}`;
    if (!used.has(id)) return id;
  }
}

/** Table row for a new account: the credential is a file path on the server, never the secret. */
export function planSubscription(
  kind: SubscriptionKind,
  apiProvider: ApiProvider,
  title: string,
  taken: Iterable<string>,
  knownWindows: Iterable<string>,
): NewSubscription {
  const known = new Set(knownWindows);
  const name = title.trim();
  if (kind === "claude") {
    const id = nextSubscriptionId("claude", taken);
    return {
      id,
      row: {
        provider: "claude",
        title: name,
        quota_row: name,
        windows: ["5h", "week"].filter((w) => known.has(w)),
        auth: "claude_oauth",
        credential: `~agents/.config/${id}/token`,
        cli: "",
      },
    };
  }
  if (kind === "codex") {
    const id = nextSubscriptionId("codex", taken);
    return {
      id,
      row: {
        provider: "codex",
        title: name,
        quota_row: name,
        windows: ["5h", "week"].filter((w) => known.has(w)),
        auth: "codex_auth",
        // subs-login logs Codex in with CODEX_HOME = the credential's folder.
        credential: `~agents/.${id}/auth.json`,
        cli: "",
      },
    };
  }
  const spec = API_PROVIDERS.find((p) => p.provider === apiProvider) ?? API_PROVIDERS[0];
  const id = nextSubscriptionId(spec.base, taken);
  return {
    id,
    row: {
      provider: spec.provider,
      title: name,
      quota_row: name,
      windows: spec.windows.filter((w) => known.has(w)),
      auth: "api_key",
      credential: `~agents/.config/${id}/key`,
      cli: "",
    },
  };
}

/** Provider a new subscription of this kind belongs to. */
export function providerOf(kind: SubscriptionKind, apiProvider: ApiProvider): string {
  return kind === "api" ? apiProvider : kind;
}

/**
 * Runtimes that can spend a subscription of `provider`: those whose current
 * subscriptions are of that provider. A runtime with no subscriptions yet is
 * offered to every provider.
 */
export function compatibleRuntimes(
  runtimes: RuntimeRow[],
  subscriptions: SubscriptionRow[],
  provider: string,
): RuntimeRow[] {
  const providerOfId = new Map(subscriptions.map((s) => [s.id, s.provider]));
  return runtimes.filter(
    (runtime) =>
      runtime.subscriptions.length === 0 ||
      runtime.subscriptions.some((id) => providerOfId.get(id) === provider),
  );
}

export type Placement = "last" | "first";

/** The runtime row with `id` added at `placement`; a subscription already there keeps its place. */
export function withSubscription(runtime: RuntimeRow, id: string, placement: Placement): RuntimeRow {
  if (runtime.subscriptions.includes(id)) return runtime;
  const subscriptions =
    placement === "first" ? [id, ...runtime.subscriptions] : [...runtime.subscriptions, id];
  return { ...runtime, subscriptions };
}

/** Runtime rows that change when `id` is removed from all of them (the panel refuses to delete a subscription a runtime still spends). */
export function runtimesWithout(runtimes: RuntimeRow[], id: string): RuntimeRow[] {
  return runtimes
    .filter((runtime) => runtime.subscriptions.includes(id))
    .map((runtime) => ({ ...runtime, subscriptions: runtime.subscriptions.filter((s) => s !== id) }));
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

/** Card group of a subscription on the page. */
export function groupOf(provider: string): "claude" | "codex" | "api" {
  if (provider === "claude") return "claude";
  if (provider === "codex") return "codex";
  return "api";
}
