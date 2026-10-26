/**
 * Model profiles (server-side model choice). A profile is an ordered list of
 * runtime·model·thinking rows. A shared profile is a workspace rule that
 * agents, issues and route steps reference; a personal profile belongs to one
 * agent and is compared with the shared profile it was made from. The backend
 * picks the first available row at every claim.
 */

/**
 * Why a row, a pool, an account or a run cannot start now. The server sends
 * the code; the interface never reads the human-readable text. An unknown code
 * from a newer server stays a string and renders as "unknown".
 */
export const REASON_CODES = [
  "agent_cap",
  "provider_limit",
  "provider_refusal",
  "login_required",
  "disabled",
  "offline",
  "missing",
  "not_permitted",
  "same_family",
] as const;
export type KnownReasonCode = (typeof REASON_CODES)[number];
export type ReasonCode = KnownReasonCode | (string & {});

/** How an agent gets its models. An unknown mode reads as "fields". */
export const AGENT_MODEL_MODES = ["fields", "profile", "personal", "single"] as const;
export type AgentModelMode = (typeof AGENT_MODEL_MODES)[number];

/** The account a run confirmed with the daemon; absent on runs from before the confirmation (unknown). */
export interface AccountFact {
  subscription_id: string;
  subscription_key: string;
  account_group: string;
  pool_id: string;
  pool_revision: number;
  daemon_id: string;
  confirmed_at: string;
}

/** Where the list of a run came from. Unknown values from a newer server stay strings. */
export type ModelChoiceSource =
  | "issue_profile"
  | "agent_list"
  | "agent_profile"
  | "agent_fields"
  | (string & {});

export type ModelChoiceReason =
  | "primary"
  | "fallback_row"
  | "fallback_profile"
  | "all_unavailable"
  | "no_models"
  | (string & {});

export type ModelRowStateName =
  | "available"
  | "limited"
  | "offline"
  | "missing"
  | "not_permitted"
  | (string & {});

export interface ModelOption {
  position: number;
  runtime_id: string;
  model: string;
  thinking_level: string;
  /** null: derived from the agent's disallowed tools. */
  custom_args: string[] | null;
}

export interface ModelRowState extends ModelOption {
  profile_id?: string;
  runtime_name: string;
  provider: string;
  state: ModelRowStateName;
  until: string | null;
  reason_code: ReasonCode | null;
  until_estimated: boolean;
  chosen: boolean;
}

export interface ModelChoice {
  source: ModelChoiceSource;
  profile_id: string;
  profile_name: string;
  issue_id: string;
  reason: ModelChoiceReason;
  position: number;
  chosen: ModelOption | null;
  runtime_name: string;
  provider: string;
  exhausted_profiles: string[];
  wait_until: string | null;
  /** Why nothing can run; the code of the row that sets wait_until. */
  wait_reason_code: ReasonCode | null;
  /** wait_until is a retry estimate, not a reported reset. */
  wait_until_estimated: boolean;
  excluded_family: string;
  /** The list came from the agent's personal profile. */
  profile_personal: boolean;
  rows: ModelRowState[];
}

export interface ModelRef {
  id: string;
  name: string;
  identifier: string;
}

/** A personal profile as seen from the shared profile it was made from. */
export interface PersonalProfileRef {
  profile_id: string;
  agent: ModelRef;
  matches_base: boolean;
  /** One-based positions of the rows that differ from the shared profile. */
  diff_rows: number[];
}

export interface ModelProfile {
  id: string;
  name: string;
  description: string;
  when_exhausted_profile_id: string | null;
  options: ModelRowState[];
  agents: ModelRef[];
  personal_profiles: PersonalProfileRef[];
  open_issues: ModelRef[];
  fallback_of: ModelRef[];
  updated_at: string;
  /** A personal profile: owned by `owner_agent`, never offered to an issue, a route step or another agent. */
  personal: boolean;
  owner_agent: ModelRef | null;
  based_on: ModelRef | null;
  /** null: no base to compare with. */
  matches_base: boolean | null;
  diff_rows: number[];
}

/** Which profiles the list holds. */
export type ModelProfileKind = "shared" | "personal" | "all";

export interface ModelOptionInput {
  runtime_id: string;
  model: string;
  thinking_level: string;
  custom_args?: string[] | null;
}

export interface ModelProfileInput {
  name: string;
  description: string;
  when_exhausted_profile_id: string | null;
  options: ModelOptionInput[];
}

/** Create: rows given, or copied from an agent's current list when from_agent_id is set without options. */
export type CreateModelProfileInput = Omit<ModelProfileInput, "options"> & {
  options?: ModelOptionInput[];
  from_agent_id?: string;
};

export interface ModelProfileChange {
  kind: "agent" | "issue" | (string & {});
  ref: ModelRef;
  before: ModelOption | null;
  after: ModelOption | null;
  reason: ModelChoiceReason;
}

export interface AgentModelChoiceEntry {
  task_id: string;
  /** Account the daemon confirmed for the run; null: unknown. */
  account_fact: AccountFact | null;
  status: string;
  issue_id: string | null;
  issue_identifier: string;
  issue_title: string;
  position: number;
  choice: ModelChoice | null;
  created_at: string;
}

export interface RuntimeUnavailable {
  runtime_id: string;
  runtime_name: string;
  until: string;
  reason: string;
  reason_code: ReasonCode | null;
  until_estimated: boolean;
  /** Computed from the accounts of the pool, not a stored refusal. */
  computed: boolean;
  task_id: string | null;
  created_at: string;
}

export interface AgentModels {
  agent_id: string;
  can_edit: boolean;
  model_profile_id: string | null;
  profile_name: string;
  profile_agents: number;
  profile_issues: number;
  mode: AgentModelMode;
  personal_profile_id: string | null;
  base_profile_id: string | null;
  base_profile_name: string;
  /** null: no base to compare with. */
  matches_base: boolean | null;
  diff_rows: number[];
  personal_rows: ModelRowState[];
  own_list: boolean;
  options: ModelOption[];
  profile_rows: ModelRowState[];
  disallowed_tools: string[];
  preview: ModelChoice;
  last: AgentModelChoiceEntry | null;
  journal: AgentModelChoiceEntry[];
  waiting: AgentModelChoiceEntry[];
  issues: (ModelRef & { profile_id: string; profile_name: string })[];
  runtime_unavailable: RuntimeUnavailable[];
}

/** Omitted fields are kept; model_profile_id null clears it. Rows of the personal profile go through the personal endpoints. */
export interface UpdateAgentModelsInput {
  model_profile_id?: string | null;
  disallowed_tools?: string[];
}

/** The agent's personal profile: its rows and, optionally, the shared profile it is compared with and falls back to. */
export interface SetPersonalProfileInput {
  options: ModelOptionInput[];
  from_profile_id?: string | null;
  when_exhausted_profile_id?: string | null;
}

/** Copies a shared profile (default: the one the agent is on) into the agent's personal profile. */
export interface CreatePersonalProfileInput {
  from_profile_id?: string | null;
}

export interface SingleModelInput {
  runtime_id: string;
  model: string;
  thinking_level?: string;
  custom_args?: string[] | null;
}

export interface IssueModelProfile {
  issue_id: string;
  model_profile_id: string | null;
  profile_name: string;
}
