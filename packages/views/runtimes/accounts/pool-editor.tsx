"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { poolRevisionConflict, useSetPoolGrants, useUpdatePool } from "@multica/core/subscriptions";
import { workspaceListOptions } from "@multica/core/workspace/queries";
import type { Pool, SpendOrder, Subscription } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../../i18n";
import { useAvailabilityText } from "../../model-profiles";
import { SpendOrderField } from "./spend-order-field";
import { SubscriptionOrderList } from "./subscription-order-list";
import { VerdictBadge } from "./verdict-badge";

function errorText(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

interface Draft {
  name: string;
  order: string[];
  spend: SpendOrder;
  allWorkspaces: boolean;
  workspaceIds: string[];
}

function draftOf(pool: Pool): Draft {
  return {
    name: pool.name,
    order: pool.subscriptions.map((account) => account.key),
    spend: pool.spend_order,
    allWorkspaces: pool.all_workspaces,
    workspaceIds: pool.workspace_ids,
  };
}

function sameList(a: string[], b: string[]) {
  return a.length === b.length && a.every((value, index) => value === b[index]);
}

function sameSet(a: string[], b: string[]) {
  return a.length === b.length && a.every((value) => b.includes(value));
}

/**
 * The one editor of a pool: the order its accounts are spent in, how they are
 * spent, and the workspaces it is granted to. The subscriptions page and an
 * environment page both render it, so there is one composition by id and one
 * mutation. Every write carries the revision the editor started from; a pool
 * that moved on is refused and the person decides whether to take the new one.
 */
export function PoolEditor({
  pool,
  subscriptions,
  canManage,
  onSaved,
}: {
  pool: Pool;
  subscriptions: Subscription[];
  canManage: boolean;
  onSaved?: () => void;
}) {
  const { t } = useT("agents");
  const availability = useAvailabilityText();
  const workspaces = useQuery(workspaceListOptions()).data ?? [];
  const updatePool = useUpdatePool();
  const setGrants = useSetPoolGrants();
  const [draft, setDraft] = useState<Draft>(() => draftOf(pool));
  // The revision the draft was started from: what a write is checked against.
  const [base, setBase] = useState({ id: pool.id, revision: pool.revision });
  const busy = updatePool.isPending || setGrants.isPending;

  const original = useMemo(() => draftOf(pool), [pool]);
  const dirty =
    draft.name.trim() !== original.name ||
    !sameList(draft.order, original.order) ||
    draft.spend !== original.spend ||
    draft.allWorkspaces !== original.allWorkspaces ||
    (!draft.allWorkspaces && !sameSet(draft.workspaceIds, original.workspaceIds));
  const stale = base.id === pool.id && pool.revision !== base.revision;

  // Take the server's pool when it is another pool, or it changed under an untouched editor.
  useEffect(() => {
    if (base.id !== pool.id || (!dirty && pool.revision !== base.revision)) {
      setDraft(draftOf(pool));
      setBase({ id: pool.id, revision: pool.revision });
    }
  }, [pool, dirty, base]);

  const accountByKey = useMemo(() => new Map(pool.subscriptions.map((account) => [account.key, account])), [pool]);
  const titleOf = (key: string) => accountByKey.get(key)?.title ?? subscriptions.find((s) => s.key === key)?.title ?? key;
  const addable = subscriptions.filter((s) => s.family === pool.runtime_type && !draft.order.includes(s.key));

  const takeServerPool = () => {
    setDraft(draftOf(pool));
    setBase({ id: pool.id, revision: pool.revision });
  };

  const save = async () => {
    try {
      const name = draft.name.trim();
      const composition = !sameList(draft.order, original.order);
      const patch = {
        revision: base.revision,
        ...(name && name !== original.name ? { name } : {}),
        ...(composition ? { subscriptions: draft.order } : {}),
        ...(draft.spend !== original.spend ? { spend_order: draft.spend } : {}),
        ...(draft.allWorkspaces !== original.allWorkspaces ? { all_workspaces: draft.allWorkspaces } : {}),
      };
      let updated = pool;
      if (Object.keys(patch).length > 1) {
        updated = await updatePool.mutateAsync({ id: pool.id, input: patch });
      }
      if (!draft.allWorkspaces && !sameSet(draft.workspaceIds, original.workspaceIds)) {
        updated = await setGrants.mutateAsync({ id: pool.id, input: { revision: updated.revision, workspace_ids: draft.workspaceIds } });
      }
      toast.success(t(($) => $.subscriptions.saved));
      onSaved?.();
    } catch (error) {
      const current = poolRevisionConflict(error);
      if (current !== null) {
        toast.error(t(($) => $.subscriptions.pool.conflict, { revision: current }));
      } else {
        toast.error(errorText(error));
      }
    }
  };

  const toggleWorkspace = (id: string, checked: boolean) =>
    setDraft({ ...draft, workspaceIds: checked ? [...draft.workspaceIds, id] : draft.workspaceIds.filter((x) => x !== id) });

  const environments = pool.environments;

  return (
    <div className="min-w-0 space-y-4" data-testid="pool-editor" data-revision={pool.revision}>
      <div className="flex flex-wrap items-center gap-2">
        <VerdictBadge verdict={pool.availability} />
        {!pool.availability.usable ? (
          <span className="text-caption text-muted-foreground">{availability.availableAt(pool.availability)}</span>
        ) : null}
        <span className="text-caption text-muted-foreground">{t(($) => $.subscriptions.pool.revision, { revision: pool.revision })}</span>
      </div>

      {stale && dirty ? (
        <p role="alert" className="flex flex-wrap items-center gap-2 rounded-md border border-warning/40 bg-warning/10 px-3 py-2 text-caption">
          <span>{t(($) => $.subscriptions.pool.moved_on, { revision: pool.revision })}</span>
          <Button type="button" size="sm" variant="outline" onClick={takeServerPool}>
            {t(($) => $.subscriptions.pool.take_current)}
          </Button>
        </p>
      ) : null}

      {canManage ? (
        <label className="flex flex-col gap-1 text-caption">
          <span className="text-muted-foreground">{t(($) => $.subscriptions.pool.name)}</span>
          <Input value={draft.name} disabled={busy} onChange={(event) => setDraft({ ...draft, name: event.target.value })} />
        </label>
      ) : null}

      <SubscriptionOrderList
        order={draft.order}
        onChange={(order) => setDraft({ ...draft, order })}
        titleOf={titleOf}
        verdictOf={(key) => accountByKey.get(key)}
        addable={addable}
        editable={canManage}
        disabled={busy}
      />

      <SpendOrderField
        name={`pool-spend-order-${pool.id}`}
        value={draft.spend}
        onChange={(spend) => setDraft({ ...draft, spend })}
        disabled={!canManage || busy}
      />

      <fieldset className="min-w-0 space-y-2" disabled={!canManage || busy}>
        <legend className="mb-2 text-body font-medium">{t(($) => $.subscriptions.pool.grants_title)}</legend>
        {canManage ? (
          <>
            <label className="flex items-center gap-2 text-body">
              <Checkbox checked={draft.allWorkspaces} onCheckedChange={(checked) => setDraft({ ...draft, allWorkspaces: checked })} />
              {t(($) => $.subscriptions.new_pool.all_workspaces)}
            </label>
            {!draft.allWorkspaces ? (
              <ul className="space-y-1 pl-6">
                {workspaces.map((ws) => (
                  <li key={ws.id}>
                    <label className="flex items-center gap-2 text-body">
                      <Checkbox checked={draft.workspaceIds.includes(ws.id)} onCheckedChange={(checked) => toggleWorkspace(ws.id, checked)} />
                      {ws.name}
                    </label>
                  </li>
                ))}
              </ul>
            ) : null}
          </>
        ) : (
          <p className="text-body">
            {pool.all_workspaces
              ? t(($) => $.subscriptions.new_pool.all_workspaces)
              : workspaces.filter((ws) => pool.workspace_ids.includes(ws.id)).map((ws) => ws.name).join(", ") || "—"}
          </p>
        )}
        <p className="text-caption text-muted-foreground">
          {environments.length
            ? t(($) => $.subscriptions.pool.environments, { names: environments.map((env) => env.display_name).join(", ") })
            : t(($) => $.subscriptions.pool.no_environments)}
        </p>
      </fieldset>

      {canManage ? (
        <div className="flex flex-wrap justify-end gap-2">
          {dirty ? (
            <Button type="button" variant="outline" disabled={busy} onClick={takeServerPool}>
              {t(($) => $.subscriptions.pool.discard)}
            </Button>
          ) : null}
          <Button type="button" disabled={busy || !dirty || !draft.name.trim()} aria-busy={busy} onClick={() => void save()}>
            {t(($) => $.subscriptions.save)}
          </Button>
        </div>
      ) : null}
    </div>
  );
}
