import { useCallback } from "react";
import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { runtimeProfileKeys } from "../runtimes/profiles";
import type {
  CreateRuntimePoolInput,
  CreateSubscriptionInput,
  StartSubscriptionLoginInput,
  SubscriptionLoginSession,
  UpdateRuntimeProfileSubscriptionsInput,
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

export function runtimeProfileSubscriptionsOptions(wsId: string, profileId: string) {
  return queryOptions({
    queryKey: subscriptionKeys.profile(wsId, profileId),
    queryFn: () => api.getRuntimeProfileSubscriptions(wsId, profileId),
    enabled: wsId.length > 0 && profileId.length > 0,
  });
}

// A subscription or pool edit changes cards (used_by, order) and every
// workspace's copy of a shared profile, so both families are refreshed.
function invalidateSubscriptions(client: ReturnType<typeof useQueryClient>) {
  return Promise.all([
    client.invalidateQueries({ queryKey: subscriptionKeys.all() }),
    client.invalidateQueries({ queryKey: ["runtime-profiles"] }),
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

export function useCreateRuntimePool() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: (input: CreateRuntimePoolInput) => api.createRuntimePool(input),
    onSettled: () => invalidateSubscriptions(client),
  });
}

export function useUpdateRuntimeProfileSubscriptions(wsId: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: ({ profileId, input }: { profileId: string; input: UpdateRuntimeProfileSubscriptionsInput }) =>
      api.updateRuntimeProfileSubscriptions(wsId, profileId, input),
    onSettled: () => invalidateSubscriptions(client),
  });
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
