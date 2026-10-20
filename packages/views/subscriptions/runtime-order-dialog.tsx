"use client";

import { useEffect, useState } from "react";
import { ArrowDown, ArrowUp, X } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { useT } from "../i18n";
import { moveItem, type RuntimeRow } from "./subscription-plan";
import type { SaveRow, Subscription } from "./subscription-types";

/**
 * One runtime of the Subscriptions page: which accounts stand behind it, in
 * which order, and how they are spent. Opened from «Тратят» on a card.
 */
export function RuntimeOrderDialog({
  runtime,
  subscriptions,
  agents,
  canEdit,
  busy,
  onSave,
  onOpenChange,
}: {
  runtime: RuntimeRow | null;
  subscriptions: Subscription[];
  agents: string[];
  canEdit: boolean;
  busy: boolean;
  onSave: SaveRow;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useT("agents");
  const [order, setOrder] = useState<string[]>([]);
  const [mode, setMode] = useState("first");

  useEffect(() => {
    setOrder(runtime?.subscriptions ?? []);
    setMode(runtime?.order || "first");
  }, [runtime]);

  const title = (id: string) => subscriptions.find((s) => s.id === id)?.title ?? id;
  const providers = new Set(order.map((id) => subscriptions.find((s) => s.id === id)?.provider).filter(Boolean));
  const addable = subscriptions.filter(
    (s) => !order.includes(s.id) && (providers.size === 0 || providers.has(s.provider)),
  );

  const save = async () => {
    if (!runtime) return;
    const ok = await onSave("runtimes", runtime.name, { command: runtime.command, subscriptions: order, order: mode });
    if (ok) onOpenChange(false);
  };

  return (
    <Dialog open={runtime !== null} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{runtime?.name}</DialogTitle>
          <DialogDescription>
            {agents.length
              ? t(($) => $.subscriptions.runtime_dialog.agents, { names: agents.join(", ") })
              : t(($) => $.subscriptions.runtime_dialog.no_agents)}
          </DialogDescription>
        </DialogHeader>

        <section className="space-y-2" aria-labelledby="runtime-order-title">
          <h3 id="runtime-order-title" className="text-body font-medium">
            {t(($) => $.subscriptions.runtime_dialog.order_title)}
          </h3>
          {order.length === 0 ? (
            <p className="text-caption text-muted-foreground">{t(($) => $.subscriptions.runtime_dialog.empty)}</p>
          ) : (
            <ol className="space-y-1.5">
              {order.map((id, index) => (
                <li key={id} className="flex items-center gap-2 rounded-md border px-3 py-2">
                  <span className="w-5 text-caption text-muted-foreground tabular-nums">{index + 1}</span>
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-body">{title(id)}</span>
                    <span className="block font-mono text-caption text-muted-foreground">{id}</span>
                  </span>
                  {canEdit ? (
                    <span className="flex gap-0.5">
                      <Button
                        size="icon-sm"
                        variant="ghost"
                        aria-label={t(($) => $.subscriptions.runtime_dialog.move_up, { name: title(id) })}
                        disabled={busy || index === 0}
                        onClick={() => setOrder(moveItem(order, index, -1))}
                      >
                        <ArrowUp />
                      </Button>
                      <Button
                        size="icon-sm"
                        variant="ghost"
                        aria-label={t(($) => $.subscriptions.runtime_dialog.move_down, { name: title(id) })}
                        disabled={busy || index === order.length - 1}
                        onClick={() => setOrder(moveItem(order, index, 1))}
                      >
                        <ArrowDown />
                      </Button>
                      <Button
                        size="icon-sm"
                        variant="ghost"
                        aria-label={t(($) => $.subscriptions.runtime_dialog.remove, { name: title(id) })}
                        disabled={busy}
                        onClick={() => setOrder(order.filter((x) => x !== id))}
                      >
                        <X />
                      </Button>
                    </span>
                  ) : null}
                </li>
              ))}
            </ol>
          )}
          {canEdit && addable.length ? (
            <label className="flex flex-col gap-1 text-caption">
              <span className="text-muted-foreground">{t(($) => $.subscriptions.runtime_dialog.add)}</span>
              <select
                className="h-8 rounded-md border bg-background px-2 text-body"
                value=""
                disabled={busy}
                onChange={(event) => {
                  if (event.target.value) setOrder([...order, event.target.value]);
                }}
              >
                <option value="">{t(($) => $.subscriptions.runtime_dialog.add_placeholder)}</option>
                {addable.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.title} ({s.id})
                  </option>
                ))}
              </select>
            </label>
          ) : null}
        </section>

        <fieldset className="space-y-2" disabled={!canEdit || busy}>
          <legend className="mb-2 text-body font-medium">{t(($) => $.subscriptions.runtime_dialog.spend_title)}</legend>
          {(["first", "balanced"] as const).map((value) => (
            <label key={value} className="flex cursor-pointer items-start gap-2 rounded-md border px-3 py-2">
              <input
                type="radio"
                name="runtime-order-mode"
                className="mt-1"
                checked={mode === value}
                onChange={() => setMode(value)}
              />
              <span>
                <span className="block text-body">
                  {value === "first" ? t(($) => $.subscriptions.order_first) : t(($) => $.subscriptions.order_balanced)}
                </span>
                <span className="block text-caption text-muted-foreground">
                  {value === "first"
                    ? t(($) => $.subscriptions.runtime_dialog.first_hint)
                    : t(($) => $.subscriptions.runtime_dialog.balanced_hint)}
                </span>
              </span>
            </label>
          ))}
        </fieldset>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t(($) => $.subscriptions.cancel)}
          </Button>
          {canEdit ? (
            <Button disabled={busy} onClick={() => void save()}>
              {t(($) => $.subscriptions.save)}
            </Button>
          ) : null}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
