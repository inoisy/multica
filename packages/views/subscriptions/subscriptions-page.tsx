"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, Gauge, MoreHorizontal, Plus, RefreshCw } from "lucide-react";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import { runtimeProfileListOptions } from "@multica/core/runtimes";
import {
  groupByAccount,
  isSubscriptionPool,
  subscriptionDiscrepanciesOptions,
  subscriptionListOptions,
  useDeleteSubscription,
  useUpdateSubscription,
  windowBlocked,
  type SubscriptionAccount,
} from "@multica/core/subscriptions";
import type { RuntimeProfile, Subscription, SubscriptionUsage } from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { Input } from "@multica/ui/components/ui/input";
import { cn } from "@multica/ui/lib/utils";
import { CollectionPageHeader } from "../layout/collection-page";
import { PAGE_GUTTER } from "../layout/page-header";
import { useT } from "../i18n";
import { AddSubscriptionDialog } from "./add-subscription-dialog";
import { Discrepancies } from "./discrepancies";
import { NewPoolDialog } from "./new-pool-dialog";
import { PoolDialog } from "./pool-dialog";
import { SubscriptionLoginStatus } from "./subscription-login";
import { groupOf, parsePercent } from "./subscription-plan";

/** Figures older than this are marked with the time they were taken. */
const OLD_USAGE_MS = 10 * 60_000;

const GROUPS = ["claude", "codex", "api"] as const;

function formatTime(value: string | null | undefined) {
  if (!value) return "";
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime())
    ? value
    : parsed.toLocaleString(undefined, { day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit" });
}

function errorText(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

/** Window load: fill is spent, the tick is the agents' cap. */
function UsageBar({ used, cap, blocked }: { used: number; cap: number | undefined; blocked: boolean }) {
  const near = cap !== undefined && used >= cap * 0.8;
  return (
    <div className="relative h-2 w-full overflow-hidden rounded-full bg-muted">
      <div
        className={cn("h-full rounded-full", blocked ? "bg-destructive" : near ? "bg-amber-500" : "bg-primary")}
        style={{ width: `${Math.min(100, Math.max(0, used))}%` }}
      />
      {cap !== undefined && cap < 100 ? (
        <div className="absolute top-0 h-full w-0.5 bg-foreground/70" style={{ left: `${cap}%` }} aria-hidden="true" />
      ) : null}
    </div>
  );
}

/** The agents' cap of a window on an account card: the strictest of its subscriptions. */
function accountCap(account: SubscriptionAccount, window: string): number | undefined {
  const caps = account.subscriptions
    .map((sub) => sub.effective_caps[window])
    .filter((cap): cap is number => cap !== undefined);
  return caps.length ? Math.min(...caps) : undefined;
}

function accountBlocked(account: SubscriptionAccount): boolean {
  return account.usage.some((w) => windowBlocked(w, accountCap(account, w.window)));
}

/**
 * «Подписки»: instance subscriptions as account cards (load, agents' limits,
 * reserve, who spends them, sign-in), the workspace's subscription pools and
 * what is out of place. Everything comes from the Multica API; only an
 * instance admin (`can_manage`) changes anything.
 */
export function SubscriptionsPage() {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const listQuery = useQuery(subscriptionListOptions());
  const profilesQuery = useQuery(runtimeProfileListOptions(wsId));
  const discrepanciesQuery = useQuery(subscriptionDiscrepanciesOptions());
  const updateSubscription = useUpdateSubscription();
  const deleteSubscription = useDeleteSubscription();

  const subscriptions = useMemo(() => listQuery.data?.subscriptions ?? [], [listQuery.data]);
  const canManage = listQuery.data?.can_manage === true;
  const profiles = useMemo(() => profilesQuery.data ?? [], [profilesQuery.data]);
  const pools = useMemo(() => profiles.filter(isSubscriptionPool), [profiles]);
  const accounts = useMemo(() => groupByAccount(subscriptions), [subscriptions]);
  const limited = accounts.filter(accountBlocked);

  const [editing, setEditing] = useState<string | null>(null);
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [adding, setAdding] = useState(false);
  const [creatingPool, setCreatingPool] = useState(false);
  const [poolId, setPoolId] = useState<string | null>(null);
  const [renaming, setRenaming] = useState<Subscription | null>(null);
  const [newTitle, setNewTitle] = useState("");
  const [disabling, setDisabling] = useState<Subscription | null>(null);
  const [disableUntil, setDisableUntil] = useState("");
  const busy = updateSubscription.isPending || deleteSubscription.isPending;

  const windowNames: Record<string, string> = {
    "5h": t(($) => $.subscriptions.windows["5h"]),
    week: t(($) => $.subscriptions.windows.week),
  };
  const windowName = (key: string) => windowNames[key] ?? key;
  const titleOf = (key: string) => subscriptions.find((s) => s.key === key)?.title ?? key;
  const fetching = listQuery.isFetching || profilesQuery.isFetching || discrepanciesQuery.isFetching;

  const refresh = () => {
    void listQuery.refetch();
    void profilesQuery.refetch();
    void discrepanciesQuery.refetch();
  };

  const patch = async (sub: Subscription, body: Parameters<typeof updateSubscription.mutateAsync>[0]["patch"]) => {
    try {
      await updateSubscription.mutateAsync({ key: sub.key, patch: body });
      toast.success(t(($) => $.subscriptions.saved));
      return true;
    } catch (error) {
      toast.error(errorText(error));
      return false;
    }
  };

  const startEdit = (sub: Subscription) => {
    setEditing(sub.key);
    setDraft({
      ...Object.fromEntries(sub.windows.map((key) => [key, String(sub.caps[key] ?? 100)])),
      reserve: String(sub.reserve),
    });
  };

  const saveCaps = async (sub: Subscription) => {
    const numbers = Object.fromEntries(Object.entries(draft).map(([key, value]) => [key, parsePercent(value)]));
    if (Object.values(numbers).some((value) => value === null)) {
      toast.error(t(($) => $.subscriptions.bad_percent));
      return;
    }
    const { reserve, ...caps } = numbers as Record<string, number>;
    if (await patch(sub, { caps, reserve })) setEditing(null);
  };

  const rename = async () => {
    if (!renaming || !newTitle.trim()) return;
    if (await patch(renaming, { title: newTitle.trim() })) setRenaming(null);
  };

  const disable = async () => {
    if (!disabling) return;
    const until = new Date(disableUntil);
    if (Number.isNaN(until.getTime())) return;
    if (await patch(disabling, { disabled_until: until.toISOString() })) setDisabling(null);
  };

  // The server takes the subscription out of every pool before deleting it.
  const remove = async (sub: Subscription) => {
    if (!window.confirm(t(($) => $.subscriptions.menu.delete_confirm, { title: sub.title }))) return;
    try {
      await deleteSubscription.mutateAsync(sub.key);
      toast.success(t(($) => $.subscriptions.menu.deleted, { title: sub.title }));
    } catch (error) {
      toast.error(errorText(error));
    }
  };

  const loadError = listQuery.error ? errorText(listQuery.error) : null;

  const renderUsage = (account: SubscriptionAccount) => {
    if (!account.usage.length) {
      return <p className="text-caption text-muted-foreground">{t(($) => $.subscriptions.no_usage)}</p>;
    }
    const oldest = Math.min(...account.usage.map((w) => Date.parse(w.observed_at) || Date.now()));
    return (
      <>
        {Date.now() - oldest > OLD_USAGE_MS ? (
          <p className="text-caption text-muted-foreground">
            {t(($) => $.subscriptions.usage_as_of, { time: formatTime(new Date(oldest).toISOString()) })}
          </p>
        ) : null}
        <ul className="space-y-2">
          {account.usage.map((w: SubscriptionUsage) => {
            const cap = accountCap(account, w.window);
            const blocked = windowBlocked(w, cap);
            return (
              <li key={w.window} className="space-y-1 text-caption">
                <div className="flex flex-wrap items-baseline justify-between gap-x-3">
                  <span>
                    {windowName(w.window)}
                    {blocked ? (
                      <Badge variant="destructive" className="ml-2">
                        {t(($) => $.subscriptions.limit_reached)}
                      </Badge>
                    ) : null}
                  </span>
                  <span className="tabular-nums text-muted-foreground">
                    {w.used_percent === null ? "—" : `${Math.round(w.used_percent)}%`}
                    {cap !== undefined && cap < 100 ? ` ${t(($) => $.subscriptions.of_cap, { cap })}` : ""}
                    {w.resets_at ? ` · ${t(($) => $.subscriptions.reset, { time: formatTime(w.resets_at) })}` : ""}
                  </span>
                </div>
                {w.used_percent !== null ? <UsageBar used={w.used_percent} cap={cap} blocked={blocked} /> : null}
              </li>
            );
          })}
        </ul>
      </>
    );
  };

  const renderSubscription = (sub: Subscription, showHeading: boolean) => {
    const here = sub.used_by.filter((user) => user.workspace_id === wsId);
    const elsewhere = new Set(sub.used_by.filter((user) => user.workspace_id !== wsId).map((user) => user.workspace_id));
    const limits = Object.entries(sub.effective_caps);
    const isEditing = editing === sub.key;
    return (
      <div key={sub.key} className="space-y-2 border-t pt-3 text-caption">
        <div className="flex items-start justify-between gap-2">
          <div className="flex min-w-0 flex-wrap items-center gap-1.5">
            {showHeading ? <span className="text-body font-medium">{sub.title}</span> : null}
            <Badge variant="outline" className="font-mono">
              {sub.key}
            </Badge>
            {sub.disabled ? (
              <Badge variant="destructive">
                {sub.disabled_until
                  ? t(($) => $.subscriptions.disabled_until, { time: formatTime(sub.disabled_until) })
                  : t(($) => $.subscriptions.disabled)}
              </Badge>
            ) : null}
          </div>
          {canManage ? (
            <DropdownMenu>
              <DropdownMenuTrigger
                render={
                  <Button
                    size="icon-sm"
                    variant="ghost"
                    aria-label={t(($) => $.subscriptions.menu.open, { title: sub.title })}
                  />
                }
              >
                <MoreHorizontal />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                {sub.windows.length ? (
                  <DropdownMenuItem onClick={() => startEdit(sub)}>{t(($) => $.subscriptions.menu.caps)}</DropdownMenuItem>
                ) : null}
                {sub.disabled ? (
                  <DropdownMenuItem onClick={() => void patch(sub, { disabled_until: "" })}>
                    {t(($) => $.subscriptions.menu.enable)}
                  </DropdownMenuItem>
                ) : (
                  <DropdownMenuItem
                    onClick={() => {
                      setDisabling(sub);
                      setDisableUntil("");
                    }}
                  >
                    {t(($) => $.subscriptions.menu.disable)}
                  </DropdownMenuItem>
                )}
                {here.map((user) => (
                  <DropdownMenuItem key={user.runtime_profile_id} onClick={() => setPoolId(user.runtime_profile_id)}>
                    {t(($) => $.subscriptions.menu.runtime, { runtime: user.display_name })}
                  </DropdownMenuItem>
                ))}
                <DropdownMenuItem
                  onClick={() => {
                    setRenaming(sub);
                    setNewTitle(sub.title);
                  }}
                >
                  {t(($) => $.subscriptions.menu.rename)}
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                <DropdownMenuItem variant="destructive" onClick={() => void remove(sub)}>
                  {t(($) => $.subscriptions.menu.delete)}
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          ) : null}
        </div>

        <SubscriptionLoginStatus sub={sub} canManage={canManage} />

        {isEditing ? (
          <form
            className="space-y-2"
            onSubmit={(event) => {
              event.preventDefault();
              void saveCaps(sub);
            }}
          >
            <div className="flex flex-wrap gap-3">
              {[...sub.windows, "reserve"].map((key) => (
                <label key={key} className="flex flex-col gap-1">
                  <span className="text-muted-foreground">
                    {t(($) => $.subscriptions.field_percent, {
                      name: key === "reserve" ? t(($) => $.subscriptions.reserve_label) : windowName(key),
                    })}
                  </span>
                  <Input
                    className="w-24"
                    inputMode="numeric"
                    value={draft[key] ?? ""}
                    onChange={(event) => setDraft({ ...draft, [key]: event.target.value })}
                  />
                </label>
              ))}
            </div>
            <p className="text-muted-foreground">{t(($) => $.subscriptions.edit_hint)}</p>
            <div className="flex gap-2">
              <Button size="sm" type="submit" disabled={busy}>
                {t(($) => $.subscriptions.save)}
              </Button>
              <Button size="sm" variant="ghost" type="button" onClick={() => setEditing(null)}>
                {t(($) => $.subscriptions.cancel)}
              </Button>
            </div>
          </form>
        ) : null}

        <p>
          {limits.length
            ? t(($) => $.subscriptions.agents_up_to, {
                limits: limits.map(([key, cap]) => `${windowName(key)} ${cap}%`).join(", "),
              })
            : t(($) => $.subscriptions.provider_limit)}
          {sub.reserve ? ` · ${t(($) => $.subscriptions.reserve, { reserve: sub.reserve })}` : ""}
        </p>
        <p>
          <span className="text-muted-foreground">{t(($) => $.subscriptions.spent_by)}</span>{" "}
          {here.length
            ? here.map((user, index) => (
                <span key={user.runtime_profile_id}>
                  {index ? ", " : ""}
                  <button
                    type="button"
                    className="underline-offset-2 hover:underline"
                    onClick={() => setPoolId(user.runtime_profile_id)}
                  >
                    {t(($) => $.subscriptions.spent_by_item, { name: user.display_name, position: user.position })}
                  </button>
                </span>
              ))
            : t(($) => $.subscriptions.spent_by_none)}
          {elsewhere.size ? (
            <span className="text-muted-foreground">
              {" · "}
              {t(($) => $.subscriptions.spent_elsewhere, { n: elsewhere.size })}
            </span>
          ) : null}
        </p>
        {sub.credential_path ? (
          <p className="text-muted-foreground">
            {t(($) => $.subscriptions.credential)} <code className="font-mono">{sub.credential_path}</code>
          </p>
        ) : null}
      </div>
    );
  };

  return (
    <div className="flex h-full min-h-0 flex-col">
      <CollectionPageHeader
        icon={Gauge}
        title={t(($) => $.subscriptions.title)}
        description={t(($) => $.subscriptions.description)}
        actions={
          <div className="flex items-center gap-1">
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={t(($) => $.subscriptions.refresh)}
              title={t(($) => $.subscriptions.refresh)}
              onClick={refresh}
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
        }
      />

      <div className={cn("min-h-0 flex-1 overflow-y-auto py-4", PAGE_GUTTER)}>
        <div className="space-y-6">
          {loadError ? (
            <p role="alert" className="rounded-md border border-destructive/40 px-3 py-2 text-caption text-destructive">
              {loadError}
            </p>
          ) : null}
          {limited.length ? (
            <p
              role="status"
              className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/5 px-3 py-2 text-caption"
            >
              <AlertTriangle className="mt-0.5 size-4 shrink-0 text-amber-600" aria-hidden="true" />
              <span>
                {t(($) => $.subscriptions.limited_banner, {
                  titles: limited.map((account) => account.subscriptions.map((s) => s.title).join(", ")).join(", "),
                })}
              </span>
            </p>
          ) : null}
          {listQuery.data && !canManage ? (
            <p className="text-caption text-muted-foreground">{t(($) => $.subscriptions.readonly)}</p>
          ) : null}

          <Discrepancies
            items={discrepanciesQuery.data ?? []}
            wsId={wsId}
            profiles={profiles}
            subscriptions={subscriptions}
            onOpenPool={setPoolId}
          />

          {GROUPS.map((group) => {
            const groupAccounts = accounts.filter((account) => groupOf(account.subscriptions[0]!) === group);
            if (!groupAccounts.length) return null;
            return (
              <section key={group} className="space-y-3" aria-labelledby={`subscriptions-${group}`}>
                <h2 id={`subscriptions-${group}`} className="text-body font-medium">
                  {t(($) => $.subscriptions.groups[group])}
                </h2>
                <div className="grid gap-3 lg:grid-cols-2">
                  {groupAccounts.map((account) => {
                    const titles = [...new Set(account.subscriptions.map((s) => s.title))];
                    const shared = account.subscriptions.length > 1;
                    return (
                      <article key={account.id} className="flex flex-col gap-3 rounded-lg border p-4">
                        <h3 className="text-body font-medium">{titles.join(" · ")}</h3>
                        {renderUsage(account)}
                        {account.subscriptions.map((sub) => renderSubscription(sub, shared))}
                      </article>
                    );
                  })}
                </div>
              </section>
            );
          })}

          {listQuery.data && !subscriptions.length ? (
            <p className="text-caption text-muted-foreground">{t(($) => $.subscriptions.empty)}</p>
          ) : null}

          <section className="space-y-3" aria-labelledby="subscriptions-pools">
            <h2 id="subscriptions-pools" className="text-body font-medium">
              {t(($) => $.subscriptions.pools_title)}
            </h2>
            {pools.length ? (
              <ul className="grid gap-2 lg:grid-cols-2">
                {pools.map((pool: RuntimeProfile) => (
                  <li key={pool.id}>
                    <button
                      type="button"
                      className="flex w-full flex-col gap-1 rounded-lg border px-4 py-3 text-left hover:bg-muted/50"
                      onClick={() => setPoolId(pool.id)}
                    >
                      <span className="flex flex-wrap items-center gap-1.5">
                        <span className="text-body font-medium">{pool.display_name}</span>
                        {pool.all_workspaces ? (
                          <Badge variant="outline">{t(($) => $.subscriptions.pool.all_workspaces)}</Badge>
                        ) : null}
                        {!pool.enabled ? <Badge variant="destructive">{t(($) => $.subscriptions.pool.off)}</Badge> : null}
                      </span>
                      <span className="truncate text-caption text-muted-foreground">
                        {pool.subscriptions?.length
                          ? pool.subscriptions.map(titleOf).join(" → ")
                          : t(($) => $.subscriptions.pool.empty_short)}
                        {" · "}
                        {pool.spend_order === "balanced"
                          ? t(($) => $.subscriptions.order.balanced)
                          : t(($) => $.subscriptions.order.first)}
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            ) : (
              <p className="text-caption text-muted-foreground">{t(($) => $.subscriptions.pools_empty)}</p>
            )}
          </section>
        </div>
      </div>

      {canManage ? (
        <>
          <AddSubscriptionDialog
            open={adding}
            onOpenChange={setAdding}
            wsId={wsId}
            profiles={profiles}
            subscriptions={subscriptions}
            windowName={windowName}
          />
          <NewPoolDialog open={creatingPool} onOpenChange={setCreatingPool} wsId={wsId} subscriptions={subscriptions} />
        </>
      ) : null}

      <PoolDialog
        wsId={wsId}
        profileId={poolId}
        subscriptions={subscriptions}
        canManage={canManage}
        onOpenChange={(open) => {
          if (!open) setPoolId(null);
        }}
      />

      <Dialog open={renaming !== null} onOpenChange={(open) => (open ? null : setRenaming(null))}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t(($) => $.subscriptions.menu.rename_title)}</DialogTitle>
          </DialogHeader>
          <form
            className="space-y-4"
            onSubmit={(event) => {
              event.preventDefault();
              void rename();
            }}
          >
            <Input
              aria-label={t(($) => $.subscriptions.menu.rename_label)}
              value={newTitle}
              onChange={(event) => setNewTitle(event.target.value)}
            />
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setRenaming(null)}>
                {t(($) => $.subscriptions.cancel)}
              </Button>
              <Button type="submit" disabled={busy || !newTitle.trim()}>
                {t(($) => $.subscriptions.save)}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <Dialog open={disabling !== null} onOpenChange={(open) => (open ? null : setDisabling(null))}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t(($) => $.subscriptions.menu.disable_title, { title: disabling?.title ?? "" })}</DialogTitle>
          </DialogHeader>
          <form
            className="space-y-4"
            onSubmit={(event) => {
              event.preventDefault();
              void disable();
            }}
          >
            <label className="flex flex-col gap-1 text-caption">
              <span className="text-muted-foreground">{t(($) => $.subscriptions.menu.disable_until)}</span>
              <Input type="datetime-local" value={disableUntil} onChange={(event) => setDisableUntil(event.target.value)} />
              <span className="text-muted-foreground">{t(($) => $.subscriptions.menu.disable_hint)}</span>
            </label>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setDisabling(null)}>
                {t(($) => $.subscriptions.cancel)}
              </Button>
              <Button type="submit" disabled={busy || !disableUntil}>
                {t(($) => $.subscriptions.menu.disable_submit)}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  );
}
