"use client";

import { useEffect, useMemo, useState } from "react";
import { ChevronsUpDown, X } from "lucide-react";
import { toast } from "sonner";
import { postLogin } from "@multica/core/subs-login";
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
import { controlRequest } from "../conveyor/control-api";
import {
  API_PROVIDERS,
  compatibleRuntimes,
  planSubscription,
  providerOf,
  withSubscription,
  type ApiProvider,
  type Placement,
  type RuntimeRow,
  type SubscriptionKind,
} from "./subscription-plan";
import type { SaveRow, Subscription } from "./subscription-types";

const KINDS: SubscriptionKind[] = ["claude", "codex", "api"];

function without<T>(record: Record<string, T>, key: string): Record<string, T> {
  const next = { ...record };
  delete next[key];
  return next;
}

/**
 * «Добавить подписку»: одна форма вместо трёх таблиц — строка подписки,
 * runtime, которые её тратят, лимиты агентам и вход. Шаги идут по порядку;
 * упавший шаг останавливает остальные, уже сохранённое остаётся.
 */
export function AddSubscriptionDialog({
  open,
  onOpenChange,
  subscriptions,
  runtimes,
  windowKeys,
  windowName,
  wsSlug,
  onSave,
  onDone,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  subscriptions: Subscription[];
  runtimes: RuntimeRow[];
  windowKeys: string[];
  windowName: (key: string) => string;
  wsSlug: string;
  onSave: SaveRow;
  onDone: () => void | Promise<void>;
}) {
  const { t } = useT("agents");
  const [kind, setKind] = useState<SubscriptionKind>("claude");
  const [apiProvider, setApiProvider] = useState<ApiProvider>("zai");
  const [title, setTitle] = useState("");
  const [secret, setSecret] = useState("");
  const [picked, setPicked] = useState<Record<string, Placement>>({});
  const [caps, setCaps] = useState<Record<string, string>>({});
  const [reserve, setReserve] = useState("0");
  const [pickerOpen, setPickerOpen] = useState(false);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!open) return;
    setKind("claude");
    setApiProvider("zai");
    setTitle("");
    setSecret("");
    setPicked({});
    setCaps({});
    setReserve("0");
  }, [open]);

  const provider = providerOf(kind, apiProvider);
  const plan = useMemo(
    () => planSubscription(kind, apiProvider, title, subscriptions.map((s) => s.id), windowKeys),
    [kind, apiProvider, title, subscriptions, windowKeys],
  );
  const candidates = useMemo(
    () => compatibleRuntimes(runtimes, subscriptions, provider),
    [runtimes, subscriptions, provider],
  );
  // Switching provider drops runtimes of the old one.
  useEffect(() => {
    setPicked((prev) =>
      Object.fromEntries(Object.entries(prev).filter(([name]) => candidates.some((r) => r.name === name))),
    );
  }, [candidates]);

  const subTitle = (id: string) => subscriptions.find((s) => s.id === id)?.title ?? id;
  const pickedRuntimes = candidates.filter((r) => picked[r.name]);
  const needsSecret = kind !== "codex";

  const submit = async () => {
    const numbers = Object.fromEntries(plan.row.windows.map((key) => [key, Number(caps[key] ?? "100")]));
    const reserveNumber = Number(reserve);
    if ([...Object.values(numbers), reserveNumber].some((v) => !Number.isInteger(v) || v < 0 || v > 100)) {
      toast.error(t(($) => $.subscriptions.bad_percent));
      return;
    }
    setBusy(true);
    try {
      if (!(await onSave("items", plan.id, plan.row, { quiet: true }))) return;
      for (const runtime of pickedRuntimes) {
        const next = withSubscription(runtime, plan.id, picked[runtime.name] ?? "last");
        if (!(await onSave("runtimes", runtime.name, { command: next.command, subscriptions: next.subscriptions, order: next.order }, { quiet: true }))) {
          return;
        }
      }
      if (Object.values(numbers).some((v) => v !== 100) || reserveNumber !== 0) {
        await controlRequest("subscriptions", { id: plan.id, caps: numbers, reserve: reserveNumber }, wsSlug);
      }
      if (needsSecret && secret.trim()) {
        const session = await postLogin(wsSlug, plan.id, { action: "secret", value: secret.trim() });
        if (session.state !== "done") {
          toast.warning(session.message || t(($) => $.subscriptions.login.failed));
        }
      }
      toast.success(
        kind === "codex"
          ? t(($) => $.subscriptions.add.done_codex, { title: plan.row.title })
          : t(($) => $.subscriptions.add.done, { title: plan.row.title }),
      );
      onOpenChange(false);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(false);
      await onDone();
    }
  };

  const kindLabel = (k: SubscriptionKind) =>
    k === "claude"
      ? t(($) => $.subscriptions.add.kind_claude)
      : k === "codex"
        ? t(($) => $.subscriptions.add.kind_codex)
        : t(($) => $.subscriptions.add.kind_api);
  const kindHint = (k: SubscriptionKind) =>
    k === "claude"
      ? t(($) => $.subscriptions.add.kind_claude_hint)
      : k === "codex"
        ? t(($) => $.subscriptions.add.kind_codex_hint)
        : t(($) => $.subscriptions.add.kind_api_hint);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{t(($) => $.subscriptions.add.title)}</DialogTitle>
          <DialogDescription>{t(($) => $.subscriptions.add.description)}</DialogDescription>
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
                      {t(($) => $.subscriptions.add.providers[p.base])}
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
            <p className="text-caption text-muted-foreground">
              {t(($) => $.subscriptions.add.name_hint, { id: plan.id, path: plan.row.credential })}
            </p>
          </div>

          <div className="space-y-1">
            <span className="text-body font-medium">{t(($) => $.subscriptions.add.step_login)}</span>
            {kind === "codex" ? (
              <p className="text-caption text-muted-foreground">{t(($) => $.subscriptions.add.codex_login)}</p>
            ) : (
              <>
                <p className="text-caption text-muted-foreground">
                  {kind === "claude"
                    ? t(($) => $.subscriptions.add.claude_login)
                    : t(($) => $.subscriptions.add.key_login)}
                </p>
                <Input
                  type="password"
                  autoComplete="off"
                  disabled={busy}
                  aria-label={
                    kind === "claude"
                      ? t(($) => $.subscriptions.login.token_label)
                      : t(($) => $.subscriptions.login.key_label)
                  }
                  value={secret}
                  onChange={(event) => setSecret(event.target.value)}
                />
                <p className="text-caption text-muted-foreground">{t(($) => $.subscriptions.add.secret_hint)}</p>
              </>
            )}
          </div>

          <div className="space-y-2">
            <span className="text-body font-medium">{t(($) => $.subscriptions.add.step_runtimes)}</span>
            {pickedRuntimes.length ? (
              <ul className="space-y-1.5">
                {pickedRuntimes.map((runtime) => {
                  const placement = picked[runtime.name] ?? "last";
                  const next = withSubscription(runtime, plan.id, placement);
                  return (
                    <li key={runtime.name} className="flex items-center gap-2 rounded-md border px-3 py-2">
                      <span className="min-w-0 flex-1">
                        <span className="block text-body">{runtime.name}</span>
                        <span className="block truncate text-caption text-muted-foreground">
                          {next.subscriptions.map((id) => (id === plan.id ? `${plan.row.title || id}` : subTitle(id))).join(" → ")}
                        </span>
                      </span>
                      <select
                        className="h-8 rounded-md border bg-background px-2 text-caption"
                        aria-label={t(($) => $.subscriptions.add.placement, { runtime: runtime.name })}
                        value={placement}
                        disabled={busy}
                        onChange={(event) => setPicked({ ...picked, [runtime.name]: event.target.value as Placement })}
                      >
                        <option value="last">{t(($) => $.subscriptions.add.placement_last)}</option>
                        <option value="first">{t(($) => $.subscriptions.add.placement_first)}</option>
                      </select>
                      <Button
                        type="button"
                        size="icon-sm"
                        variant="ghost"
                        disabled={busy}
                        aria-label={t(($) => $.subscriptions.add.unpick, { runtime: runtime.name })}
                        onClick={() => setPicked(without(picked, runtime.name))}
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
                render={
                  <Button type="button" variant="outline" className="w-full justify-between font-normal" disabled={busy} />
                }
              >
                {t(($) => $.subscriptions.add.runtimes_search, { provider })}
                <ChevronsUpDown className="opacity-60" />
              </PopoverTrigger>
              <PopoverContent className="w-(--anchor-width) p-0" align="start">
                <Command>
                  <CommandInput placeholder={t(($) => $.subscriptions.add.runtimes_filter)} />
                  <CommandList>
                    <CommandEmpty>{t(($) => $.subscriptions.add.runtimes_none)}</CommandEmpty>
                    {candidates.map((runtime) => (
                      <CommandItem
                        key={runtime.name}
                        value={runtime.name}
                        data-checked={picked[runtime.name] ? "true" : undefined}
                        onSelect={() => {
                          if (picked[runtime.name]) {
                            setPicked(without(picked, runtime.name));
                          } else {
                            setPicked({ ...picked, [runtime.name]: "last" });
                          }
                        }}
                      >
                        <span className="min-w-0 flex-1">
                          <span className="block">{runtime.name}</span>
                          <span className="block truncate text-caption text-muted-foreground">
                            {runtime.subscriptions.length
                              ? runtime.subscriptions.map(subTitle).join(" → ")
                              : t(($) => $.subscriptions.add.runtime_empty)}
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

          <fieldset className="space-y-2" disabled={busy}>
            <legend className="mb-2 text-body font-medium">{t(($) => $.subscriptions.add.step_caps)}</legend>
            <div className="flex flex-wrap gap-3">
              {plan.row.windows.map((key) => (
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
            {plan.row.windows.length === 0 ? (
              <p className="text-caption text-muted-foreground">{t(($) => $.subscriptions.add.no_windows)}</p>
            ) : null}
          </fieldset>

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {t(($) => $.subscriptions.cancel)}
            </Button>
            <Button type="submit" disabled={busy || !title.trim()}>
              {needsSecret && secret.trim()
                ? t(($) => $.subscriptions.add.submit_login)
                : t(($) => $.subscriptions.add.submit)}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
