"use client";

import { useState } from "react";
import { MoreHorizontal } from "@multica/ui/icons";
import { toast } from "sonner";
import {
  agentLimit,
  peopleShare,
  useDeleteSubscription,
  useUpdateSubscription,
  type SubscriptionAccount,
} from "@multica/core/subscriptions";
import type { Pool, Subscription } from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@multica/ui/components/ui/dropdown-menu";
import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../../i18n";
import { formatModelTime } from "../../model-profiles/format-time";
import { AgentLimitFields, limitDraftOf, parseLimitDraft, type AgentLimitDraft } from "./agent-limit-fields";
import { ManualRuns } from "./manual-runs";
import { PoolEnvironmentLinks } from "./pool-environments";
import { SubscriptionLoginStatus } from "./subscription-login";
import { UsageMeters } from "./usage-meters";

function errorText(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

/**
 * An opened account: load per window, the one limit agents have on each
 * window, sign-in, the pools and environments that spend it, and — for an
 * instance admin — the actions on the account. A change of the limit goes to
 * the server, which recomputes every verdict; this screen only refetches.
 */
export function AccountDetail({
  sub,
  account,
  pools,
  wsId,
  canManage,
  windowName,
  journal,
}: {
  sub: Subscription;
  account: SubscriptionAccount;
  pools: { pool: Pool; position: number }[];
  wsId: string;
  canManage: boolean;
  windowName: (window: string) => string;
  /** Show the journal of manual runs: the Accounts tab of an instance admin only. */
  journal: boolean;
}) {
  const { t } = useT("agents");
  const updateSubscription = useUpdateSubscription();
  const deleteSubscription = useDeleteSubscription();
  const [limits, setLimits] = useState<AgentLimitDraft | null>(null);
  const [renaming, setRenaming] = useState(false);
  const [newTitle, setNewTitle] = useState("");
  const [disabling, setDisabling] = useState(false);
  const [disableUntil, setDisableUntil] = useState("");
  const busy = updateSubscription.isPending || deleteSubscription.isPending;

  const here = sub.used_by.filter((user) => user.workspace_id === wsId);
  const elsewhere = new Set(sub.used_by.filter((user) => user.workspace_id !== wsId).map((user) => user.workspace_id));

  const patch = async (body: Parameters<typeof updateSubscription.mutateAsync>[0]["patch"]) => {
    try {
      await updateSubscription.mutateAsync({ key: sub.key, patch: body });
      toast.success(t(($) => $.subscriptions.saved));
      return true;
    } catch (error) {
      toast.error(errorText(error));
      return false;
    }
  };

  const saveLimits = async () => {
    const caps = limits ? parseLimitDraft(limits) : null;
    if (!caps) {
      toast.error(t(($) => $.subscriptions.bad_percent));
      return;
    }
    if (await patch({ caps })) setLimits(null);
  };

  const remove = async () => {
    if (!window.confirm(t(($) => $.subscriptions.menu.delete_confirm, { title: sub.title }))) return;
    try {
      await deleteSubscription.mutateAsync(sub.key);
      toast.success(t(($) => $.subscriptions.menu.deleted, { title: sub.title }));
    } catch (error) {
      toast.error(errorText(error));
    }
  };

  const limitSummary = sub.windows
    .map((window) => ({ window, limit: agentLimit(sub, window) }))
    .filter((entry) => entry.limit < 100);

  return (
    <div className="grid gap-6 border-t bg-muted/20 p-4 lg:grid-cols-2" data-testid={`account-detail-${sub.key}`}>
      <div className="min-w-0 space-y-4">
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div className="flex min-w-0 flex-wrap items-center gap-1.5 text-caption text-muted-foreground">
            <Badge variant="outline" className="font-mono">
              {sub.key}
            </Badge>
            <span>{sub.provider}</span>
            {sub.disabled ? (
              <Badge variant="warning">
                {sub.disabled_until
                  ? t(($) => $.subscriptions.disabled_until, { time: formatModelTime(sub.disabled_until) })
                  : t(($) => $.subscriptions.disabled)}
              </Badge>
            ) : null}
          </div>
          {canManage ? (
            <DropdownMenu>
              <DropdownMenuTrigger
                render={<Button size="icon-sm" variant="ghost" aria-label={t(($) => $.subscriptions.menu.open, { title: sub.title })} />}
              >
                <MoreHorizontal />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                {sub.windows.length ? (
                  <DropdownMenuItem onClick={() => setLimits(limitDraftOf(sub.windows, sub.caps))}>
                    {t(($) => $.subscriptions.menu.caps)}
                  </DropdownMenuItem>
                ) : null}
                {sub.disabled ? (
                  <DropdownMenuItem onClick={() => void patch({ disabled_until: "" })}>{t(($) => $.subscriptions.menu.enable)}</DropdownMenuItem>
                ) : (
                  <DropdownMenuItem
                    onClick={() => {
                      setDisabling(true);
                      setDisableUntil("");
                    }}
                  >
                    {t(($) => $.subscriptions.menu.disable)}
                  </DropdownMenuItem>
                )}
                <DropdownMenuItem
                  onClick={() => {
                    setRenaming(true);
                    setNewTitle(sub.title);
                  }}
                >
                  {t(($) => $.subscriptions.menu.rename)}
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                <DropdownMenuItem variant="destructive" onClick={() => void remove()}>
                  {t(($) => $.subscriptions.menu.delete)}
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          ) : null}
        </div>

        <UsageMeters account={account} windowName={windowName} />

        {limits ? (
          <form
            className="space-y-3"
            onSubmit={(event) => {
              event.preventDefault();
              void saveLimits();
            }}
          >
            <AgentLimitFields windows={sub.windows} windowName={windowName} value={limits} onChange={setLimits} disabled={busy} />
            <p className="text-caption text-muted-foreground">{t(($) => $.subscriptions.edit_hint)}</p>
            <div className="flex gap-2">
              <Button size="sm" type="submit" disabled={busy}>
                {t(($) => $.subscriptions.save)}
              </Button>
              <Button size="sm" variant="ghost" type="button" onClick={() => setLimits(null)}>
                {t(($) => $.subscriptions.cancel)}
              </Button>
            </div>
          </form>
        ) : (
          <p className="text-caption" data-testid="agent-limit-summary">
            {limitSummary.length
              ? t(($) => $.subscriptions.agents_up_to, {
                  limits: limitSummary
                    .map((entry) => `${windowName(entry.window)} ${entry.limit}% (${t(($) => $.subscriptions.limit_people_left_short, { n: peopleShare(entry.limit) })})`)
                    .join(", "),
                })
              : t(($) => $.subscriptions.provider_limit)}
          </p>
        )}
      </div>

      <div className="min-w-0 space-y-3 text-caption">
        <SubscriptionLoginStatus sub={sub} canManage={canManage} />
        <div className="space-y-1">
          <span className="text-muted-foreground">{t(($) => $.subscriptions.in_pools)}</span>
          {pools.length ? (
            <ul className="space-y-1">
              {pools.map(({ pool, position }) => (
                <li key={pool.id} className="space-y-0.5">
                  <span className="break-words font-medium">
                    {t(($) => $.subscriptions.spent_by_item, { name: pool.name, position })}
                  </span>
                  <PoolEnvironmentLinks pool={pool} />
                </li>
              ))}
            </ul>
          ) : (
            <p>{t(($) => $.subscriptions.no_pool)}</p>
          )}
        </div>
        <p>
          <span className="text-muted-foreground">{t(($) => $.subscriptions.spent_by)}</span>{" "}
          {here.length ? here.map((user) => user.display_name).join(", ") : t(($) => $.subscriptions.spent_by_none)}
          {elsewhere.size ? (
            <span className="text-muted-foreground">
              {" · "}
              {t(($) => $.subscriptions.spent_elsewhere, { n: elsewhere.size })}
            </span>
          ) : null}
        </p>
        {journal ? <ManualRuns subscriptionKey={sub.key} /> : null}
        {sub.credential_path ? (
          <p className="break-all text-muted-foreground">
            {t(($) => $.subscriptions.credential)} <code className="font-mono">{sub.credential_path}</code>
          </p>
        ) : null}
      </div>

      <Dialog open={renaming} onOpenChange={setRenaming}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t(($) => $.subscriptions.menu.rename_title)}</DialogTitle>
          </DialogHeader>
          <form
            className="space-y-4"
            onSubmit={(event) => {
              event.preventDefault();
              if (newTitle.trim()) void patch({ title: newTitle.trim() }).then((ok) => ok && setRenaming(false));
            }}
          >
            <Input aria-label={t(($) => $.subscriptions.menu.rename_label)} value={newTitle} onChange={(event) => setNewTitle(event.target.value)} />
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setRenaming(false)}>
                {t(($) => $.subscriptions.cancel)}
              </Button>
              <Button type="submit" disabled={busy || !newTitle.trim()}>
                {t(($) => $.subscriptions.save)}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <Dialog open={disabling} onOpenChange={setDisabling}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t(($) => $.subscriptions.menu.disable_title, { title: sub.title })}</DialogTitle>
          </DialogHeader>
          <form
            className="space-y-4"
            onSubmit={(event) => {
              event.preventDefault();
              const until = new Date(disableUntil);
              if (!Number.isNaN(until.getTime())) void patch({ disabled_until: until.toISOString() }).then((ok) => ok && setDisabling(false));
            }}
          >
            <label className="flex flex-col gap-1 text-caption">
              <span className="text-muted-foreground">{t(($) => $.subscriptions.menu.disable_until)}</span>
              <Input type="datetime-local" value={disableUntil} onChange={(event) => setDisableUntil(event.target.value)} />
              <span className="text-muted-foreground">{t(($) => $.subscriptions.menu.disable_hint)}</span>
            </label>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setDisabling(false)}>
                {t(($) => $.subscriptions.cancel)}
              </Button>
              <Button type="submit" disabled={busy || !disableUntil}>
                {t(($) => $.subscriptions.menu.disable_submit)}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  );
}
