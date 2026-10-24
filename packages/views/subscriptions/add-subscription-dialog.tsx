"use client";

import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import {
  poolRevisionConflict,
  poolsOfFamily,
  useCreatePool,
  useCreateSubscription,
  useStartSubscriptionLogin,
  useUpdatePool,
  withSubscription,
  type Placement,
} from "@multica/core/subscriptions";
import type { Pool, Subscription } from "@multica/core/types";
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
import { useT } from "../i18n";
import {
  API_PROVIDERS,
  familyOf,
  loginMethod,
  providerOf,
  windowsOf,
  type ApiProvider,
  type SubscriptionKind,
} from "./subscription-plan";
import { AgentLimitFields, limitDraftOf, parseLimitDraft, type AgentLimitDraft } from "./agent-limit-fields";
import { emptyPoolTarget, PoolTargetFields, poolTargetReady, poolTargetScope, type PoolTarget } from "./pool-target-fields";
import { LoginPanel } from "./subscription-login";

const KINDS: SubscriptionKind[] = ["claude", "codex", "api"];
const STEPS = ["account", "pool", "usage"] as const;
type Step = (typeof STEPS)[number];
type PoolChoice = "existing" | "new" | "none";

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
 * Connecting an account in three steps: the account and its sign-in, the pool
 * it joins (an existing pool of the same CLI, a new pool granted to chosen
 * workspaces, or none yet) and where it will be used. The person may stop
 * after any step: an account without a pool stays visible under "No pool".
 * Once the account exists it is never created again: a failed later step is
 * retried from the same dialog, and a finished dialog only adds what is missing.
 */
export function AddSubscriptionDialog({
  open,
  onOpenChange,
  wsId,
  pools,
  windowName,
  onFinished,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  wsId: string;
  pools: Pool[];
  windowName: (key: string) => string;
  /** Called with the key of the connected account once the dialog finishes. */
  onFinished?: (key: string) => void;
}) {
  const { t } = useT("agents");
  const createSubscription = useCreateSubscription();
  const createPool = useCreatePool();
  const updatePool = useUpdatePool();
  const startLogin = useStartSubscriptionLogin();
  const [step, setStep] = useState<Step>("account");
  const [kind, setKind] = useState<SubscriptionKind>("claude");
  const [apiProvider, setApiProvider] = useState<ApiProvider>("zai");
  const [title, setTitle] = useState("");
  const [secret, setSecret] = useState("");
  /** The token or key typed before the account was created, kept in memory until the sign-in starts or the dialog closes. */
  const [pendingToken, setPendingToken] = useState("");
  const [model, setModel] = useState("");
  const [limits, setLimits] = useState<AgentLimitDraft>({});
  const [choice, setChoice] = useState<PoolChoice>("existing");
  const [poolId, setPoolId] = useState("");
  const [placement, setPlacement] = useState<Placement>("last");
  const [target, setTarget] = useState<PoolTarget>(() => emptyPoolTarget(wsId));
  const [busy, setBusy] = useState(false);
  /** The account once it exists, and whether its pool step is done: a retry skips what already happened. */
  const [created, setCreated] = useState<Subscription | null>(null);
  const [poolDone, setPoolDone] = useState(false);
  /** Set once the sign-in has started: the dialog turns into the sign-in. */
  const [login, setLogin] = useState<{ sub: Subscription; sessionId: string } | null>(null);

  const family = familyOf(kind);
  const provider = providerOf(kind, apiProvider);
  const windows = windowsOf(kind, apiProvider);
  const candidates = useMemo(() => poolsOfFamily(pools, family), [pools, family]);

  useEffect(() => {
    if (!open) return;
    setStep("account");
    setKind("claude");
    setApiProvider("zai");
    setTitle("");
    setSecret("");
    setPendingToken("");
    setModel("");
    setChoice("existing");
    setPoolId("");
    setPlacement("last");
    setTarget(emptyPoolTarget(wsId));
    setCreated(null);
    setPoolDone(false);
    setLogin(null);
  }, [open, wsId]);

  // A new account starts with no limit on every window of its provider.
  const windowsKey = windows.join(",");
  useEffect(() => {
    setLimits(limitDraftOf(windowsKey ? windowsKey.split(",") : [], {}));
  }, [windowsKey]);

  // The picked pool must belong to the account's CLI family.
  useEffect(() => {
    if (!candidates.some((pool) => pool.id === poolId)) setPoolId(candidates[0]?.id ?? "");
  }, [candidates, poolId]);

  const pickedPool = candidates.find((pool) => pool.id === poolId) ?? null;
  const method = kind === "codex" ? "device" : kind === "claude" ? "token" : "key";
  const accountReady = title.trim().length > 0;
  const poolReady =
    choice === "none" || (choice === "existing" ? pickedPool !== null : poolTargetReady(target));
  const hasUsageStep = choice !== "none";

  const parsedLimits = parseLimitDraft(limits);

  const connect = async (withPool: PoolChoice) => {
    if (!parsedLimits) {
      toast.error(t(($) => $.subscriptions.bad_percent));
      setStep("account");
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
      if (!poolDone && withPool === "existing" && pickedPool) {
        const latest = pools.find((pool) => pool.id === pickedPool.id) ?? pickedPool;
        await updatePool.mutateAsync({
          id: latest.id,
          input: {
            revision: latest.revision,
            subscriptions: withSubscription(latest.subscriptions.map((entry) => entry.key), account.key, placement),
          },
        });
        setPoolDone(true);
      } else if (!poolDone && withPool === "new") {
        await createPool.mutateAsync({
          name: target.name.trim(),
          runtime_type: family,
          subscriptions: [account.key],
          spend_order: target.spendOrder,
          ...poolTargetScope(target),
        });
        setPoolDone(true);
      }
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
      // The account exists; the dialog stays so the failed step can be retried without creating it again.
      const current = poolRevisionConflict(error);
      toast.error(
        current !== null
          ? t(($) => $.subscriptions.pool.conflict, { revision: current })
          : t(($) => $.subscriptions.add.partly_failed, { title: account.title, error: errorText(error) }),
      );
    } finally {
      setBusy(false);
    }
  };

  const kindLabel = (k: SubscriptionKind) => t(($) => $.subscriptions.add.kind[k]);
  const kindHint = (k: SubscriptionKind) => t(($) => $.subscriptions.add.kind_hint[k]);
  const stepIndex = STEPS.indexOf(step);
  const stepTitle = (s: Step) => t(($) => $.subscriptions.add.steps[s]);

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

  const poolWhere = (pool: Pool) =>
    pool.environments.length
      ? t(($) => $.subscriptions.add.usage_envs, { names: pool.environments.map((env) => env.display_name).join(", ") })
      : t(($) => $.subscriptions.add.usage_no_env);

  return (
    <Dialog open={open} onOpenChange={(next) => (busy ? undefined : onOpenChange(next))}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{t(($) => $.subscriptions.add.title)}</DialogTitle>
          <DialogDescription>
            {t(($) => $.subscriptions.add.step_of, { n: stepIndex + 1, total: STEPS.length })} · {stepTitle(step)}
          </DialogDescription>
        </DialogHeader>

        <ol className="flex flex-wrap gap-2" aria-label={t(($) => $.subscriptions.add.steps_label)}>
          {STEPS.map((s, index) => (
            <li
              key={s}
              aria-current={s === step ? "step" : undefined}
              className={cn(
                "flex items-center gap-2 rounded-full bg-muted px-2.5 py-1 text-caption text-muted-foreground",
                s === step && "bg-primary text-primary-foreground",
                index < stepIndex && "text-foreground",
              )}
            >
              <span className="font-semibold tabular-nums">{index + 1}</span>
              {stepTitle(s)}
            </li>
          ))}
        </ol>

        <form
          className="space-y-5"
          onSubmit={(event) => {
            event.preventDefault();
            if (step === "account" && accountReady) setStep("pool");
            else if (step === "pool" && poolReady) {
              if (hasUsageStep) setStep("usage");
              else void connect("none");
            } else if (step === "usage") void connect(choice);
          }}
        >
          {step === "account" ? (
            <>
              <fieldset className="space-y-2" disabled={busy || created !== null}>
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

              <fieldset className="space-y-2" disabled={busy || created !== null}>
                <legend className="mb-2 text-body font-medium">{t(($) => $.subscriptions.add.step_caps)}</legend>
                <AgentLimitFields windows={windows} windowName={windowName} value={limits} onChange={setLimits} disabled={busy || created !== null} />
                {windows.length === 0 ? (
                  <p className="text-caption text-muted-foreground">{t(($) => $.subscriptions.add.no_windows)}</p>
                ) : null}
              </fieldset>
            </>
          ) : null}

          {step === "pool" ? (
            <fieldset className="space-y-3" disabled={busy || poolDone}>
              <legend className="mb-2 text-body font-medium">{t(($) => $.subscriptions.add.step_spend)}</legend>
              <RadioGroup value={choice} onValueChange={setChoice} disabled={busy || poolDone} className="grid gap-2 sm:grid-cols-3">
                {(["existing", "new", "none"] as const).map((value) => (
                  <label
                    key={value}
                    className={cn(
                      "flex cursor-pointer items-center gap-2 rounded-md border px-3 py-2 text-body",
                      choice === value && "border-foreground ring-1 ring-foreground",
                    )}
                  >
                    <RadioGroupItem value={value} />
                    {t(($) => $.subscriptions.add.spend_mode[value])}
                  </label>
                ))}
              </RadioGroup>

              {choice === "existing" ? (
                candidates.length ? (
                  <div className="grid gap-3 sm:grid-cols-2">
                    <label className="flex flex-col gap-1 text-caption">
                      <span className="text-muted-foreground">{t(($) => $.subscriptions.add.pool_label)}</span>
                      <SimpleSelect
                        className="w-full"
                        value={poolId}
                        aria-label={t(($) => $.subscriptions.add.pool_label)}
                        onValueChange={setPoolId}
                        items={candidates.map((pool) => ({ value: pool.id, label: pool.name }))}
                      />
                    </label>
                    <label className="flex flex-col gap-1 text-caption">
                      <span className="text-muted-foreground">{t(($) => $.subscriptions.add.placement)}</span>
                      <SimpleSelect
                        className="w-full"
                        aria-label={t(($) => $.subscriptions.add.placement)}
                        value={placement}
                        onValueChange={(value) => { if (value === "first" || value === "last") setPlacement(value); }}
                        items={[
                          { value: "last", label: t(($) => $.subscriptions.add.placement_last) },
                          { value: "first", label: t(($) => $.subscriptions.add.placement_first) },
                        ]}
                      />
                    </label>
                  </div>
                ) : (
                  <p className="text-caption text-muted-foreground">
                    {t(($) => $.subscriptions.add.no_pool_of_family, { family: t(($) => $.subscriptions.groups[family]) })}
                  </p>
                )
              ) : null}
              {choice === "new" ? (
                <PoolTargetFields idPrefix="add-subscription-pool" value={target} onChange={setTarget} disabled={busy || poolDone} />
              ) : null}
              {choice === "none" ? (
                <p className="text-caption text-muted-foreground">{t(($) => $.subscriptions.add.pool_none_hint)}</p>
              ) : null}
            </fieldset>
          ) : null}

          {step === "usage" ? (
            <div className="space-y-3 text-body" data-testid="connect-usage">
              <h3 className="font-medium">{t(($) => $.subscriptions.add.usage_title)}</h3>
              {choice === "existing" && pickedPool ? (
                <p>
                  {t(($) => $.subscriptions.add.usage_existing, { pool: pickedPool.name })} {poolWhere(pickedPool)}
                </p>
              ) : null}
              {choice === "new" ? (
                <p>
                  {t(($) => $.subscriptions.add.usage_new, { pool: target.name.trim() })}{" "}
                  {target.allWorkspaces || target.workspaceIds.length ? t(($) => $.subscriptions.add.usage_new_grant) : t(($) => $.subscriptions.add.usage_new_no_grant)}
                </p>
              ) : null}
              <p className="text-caption text-muted-foreground">{t(($) => $.subscriptions.add.usage_running)}</p>
            </div>
          ) : null}

          <DialogFooter className="gap-2 sm:justify-between">
            <div className="flex flex-wrap gap-2">
              {step !== "account" ? (
                <Button type="button" variant="ghost" disabled={busy} onClick={() => setStep(STEPS[stepIndex - 1] ?? "account")}>
                  {t(($) => $.subscriptions.add.back)}
                </Button>
              ) : null}
              <Button type="button" variant="outline" disabled={busy} onClick={() => onOpenChange(false)}>
                {t(($) => $.subscriptions.cancel)}
              </Button>
            </div>
            <div className="flex flex-wrap gap-2">
              {step === "account" ? (
                <>
                  <Button type="button" variant="outline" disabled={busy || !accountReady || !parsedLimits} onClick={() => void connect("none")}>
                    {t(($) => $.subscriptions.add.stop_here)}
                  </Button>
                  <Button type="submit" disabled={busy || !accountReady || !parsedLimits}>
                    {t(($) => $.subscriptions.add.next)}
                  </Button>
                </>
              ) : null}
              {step === "pool" ? (
                <Button type="submit" disabled={busy || !poolReady} aria-busy={busy}>
                  {hasUsageStep ? t(($) => $.subscriptions.add.next) : t(($) => $.subscriptions.add.submit)}
                </Button>
              ) : null}
              {step === "usage" ? (
                <Button type="submit" disabled={busy} aria-busy={busy}>
                  {method === "device" || secret.trim() || pendingToken
                    ? t(($) => $.subscriptions.add.submit_login)
                    : t(($) => $.subscriptions.add.submit)}
                </Button>
              ) : null}
            </div>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
