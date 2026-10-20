"use client";

import { useEffect, useMemo, useState } from "react";
import { ChevronsUpDown, X } from "lucide-react";
import { toast } from "sonner";
import {
  poolsOfFamily,
  useCreateRuntimePool,
  useCreateSubscription,
  useStartSubscriptionLogin,
  useUpdateRuntimeProfileSubscriptions,
  withSubscription,
  type Placement,
} from "@multica/core/subscriptions";
import type { RuntimeProfile, Subscription } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  Command,
  CommandEmpty,
  CommandInput,
  CommandItem,
  CommandList,
} from "@multica/ui/components/ui/command";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@multica/ui/components/ui/popover";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../i18n";
import {
  API_PROVIDERS,
  familyOf,
  loginMethod,
  parsePercent,
  providerOf,
  windowsOf,
  type ApiProvider,
  type SubscriptionKind,
} from "./subscription-plan";
import { emptyPoolTarget, PoolTargetFields, poolTargetReady, poolTargetScope, type PoolTarget } from "./pool-target-fields";
import { LoginPanel } from "./subscription-login";

const KINDS: SubscriptionKind[] = ["claude", "codex", "api"];
type SpendMode = "existing" | "new";

function without<T>(record: Record<string, T>, key: string): Record<string, T> {
  const next = { ...record };
  delete next[key];
  return next;
}

function errorText(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

/**
 * «Добавить подписку»: the subscription, who spends it (pools of this
 * workspace or a new pool), agents' limits and the sign-in, in one form.
 * After the subscription is created, a failed later step closes the form with
 * the reason: the rest is finished from the card, not by creating it again.
 */
export function AddSubscriptionDialog({
  open,
  onOpenChange,
  wsId,
  profiles,
  subscriptions,
  windowName,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  wsId: string;
  /** Runtime profiles of the current workspace. */
  profiles: RuntimeProfile[];
  subscriptions: Subscription[];
  windowName: (key: string) => string;
}) {
  const { t } = useT("agents");
  const createSubscription = useCreateSubscription();
  const createPool = useCreateRuntimePool();
  const updatePool = useUpdateRuntimeProfileSubscriptions(wsId);
  const startLogin = useStartSubscriptionLogin();
  const [kind, setKind] = useState<SubscriptionKind>("claude");
  const [apiProvider, setApiProvider] = useState<ApiProvider>("zai");
  const [title, setTitle] = useState("");
  const [secret, setSecret] = useState("");
  const [mode, setMode] = useState<SpendMode>("existing");
  const [picked, setPicked] = useState<Record<string, Placement>>({});
  const [target, setTarget] = useState<PoolTarget>(() => emptyPoolTarget(wsId));
  const [caps, setCaps] = useState<Record<string, string>>({});
  const [reserve, setReserve] = useState("0");
  const [pickerOpen, setPickerOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  /** Set once the subscription exists and its sign-in has started: the form turns into the sign-in. */
  const [login, setLogin] = useState<{ sub: Subscription; sessionId: string } | null>(null);

  useEffect(() => {
    if (!open) return;
    setKind("claude");
    setApiProvider("zai");
    setTitle("");
    setSecret("");
    setMode("existing");
    setPicked({});
    setTarget(emptyPoolTarget(wsId));
    setCaps({});
    setReserve("0");
    setLogin(null);
  }, [open, wsId]);

  const family = familyOf(kind);
  const provider = providerOf(kind, apiProvider);
  const windows = windowsOf(kind, apiProvider);
  const candidates = useMemo(() => poolsOfFamily(profiles, family), [profiles, family]);
  // Switching family drops pools of the other one.
  useEffect(() => {
    setPicked((prev) => Object.fromEntries(Object.entries(prev).filter(([id]) => candidates.some((p) => p.id === id))));
  }, [candidates]);

  const subTitle = (key: string) => subscriptions.find((s) => s.key === key)?.title ?? key;
  const pickedPools = candidates.filter((p) => picked[p.id]);
  const method = kind === "codex" ? "device" : kind === "claude" ? "token" : "key";
  const ready = title.trim().length > 0 && (mode === "existing" || poolTargetReady(target));

  const submit = async () => {
    const capNumbers: Record<string, number> = {};
    for (const key of windows) {
      const value = parsePercent(caps[key] ?? "100");
      if (value === null) {
        toast.error(t(($) => $.subscriptions.bad_percent));
        return;
      }
      if (value !== 100) capNumbers[key] = value;
    }
    const reserveNumber = parsePercent(reserve);
    if (reserveNumber === null) {
      toast.error(t(($) => $.subscriptions.bad_percent));
      return;
    }
    const token = secret.trim();
    setSecret("");
    setBusy(true);
    let created: Subscription;
    try {
      created = await createSubscription.mutateAsync({
        provider,
        title: title.trim(),
        windows,
        ...(Object.keys(capNumbers).length ? { caps: capNumbers } : {}),
        ...(reserveNumber ? { reserve: reserveNumber } : {}),
      });
    } catch (error) {
      toast.error(errorText(error));
      setBusy(false);
      return;
    }
    try {
      if (mode === "existing") {
        for (const pool of pickedPools) {
          await updatePool.mutateAsync({
            profileId: pool.id,
            input: { subscriptions: withSubscription(pool.subscriptions ?? [], created.key, picked[pool.id] ?? "last") },
          });
        }
      } else {
        await createPool.mutateAsync({
          display_name: target.name.trim(),
          runtime_type: family,
          subscriptions: [created.key],
          spend_order: target.spendOrder,
          ...poolTargetScope(target),
        });
      }
      if (method === "device" || token) {
        const session = await startLogin(created.key, token ? { token } : {});
        toast.success(t(($) => $.subscriptions.add.done, { title: created.title }));
        setLogin({ sub: created, sessionId: session.id });
        return;
      }
      toast.success(t(($) => $.subscriptions.add.done_login_later, { title: created.title }));
      onOpenChange(false);
    } catch (error) {
      toast.error(t(($) => $.subscriptions.add.partly_failed, { title: created.title, error: errorText(error) }));
      onOpenChange(false);
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
              onOpenChange(false);
            }}
          />
          <DialogFooter>
            <Button variant="outline" onClick={() => onOpenChange(false)}>
              {t(($) => $.subscriptions.close)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    );
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{t(($) => $.subscriptions.add.title)}</DialogTitle>
        </DialogHeader>

        <form
          className="space-y-5"
          onSubmit={(event) => {
            event.preventDefault();
            void submit();
          }}
        >
          <fieldset className="space-y-2" disabled={busy}>
            <legend className="mb-2 text-body font-medium">{t(($) => $.subscriptions.add.step_kind)}</legend>
            <div className="grid gap-2 sm:grid-cols-3">
              {KINDS.map((k) => (
                <label
                  key={k}
                  className={cn(
                    "flex cursor-pointer flex-col gap-1 rounded-md border px-3 py-2",
                    kind === k && "border-foreground ring-1 ring-foreground",
                  )}
                >
                  <span className="flex items-center gap-2 text-body font-medium">
                    <input type="radio" name="subscription-kind" checked={kind === k} onChange={() => setKind(k)} />
                    {kindLabel(k)}
                  </span>
                  <span className="text-caption text-muted-foreground">{kindHint(k)}</span>
                </label>
              ))}
            </div>
            {kind === "api" ? (
              <label className="flex flex-col gap-1 text-caption">
                <span className="text-muted-foreground">{t(($) => $.subscriptions.add.provider)}</span>
                <select
                  className="h-9 rounded-md border bg-background px-2 text-body"
                  value={apiProvider}
                  onChange={(event) => setApiProvider(event.target.value as ApiProvider)}
                >
                  {API_PROVIDERS.map((p) => (
                    <option key={p.provider} value={p.provider}>
                      {t(($) => $.subscriptions.add.providers[p.provider])}
                    </option>
                  ))}
                </select>
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
              disabled={busy}
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

          <fieldset className="space-y-3" disabled={busy}>
            <legend className="mb-2 text-body font-medium">{t(($) => $.subscriptions.add.step_spend)}</legend>
            <div className="grid gap-2 sm:grid-cols-2">
              {(["existing", "new"] as const).map((value) => (
                <label
                  key={value}
                  className={cn(
                    "flex cursor-pointer items-center gap-2 rounded-md border px-3 py-2 text-body",
                    mode === value && "border-foreground ring-1 ring-foreground",
                  )}
                >
                  <input type="radio" name="subscription-spend" checked={mode === value} onChange={() => setMode(value)} />
                  {t(($) => $.subscriptions.add.spend_mode[value])}
                </label>
              ))}
            </div>

            {mode === "existing" ? (
              <div className="space-y-2">
                {pickedPools.length ? (
                  <ul className="space-y-1.5">
                    {pickedPools.map((pool) => {
                      const placement = picked[pool.id] ?? "last";
                      const next = withSubscription(pool.subscriptions ?? [], "", placement);
                      return (
                        <li key={pool.id} className="flex items-center gap-2 rounded-md border px-3 py-2">
                          <span className="min-w-0 flex-1">
                            <span className="block text-body">{pool.display_name}</span>
                            <span className="block truncate text-caption text-muted-foreground">
                              {next.map((key) => (key === "" ? title.trim() || t(($) => $.subscriptions.add.new_item) : subTitle(key))).join(" → ")}
                            </span>
                          </span>
                          <select
                            className="h-8 rounded-md border bg-background px-2 text-caption"
                            aria-label={t(($) => $.subscriptions.add.placement, { runtime: pool.display_name })}
                            value={placement}
                            onChange={(event) => setPicked({ ...picked, [pool.id]: event.target.value as Placement })}
                          >
                            <option value="last">{t(($) => $.subscriptions.add.placement_last)}</option>
                            <option value="first">{t(($) => $.subscriptions.add.placement_first)}</option>
                          </select>
                          <Button
                            type="button"
                            size="icon-sm"
                            variant="ghost"
                            aria-label={t(($) => $.subscriptions.add.unpick, { runtime: pool.display_name })}
                            onClick={() => setPicked(without(picked, pool.id))}
                          >
                            <X />
                          </Button>
                        </li>
                      );
                    })}
                  </ul>
                ) : null}
                <Popover open={pickerOpen} onOpenChange={setPickerOpen}>
                  <PopoverTrigger
                    render={<Button type="button" variant="outline" className="w-full justify-between font-normal" />}
                  >
                    {t(($) => $.subscriptions.add.runtimes_search, { family: t(($) => $.subscriptions.groups[family]) })}
                    <ChevronsUpDown className="opacity-60" />
                  </PopoverTrigger>
                  <PopoverContent className="w-(--anchor-width) p-0" align="start">
                    <Command>
                      <CommandInput placeholder={t(($) => $.subscriptions.add.runtimes_filter)} />
                      <CommandList>
                        <CommandEmpty>{t(($) => $.subscriptions.add.runtimes_none)}</CommandEmpty>
                        {candidates.map((pool) => (
                          <CommandItem
                            key={pool.id}
                            value={pool.display_name}
                            data-checked={picked[pool.id] ? "true" : undefined}
                            onSelect={() =>
                              setPicked(picked[pool.id] ? without(picked, pool.id) : { ...picked, [pool.id]: "last" })
                            }
                          >
                            <span className="min-w-0 flex-1">
                              <span className="block">{pool.display_name}</span>
                              <span className="block truncate text-caption text-muted-foreground">
                                {pool.subscriptions?.length
                                  ? pool.subscriptions.map(subTitle).join(" → ")
                                  : t(($) => $.subscriptions.pool.empty_short)}
                              </span>
                            </span>
                          </CommandItem>
                        ))}
                      </CommandList>
                    </Command>
                  </PopoverContent>
                </Popover>
                <p className="text-caption text-muted-foreground">{t(($) => $.subscriptions.add.runtimes_hint)}</p>
              </div>
            ) : (
              <PoolTargetFields idPrefix="add-subscription-pool" value={target} onChange={setTarget} disabled={busy} />
            )}
          </fieldset>

          <fieldset className="space-y-2" disabled={busy}>
            <legend className="mb-2 text-body font-medium">{t(($) => $.subscriptions.add.step_caps)}</legend>
            <div className="flex flex-wrap gap-3">
              {windows.map((key) => (
                <label key={key} className="flex flex-col gap-1 text-caption">
                  <span className="text-muted-foreground">
                    {t(($) => $.subscriptions.field_percent, { name: windowName(key) })}
                  </span>
                  <Input
                    className="w-24"
                    inputMode="numeric"
                    value={caps[key] ?? "100"}
                    onChange={(event) => setCaps({ ...caps, [key]: event.target.value })}
                  />
                </label>
              ))}
              <label className="flex flex-col gap-1 text-caption">
                <span className="text-muted-foreground">
                  {t(($) => $.subscriptions.field_percent, { name: t(($) => $.subscriptions.reserve_label) })}
                </span>
                <Input className="w-24" inputMode="numeric" value={reserve} onChange={(event) => setReserve(event.target.value)} />
              </label>
            </div>
            {windows.length === 0 ? (
              <p className="text-caption text-muted-foreground">{t(($) => $.subscriptions.add.no_windows)}</p>
            ) : null}
          </fieldset>

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {t(($) => $.subscriptions.cancel)}
            </Button>
            <Button type="submit" disabled={busy || !ready}>
              {method === "device" || secret.trim()
                ? t(($) => $.subscriptions.add.submit_login)
                : t(($) => $.subscriptions.add.submit)}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
