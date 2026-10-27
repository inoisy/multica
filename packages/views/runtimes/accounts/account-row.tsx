"use client";

import { ChevronDown, ChevronUp } from "lucide-react";
import type { SubscriptionAccount } from "@multica/core/subscriptions";
import type { AvailabilityVerdict, Pool, Subscription } from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";
import { useAvailabilityText } from "../../model-profiles";
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
  journal,
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
  journal: boolean;
}) {
  const { t } = useT("agents");
  const availability = useAvailabilityText();
  const detailId = `account-${sub.key}`;
  const free = verdict !== null && verdict.usable && !verdict.blocked;
  if (reference) {
    return (
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-t px-4 py-2.5 text-caption first:border-t-0" data-testid={`account-row-${sub.key}`}>
        <span className="min-w-0 truncate text-body font-medium">{sub.title}</span>
        <Badge variant="outline">{t(($) => $.subscriptions.same_account)}</Badge>
        {verdict ? <VerdictBadge verdict={verdict} /> : null}
        <span className="text-muted-foreground">{t(($) => $.subscriptions.counted_once)}</span>
      </div>
    );
  }
  return (
    <div className={cn("border-t first:border-t-0", open && "bg-muted/40")} data-testid={`account-row-${sub.key}`}>
      <button
        type="button"
        aria-expanded={open}
        aria-controls={detailId}
        onClick={onToggle}
        className="grid w-full min-w-0 grid-cols-[minmax(0,1fr)_auto] items-start gap-x-4 gap-y-2.5 px-4 py-3 text-left hover:bg-muted/50 focus-visible:outline-2 focus-visible:outline-ring lg:grid-cols-[minmax(0,1.3fr)_minmax(0,1.2fr)_minmax(0,1fr)_9rem_1.25rem] lg:items-center"
      >
        <span className="min-w-0 space-y-1">
          <span className="block break-words text-body font-medium">{sub.title}</span>
          <span className="flex flex-wrap items-center gap-1.5">
            {verdict ? (
              <VerdictBadge verdict={verdict} />
            ) : (
              <Badge variant="outline">{t(($) => $.subscriptions.no_pool)}</Badge>
            )}
            {sub.disabled ? <Badge variant="warning">{t(($) => $.subscriptions.disabled)}</Badge> : null}
          </span>
          <span className="block break-words text-caption text-muted-foreground">
            {pools.length
              ? pools.map(({ pool, position }) => t(($) => $.subscriptions.spent_by_item, { name: pool.name, position })).join(" · ")
              : sub.provider}
          </span>
        </span>
        <span className="col-span-2 min-w-0 lg:col-span-1">
          <UsageMeters account={account} windowName={windowName} compact />
        </span>
        <span className="col-span-2 min-w-0 lg:col-span-1">
          {verdict ? <AvailabilityLane verdict={verdict} horizon={horizon} /> : null}
        </span>
        <span className="col-span-2 min-w-0 text-caption lg:col-span-1">
          {verdict && !free ? <span className="block font-medium">{availability.availableAt(verdict)}</span> : null}
        </span>
        <span className="col-start-2 row-start-1 justify-self-end text-muted-foreground lg:col-start-5" aria-hidden="true">
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
            journal={journal}
          />
        </div>
      ) : null}
    </div>
  );
}
