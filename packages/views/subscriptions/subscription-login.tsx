"use client";

import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { KeyRound } from "lucide-react";
import { toast } from "sonner";
import {
  getLoginSession,
  subsLoginKeys,
  useSubscriptionLogin,
  type LoginAccount,
  type LoginAction,
  type LoginSession,
  type LoginState,
} from "@multica/core/subs-login";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { useT } from "../i18n";

const POLL_MS = 3_000;

const STATE_VARIANT: Record<LoginState, "secondary" | "destructive" | "outline"> = {
  logged_in: "secondary",
  revoked: "destructive",
  never: "destructive",
  check_failed: "outline",
  unchecked: "outline",
};

/** Login status of a subscription account and, for the owner, the re-login dialog. */
export function SubscriptionLogin({
  account,
  canLogin,
  wsSlug,
}: {
  account: LoginAccount;
  canLogin: boolean;
  wsSlug: string;
}) {
  const { t } = useT("agents");
  return (
    <div className="flex flex-wrap items-center gap-2 text-caption">
      <Badge variant={STATE_VARIANT[account.state]}>{t(($) => $.subscriptions.login.state[account.state])}</Badge>
      {canLogin ? <LoginDialog account={account} wsSlug={wsSlug} /> : null}
    </div>
  );
}

function LoginDialog({ account, wsSlug }: { account: LoginAccount; wsSlug: string }) {
  const { t } = useT("agents");
  const queryClient = useQueryClient();
  const login = useSubscriptionLogin(wsSlug, account.id);
  const [open, setOpen] = useState(false);
  const [session, setSession] = useState<LoginSession | null>(account.login);
  const [secret, setSecret] = useState("");
  const [error, setError] = useState<string | null>(null);
  const isCodex = account.auth === "codex_auth";
  const waiting = session?.state === "waiting";

  // Device auth completes in another tab: poll until the helper reports how it ended.
  useEffect(() => {
    if (!open || !waiting) return;
    const timer = setInterval(() => {
      getLoginSession(wsSlug, account.id)
        .then((next) => {
          if (next.state === "waiting") return;
          setSession(next);
          void queryClient.invalidateQueries({ queryKey: subsLoginKeys.accounts(wsSlug) });
        })
        .catch(() => undefined);
    }, POLL_MS);
    return () => clearInterval(timer);
  }, [open, waiting, account.id, wsSlug, queryClient]);

  // Every answer from the helper is a new object, so this runs once per outcome, retries included.
  useEffect(() => {
    if (!session) return;
    if (session.state === "done") {
      toast.success(t(($) => $.subscriptions.login.done));
      setOpen(false);
      setSecret("");
      setSession(null);
    } else if (session.state !== "waiting" && session.state !== "none") {
      setError(session.message || t(($) => $.subscriptions.login.failed));
    }
  }, [session, t]);

  const run = (action: LoginAction) => {
    setError(null);
    login.mutate(action, {
      onSuccess: setSession,
      onError: (e) => setError(e instanceof Error ? e.message : String(e)),
    });
  };

  const hint =
    account.auth === "codex_auth"
      ? t(($) => $.subscriptions.login.codex_hint)
      : account.auth === "claude_oauth"
        ? t(($) => $.subscriptions.login.claude_hint)
        : t(($) => $.subscriptions.login.key_hint);

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (next) setError(null);
      }}
    >
      <DialogTrigger render={<Button size="sm" variant="ghost" />}>
        <KeyRound />
        {t(($) => $.subscriptions.login.relogin)}
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t(($) => $.subscriptions.login.title, { account: account.id })}</DialogTitle>
          <DialogDescription>{hint}</DialogDescription>
        </DialogHeader>

        {isCodex && waiting ? (
          <div className="space-y-2 text-caption">
            {session?.url ? (
              <p>
                <a className="break-all text-primary underline" href={session.url} target="_blank" rel="noreferrer">
                  {session.url}
                </a>
              </p>
            ) : null}
            {session?.code ? (
              <p>
                {t(($) => $.subscriptions.login.code)} <code className="font-mono text-body">{session.code}</code>
              </p>
            ) : null}
            <p className="text-muted-foreground">{t(($) => $.subscriptions.login.waiting)}</p>
          </div>
        ) : null}

        {!isCodex ? (
          <label className="flex flex-col gap-1 text-caption">
            <span className="text-muted-foreground">
              {account.auth === "claude_oauth"
                ? t(($) => $.subscriptions.login.token_label)
                : t(($) => $.subscriptions.login.key_label)}
            </span>
            <Input type="password" autoComplete="off" value={secret} onChange={(event) => setSecret(event.target.value)} />
          </label>
        ) : null}

        {error ? (
          <p role="alert" className="text-caption text-destructive">
            {error}
          </p>
        ) : null}

        <DialogFooter>
          {isCodex && waiting ? (
            <Button variant="outline" disabled={login.isPending} onClick={() => run({ action: "cancel" })}>
              {t(($) => $.subscriptions.login.cancel_login)}
            </Button>
          ) : (
            <DialogClose render={<Button variant="outline" />}>{t(($) => $.subscriptions.cancel)}</DialogClose>
          )}
          {isCodex ? (
            waiting ? null : (
              <Button disabled={login.isPending} onClick={() => run({ action: "start" })}>
                {t(($) => $.subscriptions.login.start)}
              </Button>
            )
          ) : (
            <Button
              disabled={login.isPending || !secret.trim()}
              onClick={() => run({ action: "secret", value: secret })}
            >
              {t(($) => $.subscriptions.save)}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
