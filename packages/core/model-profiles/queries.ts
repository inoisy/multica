import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import type { CreateModelProfileInput, ModelProfileInput, UpdateAgentModelsInput } from "../types";

export const modelProfileKeys = {
  all: (wsId: string) => ["model-profiles", wsId] as const,
  list: (wsId: string) => [...modelProfileKeys.all(wsId), "list"] as const,
  agent: (wsId: string, agentId: string) => [...modelProfileKeys.all(wsId), "agent", agentId] as const,
  issue: (wsId: string, issueId: string) => [...modelProfileKeys.all(wsId), "issue", issueId] as const,
};

export function modelProfileListOptions(wsId: string) {
  return queryOptions({
    queryKey: modelProfileKeys.list(wsId),
    queryFn: () => api.listModelProfiles(),
    enabled: wsId.length > 0,
    staleTime: 15_000,
  });
}

/** The agent's list and what the server would choose for its next run; row states move with limits, so it refreshes. */
export function agentModelsOptions(wsId: string, agentId: string) {
  return queryOptions({
    queryKey: modelProfileKeys.agent(wsId, agentId),
    queryFn: () => api.getAgentModels(agentId),
    enabled: wsId.length > 0 && agentId.length > 0,
    refetchInterval: 30_000,
  });
}

export function issueModelProfileOptions(wsId: string, issueId: string) {
  return queryOptions({
    queryKey: modelProfileKeys.issue(wsId, issueId),
    queryFn: () => api.getIssueModelProfile(issueId),
    enabled: wsId.length > 0 && issueId.length > 0,
  });
}

// A profile edit changes the choice of every agent and issue on it, so every
// model query of the workspace is invalidated together.
function invalidateModels(client: ReturnType<typeof useQueryClient>, wsId: string) {
  return client.invalidateQueries({ queryKey: modelProfileKeys.all(wsId) });
}

export function useCreateModelProfile(wsId: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateModelProfileInput) => api.createModelProfile(input),
    onSettled: () => invalidateModels(client, wsId),
  });
}

export function useUpdateModelProfile(wsId: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: ({ id, input }: { id: string; input: ModelProfileInput }) => api.updateModelProfile(id, input),
    onSettled: () => invalidateModels(client, wsId),
  });
}

export function useDeleteModelProfile(wsId: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.deleteModelProfile(id),
    onSettled: () => invalidateModels(client, wsId),
  });
}

export function useUpdateAgentModels(wsId: string, agentId: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: UpdateAgentModelsInput) => api.updateAgentModels(agentId, input),
    onSuccess: (data) => client.setQueryData(modelProfileKeys.agent(wsId, agentId), data),
    onSettled: () => invalidateModels(client, wsId),
  });
}

export function useSetIssueModelProfile(wsId: string, issueId: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (profileId: string | null) => api.setIssueModelProfile(issueId, profileId),
    onSuccess: (data) => client.setQueryData(modelProfileKeys.issue(wsId, issueId), data),
    onSettled: () => invalidateModels(client, wsId),
  });
}

/** Whose model would change if the edit were saved; nothing is written. */
export function usePreviewModelProfile() {
  return useMutation({
    mutationFn: ({ id, input }: { id: string; input: ModelProfileInput }) => api.previewModelProfile(id, input),
  });
}
