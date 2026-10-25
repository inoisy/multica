"use client";

import { useState } from "react";
import { ArrowDown, ArrowUp, Plus, Trash2 } from "lucide-react";
import type { AgentRuntime, MemberWithUser, ModelOptionInput, ModelRowState } from "@multica/core/types";
import { isRuntimeUsableForUser } from "@multica/core/runtimes";
import { modelRowLabel, moveModelOption } from "@multica/core/model-profiles";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { SettingsCard, SettingsRow } from "../settings/components/settings-layout";
import { ModelPicker } from "../agents/components/inspector/model-picker";
import { RuntimePicker } from "../agents/components/inspector/runtime-picker";
import { ThinkingSettingField } from "../agents/components/inspector/thinking-prop-row";
import { useT } from "../i18n";

/**
 * Editable ordered model rows: runtime, then model and thinking level from
 * the runtime's catalog. Row 1 is the primary model. Arguments stay hidden
 * under "Advanced": empty means the backend derives them from the agent's
 * disallowed tools.
 */
export function ModelRowsEditor({
  rows,
  onChange,
  runtimes,
  members,
  currentUserId,
  disabled,
  single = false,
  baseRows,
  compact = false,
}: {
  rows: ModelOptionInput[];
  onChange: (rows: ModelOptionInput[]) => void;
  runtimes: AgentRuntime[];
  members: MemberWithUser[];
  currentUserId: string | null;
  disabled?: boolean;
  /** One pinned model: no order, no extra rows. */
  single?: boolean;
  /** Rows of the shared profile this one is a copy of: a row that differs shows what the shared profile has there. */
  baseRows?: ModelRowState[];
  /** Pickers at the 32px height of the form's Input and Select. */
  compact?: boolean;
}) {
  const { t } = useT("agents");
  const [advanced, setAdvanced] = useState<Set<number>>(new Set());
  const runtimeById = new Map(runtimes.map((r) => [r.id, r]));
  const base = baseRows ? [...baseRows].sort((a, b) => a.position - b.position) : null;
  const patch = (index: number, change: Partial<ModelOptionInput>) =>
    onChange(rows.map((row, i) => (i === index ? { ...row, ...change } : row)));
  const add = () => {
    const used = new Set(rows.map((r) => r.runtime_id));
    const runtime =
      runtimes.find((r) => !used.has(r.id) && isRuntimeUsableForUser(r, currentUserId)) ?? runtimes[0];
    if (!runtime) return;
    onChange([...rows, { runtime_id: runtime.id, model: "", thinking_level: "", custom_args: null }]);
  };
  const toggleAdvanced = (index: number) =>
    setAdvanced((prev) => {
      const next = new Set(prev);
      if (next.has(index)) next.delete(index);
      else next.add(index);
      return next;
    });

  return (
    <div className="space-y-3">
      {rows.map((row, index) => {
        const runtime = runtimeById.get(row.runtime_id) ?? null;
        const online = runtime?.status === "online" && isRuntimeUsableForUser(runtime, currentUserId);
        return (
          <SettingsCard key={index}>
            {single ? null : (
            <SettingsRow
              label={index === 0 ? t(($) => $.models.row_primary) : t(($) => $.models.row_n, { n: index + 1 })}
            >
              <div className="flex gap-1">
                <Button
                  size="icon-sm"
                  variant="ghost"
                  aria-label={t(($) => $.models.move_up)}
                  disabled={disabled || index === 0}
                  onClick={() => onChange(moveModelOption(rows, index, -1))}
                >
                  <ArrowUp />
                </Button>
                <Button
                  size="icon-sm"
                  variant="ghost"
                  aria-label={t(($) => $.models.move_down)}
                  disabled={disabled || index === rows.length - 1}
                  onClick={() => onChange(moveModelOption(rows, index, 1))}
                >
                  <ArrowDown />
                </Button>
                <Button
                  size="icon-sm"
                  variant="ghost"
                  aria-label={t(($) => $.models.remove)}
                  disabled={disabled}
                  onClick={() => onChange(rows.filter((_, i) => i !== index))}
                >
                  <Trash2 />
                </Button>
              </div>
            </SettingsRow>
            )}
            <SettingsRow label={t(($) => $.models.runtime)} size="select-wide">
              <RuntimePicker
                variant="field"
                showLabel={false}
                compact={compact}
                value={row.runtime_id}
                runtimes={runtimes}
                members={members}
                currentUserId={currentUserId}
                canEdit={!disabled}
                onChange={(id) => patch(index, { runtime_id: id, model: "", thinking_level: "" })}
              />
            </SettingsRow>
            <SettingsRow label={t(($) => $.models.model)} size="select-wide">
              <ModelPicker
                variant="field"
                showLabel={false}
                compact={compact}
                runtimeId={row.runtime_id}
                runtimeOnline={online}
                value={row.model}
                canEdit={!disabled}
                onChange={(model) => patch(index, { model })}
              />
            </SettingsRow>
            <ThinkingSettingField
              label={t(($) => $.models.thinking)}
              runtimeId={row.runtime_id}
              runtimeOnline={online}
              provider={runtime?.provider ?? ""}
              model={row.model}
              value={row.thinking_level}
              canEdit={!disabled}
              onChange={(thinking_level) => patch(index, { thinking_level })}
              compact={compact}
            />
            {base ? <BaseNote row={row} baseRow={base[index] ?? null} /> : null}
            {advanced.has(index) || (row.custom_args && row.custom_args.length > 0) ? (
              <SettingsRow label={t(($) => $.models.args)} description={t(($) => $.models.args_hint)} size="text" align="start">
                <Textarea
                  rows={3}
                  className="resize-y font-mono text-caption"
                  disabled={disabled}
                  placeholder={t(($) => $.models.args_auto)}
                  value={(row.custom_args ?? []).join("\n")}
                  onChange={(event) =>
                    patch(index, {
                      custom_args:
                        event.target.value.trim() === ""
                          ? null
                          : event.target.value.split("\n").filter((line) => line.trim() !== ""),
                    })
                  }
                />
              </SettingsRow>
            ) : (
              <SettingsRow label="">
                <Button size="sm" variant="ghost" disabled={disabled} onClick={() => toggleAdvanced(index)}>
                  {t(($) => $.models.advanced)}
                </Button>
              </SettingsRow>
            )}
          </SettingsCard>
        );
      })}
      {single && rows.length > 0 ? null : (
        <Button size="sm" variant="outline" disabled={disabled || runtimes.length === 0} onClick={add}>
          <Plus />
          {t(($) => $.models.add_row)}
        </Button>
      )}
    </div>
  );
}

/** Under a row of a personal profile: what the shared profile has at the same place. */
function BaseNote({ row, baseRow }: { row: ModelOptionInput; baseRow: ModelRowState | null }) {
  const { t } = useT("agents");
  const same =
    baseRow !== null &&
    baseRow.runtime_id === row.runtime_id &&
    baseRow.model === row.model &&
    baseRow.thinking_level === row.thinking_level;
  if (same) return null;
  return (
    <div className="flex flex-wrap items-center gap-2 border-t px-4 py-2.5 text-caption text-muted-foreground" data-testid="row-base-note">
      <Badge variant="warning">{t(($) => $.models.row_differs)}</Badge>
      <span>{baseRow ? t(($) => $.models.row_in_shared, { row: modelRowLabel(baseRow) }) : t(($) => $.models.row_not_in_shared)}</span>
    </div>
  );
}
