// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { AgentRuntime, RuntimeProfile } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

const api = vi.hoisted(() => ({
  getRuntimeModelUsage: vi.fn(),
  getRuntimeRunFacts: vi.fn(),
  listSubscriptions: vi.fn(),
}));
vi.mock("@multica/core/api", () => ({ api, ApiError: class ApiError extends Error {} }));
vi.mock("@multica/core/paths", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/paths")>();
  return { ...actual, useWorkspacePaths: () => actual.paths.workspace("acme") };
});
vi.mock("../../navigation", () => ({
  AppLink: ({ href, children, ...props }: { href: string; children?: React.ReactNode }) => <a href={href} {...props}>{children}</a>,
}));

import { RuntimeModelFacts } from "./runtime-model-facts";

const runtime = { id: "rt-1", profile_id: "env-1", status: "online", provider: "claude" } as AgentRuntime;
const viaAccounts = { id: "env-1", pool_id: "pool-1" } as RuntimeProfile;
const ownLogin = { id: "env-1", pool_id: null } as RuntimeProfile;

function usage(over = {}) {
  return {
    selected_in: [{ profile_id: "p1", name: "Strong", personal: false, owner_agent_id: null, positions: [1, 3] }],
    fallback_profiles: [{ profile_id: "p2", name: "Mid", personal: false, owner_agent_id: null, positions: [2] }],
    based_on: [], serving_agents: [{ id: "a1", name: "Dev", profile_ids: ["p1"] }],
    hidden_agents: 2, hidden_profiles: 1, open_issues: 4, deletable: false, ...over,
  };
}

function facts(over = {}) {
  return {
    runtime_id: "rt-1", selection_state: "active", machine_status: "offline",
    run_facts_24h: { since: "", until: "", runs: 9, unknown_account_runs: 2, accounts: { "id-acc-a": 7 } }, ...over,
  };
}

function renderFacts(profile: RuntimeProfile | null) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={client}>
      <RuntimeModelFacts runtime={runtime} profile={profile} />
    </QueryClientProvider>,
  );
}

describe("RuntimeModelFacts", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    api.getRuntimeModelUsage.mockResolvedValue(usage());
    api.getRuntimeRunFacts.mockResolvedValue(facts());
    api.listSubscriptions.mockResolvedValue({ can_manage: false, subscriptions: [{ id: "id-acc-a", key: "acc-a", title: "Plan A" }] });
  });

  it("keeps the selection state, the machine and the runs apart, and names the accounts with the unconfirmed ones counted separately", async () => {
    renderFacts(viaAccounts);
    const selection = await screen.findByText("Used in the last 24 h");
    expect(selection.closest("[data-testid=runtime-facts-selection]")!.textContent).toContain("Machine: offline");
    const runs = screen.getByTestId("runtime-facts-runs");
    expect(runs.textContent).toContain("Runs: 9");
    expect(runs.textContent).toContain("Without a confirmed account: 2");
    expect(await within(runs).findByText("Plan A")).toBeTruthy();
    expect(screen.getByTestId("runtime-facts-kind").textContent).toContain("Through accounts");
  });

  it("says where the environment is selected without naming what the reader cannot see", async () => {
    renderFacts(viaAccounts);
    const selected = await screen.findByTestId("runtime-facts-selected");
    await within(selected).findByText("Strong");
    expect(selected.textContent).toContain("rows: 1, 3");
    expect(selected.textContent).toContain("Mid");
    expect(selected.textContent).toContain("Profiles you cannot see: 1");
    const agents = screen.getByTestId("runtime-facts-agents");
    expect(within(agents).getByRole("link", { name: "Dev" }).getAttribute("href")).toBe("/acme/agents/a1");
    expect(agents.textContent).toContain("Agents you cannot see: 2");
    expect(agents.textContent).toContain("Open issues: 4");
    expect(screen.getByTestId("runtime-facts-removal").textContent).toContain("Cannot be deleted");
  });

  it("does not guess an account for an environment with its own login", async () => {
    renderFacts(ownLogin);
    const runs = await screen.findByTestId("runtime-facts-runs");
    await within(runs).findByText(/Runs: 9/);
    expect(runs.textContent).toContain("the account is unknown");
    expect(runs.textContent).not.toContain("Plan A");
    expect(screen.getByTestId("runtime-facts-kind").textContent).toContain("Own login");
  });

  it("reads an unknown selection state as not used", async () => {
    api.getRuntimeRunFacts.mockResolvedValue(facts({ selection_state: "archived" }));
    renderFacts(viaAccounts);
    expect(await screen.findByText("Not used")).toBeTruthy();
  });

  it("shows a failed load where it happened and keeps the rest", async () => {
    api.getRuntimeRunFacts.mockRejectedValue(new Error("boom"));
    renderFacts(viaAccounts);
    expect((await screen.findAllByRole("alert")).length).toBeGreaterThan(0);
    expect(await screen.findByText("Strong")).toBeTruthy();
  });
});
