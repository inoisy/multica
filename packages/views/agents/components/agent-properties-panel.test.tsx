// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, screen } from "@testing-library/react";
import type { AgentActivity } from "@multica/core/agents";
import type { Agent, AgentRuntime, MemberWithUser } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

const pickers = vi.hoisted(() => ({
  runtime: null as null | {
    runtimes: AgentRuntime[];
    canEdit?: boolean;
    onChange: (id: string) => void;
  },
}));
const activityRef = vi.hoisted(() => ({
  current: new Map<string, unknown>(),
}));
const mockInitiateListModels = vi.hoisted(() => vi.fn());
const mockGetAgentModels = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/api", () => ({
  api: {
    initiateListModels: (...args: unknown[]) => mockInitiateListModels(...args),
    getListModelsResult: vi.fn(),
    getAgentModels: (...args: unknown[]) => mockGetAgentModels(...args),
  },
}));
vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "workspace-1",
}));
vi.mock("@multica/core/agents", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/agents")>()),
  useWorkspaceActivityMap: () => ({ byAgent: activityRef.current, loading: false }),
}));
vi.mock("../../common/actor-avatar", () => ({
  ActorAvatar: () => <span data-testid="avatar" />,
}));
vi.mock("./agent-presence-indicator", () => ({
  AgentPresenceIndicator: () => <span>presence</span>,
}));
vi.mock("./inspector/runtime-picker", () => ({
  RuntimePicker: (props: NonNullable<typeof pickers.runtime>) => {
    pickers.runtime = props;
    return <div data-testid="runtime-picker" />;
  },
}));

import { AgentPropertiesPanel } from "./agent-properties-panel";

function runtime(id: string, provider: string): AgentRuntime {
  return {
    id,
    workspace_id: "workspace-1",
    daemon_id: null,
    name: id,
    runtime_mode: provider === "qoder_cloud" ? "cloud" : "local",
    provider,
    launch_header: "",
    status: "online",
    device_info: "",
    metadata: {},
    owner_id: "user-1",
    visibility: "public",
    last_seen_at: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
}

const claude = runtime("rt-claude", "claude");
const qoder = runtime("rt-qoder", "qoder_cloud");

const owner = { user_id: "user-2", name: "Ada" } as MemberWithUser;

function agentOn(bound: AgentRuntime, over: Partial<Agent> = {}): Agent {
  return {
    id: "agent-1",
    workspace_id: "workspace-1",
    name: "Lambda",
    description: "",
    runtime_id: bound.id,
    runtime_config: {},
    model: "",
    permission_mode: "public_to",
    invocation_targets: [{ target_type: "workspace", target_id: null }],
    created_at: "2026-05-28T00:00:00Z",
    ...over,
  } as Agent;
}

function renderPanel(
  bound: AgentRuntime,
  { agent = agentOn(bound) }: { agent?: Agent } = {},
) {
  const onEditAccess = vi.fn();
  const onOpenModels = vi.fn();
  renderWithI18n(
    <QueryClientProvider
      client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
    >
      <AgentPropertiesPanel
        agent={agent}
        runtime={bound}
        runtimes={[claude, qoder]}
        presence={null}
        owner={owner}
        members={[owner]}
        currentUserId="user-1"
        onEditAccess={onEditAccess}
        onOpenModels={onOpenModels}
      />
    </QueryClientProvider>,
  );
  return { onEditAccess, onOpenModels };
}

function activityToday(total: number, completed: number, failed: number): AgentActivity {
  const buckets = Array.from({ length: 30 }, () => ({
    total: 0,
    failed: 0,
    completed: 0,
    cancelled: 0,
  }));
  buckets[29] = { total, failed, completed, cancelled: total - completed - failed };
  return { buckets, daysSinceCreated: 30 };
}

beforeEach(() => {
  pickers.runtime = null;
  mockGetAgentModels.mockReset();
  mockGetAgentModels.mockResolvedValue({
    mode: "profile",
    profile_rows: [
      { position: 2, runtime_name: "codex", model: "gpt", effective_model: "gpt", thinking_level: "", model_at_run: false },
      { position: 1, runtime_name: "claude", model: "opus", effective_model: "opus", thinking_level: "high", model_at_run: false },
    ],
    personal_rows: [],
  });
  activityRef.current = new Map();
  mockInitiateListModels.mockReset();
});
afterEach(cleanup);

describe("AgentPropertiesPanel", () => {
  it("lists the agent's properties", () => {
    renderPanel(claude);

    for (const label of [
      "Properties",
      "Status",
      "Owner",
      "Runtime",
      "Harness",
      "Model",
      "Access",
      "Runs (7d)",
      "Created",
    ]) {
      expect(screen.getByText(label)).toBeInTheDocument();
    }
    expect(screen.getByText("Ada")).toBeInTheDocument();
    expect(screen.getByText("Claude")).toBeInTheDocument();
  });

  // Runtime and model belong to the agent's Models list (model profiles).
  it("shows the runtime read-only", () => {
    renderPanel(claude);

    expect(pickers.runtime?.canEdit).toBe(false);
  });

  it("shows the saved first model row and opens the Models list from it", async () => {
    const { onOpenModels } = renderPanel(claude);

    const model = await screen.findByText("claude·opus·high");
    fireEvent.click(model.closest("button")!);
    expect(onOpenModels).toHaveBeenCalledTimes(1);
  });

  it("does not offer the Qoder runtime to other agents", () => {
    renderPanel(claude);

    expect(pickers.runtime?.runtimes.map((r) => r.id)).toEqual(["rt-claude"]);
  });

  it("locks the runtime and shows the Qoder agent instead of a model on Qoder", () => {
    renderPanel(qoder, {
      agent: agentOn(qoder, { runtime_config: { qoder_agent_id: "qa-42" } }),
    });

    expect(pickers.runtime?.canEdit).toBe(false);
    expect(screen.queryByText("Model")).not.toBeInTheDocument();
    expect(mockGetAgentModels).not.toHaveBeenCalled();
    expect(screen.getByText("Remote agent")).toBeInTheDocument();
    expect(screen.getByText("qa-42")).toBeInTheDocument();
    expect(mockInitiateListModels).not.toHaveBeenCalled();
  });

  it("opens the access form from the Access value", () => {
    const { onEditAccess } = renderPanel(claude, {
      agent: agentOn(claude, { permission_mode: "private", invocation_targets: [] }),
    });

    fireEvent.click(screen.getByRole("button", { name: /^Access: .+\. Change access$/ }));
    expect(onEditAccess).toHaveBeenCalledTimes(1);
  });

  it("counts the last 7 days of runs with their success rate", () => {
    activityRef.current = new Map([["agent-1", activityToday(4, 3, 1)]]);
    renderPanel(claude);

    expect(screen.getByText("4")).toBeInTheDocument();
    expect(screen.getByText("75% success")).toBeInTheDocument();
  });

  it("shows no success rate before the agent has run", () => {
    renderPanel(claude);

    expect(screen.getByText("0")).toBeInTheDocument();
    expect(screen.queryByText(/% success/)).not.toBeInTheDocument();
  });
});
