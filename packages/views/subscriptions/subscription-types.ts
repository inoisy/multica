/** Shapes the control panel answers `GET subscriptions` with. */

export interface UsageWindow {
  key: string | null;
  label: string;
  used: number | null;
  cap: number | null;
  reset: string | null;
  blocked: boolean;
}

export interface Subscription {
  id: string;
  /** Ключи одного аккаунта (claude-main и claude-max) — карточка одна, загрузка общая. */
  keys?: string[];
  provider: string;
  title: string;
  quota_row: string | null;
  windows: string[];
  caps: Record<string, number>;
  reserve: number;
  effective: Record<string, number>;
  credential: string | null;
  auth: string | null;
  cli: string | null;
  changed_at: string | null;
  changed_by: string | null;
  usage: UsageWindow[];
  error: string | null;
  /** Когда дашборд квот получил эти цифры (у Claude опрос реже, цифры бывают старше `updated`). */
  as_of?: string | null;
  available: boolean | null;
  /** Runtime, за которыми стоит подписка (таблица runtime). */
  runtimes: string[];
  /** Агенты workspace, у которых такой runtime в цепочке; active — работает на нём сейчас. */
  agents: { id: string; name: string; active: boolean }[];
}

export interface RuntimeTableRow {
  name: string;
  command?: string | null;
  subscriptions?: string[] | null;
  order?: string | null;
}

export interface SubscriptionsState {
  /** Строки таблицы «Подписки» как есть. */
  subscriptions: Subscription[];
  /** Карточки: по одной на аккаунт. */
  accounts?: Subscription[];
  stale: boolean;
  /** Когда дашборд квот последний раз записал данные. */
  updated?: string | null;
  can_edit: boolean;
  events: { at: string; kind: string; text: string }[];
  window_names: Record<string, string>;
  runtimes: RuntimeTableRow[];
  windows: { key: string; title?: string }[];
  auths: string[];
  orders: string[];
}

/** One table write: `row: null` deletes the row. Resolves false when the panel refused it (already toasted). */
export type SaveRow = (
  table: "items" | "runtimes",
  key: string,
  row: Record<string, unknown> | null,
  options?: { quiet?: boolean },
) => Promise<boolean>;
