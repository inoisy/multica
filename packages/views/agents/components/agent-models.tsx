"use client";

import { type ReactNode, useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { X } from "lucide-react";
import { toast } from "sonner";
import type {
  Agent,
  AgentModelChoiceEntry,
  AgentRuntime,
  MemberWithUser,
  ModelChoice,
  ModelOptionInput,
  ModelRowState,
} from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  agentModelsOptions,
  modelProfileListOptions,
  modelRowLabel,
  toModelOptionInputs,
  useUpdateAgentModels,
} from "@multica/core/model-profiles";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Switch } from "@multica/ui/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { SettingsCard, SettingsRow, SettingsSection } from "../../settings/components/settings-layout";
import { useT } from "../../i18n";
import { AppLink } from "../../navigation";
import { ModelRowsEditor } from "../../model-profiles/model-rows-editor";
import { formatModelTime, useModelText } from "../../model-profiles/model-text";

const NO_PROFILE = "none";

/** A card section whose content spans the card: lists, not a single control. */
function ListBlock({ label, children, testId }: { label: ReactNode; children: ReactNode; testId?: string }) {
  return (
    <div className="px-4 py-3.5" data-testid={testId}>
      <div className="text-body font-medium">{label}</div>
      <div className="mt-2">{children}</div>
    </div>
  );
}

function rowBadgeVariant(row: ModelRowState): "secondary" | "destructive" | "outline" {
  if (row.state === "available") return "secondary";
  if (row.state === "limited") return "destructive";
  return "outline";
}

/**
 * Settings → General → Models: the one place an agent's model is set. The
 * agent references a workspace profile; its own chain is an explicit
 * exception (one row pins the model). The server picks the first available
 * row at every claim, so a save applies from the next run and never touches
 * a run in progress.
 */
export function AgentModels({
  agent,
  runtimes,
  members,
  currentUserId,
}: {
  agent: Agent;
  runtimes: AgentRuntime[];
  members: MemberWithUser[];
  currentUserId: string | null;
}) {
  const { t } = useT("agents");
  const text = useModelText();
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const query = useQuery(agentModelsOptions(wsId, agent.id));
  const profiles = useQuery(modelProfileListOptions(wsId));
  const update = useUpdateAgentModels(wsId, agent.id);
  const [draft, setDraft] = useState<ModelOptionInput[] | null>(null);
  const [tool, setTool] = useState("");

  useEffect(() => {
    setDraft(null);
    setTool("");
  }, [agent.id]);

  const data = query.data;
  // An older backend has no model endpoints: the section stays away.
  if (query.isError && !data) return null;
  if (!data) return null;

  const canEdit = data.can_edit;
  const busy = update.isPending;
  const save = (input: Parameters<typeof update.mutateAsync>[0], done?: () => void) =>
    update.mutateAsync(input).then(
      () => {
        toast.success(t(($) => $.models.saved));
        done?.();
      },
      (error: unknown) => toast.error(error instanceof Error ? error.message : String(error)),
    );

  const profileItems = [
    { value: NO_PROFILE, label: t(($) => $.models.profile_none) },
    ...(profiles.data ?? []).map((p) => ({ value: p.id, label: p.name })),
  ];
  const ownRows: ModelRowState[] =
    data.preview.source === "agent_list"
      ? data.preview.rows
      : data.options.map((o) => ({ ...o, runtime_name: runtimes.find((r) => r.id === o.runtime_id)?.name ?? "", provider: "", state: "", until: null, chosen: false }));
  // "Now" is the row the last run got; with no run yet, what the next one
  // would take. Runs that wait for a free model are listed under it.
  const current: { choice: ModelChoice; entry: AgentModelChoiceEntry | null } = data.last?.choice
    ? { choice: data.last.choice, entry: data.last }
    : { choice: data.preview, entry: null };
  const startOwn = () => {
    const rows = data.profile_rows.length ? data.profile_rows : data.preview.rows;
    setDraft(toModelOptionInputs(rows));
  };
  const tools = data.disallowed_tools;
  const addTool = () => {
    const value = tool.trim();
    if (!value || tools.includes(value)) return;
    void save({ disallowed_tools: [...tools, value] }, () => setTool(""));
  };

  return (
    <SettingsSection title={t(($) => $.models.title)}>
      <SettingsCard>
        <SettingsRow
          label={t(($) => $.models.profile_label)}
          description={
            data.model_profile_id
              ? t(($) => $.models.profile_usage, { agents: data.profile_agents, issues: data.profile_issues })
              : undefined
          }
          size="select-wide"
        >
          <div className="flex w-full items-center gap-2">
            <Select
              items={profileItems}
              value={data.model_profile_id ?? NO_PROFILE}
              disabled={!canEdit || busy}
              onValueChange={(value) => {
                if (!value || value === (data.model_profile_id ?? NO_PROFILE)) return;
                void save({ model_profile_id: value === NO_PROFILE ? null : value });
              }}
            >
              <SelectTrigger className="w-full" aria-label={t(($) => $.models.profile_label)}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {profileItems.map((item) => (
                  <SelectItem key={item.value} value={item.value}>
                    {item.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {data.model_profile_id ? (
              <AppLink href={paths.modelProfiles()} className="shrink-0 text-caption hover:underline">
                {t(($) => $.models.open_profile)}
              </AppLink>
            ) : null}
          </div>
        </SettingsRow>
        <SettingsRow label={t(($) => $.models.now_label)} size="text" align="start">
          <div className="space-y-0.5 text-body" data-testid="agent-models-now">
            <p className="font-medium">{text.chosen(current.choice) || "—"}</p>
            <p className="text-caption text-muted-foreground">
              {text.source(current.choice, current.entry?.issue_identifier)} · {text.reason(current.choice)}
            </p>
            {current.entry ? (
              <p className="text-caption text-muted-foreground">
                {current.entry.issue_identifier ? `${current.entry.issue_identifier} · ` : ""}
                {formatModelTime(current.entry.created_at)}
              </p>
            ) : (
              <p className="text-caption text-muted-foreground">{t(($) => $.models.now_next_run)}</p>
            )}
            {data.waiting.map((entry) =>
              entry.choice ? (
                <p key={entry.task_id} className="text-caption text-destructive">
                  {entry.issue_identifier ? `${entry.issue_identifier}: ` : ""}
                  {text.reason(entry.choice)}
                </p>
              ) : null,
            )}
          </div>
        </SettingsRow>
      </SettingsCard>

      {data.model_profile_id && !data.own_list && draft === null ? (
        <SettingsCard>
          <ListBlock label={t(($) => $.models.profile_rows, { name: data.profile_name })}>
            <ol className="w-full space-y-1.5">
              {data.profile_rows.map((row, index) => (
                <li key={row.position} className="flex flex-wrap items-center gap-2 text-body">
                  <span className="w-16 shrink-0 text-caption text-muted-foreground">
                    {index === 0 ? t(($) => $.models.row_primary) : t(($) => $.models.row_n, { n: index + 1 })}
                  </span>
                  <span className="min-w-0 flex-1 truncate">{modelRowLabel(row)}</span>
                  <Badge variant={rowBadgeVariant(row)}>{text.state(row)}</Badge>
                  {data.preview.source === "agent_profile" && data.preview.profile_id === data.model_profile_id && data.preview.position === row.position ? (
                    <Badge>{t(($) => $.models.current)}</Badge>
                  ) : null}
                  {canEdit ? (
                    <Button
                      size="sm"
                      variant="ghost"
                      disabled={busy}
                      onClick={() => void save({ options: toModelOptionInputs([{ ...row, position: 1 }]) })}
                    >
                      {t(($) => $.models.pin)}
                    </Button>
                  ) : null}
                </li>
              ))}
            </ol>
          </ListBlock>
        </SettingsCard>
      ) : null}

      <SettingsCard>
        <SettingsRow label={t(($) => $.models.own_label)} description={t(($) => $.models.own_hint)}>
          <Switch
            aria-label={t(($) => $.models.own_label)}
            checked={data.own_list || draft !== null}
            disabled={!canEdit || busy}
            onCheckedChange={(checked) => {
              if (checked) startOwn();
              else if (draft !== null && !data.own_list) setDraft(null);
              else void save({ options: [] }, () => setDraft(null));
            }}
          />
        </SettingsRow>
        {data.own_list && draft === null ? (
          <div className="px-4 py-3.5">
            <div className="w-full space-y-2">
              <ol className="space-y-1.5">
                {ownRows.map((row, index) => (
                  <li key={index} className="flex flex-wrap items-center gap-2 text-body">
                    <span className="w-16 shrink-0 text-caption text-muted-foreground">
                      {index === 0 ? t(($) => $.models.row_primary) : t(($) => $.models.row_n, { n: index + 1 })}
                    </span>
                    <span className="min-w-0 flex-1 truncate">{modelRowLabel(row)}</span>
                    {row.state ? <Badge variant={rowBadgeVariant(row)}>{text.state(row)}</Badge> : null}
                    {row.chosen ? <Badge>{t(($) => $.models.current)}</Badge> : null}
                  </li>
                ))}
              </ol>
              {canEdit ? (
                <Button size="sm" variant="outline" disabled={busy} onClick={() => setDraft(toModelOptionInputs(data.options))}>
                  {t(($) => $.models.edit_own)}
                </Button>
              ) : null}
            </div>
          </div>
        ) : null}
      </SettingsCard>
      {draft !== null ? (
        <div className="space-y-3">
          <ModelRowsEditor
            rows={draft}
            onChange={setDraft}
            runtimes={runtimes}
            members={members}
            currentUserId={currentUserId}
            disabled={busy}
          />
          <div className="flex gap-2">
            <Button size="sm" variant="ghost" disabled={busy} onClick={() => setDraft(null)}>
              {t(($) => $.models.cancel)}
            </Button>
            <Button size="sm" disabled={busy || draft.length === 0} onClick={() => void save({ options: draft }, () => setDraft(null))}>
              {t(($) => $.models.save)}
            </Button>
          </div>
          <p className="text-caption text-muted-foreground">{t(($) => $.models.next_run_hint)}</p>
        </div>
      ) : null}

      <SettingsCard>
        <SettingsRow label={t(($) => $.models.tools_label)} description={t(($) => $.models.tools_hint)} size="select-wide" align="start">
          <div className="w-full space-y-2">
            <div className="flex flex-wrap gap-1.5">
              {tools.length === 0 ? (
                <span className="text-caption text-muted-foreground">{t(($) => $.models.tools_none)}</span>
              ) : (
                tools.map((name) => (
                  <Badge key={name} variant="outline" className="gap-1 font-mono">
                    {name}
                    {canEdit ? (
                      <button
                        type="button"
                        aria-label={t(($) => $.models.tools_remove, { name })}
                        disabled={busy}
                        onClick={() => void save({ disallowed_tools: tools.filter((x) => x !== name) })}
                      >
                        <X className="size-3" />
                      </button>
                    ) : null}
                  </Badge>
                ))
              )}
            </div>
            {canEdit ? (
              <Input
                value={tool}
                placeholder={t(($) => $.models.tools_placeholder)}
                aria-label={t(($) => $.models.tools_add)}
                disabled={busy}
                onChange={(event) => setTool(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === "Enter") {
                    event.preventDefault();
                    addTool();
                  }
                }}
              />
            ) : null}
          </div>
        </SettingsRow>
      </SettingsCard>

      {data.issues.length > 0 ? (
        <SettingsCard>
          <ListBlock label={t(($) => $.models.issues_label)}>
            <ul className="w-full space-y-1 text-body">
              {data.issues.map((issue) => (
                <li key={issue.id} className="flex gap-2">
                  <AppLink href={paths.issueDetail(issue.id)} className="shrink-0 font-medium hover:underline">
                    {issue.identifier}
                  </AppLink>
                  <span className="min-w-0 flex-1 truncate text-muted-foreground">{issue.name}</span>
                  <Badge variant="outline">{issue.profile_name}</Badge>
                </li>
              ))}
            </ul>
          </ListBlock>
        </SettingsCard>
      ) : null}

      {data.journal.length > 0 ? (
        <SettingsCard>
          <ListBlock label={t(($) => $.models.journal_label)}>
            <ul className="w-full space-y-1.5 text-caption" data-testid="agent-models-journal">
              {data.journal.map((entry) => (
                <li key={entry.task_id} className="flex flex-wrap gap-x-2">
                  <span className="text-muted-foreground">{formatModelTime(entry.created_at)}</span>
                  {entry.issue_identifier ? <span className="font-medium">{entry.issue_identifier}</span> : null}
                  {entry.choice ? (
                    <>
                      <span>{t(($) => $.models.row_short, { n: entry.position })} · {text.chosen(entry.choice)}</span>
                      <span className="text-muted-foreground">
                        {text.source(entry.choice, entry.issue_identifier)} · {text.reason(entry.choice)}
                      </span>
                    </>
                  ) : null}
                </li>
              ))}
            </ul>
          </ListBlock>
        </SettingsCard>
      ) : null}
    </SettingsSection>
  );
}
