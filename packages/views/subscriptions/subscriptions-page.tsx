"use client";

import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Gauge, Pencil, RefreshCw } from "lucide-react";
import { toast } from "sonner";
import { useWorkspaceSlug } from "@multica/core/paths";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { cn } from "@multica/ui/lib/utils";
import { CollectionPageHeader } from "../layout/collection-page";
import { PAGE_GUTTER } from "../layout/page-header";
import { useT } from "../i18n";
import { ControlError, controlRequest } from "../conveyor/control-api";

interface UsageWindow {
  key: string | null;
  label: string;
  used: number | null;
  cap: number | null;
  reset: string | null;
  blocked: boolean;
}

interface Subscription {
  id: string;
  provider: string;
  title: string;
  quota_row: string | null;
  windows: string[];
  caps: Record<string, number>;
  reserve: number;
  effective: Record<string, number>;
  credential: string | null;
  changed_at: string | null;
  changed_by: string | null;
  usage: UsageWindow[];
  error: string | null;
}

interface SubscriptionsState {
  subscriptions: Subscription[];
  stale: boolean;
  can_edit: boolean;
  events: { at: string; kind: string; text: string }[];
  window_names: Record<string, string>;
}

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
  const [editing, setEditing] = useState<string | null>(null);
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);

  const windowName = (key: string) => state?.window_names[key] ?? key;
  const loadError =
    query.error instanceof ControlError
      ? query.error.status === 401
        ? t(($) => $.subscriptions.login_required)
        : query.error.message
      : query.error
        ? String(query.error)
        : null;

  const startEdit = (sub: Subscription) => {
    setEditing(sub.id);
    setDraft({
      ...Object.fromEntries(sub.windows.map((key) => [key, String(sub.caps[key] ?? 100)])),
      reserve: String(sub.reserve),
    });
  };

  const save = async (sub: Subscription) => {
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
      await queryClient.invalidateQueries({ queryKey });
    } catch (error) {
      toast.error(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex h-full min-h-0 flex-col">
      <CollectionPageHeader
        icon={Gauge}
        title={t(($) => $.subscriptions.title)}
        description={t(($) => $.subscriptions.description)}
        actions={
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label={t(($) => $.subscriptions.refresh)}
            onClick={() => void query.refetch()}
            disabled={query.isFetching}
          >
            <RefreshCw className={query.isFetching ? "animate-spin" : undefined} />
          </Button>
        }
      />

      <div className={cn("min-h-0 flex-1 overflow-y-auto py-4", PAGE_GUTTER)}>
        <div className="space-y-4">
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
          {state && !state.can_edit ? (
            <p className="text-caption text-muted-foreground">{t(($) => $.subscriptions.readonly)}</p>
          ) : null}

          <div className="grid gap-3 lg:grid-cols-2">
            {(state?.subscriptions ?? []).map((sub) => {
              const isEditing = editing === sub.id;
              const limited = Object.keys(sub.effective).length > 0;
              return (
                <section key={sub.id} className="flex flex-col gap-3 rounded-lg border p-4">
                  <div className="flex items-start justify-between gap-3">
                    <div className="flex min-w-0 flex-wrap items-center gap-2">
                      <h2 className="text-body font-medium">{sub.title}</h2>
                      <Badge variant="outline" className="font-mono">
                        {sub.id}
                      </Badge>
                    </div>
                    {state?.can_edit && sub.windows.length && !isEditing ? (
                      <Button size="sm" variant="ghost" onClick={() => startEdit(sub)}>
                        <Pencil />
                        {t(($) => $.subscriptions.edit)}
                      </Button>
                    ) : null}
                  </div>

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
                        void save(sub);
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
                  ) : (
                    <p className="text-caption">
                      {limited
                        ? t(($) => $.subscriptions.agents_up_to, {
                            limits: Object.entries(sub.effective)
                              .map(([key, cap]) => `${windowName(key)} ${cap}%`)
                              .join(", "),
                          })
                        : t(($) => $.subscriptions.provider_limit)}
                      {sub.reserve ? ` · ${t(($) => $.subscriptions.reserve, { reserve: sub.reserve })}` : ""}
                    </p>
                  )}

                  <p className="text-caption text-muted-foreground">
                    {sub.credential ? (
                      <>
                        {t(($) => $.subscriptions.credential)} <code className="font-mono">{sub.credential}</code>
                      </>
                    ) : null}
                    {sub.changed_at
                      ? ` · ${t(($) => $.subscriptions.changed, { time: formatTime(sub.changed_at), who: sub.changed_by ?? "" })}`
                      : ""}
                  </p>
                </section>
              );
            })}
          </div>

          {state?.events.length ? (
            <section className="space-y-1">
              <h2 className="text-body font-medium">{t(($) => $.subscriptions.events_title)}</h2>
              <ul className="space-y-0.5 text-caption">
                {[...state.events]
                  .sort((a, b) => (b.at ?? "").localeCompare(a.at ?? ""))
                  .map((event, index) => (
                    <li key={`${event.at}-${index}`} className="flex gap-3">
                      <time className="shrink-0 tabular-nums text-muted-foreground">{formatTime(event.at)}</time>
                      <span>{event.text}</span>
                    </li>
                  ))}
              </ul>
            </section>
          ) : null}
        </div>
      </div>
    </div>
  );
}
