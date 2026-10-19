/**
 * Model lists of agents (server-side model choice). A profile is an ordered
 * list of runtime·model·thinking rows that agents and issues reference; an
 * agent's own list is an explicit exception to its profile. The backend picks
 * the first available row at every claim.
 */

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
  rows: ModelRowState[];
}

export interface ModelRef {
  id: string;
  name: string;
  identifier: string;
}

export interface ModelProfile {
  id: string;
  name: string;
  description: string;
  when_exhausted_profile_id: string | null;
  options: ModelRowState[];
  agents: ModelRef[];
  own_list_agents: ModelRef[];
  open_issues: ModelRef[];
  fallback_of: ModelRef[];
  updated_at: string;
}

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

/** Omitted fields are kept; options [] removes the own list; model_profile_id null clears it. */
export interface UpdateAgentModelsInput {
  model_profile_id?: string | null;
  options?: ModelOptionInput[];
  disallowed_tools?: string[];
}

export interface IssueModelProfile {
  issue_id: string;
  model_profile_id: string | null;
  profile_name: string;
}
