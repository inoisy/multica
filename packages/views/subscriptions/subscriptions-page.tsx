"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, Plus, RefreshCw } from "lucide-react";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { runtimeProfileListOptions } from "@multica/core/runtimes";
import {
  groupByAccount,
  overviewByPool,
  poolListOptions,
  subscriptionDiscrepanciesOptions,
  subscriptionListOptions,
  type SubscriptionAccount,
} from "@multica/core/subscriptions";
import type { AvailabilityVerdict, Pool, Subscription } from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { cn } from "@multica/ui/lib/utils";
import { useNavigation } from "../navigation";
import { useT } from "../i18n";
import { useAvailabilityText } from "../model-profiles";
import { AccountRow } from "./account-row";
import { AddSubscriptionDialog } from "./add-subscription-dialog";
import { LANE_HORIZONS } from "./availability-lane";
import { Discrepancies } from "./discrepancies";
import { NewPoolDialog } from "./new-pool-dialog";
import { PoolDialog } from "./pool-dialog";
import { FAMILY_FILTERS, STATUS_FILTERS, useSubscriptionsView } from "./subscriptions-view-state";
import { VerdictBadge } from "./verdict-badge";

function errorText(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

/** The account is spent by agents now: the pool's verdict on it says usable and not blocked. */
function isAvailable(verdict: AvailabilityVerdict | null): boolean {
  return verdict !== null && verdict.usable && !verdict.blocked;
}

/**
 * «Подписки»: instance accounts on one screen. Accounts are grouped by the
 * pools that hold them; every account shows the server's verdict (state,
 * reason, the time it frees up), its windows and the one limit agents have on
 * each. An account in several pools is counted once; the other entries refer
 * to it. Everything comes from the Multica API; only an instance admin
 * (`can_manage`) changes anything.
 */
export function SubscriptionsPage() {
  const navigation = useNavigation();
  const paths = useWorkspacePaths();
  const target = `${paths.usage()}?tab=subscriptions`;
  const { replace } = navigation;
  useEffect(() => replace(target), [replace, target]);
  return null;
}

export function SubscriptionsContent() {
  const { t } = useT("agents");
  const availability = useAvailabilityText();
  const wsId = useWorkspaceId();
  const [view, setView] = useSubscriptionsView();
  const listQuery = useQuery(subscriptionListOptions());
  const poolsQuery = useQuery(poolListOptions());
  const profilesQuery = useQuery(runtimeProfileListOptions(wsId));
  const discrepanciesQuery = useQuery(subscriptionDiscrepanciesOptions());
  const [adding, setAdding] = useState(false);
  const [creatingPool, setCreatingPool] = useState(false);

  const subscriptions = useMemo(() => listQuery.data?.subscriptions ?? [], [listQuery.data]);
  const pools = useMemo(() => poolsQuery.data?.pools ?? [], [poolsQuery.data]);
  const profiles = useMemo(() => profilesQuery.data ?? [], [profilesQuery.data]);
  const canManage = listQuery.data?.can_manage === true;
  const overview = useMemo(() => overviewByPool(pools, subscriptions), [pools, subscriptions]);
  const accountOf = useMemo(() => {
    const byKey = new Map<string, SubscriptionAccount>();
    for (const account of groupByAccount(subscriptions)) for (const sub of account.subscriptions) byKey.set(sub.key, account);
    return byKey;
  }, [subscriptions]);
  const verdictOf = useMemo(() => {
    const byKey = new Map<string, AvailabilityVerdict>();
    for (const pool of pools) for (const entry of pool.subscriptions) if (!byKey.has(entry.key)) byKey.set(entry.key, entry);
    return byKey;
  }, [pools]);
  const poolsOf = (key: string) =>
    pools.flatMap((pool) => {
      const entry = pool.subscriptions.find((account) => account.key === key);
      return entry ? [{ pool, position: entry.position }] : [];
    });

  const windowNames: Record<string, string> = {
    "5h": t(($) => $.subscriptions.windows["5h"]),
    week: t(($) => $.subscriptions.windows.week),
  };
  const windowName = (key: string) => windowNames[key] ?? key;
  const fetching = listQuery.isFetching || poolsQuery.isFetching || profilesQuery.isFetching || discrepanciesQuery.isFetching;
  const refresh = () => {
    void listQuery.refetch();
    void poolsQuery.refetch();
    void profilesQuery.refetch();
    void discrepanciesQuery.refetch();
  };

  const available = subscriptions.filter((sub) => isAvailable(verdictOf.get(sub.key) ?? null));
  const blocked = subscriptions.filter((sub) => verdictOf.get(sub.key)?.blocked === true);
  const needle = view.query.trim().toLowerCase();
  const matches = (sub: Subscription): boolean => {
    if (view.family !== "all" && sub.family !== view.family) return false;
    const ok = isAvailable(verdictOf.get(sub.key) ?? null);
    if (view.status === "available" && !ok) return false;
    if (view.status === "unavailable" && ok) return false;
    if (!needle) return true;
    const names = poolsOf(sub.key).map(({ pool }) => pool.name);
    return [sub.title, sub.key, sub.provider, sub.account_group, ...names].some((value) => value.toLowerCase().includes(needle));
  };
  const filtering = view.status !== "all" || view.family !== "all" || needle !== "";
  const shownKeys = new Set(subscriptions.filter(matches).map((sub) => sub.key));
  const limited = blocked.filter((sub) => verdictOf.get(sub.key)?.reason_code === "agent_cap" || verdictOf.get(sub.key)?.reason_code === "provider_limit");

  const loadError = listQuery.error ?? poolsQuery.error;
  const seen = new Set<string>();

  const renderRow = (sub: Subscription, reference: boolean) => (
    <AccountRow
      key={`${sub.key}-${reference ? "ref" : "main"}`}
      sub={sub}
      account={accountOf.get(sub.key)!}
      verdict={verdictOf.get(sub.key) ?? null}
      pools={poolsOf(sub.key)}
      reference={reference}
      open={!reference && view.account === sub.key}
      onToggle={() => setView({ account: view.account === sub.key ? null : sub.key })}
      horizon={view.horizon}
      wsId={wsId}
      canManage={canManage}
      windowName={windowName}
      onOpenPool={(poolId) => setView({ pool: poolId })}
    />
  );

  const renderGroup = (pool: Pool | null, accounts: Subscription[]) => {
    const visible = accounts.filter((sub) => shownKeys.has(sub.key));
    if (filtering && !visible.length) return null;
    const rows = visible.map((sub) => {
      const reference = seen.has(sub.key);
      seen.add(sub.key);
      return renderRow(sub, reference);
    });
    return (
      <section key={pool?.id ?? "none"} className="overflow-hidden rounded-lg border" aria-labelledby={`group-${pool?.id ?? "none"}`} data-testid={`pool-group-${pool?.id ?? "none"}`}>
        <header className="flex flex-wrap items-center justify-between gap-2 border-b bg-muted/30 px-4 py-2.5">
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <h2 id={`group-${pool?.id ?? "none"}`} className="text-body font-medium">
              {pool ? pool.name : t(($) => $.subscriptions.no_pool_title)}
            </h2>
            {pool ? (
              <>
                <Badge variant="outline">{t(($) => $.subscriptions.groups[pool.runtime_type])}</Badge>
                <span className="text-caption text-muted-foreground">
                  {t(($) => $.subscriptions.order[pool.spend_order])}
                  {" · "}
                  {pool.all_workspaces ? t(($) => $.subscriptions.new_pool.all_workspaces) : t(($) => $.subscriptions.pool.workspaces_n, { n: pool.workspace_ids.length })}
                </span>
              </>
            ) : (
              <span className="text-caption text-muted-foreground">{t(($) => $.subscriptions.no_pool_hint)}</span>
            )}
          </div>
          {pool ? (
            <div className="flex flex-wrap items-center gap-2">
              <VerdictBadge verdict={pool.availability} />
              <Button size="sm" variant={view.pool === pool.id ? "secondary" : "outline"} onClick={() => setView({ pool: pool.id })}>
                {t(($) => $.subscriptions.pool.edit)}
              </Button>
            </div>
          ) : null}
        </header>
        {pool && !pool.availability.usable ? (
          <p className="border-b px-4 py-2 text-caption text-muted-foreground">{availability.availableAt(pool.availability)}</p>
        ) : null}
        {rows.length ? (
          <div>{rows}</div>
        ) : (
          <p className="px-4 py-3 text-caption text-muted-foreground">{t(($) => $.subscriptions.pool.empty_short)}</p>
        )}
      </section>
    );
  };

  const stat = (label: string, value: number, extra: string | null, onClick?: () => void, active?: boolean) => {
    const content = (
      <>
        <span className="text-caption text-muted-foreground">{label}</span>
        <span className="block text-title-sm font-semibold tabular-nums">{value}</span>
        {extra ? <span className="block text-caption text-muted-foreground">{extra}</span> : null}
      </>
    );
    return onClick ? (
      <button type="button" aria-pressed={active} onClick={onClick} className={cn("min-w-0 rounded-lg border p-3 text-left hover:bg-muted/50", active && "border-foreground ring-1 ring-foreground")}>
        {content}
      </button>
    ) : (
      <div className="min-w-0 rounded-lg border p-3">{content}</div>
    );
  };

  return (
    <div className="min-w-0 space-y-4">
      <div className="flex flex-wrap items-center justify-end gap-2">
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label={t(($) => $.subscriptions.refresh)}
          title={t(($) => $.subscriptions.refresh)}
          onClick={refresh}
          disabled={fetching}
        >
          <RefreshCw className={fetching ? "animate-spin" : undefined} />
        </Button>
        {canManage ? (
          <>
            <Button size="sm" variant="outline" onClick={() => setCreatingPool(true)}>
              <Plus />
              {t(($) => $.subscriptions.new_pool.open)}
            </Button>
            <Button size="sm" onClick={() => setAdding(true)}>
              <Plus />
              {t(($) => $.subscriptions.add.open)}
            </Button>
          </>
        ) : null}
      </div>

      {listQuery.isLoading || poolsQuery.isLoading ? <Skeleton className="h-48 rounded-lg" /> : null}
      {loadError ? (
        <p role="alert" className="rounded-md border border-destructive/40 px-3 py-2 text-caption text-destructive">
          {errorText(loadError)}
        </p>
      ) : null}
      {limited.length ? (
        <p role="status" className="flex items-start gap-2 rounded-md border border-warning/40 bg-warning/10 px-3 py-2 text-caption">
          <AlertTriangle className="mt-0.5 size-4 shrink-0 text-warning" aria-hidden="true" />
          <span>{t(($) => $.subscriptions.limited_banner, { titles: limited.map((sub) => sub.title).join(", ") })}</span>
        </p>
      ) : null}
      {listQuery.data && !canManage ? <p className="text-caption text-muted-foreground">{t(($) => $.subscriptions.readonly)}</p> : null}

      <Discrepancies
        items={discrepanciesQuery.data ?? []}
        wsId={wsId}
        profiles={profiles}
        subscriptions={subscriptions}
        onOpenPool={(poolId) => setView({ pool: poolId })}
      />

      {listQuery.data && poolsQuery.data ? (
        <>
          <div className="grid grid-cols-2 gap-3 lg:grid-cols-4" data-testid="subscriptions-summary">
            {stat(t(($) => $.subscriptions.stats.total), overview.totalAccounts, null, () => setView({ status: null }), view.status === "all")}
            {stat(t(($) => $.subscriptions.stats.available), available.length, null, () => setView({ status: "available" }), view.status === "available")}
            {stat(t(($) => $.subscriptions.stats.unavailable), subscriptions.length - available.length, null, () => setView({ status: "unavailable" }), view.status === "unavailable")}
            {stat(t(($) => $.subscriptions.stats.no_pool), overview.unpooled.length, null)}
          </div>

          <div className="flex flex-wrap items-center gap-2" role="search">
            <Input
              type="search"
              className="w-full sm:w-64"
              aria-label={t(($) => $.subscriptions.filters.search)}
              placeholder={t(($) => $.subscriptions.filters.search)}
              value={view.query}
              onChange={(event) => setView({ query: event.target.value })}
            />
            <div role="group" aria-label={t(($) => $.subscriptions.filters.status)} className="flex flex-wrap gap-1 rounded-lg bg-muted p-0.5">
              {STATUS_FILTERS.map((value) => (
                <Button key={value} size="sm" variant={view.status === value ? "outline" : "ghost"} aria-pressed={view.status === value} onClick={() => setView({ status: value })}>
                  {t(($) => $.subscriptions.filters.statuses[value])}
                </Button>
              ))}
            </div>
            <div role="group" aria-label={t(($) => $.subscriptions.filters.family)} className="flex flex-wrap gap-1 rounded-lg bg-muted p-0.5">
              {FAMILY_FILTERS.map((value) => (
                <Button key={value} size="sm" variant={view.family === value ? "outline" : "ghost"} aria-pressed={view.family === value} onClick={() => setView({ family: value })}>
                  {value === "all" ? t(($) => $.subscriptions.filters.statuses.all) : t(($) => $.subscriptions.groups[value])}
                </Button>
              ))}
            </div>
            <div role="group" aria-label={t(($) => $.subscriptions.filters.scale)} className="flex flex-wrap gap-1 rounded-lg bg-muted p-0.5">
              {LANE_HORIZONS.map((hours) => (
                <Button key={hours} size="sm" variant={view.horizon === hours ? "outline" : "ghost"} aria-pressed={view.horizon === hours} onClick={() => setView({ horizon: String(hours) })}>
                  {t(($) => $.subscriptions.filters.horizons[String(hours) as "12" | "48" | "168"])}
                </Button>
              ))}
            </div>
            <span className="text-caption text-muted-foreground" aria-live="polite">
              {filtering
                ? t(($) => $.subscriptions.filters.found, { shown: shownKeys.size, total: subscriptions.length })
                : null}
            </span>
            {filtering ? (
              <Button size="sm" variant="ghost" onClick={() => setView({ status: null, family: null, query: null })}>
                {t(($) => $.subscriptions.filters.reset)}
              </Button>
            ) : null}
          </div>

          {pools.map((pool) => renderGroup(pool, overview.groups.find((group) => group.pool.id === pool.id)?.accounts ?? []))}
          {overview.unpooled.length ? renderGroup(null, overview.unpooled) : null}

          {!subscriptions.length ? <p className="text-caption text-muted-foreground">{t(($) => $.subscriptions.empty)}</p> : null}
          {filtering && subscriptions.length && !shownKeys.size ? (
            <p className="text-caption text-muted-foreground">{t(($) => $.subscriptions.filters.none)}</p>
          ) : null}
        </>
      ) : null}

      {canManage ? (
        <>
          <AddSubscriptionDialog
            open={adding}
            onOpenChange={setAdding}
            wsId={wsId}
            pools={pools}
            windowName={windowName}
            onFinished={(key) => setView({ account: key })}
          />
          <NewPoolDialog open={creatingPool} onOpenChange={setCreatingPool} wsId={wsId} subscriptions={subscriptions} />
        </>
      ) : null}

      <PoolDialog
        poolId={view.pool || null}
        subscriptions={subscriptions}
        canManage={canManage}
        onOpenChange={(open) => {
          if (!open) setView({ pool: null });
        }}
      />
    </div>
  );
}
