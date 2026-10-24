// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import userEvent from "@testing-library/user-event";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { RuntimeProfile, Subscription, SubscriptionList } from "@multica/core/types";
import { renderWithI18n } from "../test/i18n";

const api = vi.hoisted(() => ({
  listSubscriptions: vi.fn(),
  listSubscriptionDiscrepancies: vi.fn(),
  listRuntimeProfiles: vi.fn(),
  listWorkspaces: vi.fn(),
  createSubscription: vi.fn(),
  updateSubscription: vi.fn(),
  deleteSubscription: vi.fn(),
  startSubscriptionLogin: vi.fn(),
  getSubscriptionLogin: vi.fn(),
  cancelSubscriptionLogin: vi.fn(),
  createRuntimePool: vi.fn(),
  getRuntimeProfileSubscriptions: vi.fn(),
  updateRuntimeProfileSubscriptions: vi.fn(),
  updateRuntimeProfile: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({
  api,
  ApiError: class ApiError extends Error {},
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));

import { SubscriptionsPage } from "./subscriptions-page";

function sub(key: string, extra: Partial<Subscription> = {}): Subscription {
  return {
    id: `id-${key}`, key, provider: "claude", family: "claude", title: key, owner_note: "", auth: "claude_oauth",
    credential_path: `~/.config/${key}/token`, windows: ["5h", "week"], caps: {}, reserve: 0, effective_caps: {},
    account_group: "", disabled_until: null, disabled: false, usage: [], used_by: [], hosts: [], login_state: "logged_in",
    login: null, created_at: "", updated_at: "", ...extra,
  };
}

function profile(id: string, extra: Partial<RuntimeProfile> = {}): RuntimeProfile {
  return {
    id, workspace_id: "ws-1", display_name: id, protocol_family: "claude", runtime_type: "claude", command_name: "claude",
    description: null, fixed_args: [], visibility: "workspace", created_by: null, enabled: true, created_at: "", updated_at: "",
    subscriptions: [], spend_order: "first", share_key: null, all_workspaces: false, ...extra,
  };
}

const usedBy = (position: number) => ({ workspace_id: "ws-1", runtime_profile_id: "pool-claude", display_name: "Claude · pool", position });

function list(canManage: boolean): SubscriptionList {
  return {
    can_manage: canManage,
    subscriptions: [
      sub("acc-a", {
        title: "Plan A", account_group: "acc", caps: { "5h": 70 }, reserve: 18, effective_caps: { "5h": 70, week: 82 },
        used_by: [usedBy(1), { workspace_id: "ws-2", runtime_profile_id: "other", display_name: "Claude · pool", position: 1 }],
        usage: [{ window: "5h", used_percent: 75, resets_at: null, limit_reached: false, source: "daemon", observed_at: new Date().toISOString() }],
      }),
      sub("acc-b", { title: "Plan A max", account_group: "acc", used_by: [usedBy(2)] }),
      sub("cx-a", { title: "Codex plan", provider: "codex", family: "codex", auth: "codex_auth" }),
    ],
  };
}

const pools = [
  profile("pool-claude", { display_name: "Claude · pool", subscriptions: ["acc-a", "acc-b"], share_key: "pool-1", all_workspaces: true }),
  profile("pool-codex", { display_name: "Codex · pool", runtime_type: "codex", protocol_family: "codex", subscriptions: ["cx-a"] }),
  profile("pi", { display_name: "Pi", runtime_type: "pi", protocol_family: "pi", command_name: "pi" }),
];

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={client}>
      <SubscriptionsPage />
    </QueryClientProvider>,
  );
}

describe("SubscriptionsPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    api.listSubscriptions.mockResolvedValue(list(true));
    api.listSubscriptionDiscrepancies.mockResolvedValue([]);
    api.listRuntimeProfiles.mockResolvedValue(pools);
    api.listWorkspaces.mockResolvedValue([{ id: "ws-1", name: "STT" }, { id: "ws-2", name: "Other" }]);
    api.getSubscriptionLogin.mockResolvedValue(null);
    api.updateRuntimeProfileSubscriptions.mockResolvedValue({});
  });
  afterEach(() => vi.restoreAllMocks());

  it("shows one card per account with load, agents' limits and who spends it", async () => {
    renderPage();
    const card = (await screen.findByRole("heading", { name: "Plan A · Plan A max" })).closest("article")!;
    expect(within(card as HTMLElement).getByText("5 hours")).toBeTruthy();
    expect(within(card as HTMLElement).getByText("Agents up to: 5 hours 70%, week 82% · reserve for people 18%")).toBeTruthy();
    expect(within(card as HTMLElement).getByRole("button", { name: "Claude · pool (#1)" })).toBeTruthy();
    expect(within(card as HTMLElement).getByText(/other workspaces: 1/)).toBeTruthy();
    // 75% of a 70% cap: agents stop here.
    expect(screen.getByRole("status").textContent).toContain("Plan A, Plan A max");
    expect(screen.getByRole("heading", { name: "Codex" })).toBeTruthy();
    // Pools only: a plain CLI profile is not listed.
    const poolsSection = screen.getByRole("heading", { name: "Runtimes with subscriptions" }).closest("section")!;
    expect(within(poolsSection as HTMLElement).getByText("Claude · pool")).toBeTruthy();
    expect(within(poolsSection as HTMLElement).queryByText("Pi")).toBeNull();
  });

  it("is read-only without can_manage", async () => {
    api.listSubscriptions.mockResolvedValue(list(false));
    renderPage();
    expect(await screen.findByText("Only an instance admin changes subscriptions and runtimes.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Add subscription" })).toBeNull();
    expect(screen.queryByRole("button", { name: "New runtime" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Actions for “Plan A”" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Sign in again" })).toBeNull();
  });

  it("lists discrepancies by kind", async () => {
    api.listSubscriptionDiscrepancies.mockResolvedValue([
      { kind: "credential_missing", message: "", subscription_key: "acc-a", device_name: "vps", runtime_profile_id: null, runtime_name: null, workspace_id: null, agent_id: null, agent_name: null },
      { kind: "runtime_without_subscriptions", message: "", subscription_key: null, device_name: null, runtime_profile_id: "pool-codex", runtime_name: "Codex · pool", workspace_id: "ws-1", agent_id: null, agent_name: null },
    ]);
    renderPage();
    const block = (await screen.findByRole("heading", { name: "Discrepancies" })).closest("section")!;
    expect(within(block as HTMLElement).getByText("No credential on the host")).toBeTruthy();
    expect(within(block as HTMLElement).getByText("Plan A · vps")).toBeTruthy();
    expect(within(block as HTMLElement).getByText("Runtime without subscriptions")).toBeTruthy();
    expect(within(block as HTMLElement).getByRole("button", { name: "Open runtime" })).toBeTruthy();
  });

  it("adds a Claude subscription first in an existing runtime and sends the token once", async () => {
    api.createSubscription.mockResolvedValue(sub("plan-c", { title: "Plan C" }));
    api.startSubscriptionLogin.mockResolvedValue({ id: "l1", state: "done", url: null, code: null, message: "", started_at: null });
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "Add subscription" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("2. Name"), { target: { value: "Plan C" } });
    fireEvent.change(within(dialog).getByLabelText("Token"), { target: { value: "sk-ant-oat01-secret" } });
    fireEvent.click(within(dialog).getByRole("button", { name: /Pick runtimes/ }));
    fireEvent.click(await screen.findByRole("option", { name: /Claude · pool/ }));
    expect(screen.queryByRole("option", { name: /Codex · pool/ })).toBeNull();
    await userEvent.click(within(dialog).getByRole("combobox", { name: "Place in the spending order: Claude · pool" }));
    await userEvent.click(await screen.findByRole("option", { name: "first" }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Add and sign in" }));

    await waitFor(() => expect(api.startSubscriptionLogin).toHaveBeenCalledWith("plan-c", { token: "sk-ant-oat01-secret" }));
    expect(api.createSubscription).toHaveBeenCalledWith({ provider: "claude", title: "Plan C", windows: ["5h", "week"] });
    expect(api.updateRuntimeProfileSubscriptions).toHaveBeenCalledWith("ws-1", "pool-claude", {
      subscriptions: ["plan-c", "acc-a", "acc-b"],
    });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("writes an optional model of a key subscription into its settings", async () => {
    api.createSubscription.mockResolvedValue(sub("zai-a", { title: "GLM", provider: "zai", auth: "api_key" }));
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "Add subscription" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("radio", { name: /API key/ }));
    fireEvent.change(within(dialog).getByLabelText("2. Name"), { target: { value: "GLM" } });
    fireEvent.change(within(dialog).getByLabelText(/^Model/), { target: { value: "glm-4.6" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add" }));
    await waitFor(() =>
      expect(api.createSubscription).toHaveBeenCalledWith({
        provider: "zai", title: "GLM", windows: ["5h", "week"],
        settings: { env: { ANTHROPIC_MODEL: "glm-4.6", ANTHROPIC_DEFAULT_OPUS_MODEL: "glm-4.6", ANTHROPIC_DEFAULT_SONNET_MODEL: "glm-4.6", ANTHROPIC_DEFAULT_HAIKU_MODEL: "glm-4.6" } },
      }),
    );
    expect(api.startSubscriptionLogin).not.toHaveBeenCalled();
  });

  it("adds a Codex subscription in a new runtime for all workspaces and shows the device code", async () => {
    api.createSubscription.mockResolvedValue(sub("cx-b", { title: "Codex B", provider: "codex", family: "codex", auth: "codex_auth" }));
    api.createRuntimePool.mockResolvedValue({ share_key: "pool-2", runtime_profiles: [], skipped_workspace_ids: [] });
    api.startSubscriptionLogin.mockResolvedValue({ id: "l2", state: "waiting", url: "https://auth.test/device", code: "AB-12", message: "", started_at: null });
    api.getSubscriptionLogin.mockResolvedValue({ id: "l2", state: "waiting", url: "https://auth.test/device", code: "AB-12", message: "", started_at: null });
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "Add subscription" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("radio", { name: /Codex/ }));
    fireEvent.change(within(dialog).getByLabelText("2. Name"), { target: { value: "Codex B" } });
    fireEvent.click(within(dialog).getByRole("radio", { name: "New runtime" }));
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Codex · new" } });
    expect(within(dialog).getByRole("checkbox", { name: "All workspaces, including new ones" })).toBeChecked();
    fireEvent.click(within(dialog).getByRole("button", { name: "Add and sign in" }));

    expect(await screen.findByRole("link", { name: "https://auth.test/device" })).toBeTruthy();
    expect(screen.getByText("AB-12")).toBeTruthy();
    expect(api.createRuntimePool).toHaveBeenCalledWith({
      display_name: "Codex · new", runtime_type: "codex", subscriptions: ["cx-b"], spend_order: "first", all_workspaces: true,
    });
    expect(api.startSubscriptionLogin).toHaveBeenCalledWith("cx-b", {});
  });

  it("creates a runtime from the page header in chosen workspaces", async () => {
    api.createRuntimePool.mockResolvedValue({ share_key: "pool-3", runtime_profiles: [], skipped_workspace_ids: [] });
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "New runtime" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Claude · team" } });
    fireEvent.click(within(dialog).getByRole("checkbox", { name: "All workspaces, including new ones" }));
    expect(await within(dialog).findByRole("checkbox", { name: "STT" })).toBeChecked();
    await userEvent.click(within(dialog).getByRole("combobox", { name: "Add subscription" }));
    await userEvent.click(await screen.findByRole("option", { name: /Plan A max \(acc-b\)/ }));
    fireEvent.click(within(dialog).getByRole("radio", { name: /evenly over the week/ }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Create" }));
    await waitFor(() =>
      expect(api.createRuntimePool).toHaveBeenCalledWith({
        display_name: "Claude · team", runtime_type: "claude", subscriptions: ["acc-b"], spend_order: "balanced", workspace_ids: ["ws-1"],
      }),
    );
  });

  it("saves agents' limits and reserve", async () => {
    api.updateSubscription.mockResolvedValue(sub("acc-a"));
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "Actions for “Plan A”" }));
    fireEvent.click(await screen.findByRole("menuitem", { name: "Agents' limits…" }));
    fireEvent.change(screen.getByLabelText("week, %"), { target: { value: "90" } });
    fireEvent.change(screen.getByLabelText("Reserve, %"), { target: { value: "10" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(api.updateSubscription).toHaveBeenCalledWith("acc-a", { caps: { "5h": 70, week: 90 }, reserve: 10 }),
    );
  });

  it("deletes a subscription after confirmation and leaves detaching to the server", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    api.deleteSubscription.mockResolvedValue({ deleted: true, detached_runtime_profile_ids: ["pool-claude"] });
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "Actions for “Plan A”" }));
    fireEvent.click(await screen.findByRole("menuitem", { name: "Delete subscription…" }));
    await waitFor(() => expect(api.deleteSubscription).toHaveBeenCalledWith("acc-a"));
    expect(api.updateRuntimeProfileSubscriptions).not.toHaveBeenCalled();
  });

  it("reorders a runtime from the card's spent-by link", async () => {
    api.getRuntimeProfileSubscriptions.mockResolvedValue({
      runtime_profile: pools[0], spend_order: "first", subscriptions: [sub("acc-a", { title: "Plan A" }), sub("acc-b", { title: "Plan A max" })], missing: [],
      agents: [{ id: "a1", name: "Writer" }, { id: "a2", name: "Reviewer" }],
    });
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "Claude · pool (#2)" }));
    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByText("Agents on this runtime: Writer, Reviewer")).toBeTruthy();
    fireEvent.click(await within(dialog).findByRole("button", { name: "Up: Plan A max" }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(api.updateRuntimeProfileSubscriptions).toHaveBeenCalledWith("ws-1", "pool-claude", {
        subscriptions: ["acc-b", "acc-a"], spend_order: "first",
      }),
    );
    expect(api.updateRuntimeProfile).not.toHaveBeenCalled();
  });
});
