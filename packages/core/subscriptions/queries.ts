import { useCallback } from "react";
import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { ApiError, api } from "../api";
import { PoolRevisionConflictSchema } from "../api/schemas";
import { runtimeProfileKeys } from "../runtimes/profiles";
import type {
  CreatePoolInput,
  CreateSubscriptionInput,
  SetPoolGrantsInput,
  StartSubscriptionLoginInput,
  SubscriptionLoginSession,
  UpdatePoolInput,
  UpdateSubscriptionInput,
} from "../types/subscription";

/** Polling period of a sign-in that waits for the person or the daemon. */
export const SUBSCRIPTION_LOGIN_POLL_MS = 2_000;

// Subscriptions belong to the instance, not to a workspace: their keys are
// account-scoped. A pool's subscription list is read through one workspace's
// copy of the profile, so it lives under that workspace's profile keys.
export const subscriptionKeys = {
  all: () => ["subscriptions"] as const,
  list: () => [...subscriptionKeys.all(), "list"] as const,
  discrepancies: () => [...subscriptionKeys.all(), "discrepancies"] as const,
  pools: () => [...subscriptionKeys.all(), "pools"] as const,
  manualRuns: (key: string) => [...subscriptionKeys.all(), "manual-runs", key] as const,
  login: (key: string) => [...subscriptionKeys.all(), "login", key] as const,
  profile: (wsId: string, profileId: string) => [...runtimeProfileKeys.all(wsId), "subscriptions", profileId] as const,
};

export function subscriptionListOptions() {
  return queryOptions({
    queryKey: subscriptionKeys.list(),
    queryFn: () => api.listSubscriptions(),
    refetchInterval: 60_000,
  });
}

/** Pools that reach the reader's workspaces, with their accounts and verdicts; the one source of a pool's composition. */
export function poolListOptions() {
  return queryOptions({
    queryKey: subscriptionKeys.pools(),
    queryFn: () => api.listPools(),
    refetchInterval: 60_000,
  });
}

export function subscriptionDiscrepanciesOptions() {
  return queryOptions({
    queryKey: subscriptionKeys.discrepancies(),
    queryFn: () => api.listSubscriptionDiscrepancies(),
    refetchInterval: 60_000,
    retry: false,
  });
}

/** The current sign-in of a subscription; polls while it waits for the person or the daemon. */
export function subscriptionLoginOptions(key: string, enabled = true) {
  return queryOptions({
    queryKey: subscriptionKeys.login(key),
    queryFn: () => api.getSubscriptionLogin(key),
    enabled: enabled && key.length > 0,
    refetchInterval: (query) => (query.state.data?.state === "waiting" ? SUBSCRIPTION_LOGIN_POLL_MS : false),
  });
}

export const MANUAL_RUNS_PAGE = 20;

/** The journal of commands run by hand under an account; only an instance admin may read it, so a refusal is not retried. */
export function manualRunsOptions(key: string, before?: string) {
  return queryOptions({
    queryKey: [...subscriptionKeys.manualRuns(key), before ?? "first"] as const,
    queryFn: () => api.listSubscriptionManualRuns(key, { limit: MANUAL_RUNS_PAGE, before }),
    enabled: key.length > 0,
    retry: false,
  });
}

export function runtimeProfileSubscriptionsOptions(wsId: string, profileId: string) {
  return queryOptions({
    queryKey: subscriptionKeys.profile(wsId, profileId),
    queryFn: () => api.getRuntimeProfileSubscriptions(wsId, profileId),
    enabled: wsId.length > 0 && profileId.length > 0,
  });
}

// An account, limit or pool edit changes every screen that shows the same
// verdict: account cards and pools, the environments that spend them, and the
// forecast of every agent, issue and profile. Running tasks are not touched
// server-side, so their stored facts stay as they were.
function invalidateSubscriptions(client: ReturnType<typeof useQueryClient>) {
  return Promise.all([
    client.invalidateQueries({ queryKey: subscriptionKeys.all() }),
    client.invalidateQueries({ queryKey: ["runtime-profiles"] }),
    client.invalidateQueries({ queryKey: ["model-profiles"] }),
  ]);
}

export function useCreateSubscription() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateSubscriptionInput) => api.createSubscription(input),
    onSettled: () => invalidateSubscriptions(client),
  });
}

export function useUpdateSubscription() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: ({ key, patch }: { key: string; patch: UpdateSubscriptionInput }) => api.updateSubscription(key, patch),
    onSettled: () => invalidateSubscriptions(client),
  });
}

export function useDeleteSubscription() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (key: string) => api.deleteSubscription(key),
    onSettled: () => invalidateSubscriptions(client),
  });
}

export function useCreatePool() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: CreatePoolInput) => api.createPool(input),
    onSettled: () => invalidateSubscriptions(client),
  });
}

/** Account order, spend order, name and reach of a pool; the same mutation from the subscriptions page and from an environment. */
export function useUpdatePool() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: ({ id, input }: { id: string; input: UpdatePoolInput }) => api.updatePool(id, input),
    onSettled: () => invalidateSubscriptions(client),
  });
}

export function useSetPoolGrants() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: ({ id, input }: { id: string; input: SetPoolGrantsInput }) => api.setPoolGrants(id, input),
    onSettled: () => invalidateSubscriptions(client),
  });
}

export function useDeletePool() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.deletePool(id),
    onSettled: () => invalidateSubscriptions(client),
  });
}

/** The pool's current revision when `error` is a stale-revision refusal of a pool edit; null for any other error. */
export function poolRevisionConflict(error: unknown): number | null {
  if (!(error instanceof ApiError) || error.status !== 409) return null;
  const parsed = PoolRevisionConflictSchema.safeParse(error.body);
  return parsed.success ? parsed.data.current_revision : null;
}

/**
 * Starts a sign-in and seeds the polled login query with its session. Not a
 * `useMutation` on purpose: the mutation cache would keep the token as the
 * mutation's variables after the request is gone.
 */
export function useStartSubscriptionLogin() {
  const client = useQueryClient();
  return useCallback(
    async (key: string, input: StartSubscriptionLoginInput): Promise<SubscriptionLoginSession> => {
      const session = await api.startSubscriptionLogin(key, input);
      client.setQueryData(subscriptionKeys.login(key), session);
      void client.invalidateQueries({ queryKey: subscriptionKeys.list() });
      return session;
    },
    [client],
  );
}

export function useCancelSubscriptionLogin() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (key: string) => api.cancelSubscriptionLogin(key),
    onSettled: (_data, _error, key) =>
      Promise.all([
        client.invalidateQueries({ queryKey: subscriptionKeys.login(key) }),
        client.invalidateQueries({ queryKey: subscriptionKeys.list() }),
      ]),
  });
}
