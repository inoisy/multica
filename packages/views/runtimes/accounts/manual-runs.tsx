"use client";

import { useState } from "react";
import { useInfiniteQuery } from "@tanstack/react-query";
import { Check, Copy, Terminal } from "@multica/ui/icons";
import { manualRunsOptions } from "@multica/core/subscriptions";
import type { ManualRun } from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { copyText } from "@multica/ui/lib/clipboard";
import { useT } from "../../i18n";
import { formatModelTime } from "../../model-profiles/format-time";

/** The command a person runs on the daemon host to start the account's own CLI by hand. */
export function manualRunCommand(key: string): string {
  return `multica subscription run ${key}`;
}

function CommandForHuman({ subscriptionKey }: { subscriptionKey: string }) {
  const { t } = useT("agents");
  const [copied, setCopied] = useState(false);
  const command = manualRunCommand(subscriptionKey);
  return (
    <div className="space-y-1" data-testid="manual-run-command">
      <p className="text-caption text-muted-foreground">{t(($) => $.subscriptions.manual.command_hint)}</p>
      <div className="flex min-w-0 items-center gap-2 rounded-md bg-muted px-3 py-2 font-mono text-caption">
        <Terminal className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
        <code className="min-w-0 flex-1 break-all">{command}</code>
        <Button
          type="button"
          size="icon-sm"
          variant="ghost"
          aria-label={t(($) => $.subscriptions.manual.copy)}
          onClick={() => {
            void copyText(command).then((ok) => {
              if (ok) setCopied(true);
            });
          }}
        >
          {copied ? <Check className="text-success" /> : <Copy />}
        </Button>
      </div>
    </div>
  );
}

function RunRow({ run }: { run: ManualRun }) {
  const { t } = useT("agents");
  return (
    <li className="space-y-1 rounded-md border px-3 py-2 text-caption" data-testid={`manual-run-${run.id}`}>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <span className="break-words font-medium">{run.user_name || run.user_id}</span>
        <Badge variant="outline" className="font-mono">
          {run.command}
        </Badge>
        {run.unfinished ? <Badge variant="warning">{t(($) => $.subscriptions.manual.unfinished)}</Badge> : null}
        {run.agent_cap_exceeded ? <Badge variant="warning">{t(($) => $.subscriptions.manual.over_cap)}</Badge> : null}
      </div>
      <p className="text-muted-foreground">
        {t(($) => $.subscriptions.manual.machine, { machine: run.daemon_id })}
      </p>
      <p className="tabular-nums text-muted-foreground">
        {formatModelTime(run.started_at)}
        {" → "}
        {run.finished_at ? formatModelTime(run.finished_at) : "—"}
        {run.exit_code !== null ? ` · ${t(($) => $.subscriptions.manual.exit_code, { code: run.exit_code })}` : ""}
      </p>
    </li>
  );
}

/**
 * Journal of the commands an instance admin ran by hand under the account:
 * who, on which machine, which CLI, when and with what exit code. Arguments
 * are never recorded. Only an instance admin in person can read it, so the
 * block is not rendered for anyone else and a refusal says so.
 */
export function ManualRuns({ subscriptionKey }: { subscriptionKey: string }) {
  const { t } = useT("agents");
  const query = useInfiniteQuery(manualRunsOptions(subscriptionKey));
  const runs = query.data?.pages.flatMap((page) => page.runs) ?? [];
  return (
    <section className="space-y-2" aria-labelledby={`manual-runs-${subscriptionKey}`} data-testid="manual-runs">
      <h3 id={`manual-runs-${subscriptionKey}`} className="text-body font-medium">
        {t(($) => $.subscriptions.manual.title)}
      </h3>
      <CommandForHuman subscriptionKey={subscriptionKey} />
      {query.isLoading ? <Skeleton className="h-16 rounded-md" /> : null}
      {query.isError ? (
        <p role="alert" className="text-caption text-destructive">
          {t(($) => $.subscriptions.manual.load_failed)}
        </p>
      ) : null}
      {query.isSuccess && runs.length === 0 ? (
        <p className="text-caption text-muted-foreground">{t(($) => $.subscriptions.manual.empty)}</p>
      ) : null}
      {runs.length ? (
        <ul className="space-y-1.5">
          {runs.map((run) => (
            <RunRow key={run.id} run={run} />
          ))}
        </ul>
      ) : null}
      {query.hasNextPage ? (
        <Button type="button" size="sm" variant="outline" disabled={query.isFetchingNextPage} onClick={() => void query.fetchNextPage()}>
          {t(($) => $.subscriptions.manual.more)}
        </Button>
      ) : null}
    </section>
  );
}
