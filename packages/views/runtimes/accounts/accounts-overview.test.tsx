// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ManualRun } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

const api = vi.hoisted(() => ({
  listSubscriptions: vi.fn(),
  listPools: vi.fn(),
  listSubscriptionDiscrepancies: vi.fn(),
  listRuntimes: vi.fn(),
  listWorkspaces: vi.fn(),
  createSubscription: vi.fn(),
  updateSubscription: vi.fn(),
  deleteSubscription: vi.fn(),
  startSubscriptionLogin: vi.fn(),
  getSubscriptionLogin: vi.fn(),
  cancelSubscriptionLogin: vi.fn(),
  createPool: vi.fn(),
  updatePool: vi.fn(),
  setPoolGrants: vi.fn(),
  listSubscriptionManualRuns: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({
  api,
  ApiError: class ApiError extends Error {
    constructor(message: string, readonly status: number, readonly statusText: string, readonly body?: unknown) {
      super(message);
    }
  },
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("../../navigation", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../navigation")>()),
  AppLink: ({ href, children, ...props }: { href: string; children?: React.ReactNode }) => <a href={href} {...props}>{children}</a>,
}));
vi.mock("@multica/core/paths", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/paths")>();
  return { ...actual, useWorkspacePaths: () => actual.paths.workspace("acme") };
});

import { NavigationProvider, type NavigationAdapter } from "../../navigation";
import { AccountsOverview } from "./accounts-overview";
import { entry, pool, poolList, sub, subscriptionList } from "./accounts.fixtures";

function renderTab(search = "tab=accounts") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const replace = vi.fn();
  const navigation: NavigationAdapter = {
    push: vi.fn(), replace, back: vi.fn(), pathname: "/acme/runtimes", searchParams: new URLSearchParams(search), hash: "",
    getShareableUrl: (path) => path,
  };
  const view = renderWithI18n(
    <NavigationProvider value={navigation}>
      <QueryClientProvider client={client}>
        <AccountsOverview mode="manage" />
      </QueryClientProvider>
    </NavigationProvider>,
  );
  return { ...view, replace };
}

function manualRun(id: string, extra: Partial<ManualRun> = {}): ManualRun {
  return {
    id, subscription_key: "acc-a", user_id: "u1", user_name: "Evgeniy", daemon_id: "vps-1", command: "claude",
    started_at: "2026-10-25T10:00:00Z", finished_at: "2026-10-25T10:05:00Z", exit_code: 0, unfinished: false,
    agent_cap_exceeded: false, ...extra,
  };
}

describe("Environments → Accounts", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    api.listSubscriptions.mockResolvedValue(subscriptionList(true));
    api.listPools.mockResolvedValue(poolList());
    api.listSubscriptionDiscrepancies.mockResolvedValue([]);
    api.listRuntimes.mockResolvedValue([]);
    api.listWorkspaces.mockResolvedValue([{ id: "ws-1", name: "STT" }, { id: "ws-2", name: "Other" }]);
    api.getSubscriptionLogin.mockResolvedValue(null);
    api.listSubscriptionManualRuns.mockResolvedValue({ key: "acc-a", runs: [], next_before: null });
  });
  afterEach(() => vi.restoreAllMocks());

  it("shows the quota with the time it was measured and the sign-in action to an admin", async () => {
    renderTab("account=acc-a");
    const detail = await screen.findByTestId("account-detail-acc-a");
    expect(within(detail).getByTestId("usage-measured-at")).toBeTruthy();
    expect(within(detail).getByText("Pools:").parentElement!.textContent).toContain("claude (#1)");
    expect(screen.getByRole("button", { name: "Actions for “Plan A”" })).toBeTruthy();
  });

  it("shows the journal of manual runs with the command for a person, marking a run that never ended", async () => {
    api.listSubscriptionManualRuns.mockResolvedValue({
      key: "acc-a",
      runs: [manualRun("r1", { exit_code: 3 }), manualRun("r2", { finished_at: null, exit_code: null, unfinished: true, agent_cap_exceeded: true })],
      next_before: null,
    });
    renderTab("account=acc-a");
    const journal = await screen.findByTestId("manual-runs");
    expect(within(journal).getByTestId("manual-run-command").textContent).toContain("multica subscription run acc-a");
    const first = await within(journal).findByTestId("manual-run-r1");
    expect(first.textContent).toContain("Evgeniy");
    expect(first.textContent).toContain("vps-1");
    expect(first.textContent).toContain("exit code 3");
    const second = within(journal).getByTestId("manual-run-r2");
    expect(second.textContent).toContain("not finished");
    expect(second.textContent).toContain("agent limit was exceeded");
    expect(api.listSubscriptionManualRuns).toHaveBeenCalledWith("acc-a", { limit: 20, before: undefined });
  });

  it("pages the journal from the cursor the server gave", async () => {
    api.listSubscriptionManualRuns
      .mockResolvedValueOnce({ key: "acc-a", runs: [manualRun("r1")], next_before: "2026-10-25T10:00:00Z" })
      .mockResolvedValueOnce({ key: "acc-a", runs: [manualRun("r0")], next_before: null });
    renderTab("account=acc-a");
    fireEvent.click(await screen.findByRole("button", { name: "Show more" }));
    await screen.findByTestId("manual-run-r0");
    expect(api.listSubscriptionManualRuns).toHaveBeenLastCalledWith("acc-a", { limit: 20, before: "2026-10-25T10:00:00Z" });
    expect(screen.queryByRole("button", { name: "Show more" })).toBeNull();
  });

  it("says the journal is unavailable when the server refuses it, without breaking the card", async () => {
    api.listSubscriptionManualRuns.mockRejectedValue(new Error("forbidden"));
    renderTab("account=acc-a");
    const journal = await screen.findByTestId("manual-runs");
    expect((await within(journal).findByRole("alert")).textContent).toContain("only an instance admin reads it");
    expect(screen.getByTestId("agent-limit-summary")).toBeTruthy();
  });

  it("neither reads nor shows the journal to a reader who is not an instance admin", async () => {
    api.listSubscriptions.mockResolvedValue(subscriptionList(false));
    api.listPools.mockResolvedValue(poolList(false));
    renderTab("account=acc-a");
    await screen.findByTestId("account-detail-acc-a");
    expect(screen.queryByTestId("manual-runs")).toBeNull();
    expect(screen.queryByRole("button", { name: "Add subscription" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Actions for “Plan A”" })).toBeNull();
    expect(api.listSubscriptionManualRuns).not.toHaveBeenCalled();
  });

  it("saves one limit per window and refuses a value outside 0…100", async () => {
    api.updateSubscription.mockResolvedValue(sub("acc-a"));
    renderTab("account=acc-a");
    fireEvent.click(await screen.findByRole("button", { name: "Actions for “Plan A”" }));
    fireEvent.click(await screen.findByRole("menuitem", { name: "Agents' limits…" }));
    const week = screen.getByLabelText("Agents' limit, week, %");
    fireEvent.change(week, { target: { value: "101" } });
    expect(screen.getByText("Enter a whole number from 0 to 100")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(api.updateSubscription).not.toHaveBeenCalled();
    fireEvent.change(week, { target: { value: "80" } });
    expect(screen.getByText("People keep 20%")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.updateSubscription).toHaveBeenCalledWith("acc-a", { caps: { "5h": 70, week: 80 } }));
  });

  it("accepts 0 and 100 as limits", async () => {
    api.updateSubscription.mockResolvedValue(sub("acc-a"));
    renderTab("account=acc-a");
    fireEvent.click(await screen.findByRole("button", { name: "Actions for “Plan A”" }));
    fireEvent.click(await screen.findByRole("menuitem", { name: "Agents' limits…" }));
    fireEvent.change(screen.getByLabelText("Agents' limit, 5 hours, %"), { target: { value: "0" } });
    fireEvent.change(screen.getByLabelText("Agents' limit, week, %"), { target: { value: "100" } });
    expect(screen.getByText("People keep 100%")).toBeTruthy();
    expect(screen.getByText("People keep 0%")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.updateSubscription).toHaveBeenCalledWith("acc-a", { caps: { "5h": 0, week: 100 } }));
  });

  it("refetches the verdicts after a limit change instead of recomputing them", async () => {
    api.updateSubscription.mockResolvedValue(sub("acc-a"));
    renderTab("account=acc-a");
    await screen.findByTestId("account-row-acc-a");
    const before = api.listPools.mock.calls.length;
    api.listPools.mockResolvedValue({
      can_manage: true,
      pools: [pool("claude", [entry("acc-a", 1), entry("acc-b", 2)])],
    });
    fireEvent.click(await screen.findByRole("button", { name: "Actions for “Plan A”" }));
    fireEvent.click(await screen.findByRole("menuitem", { name: "Agents' limits…" }));
    fireEvent.change(screen.getByLabelText("Agents' limit, 5 hours, %"), { target: { value: "80" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.listPools.mock.calls.length).toBeGreaterThan(before));
    await waitFor(() => expect(screen.getByTestId("account-row-acc-a").textContent).not.toContain("Agent limit reached"));
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("deletes an account after confirmation and leaves detaching to the server", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    api.deleteSubscription.mockResolvedValue({ deleted: true, detached_runtime_profile_ids: ["env-claude"] });
    renderTab("account=acc-a");
    fireEvent.click(await screen.findByRole("button", { name: "Actions for “Plan A”" }));
    fireEvent.click(await screen.findByRole("menuitem", { name: "Delete subscription…" }));
    await waitFor(() => expect(api.deleteSubscription).toHaveBeenCalledWith("acc-a"));
    expect(api.updatePool).not.toHaveBeenCalled();
  });
});

describe("Connect an account", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    api.listSubscriptions.mockResolvedValue(subscriptionList(true));
    api.listPools.mockResolvedValue(poolList());
    api.listSubscriptionDiscrepancies.mockResolvedValue([]);
    api.listRuntimes.mockResolvedValue([]);
    api.listSubscriptionManualRuns.mockResolvedValue({ key: "x", runs: [], next_before: null });
    api.listWorkspaces.mockResolvedValue([{ id: "ws-1", name: "STT" }, { id: "ws-2", name: "Other" }]);
    api.getSubscriptionLogin.mockResolvedValue(null);
    api.createSubscription.mockResolvedValue(sub("plan-c", { title: "Plan C" }));
    api.startSubscriptionLogin.mockResolvedValue({ id: "l1", state: "done", url: null, code: null, message: "", started_at: null });
  });

  async function openWizard() {
    const view = renderTab("");
    fireEvent.click(await screen.findByRole("button", { name: "Add subscription" }));
    const dialog = await screen.findByRole("dialog");
    return { ...view, dialog };
  }

  it("connects an account without touching any pool", async () => {
    const { dialog, replace } = await openWizard();
    expect(within(dialog).queryByRole("radio", { name: "New pool" })).toBeNull();
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Plan C" } });
    fireEvent.change(within(dialog).getByLabelText("Agents' limit, 5 hours, %"), { target: { value: "70" } });
    expect(within(dialog).getByText("People keep 30%")).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("button", { name: "Connect" }));
    await waitFor(() => expect(api.createSubscription).toHaveBeenCalledWith({ provider: "claude", title: "Plan C", windows: ["5h", "week"], caps: { "5h": 70 } }));
    expect(api.updatePool).not.toHaveBeenCalled();
    expect(api.createPool).not.toHaveBeenCalled();
    expect(api.startSubscriptionLogin).not.toHaveBeenCalled();
    await waitFor(() => expect(replace).toHaveBeenCalledWith("/acme/runtimes?account=plan-c"));
  });

  it("signs in once with the pasted token", async () => {
    const { dialog } = await openWizard();
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Plan C" } });
    fireEvent.change(within(dialog).getByLabelText("Token"), { target: { value: "sk-ant-oat01-secret" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Connect and sign in" }));
    await waitFor(() => expect(api.startSubscriptionLogin).toHaveBeenCalledWith("plan-c", { token: "sk-ant-oat01-secret" }));
    expect(api.createSubscription).toHaveBeenCalledTimes(1);
    expect(api.updatePool).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("does not create a second account when starting the sign-in fails and is retried", async () => {
    api.startSubscriptionLogin.mockRejectedValueOnce(new Error("daemon offline"));
    const { dialog } = await openWizard();
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Plan C" } });
    fireEvent.change(within(dialog).getByLabelText("Token"), { target: { value: "sk-ant-oat01-secret" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Connect and sign in" }));
    await waitFor(() => expect(api.startSubscriptionLogin).toHaveBeenCalledTimes(1));
    expect(screen.getByRole("dialog")).toBeTruthy();

    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Connect and sign in" }));
    await waitFor(() => expect(api.startSubscriptionLogin).toHaveBeenCalledTimes(2));
    expect(api.startSubscriptionLogin).toHaveBeenLastCalledWith("plan-c", { token: "sk-ant-oat01-secret" });
    expect(api.createSubscription).toHaveBeenCalledTimes(1);
  });

  it("writes an optional model of a key subscription into its settings", async () => {
    api.createSubscription.mockResolvedValue(sub("zai-a", { title: "GLM", provider: "zai", auth: "api_key" }));
    const { dialog } = await openWizard();
    fireEvent.click(within(dialog).getByRole("radio", { name: /API key/ }));
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "GLM" } });
    fireEvent.change(within(dialog).getByLabelText(/^Model/), { target: { value: "glm-4.6" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Connect" }));
    await waitFor(() =>
      expect(api.createSubscription).toHaveBeenCalledWith({
        provider: "zai", title: "GLM", windows: ["5h", "week"],
        settings: { env: { ANTHROPIC_MODEL: "glm-4.6", ANTHROPIC_DEFAULT_OPUS_MODEL: "glm-4.6", ANTHROPIC_DEFAULT_SONNET_MODEL: "glm-4.6", ANTHROPIC_DEFAULT_HAIKU_MODEL: "glm-4.6" } },
      }),
    );
    expect(api.startSubscriptionLogin).not.toHaveBeenCalled();
  });

  it("shows the device code of a Codex account after connecting", async () => {
    api.createSubscription.mockResolvedValue(sub("cx-b", { title: "Codex B", provider: "codex", family: "codex", auth: "codex_auth" }));
    api.startSubscriptionLogin.mockResolvedValue({ id: "l2", state: "waiting", url: "https://auth.test/device", code: "AB-12", message: "", started_at: null });
    api.getSubscriptionLogin.mockResolvedValue({ id: "l2", state: "waiting", url: "https://auth.test/device", code: "AB-12", message: "", started_at: null });
    const { dialog } = await openWizard();
    fireEvent.click(within(dialog).getByRole("radio", { name: /Codex/ }));
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Codex B" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Connect and sign in" }));
    expect(await screen.findByRole("link", { name: "https://auth.test/device" })).toBeTruthy();
    expect(screen.getByText("AB-12")).toBeTruthy();
    expect(api.startSubscriptionLogin).toHaveBeenCalledWith("cx-b", {});
  });
});
