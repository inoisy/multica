"use client";

import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { MoreHorizontal, Pause, Play, Plus, RefreshCw, Workflow } from "lucide-react";
import { toast } from "sonner";
import { useWorkspacePaths, useWorkspaceSlug } from "@multica/core/paths";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { cn } from "@multica/ui/lib/utils";
import { CollectionPageHeader } from "../layout/collection-page";
import { PAGE_GUTTER } from "../layout/page-header";
import { AppLink } from "../navigation";
import { useT } from "../i18n";
import { ControlError, controlRequest } from "./control-api";

interface Target {
  key: string;
  name: string;
  kind: "epic" | "issue";
  enabled: boolean;
  paused: boolean;
  auto_merge: boolean;
}

interface Card {
  key: string;
  id: string;
  title: string;
  status: string;
  held: boolean;
  unmerged: string[];
  running: { agent: string; since: string } | null;
  last: { agent: string; status: string; at: string; quota: boolean } | null;
}

interface Stage {
  stage: number | null;
  done: number;
  total: number;
  state: "done" | "active" | "waiting";
  issues: Card[];
}

interface Lane {
  key: string;
  name: string;
  kind: "epic" | "issue";
  paused: boolean;
  auto_merge: boolean;
  observed: boolean;
  active_stage: number | null;
  done: number;
  total: number;
  stages: Stage[];
}

interface ConveyorState {
  conveyor: { paused: boolean; can_control: boolean; targets: Target[] } | null;
  board: Lane[];
  events: { at: string; kind: string; text: string }[];
  conveyor_at: string | null;
  errors: string[];
  conveyor_errors: string[];
}

type CardState =
  | "running"
  | "cancelled"
  | "review"
  | "success"
  | "held"
  | "blocked"
  | "quota"
  | "failed"
  | "created"
  | "queued";

/** Та же классификация, что у пульта (`conveyor/control.js`). */
function cardState(card: Card): CardState {
  if (card.running) return "running";
  if (card.status === "cancelled" || card.status === "canceled") return "cancelled";
  if (card.unmerged.length || card.status === "in_review") return "review";
  if (card.status === "done") return "success";
  if (card.held) return "held";
  if (card.status === "blocked") return "blocked";
  if (card.last?.status === "failed") return card.last.quota ? "quota" : "failed";
  return card.status === "backlog" ? "created" : "queued";
}

const CARD_ICONS: Record<CardState, string> = {
  running: "▶",
  cancelled: "∅",
  review: "⇄",
  success: "✓",
  held: "Ⅱ",
  blocked: "!",
  quota: "⏸",
  failed: "✕",
  created: "",
  queued: "○",
};

const CARD_CLASSES: Record<CardState, string> = {
  running: "border-primary/60 bg-primary/10",
  cancelled: "opacity-60",
  review: "border-amber-500/50 bg-amber-500/10",
  success: "bg-muted/40 text-muted-foreground",
  held: "border-dashed",
  blocked: "border-destructive/50",
  quota: "border-amber-500/50",
  failed: "border-destructive/60 bg-destructive/10",
  created: "border-dashed text-muted-foreground",
  queued: "",
};

const PROBLEMS: CardState[] = ["quota", "failed", "held", "blocked"];

function minutesSince(since: string | null | undefined, now: number) {
  const time = since ? new Date(since).getTime() : NaN;
  if (Number.isNaN(time)) return "";
  const total = Math.max(0, Math.round((now - time) / 60000));
  return total < 60 ? `${total} мин` : `${Math.floor(total / 60)} ч ${total % 60} мин`;
}

function formatTime(value: string | null | undefined) {
  if (!value) return "";
  const parsed = new Date(value.includes("T") ? value : `${value.replace(" ", "T")}Z`);
  return Number.isNaN(parsed.getTime())
    ? value
    : parsed.toLocaleString(undefined, {
        day: "2-digit",
        month: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
      });
}

/** Номер, ссылка Multica или UUID задачи → ссылка для пульта; голое число — задача stt. */
function parseRef(value: string): string | null {
  let ref = value.trim();
  if (ref.includes("://")) {
    try {
      const url = new URL(ref);
      ref =
        url.searchParams.get("issue") ??
        url.pathname.match(/[A-Za-z][A-Za-z0-9]*-\d+/)?.[0] ??
        url.pathname.match(/[0-9a-f-]{36}/i)?.[0] ??
        "";
    } catch {
      return null;
    }
  }
  if (/^\d+$/.test(ref)) ref = `STT-${ref}`;
  return /^[A-Za-z][A-Za-z0-9]*-\d+$/.test(ref) || /^[0-9a-f-]{36}$/i.test(ref)
    ? ref
    : null;
}

function shortTitle(title: string) {
  return title.replace(/^[^:]{2,40}:\s*/, "").replace(/\s*T-\d+\s*$/, "");
}

export function ConveyorPage() {
  const { t } = useT("agents");
  const paths = useWorkspacePaths();
  const wsSlug = useWorkspaceSlug();
  const queryClient = useQueryClient();
  const queryKey = ["control", "state", wsSlug] as const;
  const query = useQuery({
    queryKey,
    queryFn: () => controlRequest<ConveyorState>("state", undefined, wsSlug),
    refetchInterval: 15_000,
    retry: false,
  });
  const state = query.data;
  const [busy, setBusy] = useState(false);
  const [ref, setRef] = useState("");
  const [dialogCard, setDialogCard] = useState<Card | null>(null);
  const [content, setContent] = useState("");
  const now = Date.now();

  const run = async (path: string, body: unknown) => {
    setBusy(true);
    try {
      const result = await controlRequest<{ message?: string }>(path, body, wsSlug);
      toast.success(result.message ?? t(($) => $.conveyor.done));
      await queryClient.invalidateQueries({ queryKey });
      return true;
    } catch (error) {
      toast.error(error instanceof Error ? error.message : String(error));
      return false;
    } finally {
      setBusy(false);
    }
  };

  const control = state?.conveyor ?? null;
  const canControl = control?.can_control ?? false;
  const errors = [...(state?.errors ?? []), ...(state?.conveyor_errors ?? [])];
  const loadError =
    query.error instanceof ControlError
      ? query.error.status === 401
        ? t(($) => $.conveyor.login_required)
        : query.error.message
      : query.error
        ? String(query.error)
        : null;

  const addTarget = async () => {
    const parsed = parseRef(ref);
    if (!parsed) {
      toast.error(t(($) => $.conveyor.bad_ref));
      return;
    }
    if (await run("conveyor/targets", { ref: parsed, action: "add" })) setRef("");
  };

  const targetButtons = (target: Target) =>
    canControl ? (
      <div className="flex shrink-0 flex-wrap gap-1.5">
        {!target.enabled ? (
          <Button size="sm" disabled={busy} onClick={() => run("conveyor/targets", { ref: target.key, action: "add" })}>
            {t(($) => $.conveyor.enable)}
          </Button>
        ) : target.paused ? (
          <Button size="sm" disabled={busy} onClick={() => run("conveyor/targets", { ref: target.key, action: "resume" })}>
            <Play />
            {t(($) => $.conveyor.resume)}
          </Button>
        ) : (
          <Button size="sm" variant="outline" disabled={busy} onClick={() => run("conveyor/targets", { ref: target.key, action: "pause" })}>
            <Pause />
            {t(($) => $.conveyor.pause)}
          </Button>
        )}
        <Button
          size="sm"
          variant="ghost"
          disabled={busy}
          onClick={() => {
            if (window.confirm(t(($) => $.conveyor.remove_confirm, { key: target.key }))) {
              void run("conveyor/targets", { ref: target.key, action: "remove" });
            }
          }}
        >
          {t(($) => $.conveyor.remove)}
        </Button>
      </div>
    ) : null;

  const excluded = (control?.targets ?? []).filter((target) => !target.enabled);
  const lanes = state?.board ?? [];
  const working = lanes.filter((lane) => !lane.paused).length;

  return (
    <div className="flex h-full min-h-0 flex-col">
      <CollectionPageHeader
        icon={Workflow}
        title={t(($) => $.conveyor.title)}
        description={
          state?.conveyor_at
            ? t(($) => $.conveyor.tick, { time: formatTime(state.conveyor_at) })
            : undefined
        }
        actions={
          <div className="flex items-center gap-1">
            {canControl && control ? (
              <Button
                size="sm"
                variant={control.paused ? "default" : "outline"}
                disabled={busy}
                onClick={() => run("conveyor/pause", { paused: !control.paused })}
              >
                {control.paused ? <Play /> : <Pause />}
                {control.paused ? t(($) => $.conveyor.resume_all) : t(($) => $.conveyor.pause_all)}
              </Button>
            ) : null}
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={t(($) => $.conveyor.refresh)}
              onClick={() => void query.refetch()}
              disabled={query.isFetching}
            >
              <RefreshCw className={query.isFetching ? "animate-spin" : undefined} />
            </Button>
          </div>
        }
      />

      <div className={cn("min-h-0 flex-1 overflow-y-auto py-4", PAGE_GUTTER)}>
        <div className="space-y-4">
          {loadError ? (
            <p className="rounded-md border border-destructive/40 px-3 py-2 text-caption text-destructive">
              {loadError}
            </p>
          ) : null}
          {errors.length ? (
            <p className="rounded-md border border-amber-500/40 px-3 py-2 text-caption">
              {errors.join(" · ")}
            </p>
          ) : null}

          {control ? (
            <p className="text-caption text-muted-foreground">
              {control.paused
                ? t(($) => $.conveyor.paused_note)
                : working
                  ? t(($) => $.conveyor.working, { count: working, total: lanes.length })
                  : t(($) => $.conveyor.idle)}
            </p>
          ) : null}

          {canControl ? (
            <form
              className="flex max-w-xl gap-2"
              onSubmit={(event) => {
                event.preventDefault();
                void addTarget();
              }}
            >
              <Input
                value={ref}
                placeholder={t(($) => $.conveyor.ref_placeholder)}
                onChange={(event) => setRef(event.target.value)}
                aria-label={t(($) => $.conveyor.ref_placeholder)}
              />
              <Button type="submit" disabled={busy || ref.trim() === ""}>
                <Plus />
                {t(($) => $.conveyor.add)}
              </Button>
            </form>
          ) : control ? (
            <p className="text-caption text-muted-foreground">{t(($) => $.conveyor.readonly)}</p>
          ) : null}

          {lanes.map((lane) => {
            const target =
              control?.targets.find((item) => item.key === lane.key) ??
              ({ key: lane.key, name: lane.name, kind: lane.kind, enabled: true, paused: lane.paused, auto_merge: lane.auto_merge } as Target);
            const cards = lane.stages.flatMap((stage) => stage.issues);
            const count = (kind: CardState) => cards.filter((card) => cardState(card) === kind).length;
            const current = lane.stages.filter((stage) => stage.state === "active").flatMap((stage) => stage.issues);
            const status = control?.paused
              ? t(($) => $.conveyor.all_paused)
              : lane.paused
                ? t(($) => $.conveyor.lane_paused)
                : !lane.observed
                  ? t(($) => $.conveyor.lane_waiting_tick)
                  : lane.active_stage === null
                    ? t(($) => $.conveyor.lane_finished)
                    : t(($) => $.conveyor.lane_stage, { stage: lane.active_stage });
            return (
              <article
                key={lane.key}
                className={cn("rounded-lg border p-4", (lane.paused || control?.paused) && "opacity-75")}
              >
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div className="min-w-0">
                    <h2 className="text-body font-medium">
                      <AppLink href={paths.issueDetail(lane.key)} className="mr-2 font-mono hover:underline">
                        {lane.key}
                      </AppLink>
                      {lane.name}
                    </h2>
                    <div className="mt-1.5 flex flex-wrap items-center gap-2">
                      <Badge variant={lane.paused || control?.paused ? "outline" : "default"}>{status}</Badge>
                      <span className="text-caption text-muted-foreground">
                        {lane.kind === "issue" ? t(($) => $.conveyor.kind_issue) : t(($) => $.conveyor.kind_epic)}
                        {" · "}
                        {lane.auto_merge ? t(($) => $.conveyor.auto_merge) : t(($) => $.conveyor.no_merge)}
                      </span>
                    </div>
                  </div>
                  {targetButtons(target)}
                </div>

                {!lane.observed ? (
                  <p className="mt-3 text-caption text-muted-foreground">{t(($) => $.conveyor.lane_first_tick)}</p>
                ) : (
                  <>
                    <div className="mt-3">
                      <div className="flex h-1.5 overflow-hidden rounded-full bg-muted">
                        {(
                          [
                            ["bg-emerald-500", lane.done],
                            ["bg-amber-500", count("review")],
                            ["bg-primary", count("running")],
                          ] as const
                        ).map(([color, n]) =>
                          n && lane.total ? (
                            <span key={color} className={color} style={{ width: `${(100 * n) / lane.total}%` }} />
                          ) : null,
                        )}
                      </div>
                      <p className="mt-1 text-caption text-muted-foreground">
                        {t(($) => $.conveyor.progress, {
                          done: lane.done,
                          total: lane.total,
                          stagesDone: lane.stages.filter((stage) => stage.state === "done").length,
                          stages: lane.stages.length,
                        })}
                        {count("review") ? ` · ${t(($) => $.conveyor.in_review, { count: count("review") })}` : ""}
                      </p>
                    </div>

                    {cards.some((card) => card.running) ||
                    current.some((card) => PROBLEMS.includes(cardState(card))) ? (
                      <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-caption">
                        {cards
                          .filter((card) => card.running)
                          .map((card) => (
                            <span key={card.key}>
                              ▶ {card.running!.agent} — {card.key} · {minutesSince(card.running!.since, now)}
                            </span>
                          ))}
                        {current
                          .filter((card) => PROBLEMS.includes(cardState(card)))
                          .map((card) => (
                            <span key={card.key} className={cardState(card) === "failed" ? "text-destructive" : ""}>
                              {CARD_ICONS[cardState(card)]} {card.key} — {t(($) => $.conveyor.card[cardState(card)])}
                            </span>
                          ))}
                      </div>
                    ) : null}

                    <StageTrack lane={lane} onOpenActions={(card) => {
                      setDialogCard(card);
                      setContent("");
                    }} />
                  </>
                )}
              </article>
            );
          })}

          {state && !lanes.length ? (
            <p className="text-caption text-muted-foreground">{t(($) => $.conveyor.empty)}</p>
          ) : null}

          {excluded.length ? (
            <section className="space-y-2">
              <h2 className="text-body font-medium">{t(($) => $.conveyor.excluded_title)}</h2>
              {excluded.map((target) => (
                <div key={target.key} className="flex flex-wrap items-center justify-between gap-2 rounded-md border px-3 py-2">
                  <div className="min-w-0 text-body">
                    <span className="mr-2 font-mono">{target.key}</span>
                    {target.name}
                    <p className="text-caption text-muted-foreground">{t(($) => $.conveyor.excluded_note)}</p>
                  </div>
                  {targetButtons(target)}
                </div>
              ))}
            </section>
          ) : null}

          {state?.events.length ? (
            <section className="space-y-1">
              <h2 className="text-body font-medium">{t(($) => $.conveyor.events_title)}</h2>
              <ul className="space-y-0.5 text-caption">
                {[...state.events]
                  .sort((a, b) => (b.at ?? "").localeCompare(a.at ?? ""))
                  .slice(0, 30)
                  .map((event, index) => (
                    <li key={`${event.at}-${index}`} className="flex gap-3">
                      <time className="shrink-0 tabular-nums text-muted-foreground">{formatTime(event.at)}</time>
                      <span>{event.text}</span>
                    </li>
                  ))}
              </ul>
            </section>
          ) : null}
        </div>
      </div>

      <Dialog open={dialogCard !== null} onOpenChange={(open) => !open && setDialogCard(null)}>
        <DialogContent className="sm:max-w-lg">
          {dialogCard ? (
            <>
              <DialogHeader>
                <DialogTitle>
                  {dialogCard.key} · {shortTitle(dialogCard.title)}
                </DialogTitle>
                <DialogDescription>
                  {dialogCard.running
                    ? t(($) => $.conveyor.dialog_running, { agent: dialogCard.running.agent })
                    : t(($) => $.conveyor.dialog_idle)}
                </DialogDescription>
              </DialogHeader>
              <Textarea
                rows={5}
                value={content}
                maxLength={16000}
                placeholder={t(($) => $.conveyor.continue_placeholder)}
                onChange={(event) => setContent(event.target.value)}
              />
              <DialogFooter className="flex-wrap gap-2">
                <AppLink
                  href={paths.issueDetail(dialogCard.id)}
                  className="mr-auto self-center text-body text-muted-foreground hover:text-foreground hover:underline"
                >
                  {t(($) => $.conveyor.open_issue)}
                </AppLink>
                <Button
                  variant="outline"
                  disabled={busy || dialogCard.running !== null}
                  title={dialogCard.running ? t(($) => $.conveyor.retry_running) : undefined}
                  onClick={async () => {
                    if (await run(`issues/${dialogCard.id}/retry`, {})) setDialogCard(null);
                  }}
                >
                  {t(($) => $.conveyor.retry)}
                </Button>
                <Button
                  disabled={busy || content.trim() === ""}
                  onClick={async () => {
                    if (await run(`issues/${dialogCard.id}/continue`, { content })) setDialogCard(null);
                  }}
                >
                  {t(($) => $.conveyor.continue)}
                </Button>
              </DialogFooter>
            </>
          ) : null}
        </DialogContent>
      </Dialog>
    </div>
  );
}

function StageTrack({ lane, onOpenActions }: { lane: Lane; onOpenActions: (card: Card) => void }) {
  const { t } = useT("agents");
  const paths = useWorkspacePaths();
  const trackRef = useRef<HTMLDivElement>(null);
  const activeRef = useRef<HTMLElement>(null);
  const now = Date.now();

  // Прокрутка к текущему этапу — только когда он сменился, ручную прокрутку не сбиваем.
  useEffect(() => {
    const track = trackRef.current;
    const active = activeRef.current;
    if (track && active) track.scrollLeft = Math.max(0, active.offsetLeft - track.offsetLeft - 200);
  }, [lane.active_stage]);

  return (
    <div ref={trackRef} className="mt-3 flex gap-3 overflow-x-auto pb-2">
      {lane.stages.map((stage) => (
        <section
          key={String(stage.stage)}
          ref={stage.state === "active" ? activeRef : undefined}
          className={cn(
            "flex w-56 shrink-0 flex-col gap-1.5 rounded-md p-2",
            stage.state === "active" ? "bg-surface-selected" : "bg-muted/30",
          )}
        >
          <div className="flex justify-between text-caption text-muted-foreground">
            <span>
              {stage.stage === null
                ? t(($) => $.conveyor.no_stage)
                : t(($) => $.conveyor.stage, { stage: stage.stage })}
            </span>
            <span className="tabular-nums">
              {stage.done}/{stage.total}
            </span>
          </div>
          {stage.issues.map((card) => {
            const kind = cardState(card);
            const sub = card.running
              ? `${card.running.agent} · ${minutesSince(card.running.since, now)}`
              : t(($) => $.conveyor.card[kind]) +
                (card.last && kind !== "created" ? ` · ${card.last.agent}` : "");
            return (
              <div
                key={card.key}
                className={cn("group relative rounded-md border bg-background text-caption", CARD_CLASSES[kind])}
              >
                <AppLink
                  href={paths.issueDetail(card.id)}
                  className="flex gap-2 p-2 pr-8"
                  title={`${card.key} · ${t(($) => $.conveyor.card[kind])}`}
                >
                  <span className="w-3 shrink-0" aria-hidden="true">
                    {CARD_ICONS[kind]}
                  </span>
                  <span className="min-w-0">
                    <b className="font-mono">{card.key}</b>
                    <span className="line-clamp-2">{shortTitle(card.title)}</span>
                    <span className="text-muted-foreground">{sub}</span>
                  </span>
                </AppLink>
                <Button
                  size="icon-xs"
                  variant="ghost"
                  className="absolute top-1 right-1"
                  aria-label={t(($) => $.conveyor.card_actions, { key: card.key })}
                  onClick={() => onOpenActions(card)}
                >
                  <MoreHorizontal />
                </Button>
              </div>
            );
          })}
        </section>
      ))}
    </div>
  );
}
