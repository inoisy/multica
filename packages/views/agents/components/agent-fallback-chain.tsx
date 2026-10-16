"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowDown, ArrowUp, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";
import type {
  Agent,
  AgentRuntime,
  MemberWithUser,
} from "@multica/core/types";
import { api } from "@multica/core/api";
import { isRuntimeUsableForUser } from "@multica/core/runtimes";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspaceSlug } from "@multica/core/paths";
import { workspaceKeys } from "@multica/core/workspace";
import { Badge } from "@multica/ui/components/ui/badge";
import { Input } from "@multica/ui/components/ui/input";
import { Button } from "@multica/ui/components/ui/button";
import { Textarea } from "@multica/ui/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import {
  SettingsCard,
  SettingsRow,
  SettingsSection,
} from "../../settings/components/settings-layout";
import { useT } from "../../i18n";
import { ModelPicker } from "./inspector/model-picker";
import { RuntimePicker } from "./inspector/runtime-picker";
import { ThinkingSettingField } from "./inspector/thinking-prop-row";

/**
 * Цепочка фоллбеков живёт у пульта конвейера (multica-control, /control/),
 * а не в Multica: сторож переключает профиль при лимите провайдера. Пульт
 * отвечает в том же origin и принимает ту же сессию, что и API Multica.
 */
const CONTROL_API = "/control/api";

interface FallbackProfile {
  runtime_id: string;
  model: string;
  thinking_level: string;
  /** null — пульт выведет из профиля того же семейства. */
  custom_args: string[] | null;
}

interface FallbackProfileView extends FallbackProfile {
  runtime: string;
  provider: string | null;
  missing: boolean;
  available: boolean | null;
  cooldown_until: string | null;
  grade: number;
}

interface FallbackTier {
  id: string;
  name: string;
  floor: number;
  description: string;
  builtin: boolean;
  chain: FallbackProfile[];
}

interface Grades {
  models?: Record<string, number>;
  runtimes?: Record<string, number>;
  level_bonus?: Record<string, number>;
}

interface FallbackView {
  enabled: boolean;
  source: "config" | "panel" | null;
  in_config: boolean;
  can_edit: boolean;
  mode: "auto" | "manual";
  selected: number | null;
  active_index: number | null;
  since: string | null;
  restore_hours: number;
  restore_minutes: number;
  waiting_until: string | null;
  pending_apply: boolean;
  tier: string | null;
  can_manage_tiers: boolean;
  grade_names: Record<string, string>;
  grades: Grades;
  tiers: FallbackTier[];
  chain: FallbackProfileView[];
}

/** Та же формула, что `control_state.grade`: модель, иначе runtime, плюс поправка за рассуждение. */
function gradeOf(profile: FallbackProfile, grades: Grades): number {
  const base =
    grades.models?.[profile.model] || grades.runtimes?.[profile.runtime_id] || 0;
  if (!base) return 0;
  const bonus = grades.level_bonus?.[profile.thinking_level] ?? 0;
  return Math.max(1, Math.min(5, base + bonus));
}

class ControlError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
  }
}

function controlHeaders(): Record<string, string> {
  const token = api.getToken?.();
  if (token) return { Authorization: `Bearer ${token}` };
  const cookies = new Map(
    document.cookie
      .split(";")
      .map((part) => part.trim().split("=") as [string, string])
      .filter(([key]) => key),
  );
  const csrf = cookies.get("multica_csrf_session") ?? cookies.get("multica_csrf");
  return csrf ? { "X-CSRF-Token": decodeURIComponent(csrf) } : {};
}

async function controlRequest<T>(
  path: string,
  body?: unknown,
  workspaceSlug?: string | null,
): Promise<T> {
  const headers = controlHeaders();
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    headers["X-Control-Request"] = "1";
  }
  const query = workspaceSlug ? `?ws=${encodeURIComponent(workspaceSlug)}` : "";
  const response = await fetch(`${CONTROL_API}/${path}${query}`, {
    method: body === undefined ? "GET" : "POST",
    credentials: "same-origin",
    cache: "no-store",
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data = (await response.json().catch(() => ({}))) as { error?: string };
  if (!response.ok) {
    throw new ControlError(response.status, data.error ?? response.statusText);
  }
  return data as T;
}

function toDraft(chain: FallbackProfileView[]): FallbackProfile[] {
  return chain.map((p) => ({
    runtime_id: p.runtime_id,
    model: p.model,
    thinking_level: p.thinking_level,
    custom_args: p.custom_args,
  }));
}

function formatTime(value: string) {
  return new Date(value).toLocaleString(undefined, {
    day: "2-digit",
    month: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

export function AgentFallbackChain({
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
  const wsId = useWorkspaceId();
  // Пульт обслуживает несколько workspace: свой берёт из параметра.
  const wsSlug = useWorkspaceSlug();
  const queryClient = useQueryClient();
  const queryKey = ["control", "fallback", wsSlug, agent.id] as const;
  const query = useQuery({
    queryKey,
    queryFn: () =>
      controlRequest<FallbackView>(`agents/${agent.id}/fallback`, undefined, wsSlug),
    retry: false,
    refetchInterval: 30_000,
  });
  const view = query.data;
  const [draft, setDraft] = useState<FallbackProfile[] | null>(null);
  // undefined — уровень не меняли; null — «без уровня».
  const [tierDraft, setTierDraft] = useState<string | null | undefined>(undefined);
  const [tierForm, setTierForm] = useState<{ name: string; floor: number } | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    setDraft(null);
    setTierDraft(undefined);
    setTierForm(null);
  }, [agent.id]);

  const profiles = draft ?? (view ? toDraft(view.chain) : []);
  const dirty = draft !== null || tierDraft !== undefined;
  const tierId = tierDraft === undefined ? (view?.tier ?? null) : tierDraft;
  const tier = view?.tiers.find((item) => item.id === tierId) ?? null;
  const grades = view?.grades ?? {};
  const gradeName = (grade: number) =>
    grade ? (view?.grade_names[String(grade)] ?? String(grade)) : t(($) => $.fallback.grade_unknown);
  const weak = tier
    ? profiles.flatMap((p, i) => (gradeOf(p, grades) < tier.floor ? [i] : []))
    : [];
  const canEdit = view?.can_edit ?? false;

  const runtimeById = useMemo(
    () => new Map(runtimes.map((r) => [r.id, r])),
    [runtimes],
  );

  if (query.error instanceof ControlError && query.error.status === 404) {
    return null;
  }
  if (query.isPending) return null;

  const refresh = async () => {
    await queryClient.invalidateQueries({ queryKey });
    if (wsId) {
      await queryClient.invalidateQueries({ queryKey: workspaceKeys.agents(wsId) });
    }
  };

  const run = async (path: string, body: unknown) => {
    setBusy(true);
    try {
      const result = await controlRequest<{ message?: string }>(path, body, wsSlug);
      toast.success(result.message ?? t(($) => $.fallback.saved));
      setDraft(null);
      setTierDraft(undefined);
      setTierForm(null);
      await refresh();
    } catch (error) {
      toast.error(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(false);
    }
  };

  const edit = (next: FallbackProfile[]) => setDraft(next);
  const patch = (index: number, change: Partial<FallbackProfile>) =>
    edit(profiles.map((p, i) => (i === index ? { ...p, ...change } : p)));
  const move = (index: number, delta: number) => {
    const next = [...profiles];
    const [item] = next.splice(index, 1);
    next.splice(index + delta, 0, item!);
    edit(next);
  };
  const addProfile = () => {
    const used = new Set(profiles.map((p) => p.runtime_id));
    const runtime =
      runtimes.find((r) => !used.has(r.id) && isRuntimeUsableForUser(r, currentUserId)) ??
      runtimes.find((r) => r.id === agent.runtime_id);
    if (!runtime) return;
    edit([
      ...profiles,
      { runtime_id: runtime.id, model: "", thinking_level: "", custom_args: null },
    ]);
  };
  const enable = () =>
    edit([
      {
        runtime_id: agent.runtime_id,
        model: agent.model ?? "",
        thinking_level: agent.thinking_level ?? "",
        custom_args: agent.custom_args ?? [],
      },
    ]);

  const modeValue =
    view?.mode === "manual" && view.selected != null ? String(view.selected) : "auto";
  const chooseTier = (value: string) => {
    const next = view?.tiers.find((item) => item.id === value) ?? null;
    setTierDraft(next ? next.id : null);
    // Уровень приносит свою стартовую цепочку; аргументы выведет пульт под этого агента.
    if (next) edit(next.chain.map((p) => ({ ...p, custom_args: null })));
  };
  const tierItems = [
    { value: "none", label: t(($) => $.fallback.tier_none) },
    ...(view?.tiers ?? []).map((item) => ({
      value: item.id,
      label: t(($) => $.fallback.tier_option, {
        name: item.name,
        floor: gradeName(item.floor),
      }),
    })),
  ];
  const minGrade = profiles.length
    ? Math.min(...profiles.map((p) => gradeOf(p, grades)))
    : 0;
  const floorItems = [1, 2, 3, 4, 5].map((n) => ({
    value: String(n),
    label: `${n} · ${gradeName(n)}`,
  }));
  const saveChain = () =>
    run(`agents/${agent.id}/fallback`, {
      chain: profiles,
      ...(tierId ? { tier: tierId } : {}),
    });

  const modeItems = [
    { value: "auto", label: t(($) => $.fallback.mode_auto) },
    ...(view?.chain ?? []).map((p, i) => ({
      value: String(i),
      label: t(($) => $.fallback.mode_manual, {
        n: i + 1,
        name: [p.runtime, p.model].filter(Boolean).join(" · "),
      }),
    })),
  ];
  const loadError =
    query.error instanceof Error ? query.error.message : null;

  return (
    <SettingsSection
      title={t(($) => $.fallback.title)}
      description={t(($) => $.fallback.description, {
        hours: view?.restore_hours ?? 5,
      })}
      action={
        dirty ? (
          <div className="flex gap-2">
            <Button
              size="sm"
              variant="ghost"
              disabled={busy}
              onClick={() => {
                setDraft(null);
                setTierDraft(undefined);
              }}
            >
              {t(($) => $.fallback.cancel)}
            </Button>
            <Button
              size="sm"
              disabled={busy || weak.length > 0 || profiles.length === 0}
              onClick={saveChain}
            >
              {t(($) => $.fallback.save)}
            </Button>
          </div>
        ) : null
      }
    >
      {loadError ? (
        <p className="px-0.5 text-caption text-destructive">
          {t(($) => $.fallback.load_failed, { error: loadError })}
        </p>
      ) : null}

      {view && !view.can_edit ? (
        <p className="px-0.5 text-caption text-muted-foreground">
          {t(($) => $.fallback.readonly)}
        </p>
      ) : null}

      {view?.pending_apply ? (
        <p className="rounded-md border border-surface-border px-3 py-2 text-caption">
          {t(($) => $.fallback.pending)}
        </p>
      ) : null}

      {view?.waiting_until ? (
        <p className="rounded-md border border-surface-border px-3 py-2 text-caption">
          {t(($) => $.fallback.waiting, { time: formatTime(view.waiting_until) })}
        </p>
      ) : null}

      {view ? (
        <SettingsCard>
          <SettingsRow
            label={t(($) => $.fallback.tier_label)}
            description={tier ? tier.description || t(($) => $.fallback.tier_hint) : t(($) => $.fallback.tier_hint)}
            size="select-wide"
          >
            <Select
              items={tierItems}
              value={tierId ?? "none"}
              disabled={!canEdit || busy}
              onValueChange={(value) => value && chooseTier(value)}
            >
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {tierItems.map((item) => (
                  <SelectItem key={item.value} value={item.value}>
                    {item.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </SettingsRow>
          {weak.length > 0 ? (
            <SettingsRow label={t(($) => $.fallback.below_floor_title)}>
              <p className="text-caption text-destructive">
                {t(($) => $.fallback.below_floor, {
                  list: weak.map((i) => i + 1).join(", "),
                  floor: gradeName(tier?.floor ?? 0),
                })}
              </p>
            </SettingsRow>
          ) : null}
        </SettingsCard>
      ) : null}

      {view && profiles.length > 0 && !dirty ? (
        <SettingsCard>
          <SettingsRow
            label={t(($) => $.fallback.mode_label)}
            description={t(($) => $.fallback.mode_hint, {
              minutes: view.restore_minutes,
            })}
            size="select-wide"
          >
            <Select
              items={modeItems}
              value={modeValue}
              disabled={!canEdit || busy}
              onValueChange={(value) => {
                if (!value || value === modeValue) return;
                void run(
                  `agents/${agent.id}/profile`,
                  value === "auto"
                    ? { mode: "auto" }
                    : { mode: "manual", index: Number(value) },
                );
              }}
            >
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {modeItems.map((item) => (
                  <SelectItem key={item.value} value={item.value}>
                    {item.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </SettingsRow>
          <SettingsRow label={t(($) => $.fallback.current_label)} size="select-wide">
            <p className="text-body">
              {view.active_index == null
                ? t(($) => $.fallback.current_outside)
                : t(($) => $.fallback.current_profile, { n: view.active_index + 1 })}
              {view.since ? (
                <span className="text-muted-foreground">
                  {" "}
                  {t(($) => $.fallback.since, { time: formatTime(view.since) })}
                </span>
              ) : null}
            </p>
          </SettingsRow>
        </SettingsCard>
      ) : null}

      {profiles.map((profile, index) => {
        const runtime = runtimeById.get(profile.runtime_id) ?? null;
        const info = !dirty ? view?.chain[index] : undefined;
        const online =
          runtime?.status === "online" && isRuntimeUsableForUser(runtime, currentUserId);
        const active = !dirty && view?.active_index === index;
        return (
          <SettingsCard key={index}>
            <SettingsRow
              label={
                <span className="flex flex-wrap items-center gap-2">
                  {index === 0
                    ? t(($) => $.fallback.primary)
                    : t(($) => $.fallback.backup, { n: index })}
                  {active ? <Badge>{t(($) => $.fallback.active)}</Badge> : null}
                  <Badge variant={weak.includes(index) ? "destructive" : "outline"}>
                    {weak.includes(index)
                      ? t(($) => $.fallback.grade_below, { grade: gradeName(gradeOf(profile, grades)) })
                      : gradeName(gradeOf(profile, grades))}
                  </Badge>
                  {info?.available === false ? (
                    <Badge variant="destructive">{t(($) => $.fallback.quota_limit)}</Badge>
                  ) : info?.available === true ? (
                    <Badge variant="secondary">{t(($) => $.fallback.quota_ok)}</Badge>
                  ) : null}
                  {info?.cooldown_until && new Date(info.cooldown_until) > new Date() ? (
                    <Badge variant="outline">
                      {t(($) => $.fallback.cooldown, {
                        time: formatTime(info.cooldown_until),
                      })}
                    </Badge>
                  ) : null}
                  {info?.missing ? (
                    <Badge variant="destructive">{t(($) => $.fallback.runtime_missing)}</Badge>
                  ) : null}
                </span>
              }
            >
              {canEdit ? (
                <div className="flex gap-1">
                  <Button
                    size="icon-sm"
                    variant="ghost"
                    aria-label={t(($) => $.fallback.move_up)}
                    disabled={busy || index === 0}
                    onClick={() => move(index, -1)}
                  >
                    <ArrowUp />
                  </Button>
                  <Button
                    size="icon-sm"
                    variant="ghost"
                    aria-label={t(($) => $.fallback.move_down)}
                    disabled={busy || index === profiles.length - 1}
                    onClick={() => move(index, 1)}
                  >
                    <ArrowDown />
                  </Button>
                  <Button
                    size="icon-sm"
                    variant="ghost"
                    aria-label={t(($) => $.fallback.remove)}
                    disabled={busy}
                    onClick={() => edit(profiles.filter((_, i) => i !== index))}
                  >
                    <Trash2 />
                  </Button>
                </div>
              ) : null}
            </SettingsRow>
            <SettingsRow label={t(($) => $.fallback.runtime)} size="select-wide">
              <RuntimePicker
                variant="field"
                showLabel={false}
                value={profile.runtime_id}
                runtimes={runtimes}
                members={members}
                currentUserId={currentUserId}
                canEdit={canEdit && !busy}
                onChange={(id) =>
                  patch(index, {
                    runtime_id: id,
                    model: "",
                    thinking_level: "",
                    custom_args: null,
                  })
                }
              />
            </SettingsRow>
            <SettingsRow label={t(($) => $.fallback.model)} size="select-wide">
              <ModelPicker
                variant="field"
                showLabel={false}
                runtimeId={profile.runtime_id}
                runtimeOnline={online}
                value={profile.model}
                canEdit={canEdit && !busy}
                onChange={(model) => patch(index, { model })}
              />
            </SettingsRow>
            <ThinkingSettingField
              label={t(($) => $.fallback.thinking)}
              runtimeId={profile.runtime_id}
              runtimeOnline={online}
              provider={runtime?.provider ?? ""}
              model={profile.model}
              value={profile.thinking_level}
              canEdit={canEdit && !busy}
              onChange={(thinking_level) => patch(index, { thinking_level })}
            />
            <SettingsRow
              label={t(($) => $.fallback.args)}
              description={t(($) => $.fallback.args_hint)}
              size="text"
              align="start"
            >
              <Textarea
                rows={3}
                className="resize-y font-mono text-caption"
                disabled={!canEdit || busy}
                placeholder={t(($) => $.fallback.args_auto)}
                value={(profile.custom_args ?? []).join("\n")}
                onChange={(event) => {
                  const lines = event.target.value.split("\n");
                  patch(index, {
                    custom_args:
                      event.target.value.trim() === ""
                        ? null
                        : lines.filter((line) => line.trim() !== ""),
                  });
                }}
              />
            </SettingsRow>
          </SettingsCard>
        );
      })}

      {view && profiles.length === 0 ? (
        <p className="px-0.5 text-caption text-muted-foreground">
          {t(($) => $.fallback.disabled)}
        </p>
      ) : null}

      {canEdit ? (
        <div className="flex flex-wrap gap-2 px-0.5">
          {profiles.length === 0 ? (
            <Button size="sm" variant="outline" disabled={busy} onClick={enable}>
              <Plus />
              {t(($) => $.fallback.enable)}
            </Button>
          ) : (
            <Button size="sm" variant="outline" disabled={busy} onClick={addProfile}>
              <Plus />
              {t(($) => $.fallback.add)}
            </Button>
          )}
          {!dirty && view?.source === "panel" && view.in_config ? (
            <Button
              size="sm"
              variant="ghost"
              disabled={busy}
              onClick={() => run(`agents/${agent.id}/fallback`, { reset: true })}
            >
              {t(($) => $.fallback.reset)}
            </Button>
          ) : null}
          {!dirty && profiles.length > 0 ? (
            <Button
              size="sm"
              variant="ghost"
              disabled={busy}
              onClick={() => run(`agents/${agent.id}/fallback`, { chain: [] })}
            >
              {t(($) => $.fallback.disable)}
            </Button>
          ) : null}
          {view?.can_manage_tiers && profiles.length > 0 && !tierForm ? (
            <Button
              size="sm"
              variant="ghost"
              disabled={busy || minGrade === 0}
              onClick={() => setTierForm({ name: "", floor: minGrade })}
            >
              {t(($) => $.fallback.tier_create)}
            </Button>
          ) : null}
          {view?.can_manage_tiers && tier && !tier.builtin && !dirty ? (
            <Button
              size="sm"
              variant="ghost"
              disabled={busy}
              onClick={() => run(`fallback-tiers/${tier.id}/delete`, {})}
            >
              {t(($) => $.fallback.tier_delete, { name: tier.name })}
            </Button>
          ) : null}
        </div>
      ) : null}

      {tierForm ? (
        <SettingsCard>
          <SettingsRow label={t(($) => $.fallback.tier_name)} size="select-wide">
            <Input
              autoFocus
              value={tierForm.name}
              maxLength={60}
              onChange={(event) => setTierForm({ ...tierForm, name: event.target.value })}
            />
          </SettingsRow>
          <SettingsRow
            label={t(($) => $.fallback.tier_floor)}
            description={t(($) => $.fallback.tier_floor_hint, { grade: gradeName(minGrade) })}
            size="select-wide"
          >
            <Select
              items={floorItems.filter((item) => Number(item.value) <= minGrade)}
              value={String(tierForm.floor)}
              onValueChange={(value) => value && setTierForm({ ...tierForm, floor: Number(value) })}
            >
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {floorItems
                  .filter((item) => Number(item.value) <= minGrade)
                  .map((item) => (
                    <SelectItem key={item.value} value={item.value}>
                      {item.label}
                    </SelectItem>
                  ))}
              </SelectContent>
            </Select>
          </SettingsRow>
          <SettingsRow label="">
            <div className="flex gap-2">
              <Button size="sm" variant="ghost" disabled={busy} onClick={() => setTierForm(null)}>
                {t(($) => $.fallback.cancel)}
              </Button>
              <Button
                size="sm"
                disabled={busy || tierForm.name.trim() === ""}
                onClick={() =>
                  run("fallback-tiers", {
                    name: tierForm.name.trim(),
                    floor: tierForm.floor,
                    chain: profiles.map(({ runtime_id, model, thinking_level }) => ({
                      runtime_id,
                      model,
                      thinking_level,
                    })),
                  })
                }
              >
                {t(($) => $.fallback.tier_save)}
              </Button>
            </div>
          </SettingsRow>
        </SettingsCard>
      ) : null}
    </SettingsSection>
  );
}
