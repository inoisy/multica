"use client";

import { useQuery } from "@tanstack/react-query";
import { poolListOptions, subscriptionListOptions } from "@multica/core/subscriptions";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useT } from "../i18n";
import { PoolEditor } from "./pool-editor";

/**
 * The accounts of the pool an environment spends, in the same editor the
 * subscriptions page uses. Renders nothing for an environment without a pool.
 */
export function PoolAccountsSection({ poolId }: { poolId: string | null | undefined }) {
  const { t } = useT("agents");
  const pools = useQuery({ ...poolListOptions(), enabled: Boolean(poolId) });
  const subscriptions = useQuery({ ...subscriptionListOptions(), enabled: Boolean(poolId) });
  if (!poolId) return null;
  const pool = pools.data?.pools.find((candidate) => candidate.id === poolId);
  return (
    <section className="space-y-3 rounded-lg border p-4" aria-labelledby="runtime-pool-title" data-testid="runtime-pool">
      <h2 id="runtime-pool-title" className="text-body font-medium">
        {pool ? t(($) => $.subscriptions.pool.of_environment, { name: pool.name }) : t(($) => $.subscriptions.pool.loading)}
      </h2>
      {pools.isLoading ? <Skeleton className="h-40 rounded-lg" /> : null}
      {pools.isError ? (
        <p role="alert" className="text-caption text-destructive">
          {pools.error instanceof Error ? pools.error.message : String(pools.error)}
        </p>
      ) : null}
      {pool ? (
        <PoolEditor
          key={pool.id}
          pool={pool}
          subscriptions={subscriptions.data?.subscriptions ?? []}
          canManage={pools.data?.can_manage === true}
        />
      ) : null}
      {pools.data && !pool ? <p className="text-caption text-muted-foreground">{t(($) => $.subscriptions.pool.not_found)}</p> : null}
    </section>
  );
}
