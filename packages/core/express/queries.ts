import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

/** Query keys for eXpress installations. Realtime sync invalidates
 * `installations(wsId)` on `express_installation:*` events. */
export const expressKeys = {
  all: (wsId: string) => ["express", wsId] as const,
  installations: (wsId: string) => [...expressKeys.all(wsId), "installations"] as const,
};

export const expressInstallationsOptions = (wsId: string) =>
  queryOptions({
    queryKey: expressKeys.installations(wsId),
    queryFn: () => api.listExpressInstallations(wsId),
    enabled: !!wsId,
  });
