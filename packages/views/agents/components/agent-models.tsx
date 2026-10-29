"use client";

import { type ReactNode, useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { X } from "@multica/ui/icons";
import { toast } from "sonner";
import type {
  Agent,
  AgentModels as AgentModelsData,
  AgentRuntime,
  MemberWithUser,
  ModelOptionInput,
  ModelRowState,
} from "@multica/core/types";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import {
  agentModelsOptions,
  modelProfileListOptions,
  toModelOptionInputs,
  useAgentPersonalProfile,
  useUpdateAgentModels,
} from "@multica/core/model-profiles";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { RadioGroup, RadioGroupItem } from "@multica/ui/components/ui/radio-group";
import { Input } from "@multica/ui/components/ui/input";
import { cn } from "@multica/ui/lib/utils";
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
import { AccountFactLine } from "../../model-profiles/account-fact";
import { useAvailabilityText } from "../../model-profiles/availability-text";
import { ModelRowsEditor } from "../../model-profiles/model-rows-editor";
import { formatModelTime, useModelText } from "../../model-profiles/model-text";
import { PersonalRowsList, ProfileDiffBadge } from "../../model-profiles/profile-diff";
import { ResetPersonalDialog } from "../../model-profiles/reset-personal-dialog";
import { AGENT_MODELS_ANCHOR } from "./agent-models-anchor";
import { savedFirstRow } from "./inspector/saved-model-row";

const NO_PROFILE = "none";

/** Where the agent's model comes from; "fields" — no profile at all: General's runtime and model. */
type SourceMode = AgentModelsData["mode"];

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
 * Settings → General → Models: the one place an agent's model is set, by one
 * of three sources — a shared profile, the agent's personal profile (its own
 * copy of a shared one, compared with it row by row) or one model. Personal
 * profile and one model are edited with the same row editor as a shared
 * profile. The server picks the first available row at every claim, so a
 * save applies from the next run and never touches a run in progress.
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
  const availability = useAvailabilityText();
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const query = useQuery(agentModelsOptions(wsId, agent.id));
  const profiles = useQuery(modelProfileListOptions(wsId));
  const update = useUpdateAgentModels(wsId, agent.id);
  const personal = useAgentPersonalProfile(wsId, agent.id);
  const [draft, setDraft] = useState<ModelOptionInput[] | null>(null);
  const [pending, setPending] = useState<SourceMode | null>(null);
  const [pickedProfile, setPickedProfile] = useState<string>(NO_PROFILE);
  const [confirmReset, setConfirmReset] = useState(false);
  const [tool, setTool] = useState("");

  useEffect(() => {
    setDraft(null);
    setPending(null);
    setConfirmReset(false);
    setTool("");
  }, [agent.id]);

  const data = query.data;
  // An older backend has no model endpoints: the section stays away.
  if (query.isError && !data) return null;
  if (!data) return null;

  const canEdit = data.can_edit;
  const busy = update.isPending || personal.create.isPending || personal.save.isPending || personal.reset.isPending || personal.single.isPending;
  const fail = (error: unknown) => toast.error(error instanceof Error ? error.message : String(error));
  const done = (message: string, after?: () => void) => () => {
    toast.success(message);
    after?.();
  };
  const savedToast = t(($) => $.models.saved);
  const save = (input: Parameters<typeof update.mutateAsync>[0], after?: () => void) =>
    update.mutateAsync(input).then(done(savedToast, after), fail);

  const profileItems = [
    { value: NO_PROFILE, label: t(($) => $.models.profile_none) },
    ...(profiles.data ?? []).map((p) => ({ value: p.id, label: p.name })),
  ];
  const profileName = (id: string | null) => (profiles.data ?? []).find((p) => p.id === id)?.name ?? "";
  const mode: SourceMode = data.mode;
  const saved = savedFirstRow(data);
  const shown: SourceMode = pending ?? mode;
  const sharedId = mode === "profile" ? data.model_profile_id : data.base_profile_id;
  const sharedName = mode === "profile" ? data.profile_name : data.base_profile_name;
  const reset = () => {
    setPending(null);
    setDraft(null);
  };
  const seedRows = (): ModelOptionInput[] => {
    const rows = data.personal_rows.length ? data.personal_rows : data.profile_rows.length ? data.profile_rows : data.preview.rows;
    if (rows.length) return toModelOptionInputs(rows);
    return agent.runtime_id
      ? [{ runtime_id: agent.runtime_id, model: agent.model ?? "", thinking_level: agent.thinking_level ?? "", custom_args: null }]
      : [];
  };
  const choose = (next: SourceMode) => {
    if (next === shown) return;
    if (next === mode) {
      reset();
      return;
    }
    setPending(next);
    if (next === "profile") {
      setDraft(null);
      setPickedProfile(data.base_profile_id ?? profiles.data?.[0]?.id ?? NO_PROFILE);
    } else if (next === "personal") {
      // A copy is made on the server; without a profile to copy the person edits first.
      setDraft(data.personal_profile_id || sharedId ? null : seedRows());
    } else if (next === "single") {
      setDraft(seedRows().slice(0, 1));
    }
  };
  const tools = data.disallowed_tools;
  const addTool = () => {
    const value = tool.trim();
    if (!value || tools.includes(value)) return;
    void save({ disallowed_tools: [...tools, value] }, () => setTool(""));
  };
  const sources: { value: Exclude<SourceMode, "fields">; label: string; hint: string }[] = [
    { value: "profile", label: t(($) => $.models.mode_profile), hint: t(($) => $.models.mode_profile_hint) },
    { value: "personal", label: t(($) => $.models.mode_personal), hint: t(($) => $.models.mode_personal_hint) },
    { value: "single", label: t(($) => $.models.mode_single), hint: t(($) => $.models.mode_one_hint) },
  ];

  const switchToPersonal = () =>
    data.personal_profile_id
      ? personal.save
          .mutateAsync({ options: toModelOptionInputs(data.personal_rows), from_profile_id: data.base_profile_id })
          .then(done(savedToast, reset), fail)
      : personal.create.mutateAsync({ from_profile_id: sharedId }).then(done(savedToast, reset), fail);
  const savePersonalDraft = () =>
    draft &&
    personal.save
      .mutateAsync({ options: draft, from_profile_id: sharedId })
      .then(done(savedToast, reset), fail);
  const saveSingleDraft = () => {
    const [row] = draft ?? [];
    return row && personal.single.mutateAsync({ ...row }).then(done(savedToast, reset), fail);
  };
  const resetToShared = () =>
    personal.reset.mutateAsync().then(
      done(t(($) => $.models.reset_done), () => {
        setConfirmReset(false);
        reset();
      }),
      fail,
    );

  return (
    <div id={AGENT_MODELS_ANCHOR} tabIndex={-1} className="scroll-mt-6 outline-none">
    <SettingsSection title={t(($) => $.models.title)}>
      <SettingsCard>
        <SettingsRow label={t(($) => $.models.setting_label)} size="text" align="start">
          <div className="space-y-0.5 text-body" data-testid="agent-models-setting">
            <p className="flex flex-wrap items-center gap-2 font-medium">
              <span className="break-words font-mono">{saved ? text.rowLabel(saved) : "—"}</span>
              <Badge variant="outline">{t(($) => $.models.setting_badge)}</Badge>
            </p>
            <p className="text-caption text-muted-foreground">
              {mode === "profile"
                ? t(($) => $.models.setting_source_profile, { name: data.profile_name })
                : mode === "personal"
                  ? t(($) => $.models.setting_source_personal)
                  : mode === "single"
                    ? t(($) => $.models.setting_source_single)
                    : t(($) => $.models.setting_source_fields)}
            </p>
          </div>
        </SettingsRow>
        <SettingsRow label={t(($) => $.models.forecast_label)} size="text" align="start">
          <div className="space-y-0.5 text-body" data-testid="agent-models-forecast">
            <p className="flex flex-wrap items-center gap-2 font-medium">
              <span className="break-words font-mono">{text.chosen(data.preview) || "—"}</span>
              <Badge variant="outline">{t(($) => $.models.forecast_badge)}</Badge>
            </p>
            <p className="text-caption text-muted-foreground">
              {text.source(data.preview)} · {text.reason(data.preview)}
            </p>
            <p className="text-caption text-muted-foreground">{t(($) => $.models.now_next_run)}</p>
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
        <SettingsRow label={t(($) => $.models.last_label)} size="text" align="start">
          <div className="space-y-0.5 text-body" data-testid="agent-models-last">
            {data.last?.choice ? (
              <>
                <p className="flex flex-wrap items-center gap-2 font-medium">
                  <span className="break-words font-mono">{text.chosen(data.last.choice) || "—"}</span>
                  <Badge variant="outline">{t(($) => $.models.fact_badge)}</Badge>
                </p>
                <p className="text-caption text-muted-foreground">
                  {text.source(data.last.choice, data.last.issue_identifier)} · {text.reason(data.last.choice)}
                </p>
                <p className="text-caption text-muted-foreground">
                  {data.last.issue_identifier ? `${data.last.issue_identifier} · ` : ""}
                  {formatModelTime(data.last.created_at)}
                </p>
                <p className="text-caption" data-testid="agent-models-account">
                  <span className="text-muted-foreground">{t(($) => $.models.account_label)}: </span>
                  <AccountFactLine fact={data.last.account_fact} />
                </p>
              </>
            ) : (
              <p className="text-caption text-muted-foreground">{t(($) => $.models.last_none)}</p>
            )}
          </div>
        </SettingsRow>
      </SettingsCard>

      {data.runtime_unavailable.length > 0 ? (
        <SettingsCard>
          <ListBlock label={t(($) => $.models.unavailable_label)} testId="agent-models-unavailable">
            <ul className="w-full space-y-1 text-caption">
              {data.runtime_unavailable.map((block) => (
                <li key={`${block.runtime_id}-${block.reason_code ?? ""}-${block.until}`} className="flex flex-wrap gap-x-2">
                  <span className="font-medium">{block.runtime_name}</span>
                  <span className="text-muted-foreground">
                    {availability.describe({ blocked: true, reason_code: block.reason_code, until: block.until, until_estimated: block.until_estimated })}
                  </span>
                </li>
              ))}
            </ul>
          </ListBlock>
        </SettingsCard>
      ) : null}

      <SettingsCard>
        <ListBlock label={t(($) => $.models.mode_label)} testId="agent-models-source">
          <RadioGroup value={shown} onValueChange={choose} disabled={!canEdit || busy} aria-label={t(($) => $.models.mode_label)} className="grid gap-2 sm:grid-cols-3">
            {sources.map((source) => (
              <label
                key={source.value}
                className={cn(
                  "flex cursor-pointer flex-col gap-1 rounded-md border px-3 py-2",
                  shown === source.value && "border-foreground ring-1 ring-foreground",
                  (!canEdit || busy) && "cursor-default opacity-70",
                )}
              >
                <span className="flex items-center gap-2 text-body font-medium">
                  <RadioGroupItem value={source.value} />
                  {source.label}
                </span>
                <span className="text-caption text-muted-foreground">{source.hint}</span>
              </label>
            ))}
          </RadioGroup>
          {shown === "fields" ? (
            <p className="mt-2 text-caption text-muted-foreground">{t(($) => $.models.mode_fields_hint)}</p>
          ) : null}
        </ListBlock>
      </SettingsCard>

      {shown === "profile" ? (
        <SettingsCard>
          <SettingsRow
            label={t(($) => $.models.profile_label)}
            description={
              mode === "profile" && data.model_profile_id
                ? t(($) => $.models.profile_usage, { agents: data.profile_agents, issues: data.profile_issues })
                : undefined
            }
            size="select-wide"
          >
            <div className="flex w-full items-center gap-2">
              <Select
                items={profileItems}
                value={mode === "profile" ? (data.model_profile_id ?? NO_PROFILE) : pickedProfile}
                disabled={!canEdit || busy}
                onValueChange={(value) => {
                  if (!value) return;
                  if (mode !== "profile") {
                    setPickedProfile(value);
                    return;
                  }
                  if (value === (data.model_profile_id ?? NO_PROFILE)) return;
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
          {mode !== "profile" ? (
            <div className="flex flex-wrap gap-2 px-4 py-3.5">
              <Button size="sm" variant="ghost" disabled={busy} onClick={reset}>
                {t(($) => $.models.cancel)}
              </Button>
              <Button
                size="sm"
                disabled={busy || pickedProfile === NO_PROFILE}
                onClick={() => void save({ model_profile_id: pickedProfile }, reset)}
              >
                {t(($) => $.models.switch_to_profile, { name: profileName(pickedProfile) })}
              </Button>
            </div>
          ) : null}
        </SettingsCard>
      ) : null}

      {mode === "profile" && shown === "profile" && data.model_profile_id ? (
        <SettingsCard>
          <ListBlock label={t(($) => $.models.profile_rows, { name: data.profile_name })}>
            <ol className="w-full space-y-1.5">
              {data.profile_rows.map((row, index) => (
                <li key={row.position} className="flex flex-wrap items-center gap-2 text-body">
                  <span className="w-16 shrink-0 text-caption text-muted-foreground">
                    {index === 0 ? t(($) => $.models.row_primary) : t(($) => $.models.row_n, { n: index + 1 })}
                  </span>
                  <span className="min-w-0 flex-1 truncate">{text.rowLabel(row)}</span>
                  <Badge variant={rowBadgeVariant(row)}>{text.state(row)}</Badge>
                  {data.preview.source === "agent_profile" && data.preview.profile_id === data.model_profile_id && data.preview.position === row.position ? (
                    <Badge>{t(($) => $.models.current)}</Badge>
                  ) : null}
                  {canEdit ? (
                    <Button
                      size="sm"
                      variant="ghost"
                      disabled={busy}
                      onClick={() =>
                        void personal.single
                          .mutateAsync({ runtime_id: row.runtime_id, model: row.model, thinking_level: row.thinking_level, custom_args: row.custom_args })
                          .then(done(savedToast), fail)
                      }
                    >
                      {t(($) => $.models.pin)}
                    </Button>
                  ) : null}
                </li>
              ))}
            </ol>
          </ListBlock>
          {data.personal_profile_id ? (
            <div className="space-y-2 border-t px-4 py-3.5" data-testid="agent-models-personal-unused">
              <p className="flex flex-wrap items-center gap-2 text-caption text-muted-foreground">
                <ProfileDiffBadge matchesBase={data.matches_base} diffRows={data.diff_rows} />
              </p>
              <p className="text-caption text-muted-foreground">
                {t(($) => $.models.personal_unused, {
                  state: data.matches_base ? t(($) => $.models.matches_base) : t(($) => $.models.differs_rows, { rows: data.diff_rows.join(", ") }),
                })}
              </p>
              {canEdit ? (
                <Button size="sm" variant="outline" disabled={busy} onClick={() => void switchToPersonal()}>
                  {t(($) => $.models.use_personal)}
                </Button>
              ) : null}
            </div>
          ) : null}
        </SettingsCard>
      ) : null}

      {shown === "personal" && draft === null ? (
        <SettingsCard>
          <ListBlock label={t(($) => $.models.personal_title)} testId="agent-models-personal">
            <div className="w-full space-y-3">
              {mode === "personal" ? (
                <>
                  <div className="flex flex-wrap items-center gap-2 text-caption">
                    {data.base_profile_id ? (
                      <>
                        <span className="text-muted-foreground">{t(($) => $.models.personal_base)}:</span>
                        <AppLink href={paths.modelProfiles()} className="font-medium hover:underline">
                          {data.base_profile_name}
                        </AppLink>
                      </>
                    ) : (
                      <span className="text-muted-foreground">{t(($) => $.models.personal_base_none)}</span>
                    )}
                    <ProfileDiffBadge matchesBase={data.matches_base} diffRows={data.diff_rows} />
                  </div>
                  <PersonalRowsList rows={data.personal_rows} baseRows={data.profile_rows} diffRows={data.diff_rows} />
                  <p className="text-caption text-muted-foreground">{t(($) => $.models.personal_hint)}</p>
                  {canEdit ? (
                    <div className="flex flex-wrap gap-2">
                      <Button size="sm" variant="outline" disabled={busy} onClick={() => setDraft(toModelOptionInputs(data.personal_rows))}>
                        {t(($) => $.models.edit_personal)}
                      </Button>
                      {data.base_profile_id ? (
                        <Button size="sm" variant="ghost" disabled={busy} onClick={() => setConfirmReset(true)}>
                          {t(($) => $.models.reset_to_shared)}
                        </Button>
                      ) : null}
                    </div>
                  ) : null}
                </>
              ) : (
                <div className="flex flex-wrap gap-2">
                  <Button size="sm" variant="ghost" disabled={busy} onClick={reset}>
                    {t(($) => $.models.cancel)}
                  </Button>
                  <Button size="sm" disabled={busy} onClick={() => void switchToPersonal()}>
                    {data.personal_profile_id
                      ? t(($) => $.models.use_personal)
                      : t(($) => $.models.create_personal, { name: sharedName })}
                  </Button>
                </div>
              )}
            </div>
          </ListBlock>
        </SettingsCard>
      ) : null}

      {shown === "single" && draft === null ? (
        <SettingsCard>
          <ListBlock label={t(($) => $.models.one_title)}>
            <div className="w-full space-y-2">
              <ol className="space-y-1.5" data-testid="agent-models-own">
                {data.personal_rows.slice(0, 1).map((row) => (
                  <li key={row.position} className="flex flex-wrap items-center gap-2 text-body">
                    <span className="min-w-0 flex-1 truncate">{text.rowLabel(row)}</span>
                    {row.state ? <Badge variant={rowBadgeVariant(row)}>{text.state(row)}</Badge> : null}
                  </li>
                ))}
              </ol>
              <p className="text-caption text-muted-foreground">{t(($) => $.models.one_hint)}</p>
              {canEdit ? (
                <Button size="sm" variant="outline" disabled={busy} onClick={() => setDraft(toModelOptionInputs(data.personal_rows).slice(0, 1))}>
                  {t(($) => $.models.edit_single)}
                </Button>
              ) : null}
            </div>
          </ListBlock>
        </SettingsCard>
      ) : null}

      {draft !== null ? (
        <div className="space-y-3">
          <ModelRowsEditor
            rows={draft}
            onChange={setDraft}
            runtimes={runtimes}
            members={members}
            currentUserId={currentUserId}
            disabled={busy}
            single={shown === "single"}
            baseRows={shown === "personal" && sharedId ? data.profile_rows : undefined}
          />
          {shown === "single" ? <p className="text-caption text-muted-foreground">{t(($) => $.models.one_hint)}</p> : null}
          <div className="flex gap-2">
            <Button size="sm" variant="ghost" disabled={busy} onClick={reset}>
              {t(($) => $.models.cancel)}
            </Button>
            <Button
              size="sm"
              disabled={busy || draft.length === 0}
              onClick={() => void (shown === "single" ? saveSingleDraft() : savePersonalDraft())}
            >
              {t(($) => $.models.save)}
            </Button>
          </div>
          <p className="text-caption text-muted-foreground">{t(($) => $.models.next_run_hint)}</p>
        </div>
      ) : null}

      <ResetPersonalDialog
        open={confirmReset}
        baseName={data.base_profile_name}
        busy={personal.reset.isPending}
        onConfirm={() => void resetToShared()}
        onOpenChange={setConfirmReset}
      />

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
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon-xs"
                        aria-label={t(($) => $.models.tools_remove, { name })}
                        disabled={busy}
                        onClick={() => void save({ disallowed_tools: tools.filter((x) => x !== name) })}
                      >
                        <X className="size-3" />
                      </Button>
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
                      <span className="text-muted-foreground">
                        {t(($) => $.models.account_label)}: <AccountFactLine fact={entry.account_fact} />
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
    </div>
  );
}
