import { z } from "zod";

import type { ExpressInstallation, ListExpressInstallationsResponse, RedeemExpressBindingTokenResponse } from "../types";

// Schemas of fork endpoints and fields. schemas.ts re-exports them.

// Model profiles (server-side model choice). Enum-like fields stay strings so
// a newer server's values still parse; every optional field has a default.
export const ModelOptionSchema = z.object({
  position: z.number().int().default(0),
  runtime_id: z.string(),
  model: z.string().default(""),
  thinking_level: z.string().default(""),
  custom_args: z.array(z.string()).nullish().catch(null).transform((v) => v ?? null),
});

const optionalCode = z.string().nullish().catch(null).transform((v) => v || null);
const estimateFlag = z.boolean().nullish().catch(false).transform((v) => v === true);

export const ModelRowStateSchema = ModelOptionSchema.extend({
  profile_id: z.string().optional().catch(undefined),
  runtime_name: z.string().default(""),
  provider: z.string().default(""),
  state: z.string().default("available"),
  until: z.string().nullish().catch(null).transform((v) => v ?? null),
  reason_code: optionalCode,
  until_estimated: estimateFlag,
  chosen: z.boolean().default(false),
  effective_model: z.string().nullish().catch("").transform((v) => v ?? ""),
  model_at_run: estimateFlag,
});

export const ModelChoiceSchema = z.object({
  source: z.string().default("agent_fields"),
  profile_id: z.string().default(""),
  profile_name: z.string().default(""),
  issue_id: z.string().default(""),
  excluded_family: z.string().nullish().catch("").transform((v) => v ?? ""),
  reason: z.string().default("no_models"),
  position: z.number().int().default(0),
  chosen: ModelOptionSchema.nullish().catch(null).transform((v) => v ?? null),
  runtime_name: z.string().default(""),
  provider: z.string().default(""),
  exhausted_profiles: z.array(z.string()).nullish().catch([]).transform((v) => v ?? []),
  wait_until: z.string().nullish().catch(null).transform((v) => v ?? null),
  wait_reason_code: optionalCode,
  wait_until_estimated: estimateFlag,
  profile_personal: estimateFlag,
  rows: z.array(ModelRowStateSchema).nullish().catch([]).transform((v) => v ?? []),
});

// The account a run confirmed with the daemon. A run from before the
// confirmation, or on an older daemon, has none: the interface says "unknown".
export const AccountFactSchema = z.object({
  subscription_id: z.string(),
  subscription_key: z.string(),
  account_group: z.string().nullish().catch("").transform((v) => v ?? ""),
  pool_id: z.string().nullish().catch("").transform((v) => v ?? ""),
  pool_revision: z.number().int().nullish().catch(0).transform((v) => v ?? 0),
  daemon_id: z.string().nullish().catch("").transform((v) => v ?? ""),
  confirmed_at: z.string().nullish().catch("").transform((v) => v ?? ""),
});
export const optionalAccountFact = AccountFactSchema.nullish().catch(null).transform((v) => v ?? null);

export const ExpressInstallationSchema = z.object({
  id: z.string(),
  workspace_id: z.string().default(""),
  agent_id: z.string().default(""),
  bot_id: z.string().default(""),
  cts_url: z.string().default(""),
  bot_name: z.string().default(""),
  installer_user_id: z.string().default(""),
  status: z.string().default("revoked"),
  installed_at: z.string().default(""),
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
}).loose();

export const EMPTY_EXPRESS_INSTALLATION: ExpressInstallation = {
  id: "",
  workspace_id: "",
  agent_id: "",
  bot_id: "",
  cts_url: "",
  bot_name: "",
  installer_user_id: "",
  status: "revoked",
  installed_at: "",
  created_at: "",
  updated_at: "",
};

export const ListExpressInstallationsResponseSchema = z.object({
  installations: z.array(ExpressInstallationSchema).default([]),
  configured: z.boolean().default(false),
  install_supported: z.boolean().optional(),
}).loose();

export const EMPTY_LIST_EXPRESS_INSTALLATIONS_RESPONSE: ListExpressInstallationsResponse = {
  installations: [],
  configured: false,
};

export const RedeemExpressBindingTokenResponseSchema = z.object({
  workspace_id: z.string().default(""),
  installation_id: z.string().default(""),
  express_user_id: z.string().default(""),
}).loose();

export const EMPTY_REDEEM_EXPRESS_BINDING_TOKEN_RESPONSE: RedeemExpressBindingTokenResponse = {
  workspace_id: "",
  installation_id: "",
  express_user_id: "",
};

export const CustomPagesStateSchema = z.object({
  pages: z.array(z.object({
    id: z.string().min(1),
    title: z.string(),
    url: z.string().min(1),
    mode: z.enum(["embed", "tab"]).catch("tab"),
    description: z.string().catch(""),
  })),
  can_edit: z.boolean().catch(false),
});

export const ModelRefSchema = z.object({
  id: z.string(),
  name: z.string().default(""),
  identifier: z.string().default(""),
});

export const ModelProfileSchema = z.object({
  id: z.string(),
  name: z.string(),
  description: z.string().default(""),
  when_exhausted_profile_id: z.string().nullish().catch(null).transform((v) => v ?? null),
  options: z.array(ModelRowStateSchema).default([]),
  agents: z.array(ModelRefSchema).default([]),
  personal_profiles: z
    .array(
      z.object({
        profile_id: z.string(),
        agent: ModelRefSchema,
        matches_base: estimateFlag,
        diff_rows: z.array(z.number().int()).nullish().catch([]).transform((v) => v ?? []),
      }),
    )
    .nullish()
    .catch([])
    .transform((v) => v ?? []),
  open_issues: z.array(ModelRefSchema).default([]),
  fallback_of: z.array(ModelRefSchema).default([]),
  updated_at: z.string().default(""),
  personal: estimateFlag,
  owner_agent: ModelRefSchema.nullish().catch(null).transform((v) => v ?? null),
  based_on: ModelRefSchema.nullish().catch(null).transform((v) => v ?? null),
  matches_base: z.boolean().nullish().catch(null).transform((v) => v ?? null),
  diff_rows: z.array(z.number().int()).nullish().catch([]).transform((v) => v ?? []),
});

export const ModelProfileChangeSchema = z.object({
  kind: z.string(),
  ref: ModelRefSchema,
  before: ModelOptionSchema.nullish().catch(null).transform((v) => v ?? null),
  after: ModelOptionSchema.nullish().catch(null).transform((v) => v ?? null),
  reason: z.string().default(""),
});

export const AgentModelChoiceEntrySchema = z.object({
  task_id: z.string(),
  status: z.string().default(""),
  issue_id: z.string().nullish().transform((v) => v ?? null),
  issue_identifier: z.string().default(""),
  issue_title: z.string().default(""),
  position: z.number().int().default(0),
  choice: ModelChoiceSchema.nullish().catch(null).transform((v) => v ?? null),
  account_fact: optionalAccountFact,
  created_at: z.string().default(""),
});

export const RuntimeUnavailableSchema = z.object({
  runtime_id: z.string(),
  runtime_name: z.string().default(""),
  until: z.string(),
  reason: z.string().default(""),
  reason_code: optionalCode,
  until_estimated: estimateFlag,
  computed: estimateFlag,
  task_id: z.string().nullish().transform((v) => v ?? null),
  created_at: z.string().default(""),
});

export const AgentModelsSchema = z.object({
  agent_id: z.string(),
  can_edit: z.boolean().default(false),
  model_profile_id: z.string().nullish().transform((v) => v ?? null),
  profile_name: z.string().default(""),
  profile_agents: z.number().int().nonnegative().default(0),
  profile_issues: z.number().int().nonnegative().default(0),
  // An unknown mode from a newer server reads as "fields": no profile.
  mode: z.enum(["fields", "profile", "personal", "single"]).catch("fields"),
  personal_profile_id: z.string().nullish().catch(null).transform((v) => v ?? null),
  base_profile_id: z.string().nullish().catch(null).transform((v) => v ?? null),
  base_profile_name: z.string().nullish().catch("").transform((v) => v ?? ""),
  matches_base: z.boolean().nullish().catch(null).transform((v) => v ?? null),
  diff_rows: z.array(z.number().int()).nullish().catch([]).transform((v) => v ?? []),
  personal_rows: z.array(ModelRowStateSchema).nullish().catch([]).transform((v) => v ?? []),
  own_list: z.boolean().default(false),
  options: z.array(ModelOptionSchema).default([]),
  profile_rows: z.array(ModelRowStateSchema).default([]),
  disallowed_tools: z.array(z.string()).default([]),
  preview: ModelChoiceSchema,
  last: AgentModelChoiceEntrySchema.nullish().catch(null).transform((v) => v ?? null),
  journal: z.array(AgentModelChoiceEntrySchema).default([]),
  waiting: z.array(AgentModelChoiceEntrySchema).default([]),
  issues: z.array(ModelRefSchema.extend({ profile_id: z.string().default(""), profile_name: z.string().default("") })).default([]),
  runtime_unavailable: z.array(RuntimeUnavailableSchema).default([]),
});

export const IssueModelProfileSchema = z.object({
  issue_id: z.string(),
  model_profile_id: z.string().nullish().transform((v) => v ?? null),
  profile_name: z.string().default(""),
});

export const ModelProfilePreviewSchema = z.object({ changes: z.array(ModelProfileChangeSchema).default([]) });

// Instance subscriptions. Enum-like fields fall back to a safe value so a
// newer server's states still render; every optional field has a default.
const nullableString = z.string().nullish().catch(null).transform((v) => v ?? null);
const stringOrEmpty = z.string().nullish().catch("").transform((v) => v ?? "");
const percentMap = z
  .record(z.string(), z.number())
  .nullish()
  .catch({})
  .transform((v) => v ?? {});

export const SubscriptionLoginSessionSchema = z.object({
  id: stringOrEmpty,
  state: z.enum(["waiting", "done", "failed", "cancelled", "expired"]).catch("failed"),
  url: nullableString,
  code: nullableString,
  message: stringOrEmpty,
  started_at: nullableString,
});

export const SubscriptionSchema = z
  .object({
    id: z.string(),
    key: z.string(),
    provider: z.string(),
    family: z.string().nullish().catch(null),
    title: stringOrEmpty,
    owner_note: stringOrEmpty,
    auth: stringOrEmpty,
    credential_path: stringOrEmpty,
    windows: z.array(z.string()).nullish().catch([]).transform((v) => v ?? []),
    caps: percentMap,
    effective_caps: percentMap,
    account_group: stringOrEmpty,
    disabled_until: nullableString,
    disabled: z.boolean().nullish().catch(false).transform((v) => v === true),
    usage: z
      .array(
        z.object({
          window: z.string(),
          used_percent: z.number().nullish().catch(null).transform((v) => v ?? null),
          resets_at: nullableString,
          limit_reached: z.boolean().nullish().catch(false).transform((v) => v === true),
          source: stringOrEmpty,
          observed_at: stringOrEmpty,
        }),
      )
      .nullish()
      .catch([])
      .transform((v) => v ?? []),
    used_by: z
      .array(
        z.object({
          workspace_id: z.string(),
          runtime_profile_id: z.string(),
          display_name: stringOrEmpty,
          position: z.number().int().nullish().catch(0).transform((v) => v ?? 0),
        }),
      )
      .nullish()
      .catch([])
      .transform((v) => v ?? []),
    hosts: z
      .array(
        z.object({
          daemon_id: stringOrEmpty,
          device_name: stringOrEmpty,
          credential_state: z.enum(["present", "missing", "revoked", "unreadable", "unknown"]).catch("unknown"),
          checked_at: nullableString,
          error: stringOrEmpty,
        }),
      )
      .nullish()
      .catch([])
      .transform((v) => v ?? []),
    // Absent on servers that do not check sign-in yet; an unknown state reads as unchecked.
    login_state: z
      .enum(["logged_in", "revoked", "never", "check_failed", "unchecked"])
      .nullish()
      .catch("unchecked")
      .transform((v) => v ?? null),
    login: SubscriptionLoginSessionSchema.nullish().catch(null).transform((v) => v ?? null),
    created_at: stringOrEmpty,
    updated_at: stringOrEmpty,
  })
  .transform((sub) => ({
    ...sub,
    // Every provider but codex runs through Claude Code.
    family: sub.family === "codex" || sub.family === "claude" ? sub.family : sub.provider === "codex" ? "codex" : "claude",
  }));

export const SubscriptionListSchema = z.object({
  subscriptions: z.array(SubscriptionSchema),
  can_manage: z.boolean().nullish().catch(false).transform((v) => v === true),
});

export const SubscriptionLoginResponseSchema = z.object({
  login: SubscriptionLoginSessionSchema.nullish().transform((v) => v ?? null),
});

export const SubscriptionDiscrepancyListSchema = z.object({
  items: z.array(
    z.object({
      kind: z.string(),
      message: stringOrEmpty,
      subscription_key: nullableString,
      runtime_profile_id: nullableString,
      runtime_name: nullableString,
      workspace_id: nullableString,
      agent_id: nullableString,
      agent_name: nullableString,
      device_name: nullableString,
    }),
  ),
});

export const DeleteSubscriptionResultSchema = z.object({
  deleted: z.boolean().catch(true),
  detached_runtime_profile_ids: z.array(z.string()).nullish().catch([]).transform((v) => v ?? []),
});


// A verdict carries the reason as a code only; an unknown code stays a string.
const availabilityVerdictShape = {
  blocked: z.boolean().nullish().catch(false).transform((v) => v === true),
  usable: z.boolean().nullish().catch(false).transform((v) => v === true),
  reason_code: optionalCode,
  until: nullableString,
  until_estimated: estimateFlag,
};

export const AvailabilityVerdictSchema = z.object(availabilityVerdictShape);

export const PoolAccountSchema = z.object({
  id: z.string(),
  key: z.string(),
  title: stringOrEmpty,
  provider: stringOrEmpty,
  account_group: stringOrEmpty,
  position: z.number().int().nullish().catch(0).transform((v) => v ?? 0),
  ...availabilityVerdictShape,
});

export const PoolSchema = z
  .object({
    id: z.string(),
    name: stringOrEmpty,
    runtime_type: z.string().nullish().catch("claude"),
    description: stringOrEmpty,
    spend_order: z.string().nullish().catch("first"),
    all_workspaces: estimateFlag,
    revision: z.number().int().nullish().catch(0).transform((v) => v ?? 0),
    subscriptions: z.array(PoolAccountSchema).nullish().catch([]).transform((v) => v ?? []),
    workspace_ids: z.array(z.string()).nullish().catch([]).transform((v) => v ?? []),
    environments: z
      .array(
        z.object({
          runtime_profile_id: z.string(),
          workspace_id: z.string(),
          display_name: stringOrEmpty,
          enabled: z.boolean().nullish().catch(true).transform((v) => v !== false),
        }),
      )
      .nullish()
      .catch([])
      .transform((v) => v ?? []),
    availability: AvailabilityVerdictSchema.nullish().catch(null),
    created_at: stringOrEmpty,
    updated_at: stringOrEmpty,
  })
  .transform((pool) => ({
    ...pool,
    runtime_type: pool.runtime_type === "codex" ? ("codex" as const) : ("claude" as const),
    spend_order: pool.spend_order === "balanced" ? ("balanced" as const) : ("first" as const),
    availability: pool.availability ?? { blocked: false, usable: false, reason_code: null, until: null, until_estimated: false },
  }));

export const PoolListSchema = z.object({
  pools: z.array(PoolSchema),
  can_manage: estimateFlag,
});

export const PoolRevisionConflictSchema = z.object({
  code: z.literal("pool_revision_conflict"),
  current_revision: z.number().int(),
});


const profileRef = z.object({
  profile_id: z.string(),
  name: stringOrEmpty,
  personal: estimateFlag,
  owner_agent_id: nullableString,
  positions: z.array(z.number().int()).nullish().catch([]).transform((v) => v ?? []),
});
const profileRefs = z.array(profileRef).nullish().catch([]).transform((v) => v ?? []);
const count = z.number().int().nullish().catch(0).transform((v) => v ?? 0);

export const RuntimeModelUsageSchema = z.object({
  selected_in: profileRefs,
  fallback_profiles: profileRefs,
  based_on: profileRefs,
  serving_agents: z
    .array(
      z.object({
        id: z.string(),
        name: stringOrEmpty,
        profile_ids: z.array(z.string()).nullish().catch([]).transform((v) => v ?? []),
      }),
    )
    .nullish()
    .catch([])
    .transform((v) => v ?? []),
  hidden_agents: count,
  hidden_profiles: count,
  open_issues: count,
  deletable: estimateFlag,
});

export const RuntimeRunFactsSchema = z.object({
  runtime_id: z.string(),
  selection_state: z.string().nullish().catch("unused").transform((v) => v || "unused"),
  machine_status: stringOrEmpty,
  run_facts_24h: z.object({
    since: stringOrEmpty,
    until: stringOrEmpty,
    runs: count,
    unknown_account_runs: count,
    accounts: z
      .record(z.string(), z.number())
      .nullish()
      .catch({})
      .transform((v) => v ?? {}),
  }),
});

export const ManualRunListSchema = z.object({
  key: stringOrEmpty,
  runs: z
    .array(
      z.object({
        id: z.string(),
        subscription_key: stringOrEmpty,
        user_id: stringOrEmpty,
        user_name: stringOrEmpty,
        daemon_id: stringOrEmpty,
        command: stringOrEmpty,
        started_at: z.string(),
        finished_at: nullableString,
        exit_code: z.number().int().nullish().catch(null).transform((v) => v ?? null),
        unfinished: estimateFlag,
        agent_cap_exceeded: estimateFlag,
      }),
    )
    .nullish()
    .catch([])
    .transform((v) => v ?? []),
  next_before: nullableString,
});

