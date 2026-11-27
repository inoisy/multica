import type { ApiClient } from "./client";
import { parseWithFallback } from "./schema";
import type { AgentModels, CreateModelProfileInput, CreatePersonalProfileInput, IssueModelProfile, ModelProfile, ModelProfileChange, ModelProfileInput, ModelProfileKind, RuntimeUnavailable, SetPersonalProfileInput, SingleModelInput, UpdateAgentModelsInput } from "../types/model-profile";
import { AgentModelsSchema, IssueModelProfileSchema, ModelProfilePreviewSchema, ModelProfileSchema, RuntimeUnavailableSchema } from "./schemas";
import {
  DeleteSubscriptionResultSchema,
  PoolListSchema,
  PoolSchema,
  ManualRunListSchema,
  RuntimeModelUsageSchema,
  RuntimeProfileSubscriptionsSchema,
  RuntimeRunFactsSchema,
  SubscriptionDiscrepancyListSchema,
  SubscriptionListSchema,
  SubscriptionLoginResponseSchema,
  SubscriptionSchema,
} from "./schemas";
import type {
  CreatePoolInput,
  CreateSubscriptionInput,
  DeleteSubscriptionResult,
  Pool,
  PoolList,
  RuntimeProfileSubscriptions,
  SetPoolGrantsInput,
  UpdatePoolInput,
  StartSubscriptionLoginInput,
  Subscription,
  SubscriptionDiscrepancy,
  SubscriptionList,
  SubscriptionLoginSession,
  UpdateSubscriptionInput,
} from "../types/subscription";
import type { ManualRunList, RuntimeModelUsage, RuntimeRunFacts } from "../types/runtime-model";
import type {
  ExpressInstallation,
  ListExpressInstallationsResponse,
  RegisterExpressRequest,
  RedeemExpressBindingTokenResponse,
} from "../types";
import {
  ExpressInstallationSchema,
  ListExpressInstallationsResponseSchema,
  RedeemExpressBindingTokenResponseSchema,
  EMPTY_EXPRESS_INSTALLATION,
  EMPTY_LIST_EXPRESS_INSTALLATIONS_RESPONSE,
  EMPTY_REDEEM_EXPRESS_BINDING_TOKEN_RESPONSE,
} from "./schemas";

// Endpoints the fork adds. They live outside the ApiClient class body so
// upstream edits to client.ts do not conflict with them; client.ts installs
// them on the prototype once the class is defined.

declare module "./client" {
  interface ApiClient {
    // Model profiles and agent model lists. The server chooses a run's model
    // from these at every claim; edits apply from the next run.
    /** Shared profiles by default; `personal` lists the agents' personal profiles, `all` both. */
    listModelProfiles(kind?: ModelProfileKind): Promise<ModelProfile[]>;
    createModelProfile(input: CreateModelProfileInput): Promise<ModelProfile>;
    updateModelProfile(id: string, input: ModelProfileInput): Promise<ModelProfile>;
    deleteModelProfile(id: string): Promise<void>;
    /** Who would get another model on their next run if the edit were saved. Saves nothing. */
    previewModelProfile(id: string, input: ModelProfileInput): Promise<ModelProfileChange[]>;
    getAgentModels(agentId: string): Promise<AgentModels>;
    updateAgentModels(agentId: string, input: UpdateAgentModelsInput): Promise<AgentModels>;
    /** Copies a shared profile into the agent's personal profile, which the agent then runs from. */
    createAgentPersonalProfile(agentId: string, input?: CreatePersonalProfileInput): Promise<AgentModels>;
    setAgentPersonalProfile(agentId: string, input: SetPersonalProfileInput): Promise<AgentModels>;
    /** Back to the shared profile the personal one was made from; the personal profile is deleted. */
    resetAgentPersonalProfile(agentId: string): Promise<AgentModels>;
    setAgentSingleModel(agentId: string, input: SingleModelInput): Promise<AgentModels>;
    getIssueModelProfile(issueId: string): Promise<IssueModelProfile>;
    setIssueModelProfile(issueId: string, modelProfileId: string | null): Promise<IssueModelProfile>;
    listRuntimeUnavailable(): Promise<RuntimeUnavailable[]>;
    // ---------------------------------------------------------------------
    // Instance subscriptions and the runtime pools that spend them. Reads are
    // open to members; writes need an instance admin (the server answers 403).
    // A malformed answer throws rather than rendering as "no subscriptions".
    // ---------------------------------------------------------------------
    listSubscriptions(): Promise<SubscriptionList>;
    createSubscription(input: CreateSubscriptionInput): Promise<Subscription>;
    updateSubscription(key: string, patch: UpdateSubscriptionInput): Promise<Subscription>;
    deleteSubscription(key: string): Promise<DeleteSubscriptionResult>;
    /** Things that need a person: credentials missing on a host, unused subscriptions, empty pools, agents on a switched-off pool. */
    listSubscriptionDiscrepancies(): Promise<SubscriptionDiscrepancy[]>;
    /** Starts a sign-in on the daemon host. The token is sent once and kept nowhere on the client. */
    startSubscriptionLogin(key: string, input: StartSubscriptionLoginInput): Promise<SubscriptionLoginSession>;
    getSubscriptionLogin(key: string): Promise<SubscriptionLoginSession | null>;
    cancelSubscriptionLogin(key: string): Promise<void>;
    // -------------------------------------------------------------------
    // Pools of accounts: the order the accounts are spent in and the
    // workspaces that have an environment on them. A write carries the
    // revision the editor read; a pool that moved on answers 409
    // `pool_revision_conflict` with `current_revision`.
    // -------------------------------------------------------------------
    listPools(): Promise<PoolList>;
    createPool(input: CreatePoolInput): Promise<Pool>;
    updatePool(id: string, input: UpdatePoolInput): Promise<Pool>;
    setPoolGrants(id: string, input: SetPoolGrantsInput): Promise<Pool>;
    deletePool(id: string): Promise<void>;
    getRuntimeProfileSubscriptions(workspaceId: string, profileId: string): Promise<RuntimeProfileSubscriptions>;
    /** Where the environment is selected, who it serves and whether the references let it go. */
    getRuntimeModelUsage(runtimeId: string): Promise<RuntimeModelUsage>;
    /** What ran on the environment in the last 24 hours, apart from its forecast and its machine state. */
    getRuntimeRunFacts(runtimeId: string): Promise<RuntimeRunFacts>;
    /** The journal of commands run by hand under an account; an instance admin in person only. */
    listSubscriptionManualRuns(key: string, params?: { limit?: number; before?: string }): Promise<ManualRunList>;
    listExpressInstallations(workspaceId: string): Promise<ListExpressInstallationsResponse>;
    registerExpressBot(workspaceId: string, agentId: string, body: RegisterExpressRequest): Promise<ExpressInstallation>;
    deleteExpressInstallation(workspaceId: string, installationId: string): Promise<void>;
    redeemExpressBindingToken(token: string): Promise<RedeemExpressBindingTokenResponse>;
  }
}

function parseModelProfile(this: ApiClient, raw: unknown, endpoint: string): ModelProfile {
  const parsed = parseWithFallback<ModelProfile | null>(raw, ModelProfileSchema, null, { endpoint });
  if (!parsed) throw new Error("Could not read the model profile");
  return parsed;
}

async function sendAgentModels(this: ApiClient, agentId: string, path: string, method: "POST" | "PUT" | "DELETE", body?: unknown): Promise<AgentModels> {
  const endpoint = `${method} /api/agents/:id/models${path}`;
  const raw = await this["fetch"]<unknown>(`/api/agents/${encodeURIComponent(agentId)}/models${path}`, {
    method,
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
  const parsed = parseWithFallback<AgentModels | null>(raw, AgentModelsSchema, null, { endpoint });
  if (!parsed) throw new Error("Could not read the agent's models");
  return parsed;
}

function parseSubscription(this: ApiClient, raw: unknown, endpoint: string): Subscription {
  const parsed = parseWithFallback<Subscription | null>(raw, SubscriptionSchema, null, { endpoint });
  if (!parsed) throw new Error("Could not read the subscription");
  return parsed;
}

function parseLoginSession(this: ApiClient, raw: unknown, endpoint: string): SubscriptionLoginSession | null {
  const parsed = parseWithFallback<{ login: SubscriptionLoginSession | null } | undefined>(
    raw,
    SubscriptionLoginResponseSchema,
    undefined,
    { endpoint },
  );
  if (parsed === undefined) throw new Error("Could not read the sign-in state");
  return parsed.login;
}

function parsePool(this: ApiClient, raw: unknown, endpoint: string): Pool {
  const parsed = parseWithFallback<Pool | null>(raw, PoolSchema, null, { endpoint });
  if (!parsed) throw new Error("Could not read the pool");
  return parsed;
}

function parseProfileSubscriptions(this: ApiClient, raw: unknown, endpoint: string): RuntimeProfileSubscriptions {
  const parsed = parseWithFallback<RuntimeProfileSubscriptions | null>(
    raw,
    RuntimeProfileSubscriptionsSchema,
    null,
    { endpoint },
  );
  if (!parsed) throw new Error("Could not load the runtime's subscriptions");
  return parsed;
}

export function installForkMethods(Client: typeof ApiClient): void {
  // Model profiles and agent model lists. The server chooses a run's model
  // from these at every claim; edits apply from the next run.
  /** Shared profiles by default; `personal` lists the agents' personal profiles, `all` both. */
  Client.prototype.listModelProfiles = async function (this: ApiClient, kind: ModelProfileKind = "shared"): Promise<ModelProfile[]> {
    const raw = await this["fetch"]<unknown>(kind === "shared" ? "/api/model-profiles" : `/api/model-profiles?kind=${kind}`);
    const parsed = parseWithFallback<ModelProfile[] | null>(raw, ModelProfileSchema.array(), null, { endpoint: "GET /api/model-profiles" });
    if (!parsed) throw new Error("Could not load model profiles");
    return parsed;
  };

  Client.prototype.createModelProfile = async function (this: ApiClient, input: CreateModelProfileInput): Promise<ModelProfile> {
    const raw = await this["fetch"]<unknown>("/api/model-profiles", { method: "POST", body: JSON.stringify(input) });
    return parseModelProfile.call(this, raw, "POST /api/model-profiles");
  };

  Client.prototype.updateModelProfile = async function (this: ApiClient, id: string, input: ModelProfileInput): Promise<ModelProfile> {
    const raw = await this["fetch"]<unknown>(`/api/model-profiles/${encodeURIComponent(id)}`, { method: "PUT", body: JSON.stringify(input) });
    return parseModelProfile.call(this, raw, "PUT /api/model-profiles/:id");
  };

  Client.prototype.deleteModelProfile = async function (this: ApiClient, id: string): Promise<void> {
    await this["fetch"](`/api/model-profiles/${encodeURIComponent(id)}`, { method: "DELETE" });
  };

  /** Who would get another model on their next run if the edit were saved. Saves nothing. */
  Client.prototype.previewModelProfile = async function (this: ApiClient, id: string, input: ModelProfileInput): Promise<ModelProfileChange[]> {
    const raw = await this["fetch"]<unknown>(`/api/model-profiles/${encodeURIComponent(id)}/preview`, { method: "POST", body: JSON.stringify(input) });
    const parsed = parseWithFallback<{ changes: ModelProfileChange[] } | null>(
      raw, ModelProfilePreviewSchema, null, { endpoint: "POST /api/model-profiles/:id/preview" });
    if (!parsed) throw new Error("Could not preview the profile edit");
    return parsed.changes;
  };

  Client.prototype.getAgentModels = async function (this: ApiClient, agentId: string): Promise<AgentModels> {
    const raw = await this["fetch"]<unknown>(`/api/agents/${encodeURIComponent(agentId)}/models`);
    const parsed = parseWithFallback<AgentModels | null>(raw, AgentModelsSchema, null, { endpoint: "GET /api/agents/:id/models" });
    if (!parsed) throw new Error("Could not load the agent's models");
    return parsed;
  };

  Client.prototype.updateAgentModels = async function (this: ApiClient, agentId: string, input: UpdateAgentModelsInput): Promise<AgentModels> {
    const raw = await this["fetch"]<unknown>(`/api/agents/${encodeURIComponent(agentId)}/models`, { method: "PUT", body: JSON.stringify(input) });
    const parsed = parseWithFallback<AgentModels | null>(raw, AgentModelsSchema, null, { endpoint: "PUT /api/agents/:id/models" });
    if (!parsed) throw new Error("Could not read the agent's models");
    return parsed;
  };

  /** Copies a shared profile into the agent's personal profile, which the agent then runs from. */
  Client.prototype.createAgentPersonalProfile = function (this: ApiClient, agentId: string, input: CreatePersonalProfileInput = {}): Promise<AgentModels> {
    return sendAgentModels.call(this, agentId, "/personal", "POST", input);
  };

  Client.prototype.setAgentPersonalProfile = function (this: ApiClient, agentId: string, input: SetPersonalProfileInput): Promise<AgentModels> {
    return sendAgentModels.call(this, agentId, "/personal", "PUT", input);
  };

  /** Back to the shared profile the personal one was made from; the personal profile is deleted. */
  Client.prototype.resetAgentPersonalProfile = function (this: ApiClient, agentId: string): Promise<AgentModels> {
    return sendAgentModels.call(this, agentId, "/personal", "DELETE");
  };

  Client.prototype.setAgentSingleModel = function (this: ApiClient, agentId: string, input: SingleModelInput): Promise<AgentModels> {
    return sendAgentModels.call(this, agentId, "/single", "PUT", input);
  };

  Client.prototype.getIssueModelProfile = async function (this: ApiClient, issueId: string): Promise<IssueModelProfile> {
    const raw = await this["fetch"]<unknown>(`/api/issues/${encodeURIComponent(issueId)}/model-profile`);
    const parsed = parseWithFallback<IssueModelProfile | null>(raw, IssueModelProfileSchema, null, { endpoint: "GET /api/issues/:id/model-profile" });
    if (!parsed) throw new Error("Could not load the issue's model profile");
    return parsed;
  };

  Client.prototype.setIssueModelProfile = async function (this: ApiClient, issueId: string, modelProfileId: string | null): Promise<IssueModelProfile> {
    const raw = await this["fetch"]<unknown>(`/api/issues/${encodeURIComponent(issueId)}/model-profile`, {
      method: "PUT", body: JSON.stringify({ model_profile_id: modelProfileId }),
    });
    const parsed = parseWithFallback<IssueModelProfile | null>(raw, IssueModelProfileSchema, null, { endpoint: "PUT /api/issues/:id/model-profile" });
    if (!parsed) throw new Error("Could not read the issue's model profile");
    return parsed;
  };

  Client.prototype.listRuntimeUnavailable = async function (this: ApiClient): Promise<RuntimeUnavailable[]> {
    const raw = await this["fetch"]<unknown>("/api/runtime-unavailable");
    return parseWithFallback<RuntimeUnavailable[]>(raw, RuntimeUnavailableSchema.array(), [], { endpoint: "GET /api/runtime-unavailable" });
  };

  // ---------------------------------------------------------------------
  // Instance subscriptions and the runtime pools that spend them. Reads are
  // open to members; writes need an instance admin (the server answers 403).
  // A malformed answer throws rather than rendering as "no subscriptions".
  // ---------------------------------------------------------------------
  Client.prototype.listSubscriptions = async function (this: ApiClient): Promise<SubscriptionList> {
    const raw = await this["fetch"]<unknown>("/api/subscriptions");
    const parsed = parseWithFallback<SubscriptionList | null>(raw, SubscriptionListSchema, null, {
      endpoint: "GET /api/subscriptions",
    });
    if (!parsed) throw new Error("Could not load subscriptions");
    return parsed;
  };

  Client.prototype.createSubscription = async function (this: ApiClient, input: CreateSubscriptionInput): Promise<Subscription> {
    const raw = await this["fetch"]<unknown>("/api/subscriptions", { method: "POST", body: JSON.stringify(input) });
    return parseSubscription.call(this, raw, "POST /api/subscriptions");
  };

  Client.prototype.updateSubscription = async function (this: ApiClient, key: string, patch: UpdateSubscriptionInput): Promise<Subscription> {
    const raw = await this["fetch"]<unknown>(`/api/subscriptions/${encodeURIComponent(key)}`, {
      method: "PATCH",
      body: JSON.stringify(patch),
    });
    return parseSubscription.call(this, raw, "PATCH /api/subscriptions/:key");
  };

  Client.prototype.deleteSubscription = async function (this: ApiClient, key: string): Promise<DeleteSubscriptionResult> {
    const raw = await this["fetch"]<unknown>(`/api/subscriptions/${encodeURIComponent(key)}`, { method: "DELETE" });
    return parseWithFallback<DeleteSubscriptionResult>(
      raw,
      DeleteSubscriptionResultSchema,
      { deleted: true, detached_runtime_profile_ids: [] },
      { endpoint: "DELETE /api/subscriptions/:key" },
    );
  };

  /** Things that need a person: credentials missing on a host, unused subscriptions, empty pools, agents on a switched-off pool. */
  Client.prototype.listSubscriptionDiscrepancies = async function (this: ApiClient): Promise<SubscriptionDiscrepancy[]> {
    const raw = await this["fetch"]<unknown>("/api/subscriptions/discrepancies");
    const parsed = parseWithFallback<{ items: SubscriptionDiscrepancy[] } | null>(
      raw,
      SubscriptionDiscrepancyListSchema,
      null,
      { endpoint: "GET /api/subscriptions/discrepancies" },
    );
    if (!parsed) throw new Error("Could not load discrepancies");
    return parsed.items;
  };

  /** Starts a sign-in on the daemon host. The token is sent once and kept nowhere on the client. */
  Client.prototype.startSubscriptionLogin = async function (this: ApiClient, key: string, input: StartSubscriptionLoginInput): Promise<SubscriptionLoginSession> {
    const raw = await this["fetch"]<unknown>(`/api/subscriptions/${encodeURIComponent(key)}/login`, {
      method: "POST",
      body: JSON.stringify(input),
    });
    const session = parseLoginSession.call(this, raw, "POST /api/subscriptions/:key/login");
    if (!session) throw new Error("Could not read the sign-in state");
    return session;
  };

  Client.prototype.getSubscriptionLogin = async function (this: ApiClient, key: string): Promise<SubscriptionLoginSession | null> {
    const raw = await this["fetch"]<unknown>(`/api/subscriptions/${encodeURIComponent(key)}/login`);
    return parseLoginSession.call(this, raw, "GET /api/subscriptions/:key/login");
  };

  Client.prototype.cancelSubscriptionLogin = async function (this: ApiClient, key: string): Promise<void> {
    await this["fetch"](`/api/subscriptions/${encodeURIComponent(key)}/login`, { method: "DELETE" });
  };

  // -------------------------------------------------------------------
  // Pools of accounts: the order the accounts are spent in and the
  // workspaces that have an environment on them. A write carries the
  // revision the editor read; a pool that moved on answers 409
  // `pool_revision_conflict` with `current_revision`.
  // -------------------------------------------------------------------
  Client.prototype.listPools = async function (this: ApiClient): Promise<PoolList> {
    const raw = await this["fetch"]<unknown>("/api/pools");
    const parsed = parseWithFallback<PoolList | null>(raw, PoolListSchema, null, { endpoint: "GET /api/pools" });
    if (!parsed) throw new Error("Could not load pools");
    return parsed;
  };

  Client.prototype.createPool = async function (this: ApiClient, input: CreatePoolInput): Promise<Pool> {
    const raw = await this["fetch"]<unknown>("/api/pools", { method: "POST", body: JSON.stringify(input) });
    return parsePool.call(this, raw, "POST /api/pools");
  };

  Client.prototype.updatePool = async function (this: ApiClient, id: string, input: UpdatePoolInput): Promise<Pool> {
    const raw = await this["fetch"]<unknown>(`/api/pools/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify(input) });
    return parsePool.call(this, raw, "PATCH /api/pools/:id");
  };

  Client.prototype.setPoolGrants = async function (this: ApiClient, id: string, input: SetPoolGrantsInput): Promise<Pool> {
    const raw = await this["fetch"]<unknown>(`/api/pools/${encodeURIComponent(id)}/grants`, { method: "PUT", body: JSON.stringify(input) });
    return parsePool.call(this, raw, "PUT /api/pools/:id/grants");
  };

  Client.prototype.deletePool = async function (this: ApiClient, id: string): Promise<void> {
    await this["fetch"](`/api/pools/${encodeURIComponent(id)}`, { method: "DELETE" });
  };

  Client.prototype.getRuntimeProfileSubscriptions = async function (this: ApiClient, workspaceId: string, profileId: string): Promise<RuntimeProfileSubscriptions> {
    const raw = await this["fetch"]<unknown>(
      `/api/workspaces/${workspaceId}/runtime-profiles/${encodeURIComponent(profileId)}/subscriptions`,
    );
    return parseProfileSubscriptions.call(this, raw, "GET /api/workspaces/:ws/runtime-profiles/:id/subscriptions");
  };

  /** Where the environment is selected, who it serves and whether the references let it go. */
  Client.prototype.getRuntimeModelUsage = async function (this: ApiClient, runtimeId: string): Promise<RuntimeModelUsage> {
    const raw = await this["fetch"]<unknown>(`/api/runtimes/${encodeURIComponent(runtimeId)}/model-usage`);
    const parsed = parseWithFallback<RuntimeModelUsage | null>(raw, RuntimeModelUsageSchema, null, {
      endpoint: "GET /api/runtimes/:id/model-usage",
    });
    if (!parsed) throw new Error("Could not load where the environment is used");
    return parsed;
  };

  /** What ran on the environment in the last 24 hours, apart from its forecast and its machine state. */
  Client.prototype.getRuntimeRunFacts = async function (this: ApiClient, runtimeId: string): Promise<RuntimeRunFacts> {
    const raw = await this["fetch"]<unknown>(`/api/runtimes/${encodeURIComponent(runtimeId)}/run-facts`);
    const parsed = parseWithFallback<RuntimeRunFacts | null>(raw, RuntimeRunFactsSchema, null, {
      endpoint: "GET /api/runtimes/:id/run-facts",
    });
    if (!parsed) throw new Error("Could not load the environment's runs");
    return parsed;
  };

  /** The journal of commands run by hand under an account; an instance admin in person only. */
  Client.prototype.listSubscriptionManualRuns = async function (this: ApiClient, key: string, params?: { limit?: number; before?: string }): Promise<ManualRunList> {
    const search = new URLSearchParams();
    if (params?.limit) search.set("limit", String(params.limit));
    if (params?.before) search.set("before", params.before);
    const query = search.toString();
    const raw = await this["fetch"]<unknown>(
      `/api/subscriptions/${encodeURIComponent(key)}/manual-runs${query ? `?${query}` : ""}`,
    );
    const parsed = parseWithFallback<ManualRunList | null>(raw, ManualRunListSchema, null, {
      endpoint: "GET /api/subscriptions/:key/manual-runs",
    });
    if (!parsed) throw new Error("Could not load the manual runs");
    return parsed;
  };

  Client.prototype.listExpressInstallations = async function (this: ApiClient, workspaceId: string): Promise<ListExpressInstallationsResponse> {
    const raw = await this["fetch"]<unknown>(`/api/workspaces/${workspaceId}/express/installations`);
    return parseWithFallback(
      raw,
      ListExpressInstallationsResponseSchema,
      EMPTY_LIST_EXPRESS_INSTALLATIONS_RESPONSE,
      { endpoint: "GET /api/workspaces/:id/express/installations" },
    );
  };

  Client.prototype.registerExpressBot = async function (this: ApiClient, workspaceId: string, agentId: string, body: RegisterExpressRequest): Promise<ExpressInstallation> {
    const search = new URLSearchParams({ agent_id: agentId });
    const raw = await this["fetch"]<unknown>(
      `/api/workspaces/${workspaceId}/express/install?${search.toString()}`,
      { method: "POST", body: JSON.stringify(body) },
    );
    return parseWithFallback(raw, ExpressInstallationSchema, EMPTY_EXPRESS_INSTALLATION, {
      endpoint: "POST /api/workspaces/:id/express/install",
    });
  };

  Client.prototype.deleteExpressInstallation = async function (this: ApiClient, workspaceId: string, installationId: string): Promise<void> {
    await this["fetch"](`/api/workspaces/${workspaceId}/express/installations/${installationId}`, {
      method: "DELETE",
    });
  };

  Client.prototype.redeemExpressBindingToken = async function (this: ApiClient, token: string): Promise<RedeemExpressBindingTokenResponse> {
    const raw = await this["fetch"]<unknown>(`/api/express/binding/redeem`, {
      method: "POST",
      body: JSON.stringify({ token }),
    });
    return parseWithFallback(
      raw,
      RedeemExpressBindingTokenResponseSchema,
      EMPTY_REDEEM_EXPRESS_BINDING_TOKEN_RESPONSE,
      { endpoint: "POST /api/express/binding/redeem" },
    );
  };
}
