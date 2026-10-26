import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

export const runtimeKeys = {
  all: (wsId: string) => ["runtimes", wsId] as const,
  list: (wsId: string) => [...runtimeKeys.all(wsId), "list"] as const,
  listMine: (wsId: string) => [...runtimeKeys.all(wsId), "list", "mine"] as const,
  modelUsage: (rid: string) => ["runtimes", "model-usage", rid] as const,
  runFacts: (rid: string) => ["runtimes", "run-facts", rid] as const,
  usage: (rid: string, days: number, tz: string) =>
    ["runtimes", "usage", rid, days, tz] as const,
  usageByAgent: (rid: string, days: number, tz: string) =>
    ["runtimes", "usage", "by-agent", rid, days, tz] as const,
  // by-hour now follows the viewer's tz, like the other reports.
  usageByHour: (rid: string, days: number, tz: string) =>
    ["runtimes", "usage", "by-hour", rid, days, tz] as const,
};

// `tz` is the viewer's IANA name — all reports follow the viewer's tz.
export function runtimeUsageOptions(
  runtimeId: string,
  days: number,
  tz: string,
) {
  return queryOptions({
    queryKey: runtimeKeys.usage(runtimeId, days, tz),
    queryFn: () => api.getRuntimeUsage(runtimeId, { days, tz }),
    staleTime: 60 * 1000,
  });
}

export function runtimeUsageByAgentOptions(
  runtimeId: string,
  days: number,
  tz: string,
) {
  return queryOptions({
    queryKey: runtimeKeys.usageByAgent(runtimeId, days, tz),
    queryFn: () => api.getRuntimeUsageByAgent(runtimeId, { days, tz }),
    staleTime: 60 * 1000,
  });
}

export function runtimeUsageByHourOptions(runtimeId: string, days: number, tz: string) {
  return queryOptions({
    queryKey: runtimeKeys.usageByHour(runtimeId, days, tz),
    queryFn: () => api.getRuntimeUsageByHour(runtimeId, { days, tz }),
    staleTime: 60 * 1000,
  });
}

/**
 * `wsSlug` targets a workspace other than the active one. The server resolves
 * the workspace from the slug header before the `workspace_id` param, so the
 * param alone cannot reach a workspace the app has not navigated to — which is
 * exactly the create-workspace flow's situation.
 */
export function runtimeListOptions(wsId: string, owner?: "me", wsSlug?: string) {
  return queryOptions({
    queryKey: owner === "me" ? runtimeKeys.listMine(wsId) : runtimeKeys.list(wsId),
    queryFn: () => api.listRuntimes({ workspace_id: wsId, owner }, wsSlug),
  });
}

/** Profiles, agents and open issues that lead to the environment; what the page shows under "where it is selected". */
export function runtimeModelUsageOptions(runtimeId: string) {
  return queryOptions({
    queryKey: runtimeKeys.modelUsage(runtimeId),
    queryFn: () => api.getRuntimeModelUsage(runtimeId),
    enabled: runtimeId.length > 0,
    staleTime: 30_000,
  });
}

/** Recorded runs of the last 24 hours, with the machine state kept apart from the selection state. */
export function runtimeRunFactsOptions(runtimeId: string) {
  return queryOptions({
    queryKey: runtimeKeys.runFacts(runtimeId),
    queryFn: () => api.getRuntimeRunFacts(runtimeId),
    enabled: runtimeId.length > 0,
    staleTime: 30_000,
  });
}
