"use client";

import { useEffect, useState } from "react";
import { toast } from "sonner";
import { useCreatePool } from "@multica/core/subscriptions";
import type { Subscription, SubscriptionFamily } from "@multica/core/types";
import { RadioGroup, RadioGroupItem } from "@multica/ui/components/ui/radio-group";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";
import { emptyPoolTarget, PoolTargetFields, poolTargetReady, poolTargetScope, type PoolTarget } from "./pool-target-fields";
import { SubscriptionOrderList } from "./subscription-order-list";

const FAMILIES: SubscriptionFamily[] = ["claude", "codex"];

/** A new pool of accounts, granted to one, several or all workspaces. */
export function NewPoolDialog({
  open,
  onOpenChange,
  wsId,
  subscriptions,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  wsId: string;
  subscriptions: Subscription[];
}) {
  const { t } = useT("agents");
  const create = useCreatePool();
  const [family, setFamily] = useState<SubscriptionFamily>("claude");
  const [order, setOrder] = useState<string[]>([]);
  const [target, setTarget] = useState<PoolTarget>(() => emptyPoolTarget(wsId));

  useEffect(() => {
    if (!open) return;
    setFamily("claude");
    setOrder([]);
    setTarget(emptyPoolTarget(wsId));
  }, [open, wsId]);

  const titleOf = (key: string) => subscriptions.find((s) => s.key === key)?.title ?? key;
  const addable = subscriptions.filter((s) => s.family === family && !order.includes(s.key));

  const submit = async () => {
    try {
      await create.mutateAsync({
        name: target.name.trim(),
        runtime_type: family,
        subscriptions: order,
        spend_order: target.spendOrder,
        ...poolTargetScope(target),
      });
      toast.success(t(($) => $.subscriptions.new_pool.done, { name: target.name.trim() }));
      onOpenChange(false);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : String(error));
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{t(($) => $.subscriptions.new_pool.title)}</DialogTitle>
        </DialogHeader>
        <form
          className="space-y-5"
          onSubmit={(event) => {
            event.preventDefault();
            void submit();
          }}
        >
          <fieldset className="min-w-0 space-y-2" disabled={create.isPending}>
            <legend className="mb-2 text-body font-medium">{t(($) => $.subscriptions.new_pool.family)}</legend>
            <RadioGroup value={family} disabled={create.isPending} onValueChange={(value) => { setFamily(value); setOrder([]); }} className="grid gap-2 sm:grid-cols-2">
              {FAMILIES.map((value) => (
                <label
                  key={value}
                  className={cn(
                    "flex cursor-pointer items-center gap-2 rounded-md border px-3 py-2 text-body",
                    family === value && "border-foreground ring-1 ring-foreground",
                  )}
                >
                  <RadioGroupItem value={value} />
                  {t(($) => $.subscriptions.groups[value])}
                </label>
              ))}
            </RadioGroup>
          </fieldset>
          <PoolTargetFields idPrefix="new-pool" value={target} onChange={setTarget} disabled={create.isPending} />
          <SubscriptionOrderList
            order={order}
            onChange={setOrder}
            titleOf={titleOf}
            addable={addable}
            editable
            disabled={create.isPending}
          />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {t(($) => $.subscriptions.cancel)}
            </Button>
            <Button type="submit" disabled={create.isPending || !poolTargetReady(target)}>
              {t(($) => $.subscriptions.new_pool.submit)}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
