/**
 * What the server knows about an environment apart from its settings: where
 * it is selected, who it serves and what ran on it in the last 24 hours. The
 * three are different things — a setting, a forecast and a recorded fact —
 * and the screen labels them apart.
 */

/** A profile that has a row on the environment. */
export interface RuntimeProfileRef {
  profile_id: string;
  name: string;
  personal: boolean;
  owner_agent_id: string | null;
  /** One-based places of the environment's rows in the profile. */
  positions: number[];
}

export interface RuntimeServingAgent {
  id: string;
  name: string;
  profile_ids: string[];
}

export interface RuntimeModelUsage {
  selected_in: RuntimeProfileRef[];
  fallback_profiles: RuntimeProfileRef[];
  based_on: RuntimeProfileRef[];
  serving_agents: RuntimeServingAgent[];
  /** Agents and profiles the reader may not see: counted, never named. */
  hidden_agents: number;
  hidden_profiles: number;
  open_issues: number;
  /** Advice for the page; the delete request still decides. */
  deletable: boolean;
}

export const SELECTION_STATES = ["active", "reserve", "unused", "closed"] as const;
export type KnownSelectionState = (typeof SELECTION_STATES)[number];
/** An unknown state from a newer server stays a string and reads as unused. */
export type SelectionState = KnownSelectionState | (string & {});

export interface RunFacts {
  since: string;
  until: string;
  runs: number;
  /** Runs the daemon did not confirm an account for: counted apart, never assigned by pool order. */
  unknown_account_runs: number;
  /** Subscription key → runs in the window. */
  accounts: Record<string, number>;
}

export interface RuntimeRunFacts {
  runtime_id: string;
  selection_state: SelectionState;
  /** The machine: online, offline. Not the environment's selection. */
  machine_status: string;
  run_facts_24h: RunFacts;
}

/** One command an instance admin ran by hand under an account. */
export interface ManualRun {
  id: string;
  subscription_key: string;
  user_id: string;
  user_name: string;
  daemon_id: string;
  /** `claude` or `codex`, never the arguments. */
  command: string;
  started_at: string;
  finished_at: string | null;
  exit_code: number | null;
  /** Started and never closed. */
  unfinished: boolean;
  /** The account's limit for agents was already exceeded when the command started. */
  agent_cap_exceeded: boolean;
}

export interface ManualRunList {
  key: string;
  runs: ManualRun[];
  /** Pass as `before` for the next page; null on the last page. */
  next_before: string | null;
}
