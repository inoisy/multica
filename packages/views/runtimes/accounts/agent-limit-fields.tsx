"use client";

import { useId } from "react";
import { AGENT_LIMIT_MAX, parseAgentLimit, peopleShare } from "@multica/core/subscriptions";
import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../../i18n";

/** Typed limits per window, as text so a half-typed value is not lost. */
export type AgentLimitDraft = Record<string, string>;

/** The limits an account has, as the editor starts. A window without a limit starts at 100. */
export function limitDraftOf(windows: string[], caps: Record<string, number>): AgentLimitDraft {
  return Object.fromEntries(windows.map((window) => [window, String(caps[window] ?? AGENT_LIMIT_MAX)]));
}

/** The draft as numbers, or null while any window holds something that is not a whole 0…100. */
export function parseLimitDraft(draft: AgentLimitDraft): Record<string, number> | null {
  const out: Record<string, number> = {};
  for (const [window, value] of Object.entries(draft)) {
    const parsed = parseAgentLimit(value);
    if (parsed === null) return null;
    out[window] = parsed;
  }
  return out;
}

/**
 * "Agents' limit, %" per usage window: the one number that decides how much of
 * a window agents may spend. What is left for people is always 100 − the limit
 * and is shown, never typed.
 */
export function AgentLimitFields({
  windows,
  windowName,
  value,
  onChange,
  disabled,
}: {
  windows: string[];
  windowName: (window: string) => string;
  value: AgentLimitDraft;
  onChange: (value: AgentLimitDraft) => void;
  disabled?: boolean;
}) {
  const { t } = useT("agents");
  const id = useId();
  return (
    <div className="space-y-3">
      {windows.map((window) => {
        const text = value[window] ?? "";
        const parsed = parseAgentLimit(text);
        const inputId = `${id}-${window}`;
        return (
          <div key={window} className="grid grid-cols-[minmax(4rem,1fr)_auto] items-center gap-x-3 gap-y-1 sm:grid-cols-[minmax(5rem,1fr)_8rem_minmax(8rem,1.2fr)]">
            <label htmlFor={inputId} className="text-body">
              {t(($) => $.subscriptions.limit_label, { window: windowName(window) })}
            </label>
            <span className="flex items-center gap-1.5">
              <Input
                id={inputId}
                type="number"
                min={0}
                max={AGENT_LIMIT_MAX}
                step={1}
                inputMode="numeric"
                className="w-20 text-right tabular-nums"
                value={text}
                disabled={disabled}
                aria-invalid={parsed === null}
                onChange={(event) => onChange({ ...value, [window]: event.target.value })}
              />
              <span aria-hidden="true">%</span>
            </span>
            <output htmlFor={inputId} className="col-span-2 text-caption text-muted-foreground sm:col-span-1">
              {parsed === null
                ? t(($) => $.subscriptions.limit_invalid)
                : t(($) => $.subscriptions.limit_people_left, { n: peopleShare(parsed) })}
            </output>
          </div>
        );
      })}
    </div>
  );
}
