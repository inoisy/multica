"use client";

import { AlertTriangle } from "lucide-react";
import type { RuntimeProfile, Subscription, SubscriptionDiscrepancy } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../i18n";

const KNOWN_KINDS = [
  "credential_missing",
  "unused_subscription",
  "runtime_without_subscriptions",
  "agent_on_disabled_runtime",
] as const;
type KnownKind = (typeof KNOWN_KINDS)[number];

function isKnownKind(kind: string): kind is KnownKind {
  return (KNOWN_KINDS as readonly string[]).includes(kind);
}

/** «Расхождения»: what the server found out of place in subscriptions, pools and agents of the user's workspaces. */
export function Discrepancies({
  items,
  wsId,
  profiles,
  subscriptions,
  onOpenPool,
}: {
  items: SubscriptionDiscrepancy[];
  wsId: string;
  profiles: RuntimeProfile[];
  subscriptions: Subscription[];
  /** Opens the pool an environment spends. */
  onOpenPool: (poolId: string) => void;
}) {
  const { t } = useT("agents");
  if (!items.length) return null;
  const titleOf = (key: string) => subscriptions.find((s) => s.key === key)?.title ?? key;
  return (
    <section
      className="space-y-2 rounded-md border border-warning/40 bg-warning/10 px-3 py-2"
      aria-labelledby="subscriptions-discrepancies"
    >
      <h2 id="subscriptions-discrepancies" className="flex items-center gap-2 text-body font-medium">
        <AlertTriangle className="size-4 shrink-0 text-warning" aria-hidden="true" />
        {t(($) => $.subscriptions.discrepancies.title)}
      </h2>
      <ul className="space-y-1.5 text-caption">
        {items.map((item, index) => {
          const subject = [
            item.subscription_key ? titleOf(item.subscription_key) : null,
            item.runtime_name,
            item.agent_name,
            item.device_name,
          ]
            .filter(Boolean)
            .join(" · ");
          const poolId =
            item.runtime_profile_id && (!item.workspace_id || item.workspace_id === wsId)
              ? profiles.find((p) => p.id === item.runtime_profile_id)?.pool_id
              : undefined;
          return (
            <li key={`${item.kind}-${index}`} className="flex flex-wrap items-baseline gap-x-2">
              <span className="font-medium">
                {isKnownKind(item.kind)
                  ? t(($) => $.subscriptions.discrepancies.kind[item.kind as KnownKind])
                  : t(($) => $.subscriptions.discrepancies.kind_unknown)}
              </span>
              {subject ? <span>{subject}</span> : null}
              {poolId ? (
                <Button variant="link"
                  type="button"
                  className="h-auto p-0 underline underline-offset-2"
                  onClick={() => onOpenPool(poolId)}
                >
                  {t(($) => $.subscriptions.discrepancies.open_pool)}
                </Button>
              ) : null}
            </li>
          );
        })}
      </ul>
    </section>
  );
}
