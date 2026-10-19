"use client";

import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { issueStageDispatchOptions, useSetIssueStageDispatch } from "@multica/core/conveyor";
import { squadListOptions } from "@multica/core/workspace/queries";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { PropRow } from "../common/prop-row";
import { useT } from "../i18n";

const OFF = "off";

/**
 * Issue sidebar row "Stages": the squad the server assigns this epic's stages
 * to as soon as the earlier ones close. Shown for an issue with sub-issues, or
 * while dispatch is on.
 */
export function IssueStageDispatchPicker({
  wsId,
  issueId,
  hasChildren,
}: {
  wsId: string;
  issueId: string;
  hasChildren: boolean;
}) {
  const { t } = useT("issues");
  const current = useQuery(issueStageDispatchOptions(wsId, issueId));
  const squads = useQuery(squadListOptions(wsId));
  const set = useSetIssueStageDispatch(wsId);
  if (!current.data) return null;
  const value = current.data.squad_id ?? OFF;
  if (!hasChildren && value === OFF) return null;
  const items = [
    { value: OFF, label: t(($) => $.stage_dispatch.off) },
    ...(squads.data ?? []).filter((s) => !s.archived_at).map((s) => ({ value: s.id, label: s.name })),
  ];
  return (
    <PropRow label={t(($) => $.stage_dispatch.label)}>
      <Select
        items={items}
        value={value}
        disabled={set.isPending}
        onValueChange={(next) => {
          if (!next || next === value) return;
          set.mutate(
            { issueId, squadId: next === OFF ? null : next },
            { onError: (error) => toast.error(error instanceof Error ? error.message : String(error)) },
          );
        }}
      >
        <SelectTrigger
          size="sm"
          className="h-7 w-full border-0 px-2 text-caption shadow-none"
          aria-label={t(($) => $.stage_dispatch.label)}
        >
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {items.map((item) => (
            <SelectItem key={item.value} value={item.value}>
              {item.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </PropRow>
  );
}
