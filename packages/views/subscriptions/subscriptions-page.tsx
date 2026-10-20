"use client";

import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Gauge, History, MoreHorizontal, Plus, RefreshCw } from "lucide-react";
import { toast } from "sonner";
import { useWorkspaceSlug } from "@multica/core/paths";
import { loginAccountsOptions } from "@multica/core/subs-login";
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
import { ControlError, controlRequest } from "../conveyor/control-api";
import { SubscriptionLogin } from "./subscription-login";
import { AddSubscriptionDialog } from "./add-subscription-dialog";
import { RuntimeOrderDialog } from "./runtime-order-dialog";
import { groupOf, runtimesWithout, type RuntimeRow } from "./subscription-plan";
import type { SaveRow, Subscription, SubscriptionsState } from "./subscription-types";

/** Цифры старше этого помечаются на карточке временем, когда получены. */
const OLD_USAGE_MS = 10 * 60_000;
/** Сколько ждать новых данных после «Обновить»: опрос всех провайдеров идёт до минуты. */
const REFRESH_WAIT_MS = 90_000;
const REFRESH_POLL_MS = 3_000;

const GROUPS = ["claude", "codex", "api"] as const;

function formatTime(value: string | null | undefined) {
  if (!value) return "";
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime())
    ? value
    : parsed.toLocaleString(undefined, { day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit" });
}

/** Полоса загрузки окна: заливка — израсходовано, риска — наш лимит для агентов. */
function UsageBar({ used, cap, blocked }: { used: number; cap: number | null; blocked: boolean }) {
  const near = cap !== null && used >= cap * 0.8;
  return (
    <div className="relative h-2 w-full overflow-hidden rounded-full bg-muted">
      <div
        className={cn("h-full rounded-full", blocked ? "bg-destructive" : near ? "bg-amber-500" : "bg-primary")}
        style={{ width: `${Math.min(100, Math.max(0, used))}%` }}
      />
      {cap !== null && cap < 100 ? (
        <div className="absolute top-0 h-full w-0.5 bg-foreground/70" style={{ left: `${cap}%` }} aria-hidden="true" />
      ) : null}
    </div>
  );
}

function runtimeRows(state: SubscriptionsState | undefined): RuntimeRow[] {
  return (state?.runtimes ?? []).map((row) => ({
    name: row.name,
    command: row.command ?? "",
    subscriptions: row.subscriptions ?? [],
    order: row.order || "first",
  }));
}

/**
 * «Подписки»: карточки аккаунтов по провайдерам. Добавление — мастером,
 * порядок трат — окном runtime из «Тратят», переименование и удаление — меню
 * карточки; таблицы пульта правятся только через них.
 */
export function SubscriptionsPage() {
  const { t } = useT("agents");
  const wsSlug = useWorkspaceSlug();
  const queryClient = useQueryClient();
  const queryKey = ["control", "subscriptions", wsSlug] as const;
  const query = useQuery({
    queryKey,
    queryFn: () => controlRequest<SubscriptionsState>("subscriptions", undefined, wsSlug),
    refetchInterval: 60_000,
    retry: false,
  });
  const state = query.data;
  const logins = useQuery(loginAccountsOptions(wsSlug ?? "")).data;
  const [editing, setEditing] = useState<string | null>(null);
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [adding, setAdding] = useState(false);
  const [journal, setJournal] = useState(false);
  const [runtimeName, setRuntimeName] = useState<string | null>(null);
  const [renaming, setRenaming] = useState<Subscription | null>(null);
  const [newTitle, setNewTitle] = useState("");

  const runtimes = useMemo(() => runtimeRows(state), [state]);
  const openRuntime = runtimes.find((r) => r.name === runtimeName) ?? null;
  const windowName = (key: string) => state?.window_names[key] ?? key;
  const loadError =
    query.error instanceof ControlError
      ? query.error.status === 401
        ? t(($) => $.subscriptions.login_required)
        : query.error.message
      : query.error
        ? String(query.error)
        : null;
  const cards = state?.accounts ?? state?.subscriptions ?? [];
  const limited = cards.filter((sub) => sub.usage.some((w) => w.blocked));
  const canEdit = state?.can_edit === true;

  const invalidate = () => queryClient.invalidateQueries({ queryKey });

  const startEdit = (sub: Subscription) => {
    setEditing(sub.id);
    setDraft({
      ...Object.fromEntries(sub.windows.map((key) => [key, String(sub.caps[key] ?? 100)])),
      reserve: String(sub.reserve),
    });
  };

  // «Обновить» заказывает у пульта внеочередной опрос квот и ждёт, пока дашборд запишет новые данные:
  // простой refetch перечитал бы тот же снимок, который дашборд обновляет раз в 5 минут.
  const refresh = async () => {
    setRefreshing(true);
    try {
      const result = await controlRequest<{ updated?: string | null }>("subscriptions/refresh", {}, wsSlug);
      const before = result.updated ?? state?.updated ?? null;
      const deadline = Date.now() + REFRESH_WAIT_MS;
      while (Date.now() < deadline) {
        await new Promise((resolve) => setTimeout(resolve, REFRESH_POLL_MS));
        const next = await query.refetch();
        if (next.data?.updated && next.data.updated !== before) {
          toast.success(t(($) => $.subscriptions.refreshed));
          return;
        }
      }
      toast.warning(t(($) => $.subscriptions.refresh_timeout));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : String(error));
    } finally {
      setRefreshing(false);
    }
  };

  const saveRow: SaveRow = async (table, key, row, options) => {
    setBusy(true);
    try {
      const result = await controlRequest<{ message?: string }>("subscriptions/tables", { table, key, row }, wsSlug);
      if (!options?.quiet) {
        toast.success(result.message ?? t(($) => $.subscriptions.saved));
        await invalidate();
      }
      return true;
    } catch (error) {
      toast.error(error instanceof Error ? error.message : String(error));
      return false;
    } finally {
      setBusy(false);
    }
  };

  const saveCaps = async (sub: Subscription) => {
    const numbers = Object.fromEntries(Object.entries(draft).map(([key, value]) => [key, Number(value)]));
    if (Object.values(numbers).some((value) => !Number.isInteger(value) || value < 0 || value > 100)) {
      toast.error(t(($) => $.subscriptions.bad_percent));
      return;
    }
    const { reserve, ...caps } = numbers;
    setBusy(true);
    try {
      const result = await controlRequest<{ message?: string }>("subscriptions", { id: sub.id, caps, reserve }, wsSlug);
      toast.success(result.message ?? t(($) => $.subscriptions.saved));
      setEditing(null);
      await invalidate();
    } catch (error) {
      toast.error(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(false);
    }
  };

  /** The table row of a card, as the panel stores it: rename keeps every other field. */
  const itemRow = (sub: Subscription, title: string) => ({
    provider: sub.provider,
    title,
    quota_row: sub.quota_row ?? "",
    windows: sub.windows,
    auth: sub.auth ?? "none",
    credential: sub.credential ?? "",
    cli: sub.cli ?? "",
  });

  const rename = async () => {
    if (!renaming || !newTitle.trim()) return;
    const row = state?.subscriptions.find((s) => s.id === renaming.id) ?? renaming;
    if (await saveRow("items", renaming.id, itemRow(row, newTitle.trim()))) setRenaming(null);
  };

  // The panel refuses to delete a subscription a runtime still spends: take it out of them first.
  const remove = async (sub: Subscription) => {
    if (!window.confirm(t(($) => $.subscriptions.menu.delete_confirm, { title: sub.title }))) return;
    for (const runtime of runtimesWithout(runtimes, sub.id)) {
      const ok = await saveRow(
        "runtimes",
        runtime.name,
        { command: runtime.command, subscriptions: runtime.subscriptions, order: runtime.order },
        { quiet: true },
      );
      if (!ok) {
        await invalidate();
        return;
      }
    }
    await saveRow("items", sub.id, null);
  };

  const runtimeAgents = (name: string) => {
    const names = new Set<string>();
    for (const sub of cards) {
      if (sub.runtimes.includes(name)) for (const agent of sub.agents) names.add(agent.name);
    }
    return [...names];
  };

  return (
    <div className="flex h-full min-h-0 flex-col">
      <CollectionPageHeader
        icon={Gauge}
        title={t(($) => $.subscriptions.title)}
        description={t(($) => $.subscriptions.description)}
        actions={
          <div className="flex items-center gap-1">
            {state?.events.length ? (
              <Button variant="ghost" size="sm" onClick={() => setJournal(true)}>
                <History />
                {t(($) => $.subscriptions.events_title)}
              </Button>
            ) : null}
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={t(($) => $.subscriptions.refresh)}
              title={refreshing ? t(($) => $.subscriptions.refreshing) : t(($) => $.subscriptions.refresh)}
              onClick={() => void refresh()}
              disabled={refreshing}
            >
              <RefreshCw className={refreshing || query.isFetching ? "animate-spin" : undefined} />
            </Button>
            {canEdit && wsSlug ? (
              <Button size="sm" onClick={() => setAdding(true)}>
                <Plus />
                {t(($) => $.subscriptions.add.open)}
              </Button>
            ) : null}
          </div>
        }
      />

      <div className={cn("min-h-0 flex-1 overflow-y-auto py-4", PAGE_GUTTER)}>
        <div className="space-y-6">
          {loadError ? (
            <p className="rounded-md border border-destructive/40 px-3 py-2 text-caption text-destructive">
              {loadError}
            </p>
          ) : null}
          {state?.stale ? (
            <p className="rounded-md border border-amber-500/40 px-3 py-2 text-caption">
              {t(($) => $.subscriptions.stale)}
            </p>
          ) : null}
          {limited.length ? (
            <p
              role="status"
              className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-500/5 px-3 py-2 text-caption"
            >
              <AlertTriangle className="mt-0.5 size-4 shrink-0 text-amber-600" aria-hidden="true" />
              <span>{t(($) => $.subscriptions.limited_banner, { titles: limited.map((sub) => sub.title).join(", ") })}</span>
            </p>
          ) : null}
          {state?.updated ? (
            <p className="text-caption text-muted-foreground">
              {refreshing
                ? t(($) => $.subscriptions.refreshing)
                : t(($) => $.subscriptions.data_at, { time: formatTime(state.updated) })}
            </p>
          ) : null}
          {state && !canEdit ? (
            <p className="text-caption text-muted-foreground">{t(($) => $.subscriptions.readonly)}</p>
          ) : null}

          {GROUPS.map((group) => {
            const groupCards = cards.filter((sub) => groupOf(sub.provider) === group);
            if (!groupCards.length) return null;
            return (
              <section key={group} className="space-y-3" aria-labelledby={`subscriptions-${group}`}>
                <div className="flex flex-wrap items-baseline gap-x-3">
                  <h2 id={`subscriptions-${group}`} className="text-body font-medium">
                    {t(($) => $.subscriptions.groups[group])}
                  </h2>
                  <span className="text-caption text-muted-foreground">
                    {t(($) => $.subscriptions.groups_hint[group])}
                  </span>
                </div>
                <div className="grid gap-3 lg:grid-cols-2">
                  {groupCards.map((sub) => {
                    const isEditing = editing === sub.id;
                    const keys = sub.keys ?? [sub.id];
                    const loginAccount = logins?.accounts.find((account) => keys.includes(account.id));
                    const isLimited = Object.keys(sub.effective).length > 0;
                    return (
                      <article key={sub.id} className="flex flex-col gap-3 rounded-lg border p-4">
                        <div className="flex items-start justify-between gap-3">
                          <div className="flex min-w-0 flex-col gap-1.5">
                            <h3 className="text-body font-medium">{sub.title}</h3>
                            <div className="flex flex-wrap items-center gap-1.5">
                              {keys.map((key) => (
                                <Badge key={key} variant="outline" className="font-mono">
                                  {key}
                                </Badge>
                              ))}
                            </div>
                          </div>
                          {canEdit ? (
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
                                  <DropdownMenuItem onClick={() => startEdit(sub)}>
                                    {t(($) => $.subscriptions.menu.caps)}
                                  </DropdownMenuItem>
                                ) : null}
                                {sub.runtimes.map((name) => (
                                  <DropdownMenuItem key={name} onClick={() => setRuntimeName(name)}>
                                    {t(($) => $.subscriptions.menu.runtime, { runtime: name })}
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
                                {keys.length === 1 ? (
                                  <>
                                    <DropdownMenuSeparator />
                                    <DropdownMenuItem variant="destructive" onClick={() => void remove(sub)}>
                                      {t(($) => $.subscriptions.menu.delete)}
                                    </DropdownMenuItem>
                                  </>
                                ) : null}
                              </DropdownMenuContent>
                            </DropdownMenu>
                          ) : null}
                        </div>

                        {loginAccount && wsSlug ? (
                          <SubscriptionLogin account={loginAccount} canLogin={logins?.can_login === true} wsSlug={wsSlug} />
                        ) : null}

                        {sub.usage.length && sub.as_of && Date.now() - new Date(sub.as_of).getTime() > OLD_USAGE_MS ? (
                          <p className="text-caption text-muted-foreground">
                            {t(($) => $.subscriptions.usage_as_of, { time: formatTime(sub.as_of) })}
                          </p>
                        ) : null}
                        {sub.usage.length ? (
                          <ul className="space-y-2">
                            {sub.usage.map((w, index) => (
                              <li key={`${w.label}-${index}`} className="space-y-1 text-caption">
                                <div className="flex flex-wrap items-baseline justify-between gap-x-3">
                                  <span>
                                    {w.label}
                                    {w.blocked ? (
                                      <Badge variant="destructive" className="ml-2">
                                        {t(($) => $.subscriptions.limit_reached)}
                                      </Badge>
                                    ) : null}
                                  </span>
                                  <span className="tabular-nums text-muted-foreground">
                                    {w.used === null ? "—" : `${Math.round(w.used)}%`}
                                    {w.cap !== null && w.cap < 100 ? ` ${t(($) => $.subscriptions.of_cap, { cap: w.cap })}` : ""}
                                    {w.reset ? ` · ${t(($) => $.subscriptions.reset, { time: w.reset })}` : ""}
                                  </span>
                                </div>
                                {w.used !== null ? <UsageBar used={w.used} cap={w.cap} blocked={w.blocked} /> : null}
                              </li>
                            ))}
                          </ul>
                        ) : (
                          <p className="text-caption text-muted-foreground">
                            {sub.error ? sub.error : t(($) => $.subscriptions.no_usage)}
                          </p>
                        )}

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
                                <label key={key} className="flex flex-col gap-1 text-caption">
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
                            <p className="text-caption text-muted-foreground">{t(($) => $.subscriptions.edit_hint)}</p>
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

                        <div className="space-y-1 border-t pt-3 text-caption">
                          <p>
                            {isLimited
                              ? t(($) => $.subscriptions.agents_up_to, {
                                  limits: Object.entries(sub.effective)
                                    .map(([key, cap]) => `${windowName(key)} ${cap}%`)
                                    .join(", "),
                                })
                              : t(($) => $.subscriptions.provider_limit)}
                            {sub.reserve ? ` · ${t(($) => $.subscriptions.reserve, { reserve: sub.reserve })}` : ""}
                          </p>
                          <p>
                            <span className="text-muted-foreground">{t(($) => $.subscriptions.spent_by)}</span>{" "}
                            {sub.runtimes.length
                              ? sub.runtimes.map((name, index) => (
                                  <span key={name}>
                                    {index ? ", " : ""}
                                    <button
                                      type="button"
                                      className="underline-offset-2 hover:underline"
                                      onClick={() => setRuntimeName(name)}
                                    >
                                      {name}
                                    </button>
                                  </span>
                                ))
                              : t(($) => $.subscriptions.spent_by_none)}
                            {sub.agents.length ? (
                              <span className="text-muted-foreground">
                                {" · "}
                                {sub.agents
                                  .map((agent) => (agent.active ? `${agent.name} (${t(($) => $.subscriptions.agent_now)})` : agent.name))
                                  .join(", ")}
                              </span>
                            ) : null}
                          </p>
                          {sub.credential ? (
                            <p className="text-muted-foreground">
                              {t(($) => $.subscriptions.credential)} <code className="font-mono">{sub.credential}</code>
                              {sub.changed_at
                                ? ` · ${t(($) => $.subscriptions.changed, { time: formatTime(sub.changed_at), who: sub.changed_by ?? "" })}`
                                : ""}
                            </p>
                          ) : null}
                        </div>
                      </article>
                    );
                  })}
                </div>
              </section>
            );
          })}
        </div>
      </div>

      {state && wsSlug ? (
        <AddSubscriptionDialog
          open={adding}
          onOpenChange={setAdding}
          subscriptions={state.subscriptions}
          runtimes={runtimes}
          windowKeys={state.windows.map((w) => w.key)}
          windowName={windowName}
          wsSlug={wsSlug}
          onSave={saveRow}
          onDone={invalidate}
        />
      ) : null}

      <RuntimeOrderDialog
        runtime={openRuntime}
        subscriptions={state?.subscriptions ?? []}
        agents={openRuntime ? runtimeAgents(openRuntime.name) : []}
        canEdit={canEdit}
        busy={busy}
        onSave={saveRow}
        onOpenChange={(open) => {
          if (!open) setRuntimeName(null);
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
              aria-label={t(($) => $.subscriptions.col_title)}
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

      <Dialog open={journal} onOpenChange={setJournal}>
        <DialogContent className="max-h-[80vh] overflow-y-auto sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>{t(($) => $.subscriptions.events_title)}</DialogTitle>
          </DialogHeader>
          <ul className="space-y-1 text-caption">
            {[...(state?.events ?? [])]
              .sort((a, b) => (b.at ?? "").localeCompare(a.at ?? ""))
              .map((event, index) => (
                <li key={`${event.at}-${index}`} className="flex gap-3">
                  <time className="shrink-0 tabular-nums text-muted-foreground">{formatTime(event.at)}</time>
                  <span className="break-words">{event.text}</span>
                </li>
              ))}
          </ul>
        </DialogContent>
      </Dialog>
    </div>
  );
}
