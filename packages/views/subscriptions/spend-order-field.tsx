"use client";

import type { SpendOrder } from "@multica/core/types";
import { useT } from "../i18n";

const ORDERS: SpendOrder[] = ["first", "balanced"];

/** How a pool spends its subscriptions. */
export function SpendOrderField({
  name,
  value,
  onChange,
  disabled,
}: {
  name: string;
  value: SpendOrder;
  onChange: (value: SpendOrder) => void;
  disabled?: boolean;
}) {
  const { t } = useT("agents");
  return (
    <fieldset className="space-y-2" disabled={disabled}>
      <legend className="mb-2 text-body font-medium">{t(($) => $.subscriptions.pool.spend_title)}</legend>
      {ORDERS.map((order) => (
        <label key={order} className="flex cursor-pointer items-start gap-2 rounded-md border px-3 py-2">
          <input type="radio" name={name} className="mt-1" checked={value === order} onChange={() => onChange(order)} />
          <span>
            <span className="block text-body">{t(($) => $.subscriptions.order[order])}</span>
            <span className="block text-caption text-muted-foreground">
              {t(($) => $.subscriptions.order_hint[order])}
            </span>
          </span>
        </label>
      ))}
    </fieldset>
  );
}
