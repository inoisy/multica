// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Agent, AgentModels, ModelChoice, ModelRowState } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { NavigationProvider, type NavigationAdapter } from "../../navigation";

const apiMock = vi.hoisted(() => ({
  getAgentModels: vi.fn(),
  updateAgentModels: vi.fn(),
  listModelProfiles: vi.fn(),
}));
vi.mock("@multica/core/api", () => ({ api: apiMock }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/paths", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/paths")>()),
  useWorkspacePaths: () => ({
    modelProfiles: () => "/acme/profiles",
    issueDetail: (id: string) => `/acme/issues/${id}`,
  }),
}));
// The row editor's runtime and model pickers talk to daemons; the screen's
// wiring is what this test covers.
vi.mock("../../model-profiles/model-rows-editor", () => ({
  ModelRowsEditor: ({ rows }: { rows: unknown[] }) => <div data-testid="rows-editor">{rows.length} rows</div>,
}));

import { AgentModels as AgentModelsSection } from "./agent-models";

const agent = { id: "agent-1", name: "Dev" } as unknown as Agent;

function row(position: number, runtime_name: string, model: string, state = "available", extra: Partial<ModelRowState> = {}): ModelRowState {
  return { position, runtime_id: `rt-${position}`, runtime_name, provider: "codex", model, thinking_level: "", custom_args: null, state, until: null, chosen: false, ...extra };
}

function choice(over: Partial<ModelChoice>): ModelChoice {
  return {
    source: "agent_profile", profile_id: "p1", profile_name: "Strong", issue_id: "", reason: "primary", position: 1,
    chosen: null, runtime_name: "", provider: "", exhausted_profiles: [], wait_until: null, rows: [], ...over,
  };
}

function models(over: Partial<AgentModels> = {}): AgentModels {
  const rows = [row(1, "codex", "sol", "limited", { until: "2026-10-19T15:53:00Z" }), row(2, "claude", "opus", "available", { chosen: true })];
  return {
    agent_id: "agent-1", can_edit: true, model_profile_id: "p1", profile_name: "Strong", profile_agents: 3, profile_issues: 1,
    own_list: false, options: [], profile_rows: rows, disallowed_tools: ["github:delete_repo"],
    preview: choice({ reason: "fallback_row", position: 2, rows }),
    last: null, journal: [], waiting: [], issues: [], runtime_unavailable: [], ...over,
  };
}

function renderSection() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const navigation: NavigationAdapter = {
    push: vi.fn(), replace: vi.fn(), back: vi.fn(), pathname: "/acme/agents/agent-1",
    searchParams: new URLSearchParams(), hash: "", getShareableUrl: (path) => path,
  };
  return renderWithI18n(
    <NavigationProvider value={navigation}>
      <QueryClientProvider client={client}>
        <AgentModelsSection agent={agent} runtimes={[]} members={[]} currentUserId="u1" />
      </QueryClientProvider>
    </NavigationProvider>,
  );
}

describe("AgentModels", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    apiMock.listModelProfiles.mockResolvedValue([{ id: "p1", name: "Strong" }]);
  });

  it("shows the profile, why the second row runs, and the profile rows with their state", async () => {
    apiMock.getAgentModels.mockResolvedValue(models());
    renderSection();
    expect(await screen.findByText("Models of “Strong”")).toBeTruthy();
    const now = screen.getByTestId("agent-models-now");
    expect(now.textContent).toContain("claude·opus");
    expect(now.textContent).toContain("profile “Strong”");
    expect(now.textContent).toMatch(/row 1 on a limit until .* — took row 2/);
    expect(screen.getByText("Editing the profile affects 3 agents and 1 issues.")).toBeTruthy();
    expect(screen.getAllByText(/on a limit until/).length).toBeGreaterThan(0);
    expect(screen.getByText("github:delete_repo")).toBeTruthy();
  });

  it("says when every model is on a limit and the run waits", async () => {
    const waiting = choice({ reason: "all_unavailable", position: 0, wait_until: "2026-10-19T15:53:00Z" });
    apiMock.getAgentModels.mockResolvedValue(models({
      waiting: [{ task_id: "t1", status: "queued", issue_id: "i1", issue_identifier: "STT-1", issue_title: "", position: 0, choice: waiting, created_at: "2026-10-19T12:00:00Z" }],
    }));
    renderSection();
    const now = await screen.findByTestId("agent-models-now");
    expect(now.textContent).toMatch(/every model on a limit until .* — the run waits/);
    expect(now.textContent).toContain("STT-1");
  });

  it("names the exhausted profile when the fallback profile was taken", async () => {
    const taken = choice({ reason: "fallback_profile", profile_id: "p2", profile_name: "Mid", exhausted_profiles: ["Strong"], rows: [row(1, "glm", "glm", "available", { chosen: true })] });
    apiMock.getAgentModels.mockResolvedValue(models({
      preview: taken,
      last: { task_id: "t1", status: "completed", issue_id: null, issue_identifier: "", issue_title: "", position: 1, choice: taken, created_at: "2026-10-19T12:00:00Z" },
    }));
    renderSection();
    const now = await screen.findByTestId("agent-models-now");
    expect(now.textContent).toContain("every model of “Strong” on a limit — took “Mid”");
    // The fallback profile's row is not marked current on the agent's own profile.
    expect(screen.queryByText("current")).toBeNull();
  });

  it("pins a profile row as a one-row own list", async () => {
    apiMock.getAgentModels.mockResolvedValue(models());
    apiMock.updateAgentModels.mockResolvedValue(models({ own_list: true }));
    renderSection();
    const pins = await screen.findAllByRole("button", { name: "Pin" });
    fireEvent.click(pins[1]!);
    await waitFor(() => expect(apiMock.updateAgentModels).toHaveBeenCalled());
    expect(apiMock.updateAgentModels).toHaveBeenCalledWith("agent-1", {
      options: [{ runtime_id: "rt-2", model: "opus", thinking_level: "", custom_args: null }],
    });
  });

  it("opens the own chain as a copy of the profile rows", async () => {
    apiMock.getAgentModels.mockResolvedValue(models());
    renderSection();
    expect((await screen.findByRole("radio", { name: /^Profile/ }) as HTMLInputElement).checked).toBe(true);
    fireEvent.click(screen.getByRole("radio", { name: /^Own chain/ }));
    expect((await screen.findByTestId("rows-editor")).textContent).toBe("2 rows");
  });

  it("pins one model from the profile's primary row", async () => {
    apiMock.getAgentModels.mockResolvedValue(models());
    renderSection();
    fireEvent.click(await screen.findByRole("radio", { name: /^One model/ }));
    expect((await screen.findByTestId("rows-editor")).textContent).toBe("1 rows");
  });

  it("goes back from an own chain to the profile by clearing the list", async () => {
    const own = [row(1, "claude", "sonnet"), row(2, "codex", "sol")];
    apiMock.getAgentModels.mockResolvedValue(models({ own_list: true, options: own, preview: choice({ source: "agent_list", rows: own }) }));
    apiMock.updateAgentModels.mockResolvedValue(models());
    renderSection();
    expect((await screen.findByRole("radio", { name: /^Own chain/ }) as HTMLInputElement).checked).toBe(true);
    expect(screen.getByText("Profile “Strong” is not used while the agent has its own model.")).toBeTruthy();
    fireEvent.click(screen.getByRole("radio", { name: /^Profile/ }));
    fireEvent.click(await screen.findByRole("button", { name: "Use profile “Strong”" }));
    await waitFor(() => expect(apiMock.updateAgentModels).toHaveBeenCalledWith("agent-1", { options: [], model_profile_id: "p1" }));
  });

  it("shows its own chain and the journal for an agent with an own list", async () => {
    const own = [row(1, "claude", "sonnet", "available", { chosen: true })];
    const entry = choice({ source: "agent_list", profile_name: "", rows: own });
    apiMock.getAgentModels.mockResolvedValue(models({
      own_list: true, options: own, preview: entry,
      journal: [{ task_id: "t1", status: "completed", issue_id: "i1", issue_identifier: "STT-9", issue_title: "x", position: 1, choice: entry, created_at: "2026-10-19T12:00:00Z" }],
    }));
    renderSection();
    // A one-row own list is a pinned model.
    expect((await screen.findByRole("radio", { name: /^One model/ }) as HTMLInputElement).checked).toBe(true);
    expect(screen.getByTestId("agent-models-own").textContent).toContain("sonnet");
    expect(screen.queryByText("Models of “Strong”")).toBeNull();
    expect(screen.getByTestId("agent-models-journal").textContent).toContain("STT-9");
    expect(screen.getByTestId("agent-models-journal").textContent).toContain("own chain");
  });

  it("renders nothing against a backend without model endpoints", async () => {
    apiMock.getAgentModels.mockRejectedValue(new Error("404"));
    const { container } = renderSection();
    await waitFor(() => expect(apiMock.getAgentModels).toHaveBeenCalled());
    expect(container.textContent).toBe("");
  });
});
