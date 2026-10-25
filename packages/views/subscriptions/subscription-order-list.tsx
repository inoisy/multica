"use client";

import { ArrowDown, ArrowUp, X } from "lucide-react";
import { moveItem } from "@multica/core/subscriptions";
import type { AvailabilityVerdict, Subscription } from "@multica/core/types";
import { SimpleSelect } from "@multica/ui/components/common/simple-select";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../i18n";
import { VerdictBadge } from "./verdict-badge";

/** Subscriptions of a pool in spending order; editable lists move, remove and add. */
export function SubscriptionOrderList({
  order,
  onChange,
  titleOf,
  missing,
  verdictOf,
  addable,
  editable,
  disabled,
}: {
  order: string[];
  onChange: (order: string[]) => void;
  titleOf: (key: string) => string;
  missing?: Set<string>;
  /** The server's verdict on an account of the pool; accounts it has none for show no badge. */
  verdictOf?: (key: string) => AvailabilityVerdict | undefined;
  addable: Subscription[];
  editable: boolean;
  disabled?: boolean;
}) {
  const { t } = useT("agents");
  return (
    <section className="space-y-2" aria-label={t(($) => $.subscriptions.pool.order_title)}>
      <h3 className="text-body font-medium">{t(($) => $.subscriptions.pool.order_title)}</h3>
      {order.length === 0 ? (
        <p className="text-caption text-muted-foreground">{t(($) => $.subscriptions.pool.empty)}</p>
      ) : (
        <ol className="space-y-1.5">
          {order.map((key, index) => (
            <li key={key} className="flex items-center gap-2 rounded-md border px-3 py-2">
              <span className="w-5 text-caption text-muted-foreground tabular-nums">{index + 1}</span>
              <span className="min-w-0 flex-1 space-y-0.5">
                <span className="block break-words text-body">{titleOf(key)}</span>
                <span className="block font-mono text-caption text-muted-foreground">
                  {key}
                  {missing?.has(key) ? ` · ${t(($) => $.subscriptions.pool.missing)}` : ""}
                </span>
                {verdictOf?.(key) ? <VerdictBadge verdict={verdictOf(key)!} /> : null}
              </span>
              {editable ? (
                <span className="flex gap-0.5">
                  <Button
                    type="button"
                    size="icon-sm"
                    variant="ghost"
                    aria-label={t(($) => $.subscriptions.pool.move_up, { name: titleOf(key) })}
                    disabled={disabled || index === 0}
                    onClick={() => onChange(moveItem(order, index, -1))}
                  >
                    <ArrowUp />
                  </Button>
                  <Button
                    type="button"
                    size="icon-sm"
                    variant="ghost"
                    aria-label={t(($) => $.subscriptions.pool.move_down, { name: titleOf(key) })}
                    disabled={disabled || index === order.length - 1}
                    onClick={() => onChange(moveItem(order, index, 1))}
                  >
                    <ArrowDown />
                  </Button>
                  <Button
                    type="button"
                    size="icon-sm"
                    variant="ghost"
                    aria-label={t(($) => $.subscriptions.pool.remove, { name: titleOf(key) })}
                    disabled={disabled}
                    onClick={() => onChange(order.filter((x) => x !== key))}
                  >
                    <X />
                  </Button>
                </span>
              ) : null}
            </li>
          ))}
        </ol>
      )}
      {editable && addable.length ? (
        <label className="flex flex-col gap-1 text-caption">
          <span className="text-muted-foreground">{t(($) => $.subscriptions.pool.add)}</span>
          <SimpleSelect
            className="h-8 w-full"
            value=""
            disabled={disabled}
            aria-label={t(($) => $.subscriptions.pool.add)}
            onValueChange={(key) => { if (key) onChange([...order, key]); }}
            items={[
              { value: "", label: t(($) => $.subscriptions.pool.add_placeholder) },
              ...addable.map((sub) => ({ value: sub.key, label: `${sub.title} (${sub.key})` })),
            ]}
          />
        </label>
      ) : null}
    </section>
  );
}
