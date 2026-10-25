"use client";

import { useQuery } from "@tanstack/react-query";
import { poolListOptions } from "@multica/core/subscriptions";
import type { Subscription } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { useT } from "../i18n";
import { PoolEditor } from "./pool-editor";

/** One pool in a dialog: the same editor an environment page shows. */
export function PoolDialog({
  poolId,
  subscriptions,
  canManage,
  onOpenChange,
}: {
  poolId: string | null;
  subscriptions: Subscription[];
  canManage: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useT("agents");
  const query = useQuery(poolListOptions());
  const pool = poolId ? query.data?.pools.find((candidate) => candidate.id === poolId) : undefined;
  return (
    <Dialog open={poolId !== null} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{pool?.name ?? t(($) => $.subscriptions.pool.loading)}</DialogTitle>
        </DialogHeader>
        {pool ? <PoolEditor key={pool.id} pool={pool} subscriptions={subscriptions} canManage={canManage} /> : null}
        {poolId && query.data && !pool ? (
          <p className="text-caption text-muted-foreground">{t(($) => $.subscriptions.pool.not_found)}</p>
        ) : null}
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t(($) => $.subscriptions.close)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
