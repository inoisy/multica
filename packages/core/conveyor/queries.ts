import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { issueKeys } from "../issues/queries";

export const stageDispatchKeys = {
  all: (wsId: string) => ["stage-dispatch", wsId] as const,
  list: (wsId: string) => [...stageDispatchKeys.all(wsId), "list"] as const,
  issue: (wsId: string, issueId: string) => [...stageDispatchKeys.all(wsId), "issue", issueId] as const,
};

/** The workspace's dispatched epics for the conveyor board. */
export function stageDispatchListOptions(wsId: string) {
  return queryOptions({
    queryKey: stageDispatchKeys.list(wsId),
    queryFn: () => api.listStageDispatch(),
    enabled: wsId.length > 0,
    refetchInterval: 30_000,
  });
}

export function issueStageDispatchOptions(wsId: string, issueId: string) {
  return queryOptions({
    queryKey: stageDispatchKeys.issue(wsId, issueId),
    queryFn: () => api.getIssueStageDispatch(issueId),
    enabled: wsId.length > 0 && issueId.length > 0,
  });
}

// Turning dispatch on assigns sub-issues at once, so the epic's children and
// every dispatch read of the workspace refresh together.
export function useSetIssueStageDispatch(wsId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ issueId, squadId }: { issueId: string; squadId: string | null }) => api.setIssueStageDispatch(issueId, squadId),
    onSettled: (_data, _error, { issueId }) => {
      void qc.invalidateQueries({ queryKey: stageDispatchKeys.all(wsId) });
      void qc.invalidateQueries({ queryKey: issueKeys.children(wsId, issueId) });
    },
  });
}
