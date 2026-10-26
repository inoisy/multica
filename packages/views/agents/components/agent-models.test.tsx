// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Agent, AgentModelChoiceEntry, AgentModels, ModelChoice, ModelRowState } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { NavigationProvider, type NavigationAdapter } from "../../navigation";

const apiMock = vi.hoisted(() => ({
  getAgentModels: vi.fn(),
  updateAgentModels: vi.fn(),
  listModelProfiles: vi.fn(),
  createAgentPersonalProfile: vi.fn(),
  setAgentPersonalProfile: vi.fn(),
  setAgentSingleModel: vi.fn(),
  resetAgentPersonalProfile: vi.fn(),
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
  ModelRowsEditor: ({ rows, baseRows }: { rows: unknown[]; baseRows?: unknown[] }) => (
    <div data-testid="rows-editor" data-base-rows={baseRows ? baseRows.length : "none"}>
      {rows.length} rows
    </div>
  ),
}));

import { AgentModels as AgentModelsSection } from "./agent-models";

const agent = { id: "agent-1", name: "Dev" } as unknown as Agent;

function row(position: number, runtime_name: string, model: string, state = "available", extra: Partial<ModelRowState> = {}): ModelRowState {
  return {
    position, runtime_id: `rt-${position}`, runtime_name, provider: "codex", model, thinking_level: "", custom_args: null, state,
    until: null, reason_code: null, until_estimated: false, chosen: false, effective_model: model, model_at_run: false, ...extra,
  };
}

function choice(over: Partial<ModelChoice>): ModelChoice {
  return {
    source: "agent_profile", profile_id: "p1", profile_name: "Strong", issue_id: "", excluded_family: "", reason: "primary", position: 1,
    chosen: null, runtime_name: "", provider: "", exhausted_profiles: [], wait_until: null, wait_reason_code: null, wait_until_estimated: false,
    profile_personal: false, rows: [], ...over,
  };
}

const fact = { subscription_id: "s1", subscription_key: "claude-3", account_group: "g", pool_id: "pool1", pool_revision: 12, daemon_id: "d1", confirmed_at: "2026-10-24T15:31:00Z" };

function entry(over: Partial<AgentModelChoiceEntry>): AgentModelChoiceEntry {
  return {
    task_id: "t1", status: "completed", issue_id: "i1", issue_identifier: "STT-1", issue_title: "", position: 1, choice: choice({}),
    account_fact: null, created_at: "2026-10-19T12:00:00Z", ...over,
  };
}

function models(over: Partial<AgentModels> = {}): AgentModels {
  const rows = [row(1, "codex", "sol", "limited", { until: "2026-10-19T15:53:00Z", reason_code: "agent_cap" }), row(2, "claude", "opus", "available", { chosen: true })];
  return {
    agent_id: "agent-1", can_edit: true, model_profile_id: "p1", profile_name: "Strong", profile_agents: 3, profile_issues: 1,
    mode: "profile", personal_profile_id: null, base_profile_id: null, base_profile_name: "", matches_base: null, diff_rows: [], personal_rows: [],
    own_list: false, options: [], profile_rows: rows, disallowed_tools: ["github:delete_repo"],
    preview: choice({ reason: "fallback_row", position: 2, rows }),
    last: null, journal: [], waiting: [], issues: [], runtime_unavailable: [], ...over,
  };
}

/** The agent on its own copy of the shared profile "Strong", differing in row 1. */
function personalModels(over: Partial<AgentModels> = {}): AgentModels {
  const shared = [row(1, "codex", "sol", "available"), row(2, "claude", "opus")];
  const own = [row(1, "codex", "sol", "available", { thinking_level: "xhigh", chosen: true }), row(2, "claude", "opus")];
  return models({
    mode: "personal", model_profile_id: "pp1", profile_name: "Dev", personal_profile_id: "pp1", base_profile_id: "p1",
    base_profile_name: "Strong", matches_base: false, diff_rows: [1], personal_rows: own, profile_rows: shared, own_list: true,
    options: own, preview: choice({ profile_personal: true, profile_name: "Dev", rows: own }), ...over,
  });
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
    // Three different things, each under its own label.
    const setting = screen.getByTestId("agent-models-setting");
    expect(setting.textContent).toContain("codex·sol");
    expect(setting.textContent).toContain("saved");
    expect(setting.textContent).toContain("Shared profile “Strong”, row 1");
    const now = screen.getByTestId("agent-models-forecast");
    expect(now.textContent).toContain("claude·opus");
    expect(now.textContent).toContain("forecast");
    expect(now.textContent).toContain("profile “Strong”");
    expect(screen.getByTestId("agent-models-last").textContent).toContain("No runs yet");
    // The reason is the server's code, not the text of a refusal.
    expect(now.textContent).toMatch(/row 1 unavailable \(Agent limit reached · until .*\) — took row 2/);
    expect(screen.getByText("Editing the profile affects 3 agents and 1 issues.")).toBeTruthy();
    expect(screen.getByText("github:delete_repo")).toBeTruthy();
  });

  it("states the account of the last run as the daemon confirmed it, and unknown without a confirmation", async () => {
    apiMock.getAgentModels.mockResolvedValue(models({ last: entry({ account_fact: fact }) }));
    const first = renderSection();
    const account = await screen.findByTestId("agent-models-account");
    expect(account.textContent).toContain("claude-3");
    expect(account.textContent).toContain("confirmed by the daemon");
    expect(account.textContent).toContain("pool version 12 at the time of the run");
    expect(screen.getByTestId("agent-models-last").textContent).toContain("fact");
    first.unmount();
    apiMock.getAgentModels.mockResolvedValue(models({ last: entry({ account_fact: null }) }));
    renderSection();
    expect((await screen.findByTestId("agent-models-account")).textContent).toContain("unknown");
  });

  it("says when every model is unavailable, why and until when, marking an estimate", async () => {
    const waiting = choice({ reason: "all_unavailable", position: 0, wait_until: "2026-10-19T15:53:00Z", wait_reason_code: "provider_limit", wait_until_estimated: true });
    apiMock.getAgentModels.mockResolvedValue(models({ waiting: [entry({ status: "queued", position: 0, choice: waiting })] }));
    renderSection();
    const now = await screen.findByTestId("agent-models-forecast");
    expect(now.textContent).toMatch(/every model unavailable \(Provider limit · about .*, estimate\) — the run waits/);
    expect(now.textContent).toContain("STT-1");
  });

  it("falls back to the code's own wording for an unknown reason", async () => {
    const waiting = choice({ reason: "all_unavailable", position: 0, wait_until: null, wait_reason_code: "brand_new_reason" });
    apiMock.getAgentModels.mockResolvedValue(models({ waiting: [entry({ status: "queued", position: 0, choice: waiting })] }));
    renderSection();
    const now = await screen.findByTestId("agent-models-forecast");
    expect(now.textContent).toContain("Unavailable · end unknown");
  });

  it("names the exhausted profile when the fallback profile was taken", async () => {
    const taken = choice({ reason: "fallback_profile", profile_id: "p2", profile_name: "Mid", exhausted_profiles: ["Strong"], rows: [row(1, "glm", "glm", "available", { chosen: true })] });
    apiMock.getAgentModels.mockResolvedValue(models({ preview: taken, last: entry({ choice: taken }) }));
    renderSection();
    const now = await screen.findByTestId("agent-models-forecast");
    expect(now.textContent).toContain("every model of “Strong” on a limit — took “Mid”");
    // The fallback profile's row is not marked current on the agent's own profile.
    expect(screen.queryByText("current")).toBeNull();
  });

  it("says the model is decided at launch instead of naming the first account's model", async () => {
    const rows = [row(1, "claude-api", "glm-4.6", "available", { chosen: true, effective_model: "", model_at_run: true })];
    apiMock.getAgentModels.mockResolvedValue(models({ profile_rows: rows, preview: choice({ rows }) }));
    renderSection();
    const setting = await screen.findByTestId("agent-models-setting");
    expect(setting.textContent).toContain("claude-api·Decided at launch");
    expect(setting.textContent).not.toContain("glm-4.6");
    expect(screen.getByTestId("agent-models-forecast").textContent).toContain("claude-api·Decided at launch");
  });

  it("shows the account's model, not the row's, for an API-key environment", async () => {
    const rows = [row(1, "claude-api", "sonnet", "available", { chosen: true, effective_model: "glm-4.6" })];
    apiMock.getAgentModels.mockResolvedValue(models({ profile_rows: rows, preview: choice({ rows }) }));
    renderSection();
    expect((await screen.findByTestId("agent-models-setting")).textContent).toContain("claude-api·glm-4.6");
  });

  it("lists the runtimes that cannot run now with the server's reason", async () => {
    apiMock.getAgentModels.mockResolvedValue(models({
      runtime_unavailable: [{ runtime_id: "rt-1", runtime_name: "codex", until: "2026-10-19T15:53:00Z", reason: "free text", reason_code: "login_required", until_estimated: false, computed: true, task_id: null, created_at: "" }],
    }));
    renderSection();
    const block = await screen.findByTestId("agent-models-unavailable");
    expect(block.textContent).toContain("codex");
    expect(block.textContent).toContain("Sign-in required");
    expect(block.textContent).not.toContain("free text");
  });

  it("pins a profile row as the agent's one model", async () => {
    apiMock.getAgentModels.mockResolvedValue(models());
    apiMock.setAgentSingleModel.mockResolvedValue(models({ mode: "single" }));
    renderSection();
    const pins = await screen.findAllByRole("button", { name: "Pin" });
    fireEvent.click(pins[1]!);
    await waitFor(() => expect(apiMock.setAgentSingleModel).toHaveBeenCalledWith("agent-1", {
      runtime_id: "rt-2", model: "opus", thinking_level: "", custom_args: null,
    }));
  });

  it("makes a personal copy of the shared profile on the server", async () => {
    apiMock.getAgentModels.mockResolvedValue(models());
    apiMock.createAgentPersonalProfile.mockResolvedValue(personalModels());
    renderSection();
    expect(await screen.findByRole("radio", { name: /^Shared profile/ })).toBeChecked();
    fireEvent.click(screen.getByRole("radio", { name: /^Personal profile/ }));
    fireEvent.click(await screen.findByRole("button", { name: "Make a personal copy of “Strong”" }));
    await waitFor(() => expect(apiMock.createAgentPersonalProfile).toHaveBeenCalledWith("agent-1", { from_profile_id: "p1" }));
  });

  it("opens the one-model editor with the primary row and saves it through the one-model endpoint", async () => {
    apiMock.getAgentModels.mockResolvedValue(models());
    apiMock.setAgentSingleModel.mockResolvedValue(models({ mode: "single" }));
    renderSection();
    fireEvent.click(await screen.findByRole("radio", { name: /^One model/ }));
    const editor = await screen.findByTestId("rows-editor");
    expect(editor.textContent).toBe("1 rows");
    expect(editor.getAttribute("data-base-rows")).toBe("none");
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(apiMock.setAgentSingleModel).toHaveBeenCalledWith("agent-1", expect.objectContaining({ runtime_id: "rt-1", model: "sol" })));
  });

  it("shows a personal profile with its base, the differing rows and what the shared profile has there", async () => {
    apiMock.getAgentModels.mockResolvedValue(personalModels());
    renderSection();
    const block = await screen.findByTestId("agent-models-personal");
    expect(block.textContent).toContain("Strong");
    expect(block.textContent).toContain("differs: rows 1");
    const rows = within(screen.getByTestId("personal-rows")).getAllByRole("listitem");
    expect(rows[0]!.textContent).toContain("codex·sol·xhigh");
    expect(rows[0]!.textContent).toContain("differs from shared");
    expect(rows[0]!.textContent).toContain("In shared: codex·sol");
    expect(rows[1]!.textContent).not.toContain("differs from shared");
    expect(screen.getByRole("radio", { name: /^Personal profile/ })).toBeChecked();
  });

  it("marks a personal profile that matches the shared one, without switching the agent to it", async () => {
    apiMock.getAgentModels.mockResolvedValue(models({
      personal_profile_id: "pp1", base_profile_id: "p1", base_profile_name: "Strong", matches_base: true, diff_rows: [],
      personal_rows: [row(1, "codex", "sol"), row(2, "claude", "opus")],
    }));
    apiMock.setAgentPersonalProfile.mockResolvedValue(personalModels());
    renderSection();
    const note = await screen.findByTestId("agent-models-personal-unused");
    expect(note.textContent).toContain("matches shared");
    expect(note.textContent).toContain("It is not switched automatically.");
    expect(screen.getByRole("radio", { name: /^Shared profile/ })).toBeChecked();
    fireEvent.click(within(note).getByRole("button", { name: "Use personal profile" }));
    await waitFor(() => expect(apiMock.setAgentPersonalProfile).toHaveBeenCalledWith("agent-1", {
      options: [
        { runtime_id: "rt-1", model: "sol", thinking_level: "", custom_args: null },
        { runtime_id: "rt-2", model: "opus", thinking_level: "", custom_args: null },
      ],
      from_profile_id: "p1",
    }));
  });

  it("edits the personal rows against the same shared base", async () => {
    apiMock.getAgentModels.mockResolvedValue(personalModels());
    apiMock.setAgentPersonalProfile.mockResolvedValue(personalModels());
    renderSection();
    fireEvent.click(await screen.findByRole("button", { name: "Edit rows" }));
    const editor = await screen.findByTestId("rows-editor");
    expect(editor.textContent).toBe("2 rows");
    // The editor shows each row next to what the shared profile has there.
    expect(editor.getAttribute("data-base-rows")).toBe("2");
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(apiMock.setAgentPersonalProfile).toHaveBeenCalledWith("agent-1", expect.objectContaining({ from_profile_id: "p1" })));
  });

  it("returns to the shared profile only after a confirmation", async () => {
    apiMock.getAgentModels.mockResolvedValue(personalModels());
    apiMock.resetAgentPersonalProfile.mockResolvedValue(models());
    renderSection();
    fireEvent.click(await screen.findByRole("button", { name: "Return to shared" }));
    expect(apiMock.resetAgentPersonalProfile).not.toHaveBeenCalled();
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog.textContent).toContain("Return to “Strong”?");
    fireEvent.click(within(dialog).getByRole("button", { name: "Return to shared" }));
    await waitFor(() => expect(apiMock.resetAgentPersonalProfile).toHaveBeenCalledWith("agent-1"));
  });

  it("shows one model as a single row with the one-model editor", async () => {
    const own = [row(1, "claude", "sonnet", "available", { chosen: true })];
    apiMock.getAgentModels.mockResolvedValue(personalModels({ mode: "single", personal_rows: own, options: own, diff_rows: [], matches_base: true, preview: choice({ profile_personal: true, rows: own }) }));
    renderSection();
    expect(await screen.findByRole("radio", { name: /^One model/ })).toBeChecked();
    expect(screen.getByTestId("agent-models-own").textContent).toContain("sonnet");
    expect(screen.queryByText("Models of “Strong”")).toBeNull();
  });

  it("lists the journal with the account of each run", async () => {
    const own = [row(1, "claude", "sonnet", "available", { chosen: true })];
    const picked = choice({ source: "agent_profile", profile_personal: true, profile_name: "Dev", rows: own });
    apiMock.getAgentModels.mockResolvedValue(models({
      journal: [entry({ choice: picked, issue_identifier: "STT-9", account_fact: fact }), entry({ task_id: "t2", issue_identifier: "STT-8", choice: picked, account_fact: null })],
    }));
    renderSection();
    const journal = await screen.findByTestId("agent-models-journal");
    expect(journal.textContent).toContain("STT-9");
    expect(journal.textContent).toContain("claude-3");
    expect(journal.textContent).toContain("personal profile “Dev”");
    expect(journal.textContent).toContain("unknown");
  });

  it("renders nothing against a backend without model endpoints", async () => {
    apiMock.getAgentModels.mockRejectedValue(new Error("404"));
    const { container } = renderSection();
    await waitFor(() => expect(apiMock.getAgentModels).toHaveBeenCalled());
    expect(container.textContent).toBe("");
  });
});
