"use client";

import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ChevronRight, Trash2 } from "@multica/ui/icons";
import { ExpressMark } from "./express-mark";
import { cn } from "@multica/ui/lib/utils";
import { Button } from "@multica/ui/components/ui/button";
import { Card, CardContent } from "@multica/ui/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { memberListOptions } from "@multica/core/workspace/queries";
import { useActorName } from "@multica/core/workspace/hooks";
import { expressInstallationsOptions, expressKeys } from "@multica/core/express";
import { api } from "@multica/core/api";
import type { ExpressInstallation } from "@multica/core/types";
import { ActorAvatar } from "../../common/actor-avatar";
import { useLocale, useT } from "../../i18n";
import { TooltipHint } from "@multica/ui/components/common/tooltip-hint";

// ExpressTab is the workspace settings panel for eXpress (BotX) bot
// installations, mirroring TelegramTab: listing is member-visible; the disconnect action is
// admin-only (backend-enforced; the UI hides the button to match). Adding a
// new installation flows through the Agent detail page — the install path is
// per-agent (one bot per agent, the (workspace_id, agent_id, channel_type)
// UNIQUE in channel_installation).
export function ExpressTab() {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const user = useAuthStore((s) => s.user);

  const { data: members = [] } = useQuery(memberListOptions(wsId));
  const currentMember = members.find((m) => m.user_id === user?.id) ?? null;
  const canManage =
    currentMember?.role === "owner" || currentMember?.role === "admin";

  const { data, isLoading, isError } = useQuery({
    ...expressInstallationsOptions(wsId),
    enabled: !!wsId,
  });
  const installations = data?.installations ?? [];
  const configured = data?.configured === true;

  const [disconnectTarget, setDisconnectTarget] = useState<string | null>(null);
  const [disconnecting, setDisconnecting] = useState(false);

  async function handleDisconnect() {
    if (!disconnectTarget || disconnecting) return;
    setDisconnecting(true);
    try {
      // Await the server before touching cache/UI (repo rule: no optimistic
      // removal on flows that confirm/destroy).
      await api.deleteExpressInstallation(wsId, disconnectTarget);
      await qc.invalidateQueries({ queryKey: expressKeys.installations(wsId) });
      toast.success(t(($) => $.express.toast_disconnected));
      setDisconnectTarget(null);
    } catch (e) {
      toast.error(
        e instanceof Error ? e.message : t(($) => $.express.toast_disconnect_failed),
      );
    } finally {
      setDisconnecting(false);
    }
  }

  return (
    <div className="space-y-8">
      {isError ? (
        <Card>
          <CardContent>
            <p className="text-body text-muted-foreground">
              {t(($) => $.express.load_failed)}
            </p>
          </CardContent>
        </Card>
      ) : isLoading ? (
        <Card>
          <CardContent>
            <p className="text-body text-muted-foreground">{t(($) => $.express.loading)}</p>
          </CardContent>
        </Card>
      ) : !configured ? (
        <Card>
          <CardContent className="space-y-2">
            <p className="text-body font-medium">{t(($) => $.express.not_enabled_title)}</p>
            <p className="text-caption text-muted-foreground">
              {t(($) => $.express.not_enabled_description_prefix)}{" "}
              <code className="rounded-xs bg-muted px-1 py-0.5 text-micro">
                MULTICA_EXPRESS_SECRET_KEY
              </code>{" "}
              {t(($) => $.express.not_enabled_description_suffix)}{" "}
              {t(($) => $.express.not_enabled_self_host_hint)}
            </p>
          </CardContent>
        </Card>
      ) : (
        <section className="space-y-3">
          <h2 className="text-body font-semibold">{t(($) => $.express.connected_bots)}</h2>
          {installations.length === 0 ? (
            <Card>
              <CardContent className="space-y-2">
                <p className="text-body font-medium">{t(($) => $.express.empty_title)}</p>
                <p className="text-caption text-muted-foreground">
                  {t(($) => $.express.empty_description_prefix)}{" "}
                  <strong>{t(($) => $.express.empty_description_cta)}</strong>{" "}
                  {t(($) => $.express.empty_description_suffix)}
                </p>
              </CardContent>
            </Card>
          ) : (
            <Card>
              <CardContent className="divide-y">
                {installations.map((inst) => (
                  <InstallationRow
                    key={inst.id}
                    installation={inst}
                    canManage={canManage}
                    onDisconnect={() => setDisconnectTarget(inst.id)}
                  />
                ))}
              </CardContent>
            </Card>
          )}
        </section>
      )}

      <AlertDialog
        open={!!disconnectTarget}
        onOpenChange={(v) => {
          if (!v && !disconnecting) setDisconnectTarget(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.express.disconnect_confirm_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.express.disconnect_confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={disconnecting}>
              {t(($) => $.express.disconnect_confirm_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction onClick={handleDisconnect} disabled={disconnecting}>
              {disconnecting
                ? t(($) => $.express.disconnecting)
                : t(($) => $.express.disconnect)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function InstallationRow({
  installation,
  canManage,
  onDisconnect,
}: {
  installation: ExpressInstallation;
  canManage: boolean;
  onDisconnect: () => void;
}) {
  const { t } = useT("settings");
  const locale = useLocale();
  const { getAgentName } = useActorName();
  const isActive = installation.status === "active";
  const agentName = getAgentName(installation.agent_id);
  return (
    <div className="flex items-start justify-between gap-4 py-3 first:pt-0 last:pb-0">
      <div className="flex items-start gap-3">
        <ActorAvatar
          actorType="agent"
          actorId={installation.agent_id}
          size="lg"
          enableHoverCard
          profileLink
        />
        <div className="space-y-1">
          <p className="text-body font-medium">
            {agentName}
            {installation.bot_name || installation.cts_url ? (
              <span className="ml-2 text-caption text-muted-foreground">
                {installation.bot_name || hostOf(installation.cts_url)}
              </span>
            ) : null}
            {!isActive && (
              <span className="ml-2 rounded-xs bg-muted px-1.5 py-0.5 text-micro text-muted-foreground">
                {t(($) => $.express.revoked_badge)}
              </span>
            )}
          </p>
          <p className="text-micro text-muted-foreground">
            {t(($) => $.express.installed_at_label, {
              when: new Date(installation.installed_at).toLocaleString(locale),
            })}
          </p>
        </div>
      </div>
      {canManage && isActive && (
        <Button variant="outline" size="sm" onClick={onDisconnect}>
          <Trash2 className="h-3 w-3" />
          {t(($) => $.express.disconnect)}
        </Button>
      )}
    </div>
  );
}

// hostOf shows a CTS origin without its scheme.
function hostOf(url: string): string {
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
}

const EMPTY_FORM = { ctsUrl: "", botId: "", secretKey: "", botName: "" };

// botWebhookUrl is the bot URL the CTS administrator registers. CTS calls
// <url>/command, /status and /notification/callback on it.
function botWebhookUrl(): string {
  const base = api.getBaseUrl() || (typeof window !== "undefined" ? window.location.origin : "");
  return `${base.replace(/\/$/, "")}/api/express`;
}

// ExpressAgentBindButton is the per-agent CTA on the agent detail page. The
// CTS administrator registers the bot; the admin here pastes the CTS address,
// bot_id and secret_key, which the backend verifies with a token mint before
// persisting. Visibility mirrors TelegramAgentBindButton (owner/admin only).
export function ExpressAgentBindButton({
  agentId,
  agentName,
  className,
  onShowConnectedDetails,
}: {
  agentId: string;
  agentName?: string;
  className?: string;
  /** Compact read-only connected row that invokes this instead of the full
   * badge — the agent inspector passes a "jump to the Integrations tab"
   * handler so management actions live in one place. */
  onShowConnectedDetails?: () => void;
}) {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();
  const user = useAuthStore((s) => s.user);

  const [dialogOpen, setDialogOpen] = useState(false);
  const [form, setForm] = useState(EMPTY_FORM);
  const [submitting, setSubmitting] = useState(false);

  const { data: listing } = useQuery({
    ...expressInstallationsOptions(wsId),
    enabled: !!wsId,
  });
  const installSupported = listing?.install_supported === true;

  const { data: members = [] } = useQuery({
    ...memberListOptions(wsId),
    enabled: !!wsId,
  });
  const currentMember = members.find((m) => m.user_id === user?.id) ?? null;
  const canManage =
    currentMember?.role === "owner" || currentMember?.role === "admin";

  if (!canManage) return null;

  const existing = listing?.installations.find(
    (inst) => inst.agent_id === agentId && inst.status === "active",
  );
  if (existing) {
    return onShowConnectedDetails ? (
      <ExpressAgentBotStatusRow
        onClick={onShowConnectedDetails}
        className={className}
      />
    ) : (
      <ExpressAgentBotConnectedBadge installation={existing} className={className} />
    );
  }

  if (!installSupported) return null;

  function closeDialog() {
    if (submitting) return;
    setDialogOpen(false);
    setForm(EMPTY_FORM);
  }

  async function handleSubmit() {
    const body = {
      cts_url: form.ctsUrl.trim(),
      bot_id: form.botId.trim(),
      secret_key: form.secretKey.trim(),
      bot_name: form.botName.trim(),
    };
    if (submitting || !agentId || !body.cts_url || !body.bot_id || !body.secret_key) return;
    setSubmitting(true);
    try {
      const installation = await api.registerExpressBot(wsId, agentId, body);
      if (!installation.id || installation.status !== "active") {
        throw new Error("eXpress connection returned an invalid installation");
      }
      // The express_installation realtime event also refreshes this list, but
      // invalidate explicitly so the connected badge appears immediately.
      await qc.invalidateQueries({ queryKey: expressKeys.installations(wsId) });
      toast.success(t(($) => $.express.connect_success_toast));
      setDialogOpen(false);
      setForm(EMPTY_FORM);
    } catch (e) {
      toast.error(
        e instanceof Error ? e.message : t(($) => $.express.connect_failed_toast),
      );
    } finally {
      setSubmitting(false);
    }
  }

  const canSubmit =
    form.ctsUrl.trim() !== "" &&
    form.botId.trim() !== "" &&
    form.secretKey.trim() !== "" &&
    !submitting;
  const set = (field: keyof typeof EMPTY_FORM) => (e: { target: { value: string } }) =>
    setForm((f) => ({ ...f, [field]: e.target.value }));

  return (
    <div
      className={cn("flex flex-wrap items-center gap-2", className)}
      data-testid="express-agent-bind-buttons"
    >
      <TooltipHint
        label={
          agentName
            ? t(($) => $.express.bind_button_title, { agent: agentName })
            : undefined
        }
      >
        <Button
          variant="outline"
          size="sm"
          onClick={() => setDialogOpen(true)}
          disabled={!agentId}
          data-testid="express-agent-connect"
        >
          <ExpressMark className="h-3 w-3" />
          {t(($) => $.express.bind_button)}
        </Button>
      </TooltipHint>

      <Dialog
        open={dialogOpen}
        onOpenChange={(v) => (v ? setDialogOpen(true) : closeDialog())}
      >
        <DialogContent className="sm:max-w-lg" data-testid="express-connect-dialog">
          <DialogHeader>
            <DialogTitle>{t(($) => $.express.connect_dialog_title)}</DialogTitle>
          </DialogHeader>

          <p className="text-caption text-muted-foreground">
            {t(($) => $.express.connect_dialog_description)}
          </p>
          <div className="space-y-1.5">
            <Label htmlFor="express-bot-url">{t(($) => $.express.bot_url_label)}</Label>
            <Input
              id="express-bot-url"
              data-testid="express-bot-url"
              readOnly
              value={botWebhookUrl()}
              onFocus={(e) => e.currentTarget.select()}
            />
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="express-cts-url">{t(($) => $.express.cts_url_label)}</Label>
            <Input
              id="express-cts-url"
              data-testid="express-cts-url"
              value={form.ctsUrl}
              onChange={set("ctsUrl")}
              placeholder="https://cts.example.com"
              autoComplete="off"
              spellCheck={false}
              disabled={submitting}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="express-bot-id">{t(($) => $.express.bot_id_label)}</Label>
            <Input
              id="express-bot-id"
              data-testid="express-bot-id"
              value={form.botId}
              onChange={set("botId")}
              placeholder="00000000-0000-0000-0000-000000000000"
              autoComplete="off"
              spellCheck={false}
              disabled={submitting}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="express-secret-key">{t(($) => $.express.secret_key_label)}</Label>
            <Input
              id="express-secret-key"
              data-testid="express-secret-key"
              type="password"
              value={form.secretKey}
              onChange={set("secretKey")}
              autoComplete="off"
              spellCheck={false}
              disabled={submitting}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="express-bot-name">{t(($) => $.express.bot_name_label)}</Label>
            <Input
              id="express-bot-name"
              data-testid="express-bot-name"
              value={form.botName}
              onChange={set("botName")}
              autoComplete="off"
              disabled={submitting}
            />
          </div>

          <DialogFooter>
            <Button
              variant="outline"
              size="sm"
              onClick={closeDialog}
              disabled={submitting}
            >
              {t(($) => $.express.connect_cancel)}
            </Button>
            <Button
              size="sm"
              onClick={handleSubmit}
              disabled={!canSubmit}
              data-testid="express-connect-submit"
            >
              {submitting
                ? t(($) => $.express.connect_submitting)
                : t(($) => $.express.connect_submit)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}

// ExpressAgentBotStatusRow is the compact, read-only connected affordance the
// agent inspector renders; it deep-links into the Integrations tab.
function ExpressAgentBotStatusRow({
  onClick,
  className,
}: {
  onClick: () => void;
  className?: string;
}) {
  const { t } = useT("settings");
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-caption text-muted-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
        className,
      )}
      data-testid="express-agent-bot-status"
    >
      <span className="inline-block h-1.5 w-1.5 shrink-0 rounded-full bg-emerald-500" />
      <span className="truncate">{t(($) => $.express.agent_bot_connected_label)}</span>
      <ChevronRight className="ml-auto h-3.5 w-3.5 shrink-0" />
    </button>
  );
}

// ExpressAgentBotConnectedBadge is the full "already connected" affordance:
// connected status plus Disconnect.
function ExpressAgentBotConnectedBadge({
  installation,
  className,
}: {
  installation: ExpressInstallation;
  className?: string;
}) {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const qc = useQueryClient();

  const [confirmOpen, setConfirmOpen] = useState(false);
  const [disconnecting, setDisconnecting] = useState(false);

  async function handleDisconnect() {
    if (disconnecting) return;
    setDisconnecting(true);
    try {
      await api.deleteExpressInstallation(wsId, installation.id);
      await qc.invalidateQueries({ queryKey: expressKeys.installations(wsId) });
      toast.success(t(($) => $.express.toast_disconnected));
      setConfirmOpen(false);
    } catch (e) {
      toast.error(
        e instanceof Error ? e.message : t(($) => $.express.toast_disconnect_failed),
      );
    } finally {
      setDisconnecting(false);
    }
  }

  return (
    <div
      className={cn("space-y-2", className)}
      data-testid="express-agent-bot-connected"
    >
      <div className="flex items-center justify-between gap-3">
        <span className="inline-flex min-w-0 items-center gap-2 text-caption text-muted-foreground">
          <span className="inline-block h-1.5 w-1.5 shrink-0 rounded-full bg-emerald-500" />
          <span className="truncate">
            {t(($) => $.express.agent_bot_connected_label)}
            {installation.cts_url ? ` · ${hostOf(installation.cts_url)}` : ""}
          </span>
        </span>
        <TooltipHint label={t(($) => $.express.agent_bot_disconnect_tooltip)}>
          <Button
            variant="destructive"
            size="sm"
            onClick={() => setConfirmOpen(true)}
            disabled={disconnecting}
            aria-label={t(($) => $.express.disconnect)}
            data-testid="express-agent-bot-disconnect"
          >
            <Trash2 className="h-3 w-3" />
            {disconnecting
              ? t(($) => $.express.disconnecting)
              : t(($) => $.express.disconnect)}
          </Button>
        </TooltipHint>
      </div>

      <AlertDialog
        open={confirmOpen}
        onOpenChange={(v) => {
          if (!v && !disconnecting) setConfirmOpen(false);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.express.disconnect_confirm_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.express.disconnect_confirm_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={disconnecting}>
              {t(($) => $.express.disconnect_confirm_cancel)}
            </AlertDialogCancel>
            <AlertDialogAction onClick={handleDisconnect} disabled={disconnecting}>
              {disconnecting
                ? t(($) => $.express.disconnecting)
                : t(($) => $.express.disconnect)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
