"use client";

import { useQuery } from "@tanstack/react-query";
import { useWorkspacePaths } from "@multica/core/paths";
import { runtimeModelUsageOptions, runtimeRunFactsOptions } from "@multica/core/runtimes/queries";
import { subscriptionListOptions } from "@multica/core/subscriptions";
import type { AgentRuntime, KnownSelectionState, RuntimeProfile, RuntimeProfileRef } from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { AppLink } from "../../navigation";
import { useT } from "../../i18n";

type EnvironmentKind = "via_accounts" | "own_login" | "builtin";

/** How the environment gets its login: from a pool of accounts, its own, or the machine's built-in CLI. */
export function environmentKind(runtime: AgentRuntime, profile: RuntimeProfile | null): EnvironmentKind {
  if (!runtime.profile_id) return "builtin";
  return profile?.pool_id ? "via_accounts" : "own_login";
}

/** A state from a newer server reads as "not used". */
function knownSelectionState(state: string | undefined): KnownSelectionState {
  switch (state) {
    case "active":
    case "reserve":
    case "closed":
      return state;
    default:
      return "unused";
  }
}

function Fact({ label, children, testId }: { label: string; children: React.ReactNode; testId?: string }) {
  return (
    <div className="min-w-0 space-y-1 px-4 py-3" data-testid={testId}>
      <dt className="text-micro uppercase tracking-wider text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words text-body">{children}</dd>
    </div>
  );
}

function ProfileRefs({ refs, href }: { refs: RuntimeProfileRef[]; href: string }) {
  const { t } = useT("runtimes");
  if (!refs.length) return <span className="text-muted-foreground">—</span>;
  return (
    <ul className="space-y-1">
      {refs.map((ref) => (
        <li key={ref.profile_id} className="flex flex-wrap items-baseline gap-x-2">
          <AppLink href={href} className="font-medium underline-offset-2 hover:underline">
            {ref.name}
          </AppLink>
          <span className="text-caption text-muted-foreground">
            {ref.personal ? t(($) => $.facts.personal_profile) : t(($) => $.facts.shared_profile)}
            {ref.positions.length ? ` · ${t(($) => $.facts.rows, { rows: ref.positions.join(", ") })}` : ""}
          </span>
        </li>
      ))}
    </ul>
  );
}

/**
 * What the server knows about an environment next to its settings: how it
 * logs in, where it is selected, who it serves and what ran on it in the last
 * 24 hours. The state of the machine is its own line; it is not the same as
 * whether the environment is selected or used.
 */
export function RuntimeModelFacts({ runtime, profile }: { runtime: AgentRuntime; profile: RuntimeProfile | null }) {
  const { t } = useT("runtimes");
  const paths = useWorkspacePaths();
  const usage = useQuery(runtimeModelUsageOptions(runtime.id));
  const facts = useQuery(runtimeRunFactsOptions(runtime.id));
  const subscriptions = useQuery(subscriptionListOptions());
  const kind = environmentKind(runtime, profile);
  const titleOf = (key: string) => subscriptions.data?.subscriptions.find((sub) => sub.key === key)?.title ?? key;
  const knownState = knownSelectionState(facts.data?.selection_state);

  return (
    <section className="rounded-lg border bg-card" aria-labelledby="runtime-facts-title" data-testid="runtime-model-facts">
      <h2 id="runtime-facts-title" className="border-b px-4 py-2.5 text-caption font-semibold">
        {t(($) => $.facts.title)}
      </h2>
      <dl className="grid grid-cols-1 divide-y sm:grid-cols-2 sm:divide-y-0">
        <Fact label={t(($) => $.facts.kind)} testId="runtime-facts-kind">
          {t(($) => $.facts.kinds[kind])}
          <p className="text-caption text-muted-foreground">{t(($) => $.facts.kind_hint[kind])}</p>
        </Fact>
        <Fact label={t(($) => $.facts.selection)} testId="runtime-facts-selection">
          {facts.isLoading ? (
            <Skeleton className="h-5 w-28" />
          ) : facts.isError ? (
            <span role="alert" className="text-caption text-destructive">
              {t(($) => $.facts.load_failed)}
            </span>
          ) : (
            <>
              <Badge variant={knownState === "active" ? "secondary" : "outline"}>{t(($) => $.facts.states[knownState])}</Badge>
              <p className="mt-1 text-caption text-muted-foreground">
                {t(($) => $.facts.machine, { status: facts.data?.machine_status || runtime.status })}
              </p>
            </>
          )}
        </Fact>
        <Fact label={t(($) => $.facts.runs_24h)} testId="runtime-facts-runs">
          {facts.data ? (
            <>
              <span className="tabular-nums">{t(($) => $.facts.runs, { count: facts.data.run_facts_24h.runs })}</span>
              {kind === "own_login" ? (
                <p className="text-caption text-muted-foreground">{t(($) => $.facts.own_login_accounts)}</p>
              ) : (
                <>
                  {facts.data.run_facts_24h.unknown_account_runs > 0 ? (
                    <p className="text-caption text-muted-foreground">
                      {t(($) => $.facts.unknown_account_runs, { count: facts.data.run_facts_24h.unknown_account_runs })}
                    </p>
                  ) : null}
                  {Object.keys(facts.data.run_facts_24h.accounts).length ? (
                    <ul className="mt-1 space-y-0.5 text-caption" aria-label={t(($) => $.facts.accounts_24h)}>
                      {Object.entries(facts.data.run_facts_24h.accounts).map(([key, runs]) => (
                        <li key={key} className="flex justify-between gap-3">
                          <span className="min-w-0 break-words">{titleOf(key)}</span>
                          <span className="tabular-nums text-muted-foreground">{runs}</span>
                        </li>
                      ))}
                    </ul>
                  ) : null}
                </>
              )}
            </>
          ) : (
            <Skeleton className="h-5 w-24" />
          )}
        </Fact>
        <Fact label={t(($) => $.facts.selected_in)} testId="runtime-facts-selected">
          {usage.isLoading ? (
            <Skeleton className="h-5 w-32" />
          ) : usage.isError ? (
            <span role="alert" className="text-caption text-destructive">
              {t(($) => $.facts.load_failed)}
            </span>
          ) : usage.data ? (
            <div className="space-y-2">
              <ProfileRefs refs={usage.data.selected_in} href={paths.modelProfiles()} />
              {usage.data.fallback_profiles.length ? (
                <div>
                  <p className="text-caption text-muted-foreground">{t(($) => $.facts.fallback)}</p>
                  <ProfileRefs refs={usage.data.fallback_profiles} href={paths.modelProfiles()} />
                </div>
              ) : null}
              {usage.data.based_on.length ? (
                <div>
                  <p className="text-caption text-muted-foreground">{t(($) => $.facts.based_on)}</p>
                  <ProfileRefs refs={usage.data.based_on} href={paths.modelProfiles()} />
                </div>
              ) : null}
              {usage.data.hidden_profiles > 0 ? (
                <p className="text-caption text-muted-foreground">{t(($) => $.facts.hidden_profiles, { count: usage.data.hidden_profiles })}</p>
              ) : null}
            </div>
          ) : null}
        </Fact>
        <Fact label={t(($) => $.facts.serves)} testId="runtime-facts-agents">
          {usage.data ? (
            <div className="space-y-1">
              {usage.data.serving_agents.length ? (
                <ul className="flex flex-wrap gap-x-3 gap-y-1">
                  {usage.data.serving_agents.map((agent) => (
                    <li key={agent.id} className="min-w-0 break-words">
                      <AppLink href={paths.agentDetail(agent.id)} className="font-medium underline-offset-2 hover:underline">
                        {agent.name}
                      </AppLink>
                    </li>
                  ))}
                </ul>
              ) : (
                <span className="text-muted-foreground">—</span>
              )}
              {usage.data.hidden_agents > 0 ? (
                <p className="text-caption text-muted-foreground">{t(($) => $.facts.hidden_agents, { count: usage.data.hidden_agents })}</p>
              ) : null}
              <p className="text-caption text-muted-foreground">{t(($) => $.facts.open_issues, { count: usage.data.open_issues })}</p>
            </div>
          ) : (
            <Skeleton className="h-5 w-32" />
          )}
        </Fact>
        <Fact label={t(($) => $.facts.removal)} testId="runtime-facts-removal">
          {usage.data ? (
            <span className={usage.data.deletable ? undefined : "text-muted-foreground"}>
              {usage.data.deletable ? t(($) => $.facts.deletable) : t(($) => $.facts.not_deletable)}
            </span>
          ) : (
            <Skeleton className="h-5 w-24" />
          )}
        </Fact>
      </dl>
    </section>
  );
}
