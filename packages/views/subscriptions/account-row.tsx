"use client";

import { ChevronDown, ChevronUp } from "lucide-react";
import type { SubscriptionAccount } from "@multica/core/subscriptions";
import type { AvailabilityVerdict, Pool, Subscription } from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../i18n";
import { useAvailabilityText } from "../model-profiles";
import { AccountDetail } from "./account-detail";
import { AvailabilityLane, type LaneHorizon } from "./availability-lane";
import { UsageMeters } from "./usage-meters";
import { VerdictBadge } from "./verdict-badge";

/**
 * One account in the overview: state, windows, the availability lane and the
 * time it frees up, in a row that opens into the account's card. An account
 * the pool shows a second time is a reference to the same account, not another one.
 */
export function AccountRow({
  sub,
  account,
  verdict,
  pools,
  reference,
  open,
  onToggle,
  horizon,
  wsId,
  canManage,
  windowName,
  onOpenPool,
}: {
  sub: Subscription;
  account: SubscriptionAccount;
  /** The server's verdict on the account; null while it is in no pool. */
  verdict: AvailabilityVerdict | null;
  pools: { pool: Pool; position: number }[];
  /** The account already appears under an earlier pool. */
  reference: boolean;
  open: boolean;
  onToggle: () => void;
  horizon: LaneHorizon;
  wsId: string;
  canManage: boolean;
  windowName: (window: string) => string;
  onOpenPool: (poolId: string) => void;
}) {
  const { t } = useT("agents");
  const availability = useAvailabilityText();
  const detailId = `account-${sub.key}`;
  return (
    <div className={cn("border-t first:border-t-0", open && "bg-muted/40")} data-testid={`account-row-${sub.key}`}>
      <button
        type="button"
        aria-expanded={open}
        aria-controls={detailId}
        onClick={onToggle}
        className="grid w-full min-w-0 grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 gap-y-2 px-4 py-3 text-left hover:bg-muted/50 focus-visible:outline-2 focus-visible:outline-ring lg:grid-cols-[minmax(0,1.3fr)_minmax(0,1.2fr)_minmax(0,1fr)_9rem_1.25rem]"
      >
        <span className="min-w-0 space-y-1">
          <span className="block truncate text-body font-medium">{sub.title}</span>
          <span className="flex flex-wrap items-center gap-1.5">
            {verdict ? (
              <VerdictBadge verdict={verdict} />
            ) : (
              <Badge variant="outline">{t(($) => $.subscriptions.no_pool)}</Badge>
            )}
            {sub.disabled ? <Badge variant="warning">{t(($) => $.subscriptions.disabled)}</Badge> : null}
            {reference ? <Badge variant="outline">{t(($) => $.subscriptions.same_account)}</Badge> : null}
          </span>
          <span className="block truncate text-caption text-muted-foreground">
            {pools.length
              ? pools.map(({ pool, position }) => t(($) => $.subscriptions.spent_by_item, { name: pool.name, position })).join(" · ")
              : sub.provider}
          </span>
        </span>
        <span className="order-last col-span-2 min-w-0 lg:order-none lg:col-span-1">
          <UsageMeters account={account} windowName={windowName} compact />
        </span>
        <span className="order-last col-span-2 min-w-0 lg:order-none lg:col-span-1">
          {verdict ? <AvailabilityLane verdict={verdict} horizon={horizon} /> : null}
        </span>
        <span className="min-w-0 text-caption">
          {verdict ? (
            <span className={cn("block font-medium", verdict.usable && !verdict.blocked && "text-success")}>
              {verdict.usable && !verdict.blocked ? availability.describe(verdict) : availability.availableAt(verdict)}
            </span>
          ) : null}
        </span>
        <span className="justify-self-end text-muted-foreground" aria-hidden="true">
          {open ? <ChevronUp className="size-4" /> : <ChevronDown className="size-4" />}
        </span>
      </button>
      {open ? (
        <div id={detailId}>
          <AccountDetail
            sub={sub}
            account={account}
            pools={pools}
            wsId={wsId}
            canManage={canManage}
            windowName={windowName}
            onOpenPool={onOpenPool}
          />
        </div>
      ) : null}
    </div>
  );
}
