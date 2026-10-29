"use client";

import { useEffect, useRef, useState } from "react";
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { MoreHorizontal, Plus, RefreshCw, Workflow } from "@multica/ui/icons";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { stageDispatchKeys, stageDispatchListOptions, useSetIssueStageDispatch } from "@multica/core/conveyor";
import { useWorkspaceId } from "@multica/core/hooks";
import { childIssuesOptions, issueKeys } from "@multica/core/issues/queries";
import { useWorkspacePaths } from "@multica/core/paths";
import { squadListOptions } from "@multica/core/workspace/queries";
import type { Issue, StageDispatchEpic } from "@multica/core/types";
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { cn } from "@multica/ui/lib/utils";
import { CollectionPageHeader } from "../layout/collection-page";
import { SectionTabs } from "../layout/section-tabs";
import { PAGE_GUTTER } from "../layout/page-header";
import { AppLink } from "../navigation";
import { useT } from "../i18n";

type CardState =
  | "running"
  | "cancelled"
  | "review"
  | "success"
  | "held"
  | "blocked"
  | "failed"
  | "created"
  | "queued";

interface Stage {
  stage: number | null;
  done: number;
  total: number;
  state: "done" | "active" | "waiting";
  issues: Issue[];
}

function isClosed(issue: Issue) {
  return (
    issue.status === "done" ||
    issue.status === "cancelled" ||
    issue.status_category === "done" ||
    issue.status_category === "closed"
  );
}

/** The card of a sub-issue, read from its status. */
export function cardState(issue: Issue): CardState {
  if (issue.status === "cancelled" || issue.status_category === "closed") return "cancelled";
  if (issue.status === "done" || issue.status_category === "done") return "success";
  if (issue.status === "waiting") return "held";
  if (issue.status === "blocked") return "blocked";
  if (issue.status === "in_review") return "review";
  if (issue.status_category === "started") return "running";
  if (issue.assignee_id) return "queued";
  return "created";
}

/** Stages as the server dispatches them: the first with open work not waiting for the customer is active. */
export function buildStages(children: Issue[]): Stage[] {
  const byStage = new Map<number | null, Issue[]>();
  for (const child of children) {
    const key = child.stage ?? null;
    byStage.set(key, [...(byStage.get(key) ?? []), child]);
  }
  const keys = [...byStage.keys()].sort((a, b) => (a === null ? 1 : b === null ? -1 : a - b));
  let activeFound = false;
  return keys.map((key) => {
    const issues = byStage.get(key) ?? [];
    const done = issues.filter(isClosed).length;
    const open = issues.some((issue) => !isClosed(issue) && issue.status !== "waiting");
    let state: Stage["state"] = "waiting";
    if (key !== null && !activeFound) {
      if (open) {
        state = "active";
        activeFound = true;
      } else {
        state = "done";
      }
    }
    return { stage: key, done, total: issues.length, state, issues };
  });
}

const CARD_ICONS: Record<CardState, string> = {
  running: "▶",
  cancelled: "∅",
  review: "⇄",
  success: "✓",
  held: "Ⅱ",
  blocked: "!",
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
  failed: "border-destructive/60 bg-destructive/10",
  created: "border-dashed text-muted-foreground",
  queued: "",
};

const PROBLEMS: CardState[] = ["failed", "held", "blocked"];

/** An issue key or a Multica link → the key or UUID the API accepts. */
export function parseRef(value: string): string | null {
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
  return /^[A-Za-z][A-Za-z0-9]*-\d+$/.test(ref) || /^[0-9a-f-]{36}$/i.test(ref) ? ref : null;
}

function shortTitle(title: string) {
  return title.replace(/^[^:]{2,40}:\s*/, "").replace(/\s*T-\d+\s*$/, "");
}

export function ConveyorPage() {
  const { t } = useT("agents");
  const wsId = useWorkspaceId();
  const queryClient = useQueryClient();
  const epics = useQuery(stageDispatchListOptions(wsId));
  const squads = useQuery(squadListOptions(wsId));
  const children = useQueries({
    queries: (epics.data ?? []).map((epic) => ({
      ...childIssuesOptions(wsId, epic.issue.id),
      refetchInterval: 15_000,
    })),
  });
  const setDispatch = useSetIssueStageDispatch(wsId);
  const [ref, setRef] = useState("");
  const [squadId, setSquadId] = useState("");
  const [dialogCard, setDialogCard] = useState<Issue | null>(null);
  const [content, setContent] = useState("");
  const [busy, setBusy] = useState(false);

  const squadOptions = (squads.data ?? []).filter((s) => !s.archived_at);
  const squadName = (id: string) => squadOptions.find((s) => s.id === id)?.name ?? id.slice(0, 8);
  const fetching = epics.isFetching || children.some((c) => c.isFetching);

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: stageDispatchKeys.all(wsId) });
    for (const epic of epics.data ?? []) {
      void queryClient.invalidateQueries({ queryKey: issueKeys.children(wsId, epic.issue.id) });
    }
  };

  const dispatch = (issueId: string, next: string | null, onDone?: () => void) =>
    setDispatch.mutate(
      { issueId, squadId: next },
      {
        onSuccess: () => {
          toast.success(t(($) => $.conveyor.done));
          onDone?.();
        },
        onError: (error) => toast.error(error instanceof Error ? error.message : String(error)),
      },
    );

  const addTarget = () => {
    const parsed = parseRef(ref);
    if (!parsed) {
      toast.error(t(($) => $.conveyor.bad_ref));
      return;
    }
    dispatch(parsed, squadId, () => setRef(""));
  };

  const act = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    try {
      await fn();
      toast.success(t(($) => $.conveyor.done));
      setDialogCard(null);
      refresh();
    } catch (error) {
      toast.error(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(false);
    }
  };

  const lanes = (epics.data ?? []).map((epic, index) => ({ epic, stages: buildStages(children[index]?.data ?? []), loaded: children[index]?.isSuccess ?? false }));

  return (
    <div className="flex h-full min-h-0 flex-col">
      <CollectionPageHeader
        icon={Workflow}
        title={t(($) => $.conveyor.title)}
        tabs={<SectionTabs section="issues" current="conveyor" />}
        actions={
          <Button variant="ghost" size="icon-sm" aria-label={t(($) => $.conveyor.refresh)} onClick={refresh} disabled={fetching}>
            <RefreshCw className={fetching ? "animate-spin" : undefined} />
          </Button>
        }
      />

      <div className={cn("min-h-0 flex-1 overflow-y-auto py-4", PAGE_GUTTER)}>
        <div className="space-y-4">
          {epics.error ? (
            <p className="rounded-md border border-destructive/40 px-3 py-2 text-caption text-destructive">{String(epics.error)}</p>
          ) : null}

          <form
            className="flex max-w-2xl flex-wrap gap-2"
            onSubmit={(event) => {
              event.preventDefault();
              addTarget();
            }}
          >
            <Input
              className="min-w-48 flex-1"
              value={ref}
              placeholder={t(($) => $.conveyor.ref_placeholder)}
              onChange={(event) => setRef(event.target.value)}
              aria-label={t(($) => $.conveyor.ref_placeholder)}
            />
            <Select
              items={squadOptions.map((s) => ({ value: s.id, label: s.name }))}
              value={squadId || null}
              onValueChange={(next) => setSquadId(next ?? "")}
            >
              <SelectTrigger className="w-48" aria-label={t(($) => $.conveyor.squad)}>
                <SelectValue placeholder={t(($) => $.conveyor.squad)} />
              </SelectTrigger>
              <SelectContent>
                {squadOptions.map((s) => (
                  <SelectItem key={s.id} value={s.id}>
                    {s.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Button type="submit" disabled={setDispatch.isPending || ref.trim() === "" || squadId === ""}>
              <Plus />
              {t(($) => $.conveyor.add)}
            </Button>
          </form>

          {lanes.map(({ epic, stages, loaded }) => (
            <Lane
              key={epic.issue.id}
              epic={epic}
              stages={stages}
              loaded={loaded}
              squad={squadName(epic.squad_id)}
              busy={setDispatch.isPending}
              onOff={() => {
                if (window.confirm(t(($) => $.conveyor.remove_confirm, { key: epic.issue.identifier }))) {
                  dispatch(epic.issue.id, null);
                }
              }}
              onOpenActions={(card) => {
                setDialogCard(card);
                setContent("");
              }}
            />
          ))}

          {epics.isSuccess && !lanes.length ? <p className="text-caption text-muted-foreground">{t(($) => $.conveyor.empty)}</p> : null}
        </div>
      </div>

      <Dialog open={dialogCard !== null} onOpenChange={(open) => !open && setDialogCard(null)}>
        <DialogContent className="sm:max-w-lg">
          {dialogCard ? (
            <>
              <DialogHeader>
                <DialogTitle>
                  {dialogCard.identifier} · {shortTitle(dialogCard.title)}
                </DialogTitle>
                <DialogDescription>{t(($) => $.conveyor.dialog_idle)}</DialogDescription>
              </DialogHeader>
              <Textarea
                rows={5}
                value={content}
                maxLength={16000}
                placeholder={t(($) => $.conveyor.continue_placeholder)}
                onChange={(event) => setContent(event.target.value)}
              />
              <DialogFooter className="flex-wrap gap-2">
                <DialogLink issue={dialogCard} />
                <Button variant="outline" disabled={busy} onClick={() => void act(() => api.rerunIssue(dialogCard.id))}>
                  {t(($) => $.conveyor.retry)}
                </Button>
                <Button
                  disabled={busy || content.trim() === ""}
                  onClick={() => void act(() => api.createComment(dialogCard.id, content))}
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

function DialogLink({ issue }: { issue: Issue }) {
  const { t } = useT("agents");
  const paths = useWorkspacePaths();
  return (
    <AppLink
      href={paths.issueDetail(issue.id)}
      className="mr-auto self-center text-body text-muted-foreground hover:text-foreground hover:underline"
    >
      {t(($) => $.conveyor.open_issue)}
    </AppLink>
  );
}

function Lane({
  epic,
  stages,
  loaded,
  squad,
  busy,
  onOff,
  onOpenActions,
}: {
  epic: StageDispatchEpic;
  stages: Stage[];
  loaded: boolean;
  squad: string;
  busy: boolean;
  onOff: () => void;
  onOpenActions: (card: Issue) => void;
}) {
  const { t } = useT("agents");
  const paths = useWorkspacePaths();
  const cards = stages.flatMap((stage) => stage.issues);
  const count = (kind: CardState) => cards.filter((card) => cardState(card) === kind).length;
  const done = cards.filter(isClosed).length;
  const active = stages.find((stage) => stage.state === "active");
  const current = active?.issues ?? [];
  const status = active ? t(($) => $.conveyor.lane_stage, { stage: active.stage ?? 0 }) : t(($) => $.conveyor.lane_finished);
  return (
    <article className="rounded-lg border p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h2 className="text-body font-medium">
            <AppLink href={paths.issueDetail(epic.issue.id)} className="mr-2 font-mono hover:underline">
              {epic.issue.identifier}
            </AppLink>
            {epic.issue.title}
          </h2>
          <div className="mt-1.5 flex flex-wrap items-center gap-2">
            <Badge>{status}</Badge>
            <span className="text-caption text-muted-foreground">{t(($) => $.conveyor.lane_squad, { squad })}</span>
          </div>
        </div>
        <Button size="sm" variant="ghost" disabled={busy} onClick={onOff}>
          {t(($) => $.conveyor.remove)}
        </Button>
      </div>

      {loaded ? (
        <>
          <div className="mt-3">
            <div className="flex h-1.5 overflow-hidden rounded-full bg-muted">
              {(
                [
                  ["bg-emerald-500", done],
                  ["bg-amber-500", count("review")],
                  ["bg-primary", count("running")],
                ] as const
              ).map(([color, n]) =>
                n && cards.length ? <span key={color} className={color} style={{ width: `${(100 * n) / cards.length}%` }} /> : null,
              )}
            </div>
            <p className="mt-1 text-caption text-muted-foreground">
              {t(($) => $.conveyor.progress, {
                done,
                total: cards.length,
                stagesDone: stages.filter((stage) => stage.state === "done").length,
                stages: stages.filter((stage) => stage.stage !== null).length,
              })}
              {count("review") ? ` · ${t(($) => $.conveyor.in_review, { count: count("review") })}` : ""}
            </p>
          </div>
          {current.some((card) => PROBLEMS.includes(cardState(card))) ? (
            <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-caption">
              {current
                .filter((card) => PROBLEMS.includes(cardState(card)))
                .map((card) => (
                  <span key={card.id}>
                    {CARD_ICONS[cardState(card)]} {card.identifier} — {t(($) => $.conveyor.card[cardState(card)])}
                  </span>
                ))}
            </div>
          ) : null}
          <StageTrack stages={stages} onOpenActions={onOpenActions} />
        </>
      ) : null}
    </article>
  );
}

function StageTrack({ stages, onOpenActions }: { stages: Stage[]; onOpenActions: (card: Issue) => void }) {
  const { t } = useT("agents");
  const paths = useWorkspacePaths();
  const trackRef = useRef<HTMLDivElement>(null);
  const activeRef = useRef<HTMLElement>(null);
  const activeStage = stages.find((stage) => stage.state === "active")?.stage ?? null;

  // Scroll to the active stage only when it changes, so manual scrolling sticks.
  useEffect(() => {
    const track = trackRef.current;
    const active = activeRef.current;
    if (track && active) track.scrollLeft = Math.max(0, active.offsetLeft - track.offsetLeft - 200);
  }, [activeStage]);

  return (
    <div ref={trackRef} className="mt-3 flex gap-3 overflow-x-auto pb-2">
      {stages.map((stage) => (
        <section
          key={String(stage.stage)}
          ref={stage.state === "active" ? activeRef : undefined}
          className={cn("flex w-56 shrink-0 flex-col gap-1.5 rounded-md p-2", stage.state === "active" ? "bg-surface-selected" : "bg-muted/30")}
        >
          <div className="flex justify-between text-caption text-muted-foreground">
            <span>{stage.stage === null ? t(($) => $.conveyor.no_stage) : t(($) => $.conveyor.stage, { stage: stage.stage })}</span>
            <span className="tabular-nums">
              {stage.done}/{stage.total}
            </span>
          </div>
          {stage.issues.map((card) => {
            const kind = cardState(card);
            const sub = t(($) => $.conveyor.card[kind]);
            return (
              <div key={card.id} className={cn("group relative rounded-md border bg-background text-caption", CARD_CLASSES[kind])}>
                <AppLink
                  href={paths.issueDetail(card.id)}
                  className="flex gap-2 p-2 pr-8"
                  title={`${card.identifier} · ${t(($) => $.conveyor.card[kind])}`}
                >
                  <span className="w-3 shrink-0" aria-hidden="true">
                    {CARD_ICONS[kind]}
                  </span>
                  <span className="min-w-0">
                    <b className="font-mono">{card.identifier}</b>
                    <span className="line-clamp-2">{shortTitle(card.title)}</span>
                    <span className="text-muted-foreground">{sub}</span>
                  </span>
                </AppLink>
                <Button
                  size="icon-xs"
                  variant="ghost"
                  className="absolute top-1 right-1"
                  aria-label={t(($) => $.conveyor.card_actions, { key: card.identifier })}
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
