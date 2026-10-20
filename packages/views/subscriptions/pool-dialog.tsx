"use client";

import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { useUpdateRuntimeProfile } from "@multica/core/runtimes";
import {
  runtimeProfileSubscriptionsOptions,
  useUpdateRuntimeProfileSubscriptions,
} from "@multica/core/subscriptions";
import type { SpendOrder, Subscription } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../i18n";
import { SpendOrderField } from "./spend-order-field";
import { SubscriptionOrderList } from "./subscription-order-list";

function errorText(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

/**
 * One subscription pool (a runtime profile that spends subscriptions): its
 * name, subscriptions in order and how they are spent. A save reaches every
 * workspace's copy of the pool.
 */
export function PoolDialog({
  wsId,
  profileId,
  subscriptions,
  canManage,
  onOpenChange,
}: {
  wsId: string;
  profileId: string | null;
  subscriptions: Subscription[];
  canManage: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useT("agents");
  const query = useQuery(runtimeProfileSubscriptionsOptions(wsId, profileId ?? ""));
  const pool = profileId ? query.data : undefined;
  const updatePool = useUpdateRuntimeProfileSubscriptions(wsId);
  const updateProfile = useUpdateRuntimeProfile(wsId);
  const [loadedFor, setLoadedFor] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [order, setOrder] = useState<string[]>([]);
  const [mode, setMode] = useState<SpendOrder>("first");
  const busy = updatePool.isPending || updateProfile.isPending;

  // Edits start from the server's pool once per opened pool; a background refetch does not reset them.
  useEffect(() => {
    if (!profileId) {
      setLoadedFor(null);
      return;
    }
    if (!pool || loadedFor === profileId) return;
    setLoadedFor(profileId);
    setName(pool.runtime_profile.display_name);
    setOrder([...pool.subscriptions.map((s) => s.key), ...pool.missing]);
    setMode(pool.spend_order === "balanced" ? "balanced" : "first");
  }, [profileId, pool, loadedFor]);

  const family = pool?.runtime_profile.runtime_type === "codex" ? "codex" : "claude";
  const titleOf = (key: string) => subscriptions.find((s) => s.key === key)?.title ?? key;
  const missing = new Set(pool?.missing ?? []);
  const addable = subscriptions.filter((s) => s.family === family && !order.includes(s.key));

  const save = async () => {
    if (!profileId || !pool) return;
    try {
      const trimmed = name.trim();
      if (trimmed && trimmed !== pool.runtime_profile.display_name) {
        await updateProfile.mutateAsync({ profileId, patch: { display_name: trimmed } });
      }
      await updatePool.mutateAsync({ profileId, input: { subscriptions: order, spend_order: mode } });
      toast.success(t(($) => $.subscriptions.saved));
      onOpenChange(false);
    } catch (error) {
      toast.error(errorText(error));
    }
  };

  return (
    <Dialog open={profileId !== null} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{pool?.runtime_profile.display_name ?? t(($) => $.subscriptions.pool.loading)}</DialogTitle>
          {pool ? (
            <DialogDescription>
              {pool.agents.length
                ? t(($) => $.subscriptions.pool.agents, { names: pool.agents.map((a) => a.name || a.id).join(", ") })
                : t(($) => $.subscriptions.pool.no_agents)}
            </DialogDescription>
          ) : null}
        </DialogHeader>

        {query.error ? (
          <p role="alert" className="text-caption text-destructive">
            {errorText(query.error)}
          </p>
        ) : null}

        {pool ? (
          <>
            {canManage ? (
              <label className="flex flex-col gap-1 text-caption">
                <span className="text-muted-foreground">{t(($) => $.subscriptions.pool.name)}</span>
                <Input value={name} disabled={busy} onChange={(event) => setName(event.target.value)} />
                {pool.runtime_profile.share_key ? (
                  <span className="text-muted-foreground">{t(($) => $.subscriptions.pool.shared_hint)}</span>
                ) : null}
              </label>
            ) : null}

            <SubscriptionOrderList
              order={order}
              onChange={setOrder}
              titleOf={titleOf}
              missing={missing}
              addable={addable}
              editable={canManage}
              disabled={busy}
            />

            <SpendOrderField name="pool-spend-order" value={mode} onChange={setMode} disabled={!canManage || busy} />
          </>
        ) : null}

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {canManage ? t(($) => $.subscriptions.cancel) : t(($) => $.subscriptions.close)}
          </Button>
          {canManage ? (
            <Button disabled={busy || !pool || !name.trim()} onClick={() => void save()}>
              {t(($) => $.subscriptions.save)}
            </Button>
          ) : null}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
