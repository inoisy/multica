"use client";

import { useQuery } from "@tanstack/react-query";
import { workspaceListOptions } from "@multica/core/workspace/queries";
import type { SpendOrder } from "@multica/core/types";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../i18n";
import { SpendOrderField } from "./spend-order-field";

export interface PoolTarget {
  name: string;
  allWorkspaces: boolean;
  workspaceIds: string[];
  spendOrder: SpendOrder;
}

export function emptyPoolTarget(currentWorkspaceId: string): PoolTarget {
  return { name: "", allWorkspaces: false, workspaceIds: [currentWorkspaceId], spendOrder: "first" };
}

export function poolTargetReady(target: PoolTarget): boolean {
  return target.name.trim().length > 0 && (target.allWorkspaces || target.workspaceIds.length > 0);
}

/** Workspaces part of `POST /api/pools`. */
export function poolTargetScope(target: PoolTarget): { all_workspaces: true } | { workspace_ids: string[] } {
  return target.allWorkspaces ? { all_workspaces: true } : { workspace_ids: target.workspaceIds };
}

/** Name, grants and spend order of a new pool. A grant creates the pool's environment in that workspace. */
export function PoolTargetFields({
  idPrefix,
  value,
  onChange,
  disabled,
}: {
  idPrefix: string;
  value: PoolTarget;
  onChange: (value: PoolTarget) => void;
  disabled?: boolean;
}) {
  const { t } = useT("agents");
  const workspaces = useQuery(workspaceListOptions()).data ?? [];
  const toggleWorkspace = (id: string, checked: boolean) =>
    onChange({
      ...value,
      workspaceIds: checked ? [...value.workspaceIds, id] : value.workspaceIds.filter((x) => x !== id),
    });
  return (
    <div className="space-y-4">
      <div className="space-y-1">
        <label htmlFor={`${idPrefix}-name`} className="text-body font-medium">
          {t(($) => $.subscriptions.new_pool.name)}
        </label>
        <Input
          id={`${idPrefix}-name`}
          value={value.name}
          disabled={disabled}
          placeholder={t(($) => $.subscriptions.new_pool.name_placeholder)}
          onChange={(event) => onChange({ ...value, name: event.target.value })}
        />
      </div>
      <fieldset className="min-w-0 space-y-2" disabled={disabled}>
        <legend className="mb-2 text-body font-medium">{t(($) => $.subscriptions.new_pool.workspaces)}</legend>
        <label className="flex items-center gap-2 text-body">
          <Checkbox
            disabled={disabled}
            checked={value.allWorkspaces}
            onCheckedChange={(checked) => onChange({ ...value, allWorkspaces: checked })}
          />
          {t(($) => $.subscriptions.new_pool.all_workspaces)}
        </label>
        {!value.allWorkspaces ? (
          <ul className="space-y-1 pl-6">
            {workspaces.map((ws) => (
              <li key={ws.id}>
                <label className="flex items-center gap-2 text-body">
                  <Checkbox
                    disabled={disabled}
                    checked={value.workspaceIds.includes(ws.id)}
                    onCheckedChange={(checked) => toggleWorkspace(ws.id, checked)}
                  />
                  {ws.name}
                </label>
              </li>
            ))}
          </ul>
        ) : null}
      </fieldset>
      <SpendOrderField
        name={`${idPrefix}-spend-order`}
        value={value.spendOrder}
        onChange={(spendOrder) => onChange({ ...value, spendOrder })}
        disabled={disabled}
      />
    </div>
  );
}
