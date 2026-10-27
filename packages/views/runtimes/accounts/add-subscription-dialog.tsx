"use client";

import { useEffect, useState } from "react";
import { toast } from "sonner";
import { useCreateSubscription, useStartSubscriptionLogin } from "@multica/core/subscriptions";
import type { Subscription } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { SimpleSelect } from "@multica/ui/components/common/simple-select";
import { RadioGroup, RadioGroupItem } from "@multica/ui/components/ui/radio-group";
import { Input } from "@multica/ui/components/ui/input";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";
import {
  API_PROVIDERS,
  loginMethod,
  providerOf,
  windowsOf,
  type ApiProvider,
  type SubscriptionKind,
} from "./subscription-plan";
import { AgentLimitFields, limitDraftOf, parseLimitDraft, type AgentLimitDraft } from "./agent-limit-fields";
import { LoginPanel } from "./subscription-login";

const KINDS: SubscriptionKind[] = ["claude", "codex", "api"];
/** One model for every tier of Claude Code on a key provider. */
function modelEnv(model: string): Record<string, string> {
  return {
    ANTHROPIC_MODEL: model,
    ANTHROPIC_DEFAULT_OPUS_MODEL: model,
    ANTHROPIC_DEFAULT_SONNET_MODEL: model,
    ANTHROPIC_DEFAULT_HAIKU_MODEL: model,
  };
}

function errorText(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

/**
 * Connecting an account: its kind, name, sign-in and the limit agents have on
 * it. The account is not put into a pool here: a pool is composed on the page
 * of the environment that spends it. Once the account exists it is never
 * created again: a failed sign-in start is retried from the same dialog, and
 * the server returns the existing account for a key that was already added.
 */
export function AddSubscriptionDialog({
  open,
  onOpenChange,
  windowName,
  onFinished,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  windowName: (key: string) => string;
  /** Called with the key of the connected account once the dialog finishes. */
  onFinished?: (key: string) => void;
}) {
  const { t } = useT("agents");
  const createSubscription = useCreateSubscription();
  const startLogin = useStartSubscriptionLogin();
  const [kind, setKind] = useState<SubscriptionKind>("claude");
  const [apiProvider, setApiProvider] = useState<ApiProvider>("zai");
  const [title, setTitle] = useState("");
  const [secret, setSecret] = useState("");
  /** The token or key typed before the account was created, kept in memory until the sign-in starts or the dialog closes. */
  const [pendingToken, setPendingToken] = useState("");
  const [model, setModel] = useState("");
  const [limits, setLimits] = useState<AgentLimitDraft>({});
  const [busy, setBusy] = useState(false);
  /** The account once it exists: a retry skips creating it. */
  const [created, setCreated] = useState<Subscription | null>(null);
  /** Set once the sign-in has started: the dialog turns into the sign-in. */
  const [login, setLogin] = useState<{ sub: Subscription; sessionId: string } | null>(null);

  const provider = providerOf(kind, apiProvider);
  const windows = windowsOf(kind, apiProvider);

  useEffect(() => {
    if (!open) return;
    setKind("claude");
    setApiProvider("zai");
    setTitle("");
    setSecret("");
    setPendingToken("");
    setModel("");
    setCreated(null);
    setLogin(null);
  }, [open]);

  // A new account starts with no limit on every window of its provider.
  const windowsKey = windows.join(",");
  useEffect(() => {
    setLimits(limitDraftOf(windowsKey ? windowsKey.split(",") : [], {}));
  }, [windowsKey]);

  const method = kind === "codex" ? "device" : kind === "claude" ? "token" : "key";
  const accountReady = title.trim().length > 0;
  const parsedLimits = parseLimitDraft(limits);

  const connect = async () => {
    if (!parsedLimits) {
      toast.error(t(($) => $.subscriptions.bad_percent));
      return;
    }
    const capNumbers = Object.fromEntries(Object.entries(parsedLimits).filter(([, value]) => value !== 100));
    const token = pendingToken || secret.trim();
    setPendingToken(token);
    setSecret("");
    setBusy(true);
    let account = created;
    if (!account) {
      try {
        account = await createSubscription.mutateAsync({
          provider,
          title: title.trim(),
          windows,
          ...(Object.keys(capNumbers).length ? { caps: capNumbers } : {}),
          ...(kind === "api" && model.trim() ? { settings: { env: modelEnv(model.trim()) } } : {}),
        });
        setCreated(account);
      } catch (error) {
        toast.error(errorText(error));
        setBusy(false);
        return;
      }
    }
    try {
      if (method === "device" || token) {
        const session = await startLogin(account.key, token ? { token } : {});
        setPendingToken("");
        toast.success(t(($) => $.subscriptions.add.done, { title: account.title }));
        setLogin({ sub: account, sessionId: session.id });
        return;
      }
      toast.success(t(($) => $.subscriptions.add.done_login_later, { title: account.title }));
      onFinished?.(account.key);
      onOpenChange(false);
    } catch (error) {
      // The account exists; the dialog stays so the sign-in can be started again without creating it twice.
      toast.error(t(($) => $.subscriptions.add.partly_failed, { title: account.title, error: errorText(error) }));
    } finally {
      setBusy(false);
    }
  };

  const kindLabel = (k: SubscriptionKind) => t(($) => $.subscriptions.add.kind[k]);
  const kindHint = (k: SubscriptionKind) => t(($) => $.subscriptions.add.kind_hint[k]);

  if (login) {
    return (
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent className="sm:max-w-xl">
          <DialogHeader>
            <DialogTitle>{t(($) => $.subscriptions.login.title, { account: login.sub.title })}</DialogTitle>
            {loginMethod(login.sub.auth) === "device" ? (
              <DialogDescription>{t(($) => $.subscriptions.login.codex_hint)}</DialogDescription>
            ) : null}
          </DialogHeader>
          <LoginPanel
            subscriptionKey={login.sub.key}
            auth={login.sub.auth}
            watchSessionId={login.sessionId}
            onDone={() => {
              toast.success(t(($) => $.subscriptions.login.done));
              onFinished?.(login.sub.key);
              onOpenChange(false);
            }}
          />
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => {
                onFinished?.(login.sub.key);
                onOpenChange(false);
              }}
            >
              {t(($) => $.subscriptions.close)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    );
  }

  return (
    <Dialog open={open} onOpenChange={(next) => (busy ? undefined : onOpenChange(next))}>
      <DialogContent className="max-h-[90vh] grid-cols-[minmax(0,1fr)] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{t(($) => $.subscriptions.add.title)}</DialogTitle>
          <DialogDescription>{t(($) => $.subscriptions.add.no_pool_note)}</DialogDescription>
        </DialogHeader>

        <form
          className="space-y-5"
          onSubmit={(event) => {
            event.preventDefault();
            if (accountReady && parsedLimits) void connect();
          }}
        >
          <fieldset className="min-w-0 space-y-2" disabled={busy || created !== null}>
            <legend className="mb-2 text-body font-medium">{t(($) => $.subscriptions.add.step_kind)}</legend>
            <RadioGroup value={kind} onValueChange={setKind} disabled={busy || created !== null} className="grid gap-2 sm:grid-cols-3">
              {KINDS.map((k) => (
                <label
                  key={k}
                  className={cn(
                    "flex cursor-pointer flex-col gap-1 rounded-md border px-3 py-2",
                    kind === k && "border-foreground ring-1 ring-foreground",
                  )}
                >
                  <span className="flex items-center gap-2 text-body font-medium">
                    <RadioGroupItem value={k} />
                    {kindLabel(k)}
                  </span>
                  <span className="text-caption text-muted-foreground">{kindHint(k)}</span>
                </label>
              ))}
            </RadioGroup>
            {kind === "api" ? (
              <label className="flex flex-col gap-1 text-caption">
                <span className="text-muted-foreground">{t(($) => $.subscriptions.add.provider)}</span>
                <SimpleSelect
                  className="w-full"
                  value={apiProvider}
                  aria-label={t(($) => $.subscriptions.add.provider)}
                  onValueChange={(value) => { if (API_PROVIDERS.some((p) => p.provider === value)) setApiProvider(value as ApiProvider); }}
                  items={API_PROVIDERS.map((p) => ({ value: p.provider, label: t(($) => $.subscriptions.add.providers[p.provider]) }))}
                />
              </label>
            ) : null}
            {kind === "api" ? (
              <label className="flex flex-col gap-1 text-caption">
                <span className="text-muted-foreground">{t(($) => $.subscriptions.add.model)}</span>
                <Input
                  value={model}
                  placeholder={t(($) => $.subscriptions.add.model_placeholder)}
                  onChange={(event) => setModel(event.target.value)}
                />
                <span className="text-muted-foreground">{t(($) => $.subscriptions.add.model_hint)}</span>
              </label>
            ) : null}
          </fieldset>

          <div className="space-y-1">
            <label htmlFor="subscription-title" className="text-body font-medium">
              {t(($) => $.subscriptions.add.step_name)}
            </label>
            <Input
              id="subscription-title"
              value={title}
              disabled={busy || created !== null}
              placeholder={t(($) => $.subscriptions.add.name_placeholder)}
              onChange={(event) => setTitle(event.target.value)}
            />
          </div>

          <div className="space-y-1">
            <span className="text-body font-medium">{t(($) => $.subscriptions.add.step_login)}</span>
            {method === "device" ? (
              <p className="text-caption text-muted-foreground">{t(($) => $.subscriptions.add.codex_login)}</p>
            ) : (
              <>
                <p className="text-caption text-muted-foreground">
                  {method === "token" ? t(($) => $.subscriptions.add.claude_login) : t(($) => $.subscriptions.add.key_login)}
                </p>
                <Input
                  type="password"
                  autoComplete="off"
                  disabled={busy}
                  aria-label={
                    method === "token" ? t(($) => $.subscriptions.login.token_label) : t(($) => $.subscriptions.login.key_label)
                  }
                  value={secret}
                  onChange={(event) => setSecret(event.target.value)}
                />
                <p className="text-caption text-muted-foreground">{t(($) => $.subscriptions.add.secret_hint)}</p>
              </>
            )}
          </div>

          <fieldset className="min-w-0 space-y-2" disabled={busy || created !== null}>
            <legend className="mb-2 text-body font-medium">{t(($) => $.subscriptions.add.step_caps)}</legend>
            <AgentLimitFields windows={windows} windowName={windowName} value={limits} onChange={setLimits} disabled={busy || created !== null} />
            {windows.length === 0 ? (
              <p className="text-caption text-muted-foreground">{t(($) => $.subscriptions.add.no_windows)}</p>
            ) : null}
          </fieldset>

          <DialogFooter className="gap-2">
            <Button type="button" variant="outline" disabled={busy} onClick={() => onOpenChange(false)}>
              {t(($) => $.subscriptions.cancel)}
            </Button>
            <Button type="submit" disabled={busy || !accountReady || !parsedLimits} aria-busy={busy}>
              {method === "device" || secret.trim() || pendingToken
                ? t(($) => $.subscriptions.add.submit_login)
                : t(($) => $.subscriptions.add.submit)}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
