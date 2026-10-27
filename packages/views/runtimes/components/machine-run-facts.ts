import { useQueries } from "@tanstack/react-query";
import { runtimeRunFactsOptions } from "@multica/core/runtimes/queries";
import type { AgentRuntime } from "@multica/core/types";

export type MachineRunFacts =
  | { status: "loading" }
  | { status: "error" }
  | { status: "ready"; runs: number };

/**
 * Runs of the last 24 hours across every environment of one machine. The
 * server counts per environment, so the list adds the answers up; one failed
 * answer makes the whole figure unknown rather than a silent undercount.
 */
export function useMachineRunFacts(runtimes: AgentRuntime[]): MachineRunFacts {
  return useQueries({
    queries: runtimes.map((runtime) => runtimeRunFactsOptions(runtime.id)),
    combine: (results): MachineRunFacts => {
      if (results.some((result) => result.isError)) return { status: "error" };
      if (results.some((result) => result.data === undefined)) return { status: "loading" };
      return {
        status: "ready",
        runs: results.reduce((sum, result) => sum + (result.data?.run_facts_24h.runs ?? 0), 0),
      };
    },
  });
}
