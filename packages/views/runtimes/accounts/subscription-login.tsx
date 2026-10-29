"use client";

import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { KeyRound } from "@multica/ui/icons";
import { toast } from "sonner";
import {
  subscriptionLoginOptions,
  useCancelSubscriptionLogin,
  useStartSubscriptionLogin,
} from "@multica/core/subscriptions";
import type { Subscription, SubscriptionCredentialState, SubscriptionLoginState } from "@multica/core/types";
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
import { useT } from "../../i18n";
import { loginMethod } from "./subscription-plan";

const STATE_VARIANT: Record<SubscriptionLoginState, "secondary" | "destructive" | "outline"> = {
  logged_in: "secondary",
  revoked: "destructive",
  never: "destructive",
  check_failed: "outline",
  unchecked: "outline",
};

const HOST_VARIANT: Record<SubscriptionCredentialState, "secondary" | "destructive" | "outline"> = {
  present: "secondary",
  missing: "destructive",
  revoked: "destructive",
  unreadable: "destructive",
  unknown: "outline",
};

function errorText(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

/** Sign-in state of a subscription, its credential on each daemon host and, for an admin, «Войти заново». */
export function SubscriptionLoginStatus({ sub, canManage }: { sub: Subscription; canManage: boolean }) {
  const { t } = useT("agents");
  // A server that does not check sign-in yet sends no state: nothing to show or start.
  if (sub.login_state === null) return null;
  const state = sub.login_state;
  return (
    <div className="space-y-1.5 text-caption">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant={STATE_VARIANT[state]}>{t(($) => $.subscriptions.login.state[state])}</Badge>
        {canManage && loginMethod(sub.auth) ? <LoginDialog sub={sub} /> : null}
      </div>
      {sub.hosts.length ? (
        <ul className="flex flex-wrap gap-1.5" aria-label={t(($) => $.subscriptions.login.hosts)}>
          {sub.hosts.map((host) => (
            <li key={host.daemon_id || host.device_name}>
              <Badge variant={HOST_VARIANT[host.credential_state]} title={host.error || undefined}>
                {t(($) => $.subscriptions.login.host, {
                  host: host.device_name || host.daemon_id,
                  state: t(($) => $.subscriptions.login.credential[host.credential_state]),
                })}
              </Badge>
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}

function LoginDialog({ sub }: { sub: Subscription }) {
  const { t } = useT("agents");
  const [open, setOpen] = useState(false);
  const method = loginMethod(sub.auth);
  const hint =
    method === "device"
      ? t(($) => $.subscriptions.login.codex_hint)
      : method === "token"
        ? t(($) => $.subscriptions.login.claude_hint)
        : t(($) => $.subscriptions.login.key_hint);
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger render={<Button size="sm" variant="ghost" />}>
        <KeyRound />
        {t(($) => $.subscriptions.login.relogin)}
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t(($) => $.subscriptions.login.title, { account: sub.title })}</DialogTitle>
          <DialogDescription>{hint}</DialogDescription>
        </DialogHeader>
        {open ? (
          <LoginPanel
            subscriptionKey={sub.key}
            auth={sub.auth}
            onDone={() => {
              toast.success(t(($) => $.subscriptions.login.done));
              setOpen(false);
            }}
          />
        ) : null}
        <DialogFooter>
          <DialogClose render={<Button variant="outline" />}>{t(($) => $.subscriptions.close)}</DialogClose>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/**
 * One sign-in: Codex gets a device link and code from the daemon, Claude and
 * key subscriptions take a pasted token. The token goes out in one request
 * and is cleared from the field right away. Polls every 2 s while waiting.
 */
export function LoginPanel({
  subscriptionKey,
  auth,
  watchSessionId = null,
  onDone,
}: {
  subscriptionKey: string;
  auth: string;
  /** A session this panel's owner already started (the wizard sends the token itself). */
  watchSessionId?: string | null;
  onDone: () => void;
}) {
  const { t } = useT("agents");
  const method = loginMethod(auth);
  const session = useQuery(subscriptionLoginOptions(subscriptionKey)).data ?? null;
  const start = useStartSubscriptionLogin();
  const cancel = useCancelSubscriptionLogin();
  const [secret, setSecret] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [watching, setWatching] = useState<string | null>(watchSessionId);
  const waiting = session?.state === "waiting";

  // A sign-in already in progress when the panel opens is followed too; an old finished one is not.
  useEffect(() => {
    if (session?.state === "waiting" && watching === null) setWatching(session.id);
  }, [session, watching]);

  useEffect(() => {
    if (!session || session.id !== watching) return;
    if (session.state === "done") {
      setWatching(null);
      onDone();
    } else if (session.state !== "waiting") {
      setError(t(($) => $.subscriptions.login.failed));
    }
  }, [session, watching, onDone, t]);

  const run = async (token?: string) => {
    setError(null);
    setPending(true);
    try {
      const next = await start(subscriptionKey, token === undefined ? {} : { token });
      setWatching(next.id);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setPending(false);
    }
  };

  const submitSecret = () => {
    const token = secret.trim();
    setSecret("");
    void run(token);
  };

  if (method === null) return null;
  return (
    <div className="space-y-3 text-caption">
      {waiting ? (
        <div className="space-y-2">
          {session.url ? (
            <p>
              <a className="break-all text-primary underline" href={session.url} target="_blank" rel="noreferrer">
                {session.url}
              </a>
            </p>
          ) : null}
          {session.code ? (
            <p>
              {t(($) => $.subscriptions.login.code)} <code className="font-mono text-body">{session.code}</code>
            </p>
          ) : null}
          <p className="text-muted-foreground" role="status">
            {method === "device" ? t(($) => $.subscriptions.login.waiting) : t(($) => $.subscriptions.login.waiting_host)}
          </p>
          <Button
            size="sm"
            variant="outline"
            disabled={cancel.isPending}
            onClick={() =>
              cancel.mutate(subscriptionKey, { onError: (e) => setError(errorText(e)) })
            }
          >
            {t(($) => $.subscriptions.login.cancel_login)}
          </Button>
        </div>
      ) : method === "device" ? (
        <Button size="sm" disabled={pending} onClick={() => void run()}>
          {t(($) => $.subscriptions.login.start)}
        </Button>
      ) : (
        <form
          className="flex items-end gap-2"
          onSubmit={(event) => {
            event.preventDefault();
            submitSecret();
          }}
        >
          <label className="flex min-w-0 flex-1 flex-col gap-1">
            <span className="text-muted-foreground">
              {method === "token" ? t(($) => $.subscriptions.login.token_label) : t(($) => $.subscriptions.login.key_label)}
            </span>
            <Input type="password" autoComplete="off" value={secret} onChange={(event) => setSecret(event.target.value)} />
          </label>
          <Button type="submit" size="sm" disabled={pending || !secret.trim()}>
            {t(($) => $.subscriptions.login.send)}
          </Button>
        </form>
      )}
      {error ? (
        <p role="alert" className="text-destructive">
          {error}
        </p>
      ) : null}
    </div>
  );
}
