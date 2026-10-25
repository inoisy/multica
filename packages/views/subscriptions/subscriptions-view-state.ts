import { useCallback } from "react";
import { useNavigation } from "../navigation";
import type { LaneHorizon } from "./availability-lane";
import { LANE_HORIZONS } from "./availability-lane";

export const STATUS_FILTERS = ["all", "available", "unavailable"] as const;
export type StatusFilter = (typeof STATUS_FILTERS)[number];
export const FAMILY_FILTERS = ["all", "claude", "codex"] as const;
export type FamilyFilter = (typeof FAMILY_FILTERS)[number];

export interface SubscriptionsView {
  status: StatusFilter;
  family: FamilyFilter;
  query: string;
  /** Account whose card is open. */
  account: string;
  /** Pool whose editor is open. */
  pool: string;
  horizon: LaneHorizon;
}

function pick<T extends string>(allowed: readonly T[], value: string | null, fallback: T): T {
  return allowed.find((candidate) => candidate === value) ?? fallback;
}

/**
 * Filters, search, scale and the opened account or pool live in the address, so
 * a link and a reload restore the same view. A filter change replaces the entry
 * (back does not step through filters); an empty or unknown value is no filter.
 */
export function useSubscriptionsView(): [SubscriptionsView, (patch: Partial<Record<keyof SubscriptionsView, string | null>>) => void] {
  const navigation = useNavigation();
  const { searchParams, pathname, replace } = navigation;
  const range = Number(searchParams.get("range"));
  const view: SubscriptionsView = {
    status: pick(STATUS_FILTERS, searchParams.get("status"), "all"),
    family: pick(FAMILY_FILTERS, searchParams.get("family"), "all"),
    query: searchParams.get("q") ?? "",
    account: searchParams.get("account") ?? "",
    pool: searchParams.get("pool") ?? "",
    horizon: LANE_HORIZONS.find((h) => h === range) ?? 48,
  };
  const set = useCallback(
    (patch: Partial<Record<keyof SubscriptionsView, string | null>>) => {
      const next = new URLSearchParams(searchParams);
      const keys: Record<keyof SubscriptionsView, string> = {
        status: "status", family: "family", query: "q", account: "account", pool: "pool", horizon: "range",
      };
      for (const [name, value] of Object.entries(patch) as [keyof SubscriptionsView, string | null | undefined][]) {
        const key = keys[name];
        const isDefault = value === null || value === undefined || value === "" || value === "all" || (name === "horizon" && value === "48");
        if (isDefault) next.delete(key);
        else next.set(key, value);
      }
      const query = next.toString();
      replace(query ? `${pathname}?${query}` : pathname);
    },
    [searchParams, pathname, replace],
  );
  return [view, set];
}
