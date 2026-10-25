"use client";

import { accountLimit, windowBlocked, type SubscriptionAccount } from "@multica/core/subscriptions";
import { Badge } from "@multica/ui/components/ui/badge";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../i18n";
import { formatModelTime } from "../model-profiles/format-time";

/** Figures older than this are marked with the time they were taken. */
const OLD_USAGE_MS = 10 * 60_000;

/** Window load: the fill is spent, the hatched end is what people keep, the tick is the agents' limit. */
export function UsageBar({ used, limit, blocked, label }: { used: number; limit: number | undefined; blocked: boolean; label: string }) {
  const near = limit !== undefined && used >= limit * 0.8;
  const hasLimit = limit !== undefined && limit < 100;
  return (
    <div
      role="meter"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={Math.round(Math.min(100, Math.max(0, used)))}
      className="relative h-2 w-full overflow-hidden rounded-full bg-muted"
    >
      {hasLimit ? (
        <div
          className="absolute inset-y-0 right-0 [background-image:repeating-linear-gradient(135deg,transparent_0_3px,color-mix(in_srgb,var(--foreground)_16%,transparent)_3px_5px)]"
          style={{ width: `${100 - limit}%` }}
          aria-hidden="true"
        />
      ) : null}
      <div
        className={cn("relative h-full rounded-full", blocked ? "bg-destructive" : near ? "bg-warning" : "bg-primary")}
        style={{ width: `${Math.min(100, Math.max(0, used))}%` }}
      />
      {hasLimit ? <div className="absolute top-0 h-full w-0.5 bg-foreground/70" style={{ left: `${limit}%` }} aria-hidden="true" /> : null}
    </div>
  );
}

/** Every window of an account: percent used, the agents' limit, the reset and when the figures were taken. */
export function UsageMeters({
  account,
  windowName,
  compact = false,
}: {
  account: SubscriptionAccount;
  windowName: (window: string) => string;
  compact?: boolean;
}) {
  const { t } = useT("agents");
  if (!account.usage.length) {
    return <p className="text-caption text-muted-foreground">{t(($) => $.subscriptions.no_usage)}</p>;
  }
  const oldest = Math.min(...account.usage.map((w) => Date.parse(w.observed_at) || Date.now()));
  return (
    <div className="space-y-2">
      {!compact && Date.now() - oldest > OLD_USAGE_MS ? (
        <p className="text-caption text-muted-foreground">
          {t(($) => $.subscriptions.usage_as_of, { time: formatModelTime(new Date(oldest).toISOString()) })}
        </p>
      ) : null}
      <ul className="space-y-2">
        {account.usage.map((w) => {
          const limit = accountLimit(account.subscriptions, w.window);
          const blocked = windowBlocked(w, limit);
          return (
            <li key={w.window} className="space-y-1 text-caption">
              <div className="flex flex-wrap items-baseline justify-between gap-x-3">
                <span>
                  {windowName(w.window)}
                  {blocked && !compact ? (
                    <Badge variant="destructive" className="ml-2">
                      {t(($) => $.subscriptions.limit_reached)}
                    </Badge>
                  ) : null}
                </span>
                <span className="tabular-nums text-muted-foreground">
                  {w.used_percent === null ? "—" : `${Math.round(w.used_percent)}%`}
                  {limit !== undefined && limit < 100 ? ` ${t(($) => $.subscriptions.of_cap, { cap: limit })}` : ""}
                  {!compact && w.resets_at ? ` · ${t(($) => $.subscriptions.reset, { time: formatModelTime(w.resets_at) })}` : ""}
                </span>
              </div>
              {w.used_percent !== null ? (
                <UsageBar used={w.used_percent} limit={limit} blocked={blocked} label={windowName(w.window)} />
              ) : null}
            </li>
          );
        })}
      </ul>
    </div>
  );
}
