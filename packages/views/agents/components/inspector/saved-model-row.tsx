"use client";

import type { Agent, AgentModels, AgentRuntime, ModelRowState } from "@multica/core/types";
import { runtimeDisplayName } from "@multica/core/runtimes";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../../i18n";
import { useModelText } from "../../../model-profiles/model-text";
import { scrollToAgentModels } from "../agent-models-anchor";

/** The saved first row of the agent's list: what the profile says, not what the next run will take. */
export function savedFirstRow(models: AgentModels | undefined): ModelRowState | null {
  if (!models) return null;
  const rows = models.mode === "profile" ? models.profile_rows : models.personal_rows;
  return [...rows].sort((a, b) => a.position - b.position)[0] ?? null;
}

/**
 * Runtime, model and thinking level of the agent as saved in row 1 of its
 * profile, read-only. They are changed on the Models tab; the account is not
 * named here because the server picks it at launch.
 */
export function SavedModelRow({
  agent,
  runtime,
  models,
}: {
  agent: Agent;
  runtime: AgentRuntime | null;
  models: AgentModels | undefined;
}) {
  const { t } = useT("agents");
  const text = useModelText();
  const row = savedFirstRow(models);
  // An agent without a profile keeps its own fields, which are the one row.
  const label = row
    ? text.rowLabel(row)
    : [runtime ? runtimeDisplayName(runtime) : "", agent.model ?? "", agent.thinking_level ?? ""].filter(Boolean).join("·");
  return (
    <div className="space-y-1" data-testid="inspector-saved-row">
      <p className="break-words font-mono text-body" data-testid="inspector-saved-row-label">
        {label || "—"}
      </p>
      <p className="text-caption text-muted-foreground">{t(($) => $.inspector.model_saved_hint)}</p>
      <Button type="button" variant="link" className="h-auto p-0 text-caption" onClick={scrollToAgentModels}>
        {t(($) => $.inspector.model_open_models)}
      </Button>
    </div>
  );
}
