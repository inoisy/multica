// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import userEvent from "@testing-library/user-event";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Pool, PoolAccount, RuntimeProfile, Subscription, SubscriptionList } from "@multica/core/types";
import { renderWithI18n } from "../test/i18n";

const api = vi.hoisted(() => ({
  listSubscriptions: vi.fn(),
  listPools: vi.fn(),
  listSubscriptionDiscrepancies: vi.fn(),
  listRuntimeProfiles: vi.fn(),
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
vi.mock("../navigation", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../navigation")>()),
  AppLink: ({ href, children, ...props }: { href: string; children?: React.ReactNode }) => <a href={href} {...props}>{children}</a>,
}));
vi.mock("@multica/core/paths", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/paths")>();
  return { ...actual, useWorkspacePaths: () => actual.paths.workspace("acme") };
});

import { ApiError } from "@multica/core/api";
import { NavigationProvider, type NavigationAdapter } from "../navigation";
import { SubscriptionsContent, SubscriptionsPage } from "./subscriptions-page";

function sub(key: string, extra: Partial<Subscription> = {}): Subscription {
  return {
    id: `id-${key}`, key, provider: "claude", family: "claude", title: key, owner_note: "", auth: "claude_oauth",
    credential_path: `~/.config/${key}/token`, windows: ["5h", "week"], caps: {}, effective_caps: {},
    account_group: "", disabled_until: null, disabled: false, usage: [], used_by: [], hosts: [], login_state: "logged_in",
    login: null, created_at: "", updated_at: "", ...extra,
  };
}

function entry(key: string, position: number, extra: Partial<PoolAccount> = {}): PoolAccount {
  return {
    id: `id-${key}`, key, title: key, provider: "claude", account_group: "", position, blocked: false, usable: true,
    reason_code: null, until: null, until_estimated: false, ...extra,
  };
}

function pool(id: string, accounts: PoolAccount[], extra: Partial<Pool> = {}): Pool {
  return {
    id, name: id, runtime_type: "claude", description: "", spend_order: "first", all_workspaces: false, revision: 12,
    subscriptions: accounts, workspace_ids: ["ws-1"],
    environments: [{ runtime_profile_id: `env-${id}`, workspace_id: "ws-1", display_name: `${id} env`, enabled: true }],
    availability: { blocked: false, usable: true, reason_code: null, until: null, until_estimated: false },
    created_at: "", updated_at: "", ...extra,
  };
}

const usedBy = (position: number) => ({ workspace_id: "ws-1", runtime_profile_id: "env-claude", display_name: "Claude env", position });
const limitedVerdict = { blocked: true, usable: false, reason_code: "agent_cap" as const, until: "2026-10-24T17:40:00Z", until_estimated: false };

function subscriptionList(canManage: boolean): SubscriptionList {
  return {
    can_manage: canManage,
    subscriptions: [
      sub("acc-a", {
        title: "Plan A", caps: { "5h": 70 }, effective_caps: { "5h": 70 }, used_by: [usedBy(1)],
        usage: [{ window: "5h", used_percent: 75, resets_at: null, limit_reached: false, source: "daemon", observed_at: new Date().toISOString() }],
      }),
      sub("acc-b", { title: "Plan B", used_by: [usedBy(2)] }),
      sub("cx-a", { title: "Codex plan", provider: "codex", family: "codex", auth: "codex_auth" }),
      sub("loose", { title: "Loose key", auth: "api_key", provider: "zai" }),
    ],
  };
}

function poolList(canManage = true) {
  return {
    can_manage: canManage,
    pools: [
      pool("claude", [entry("acc-a", 1, limitedVerdict), entry("acc-b", 2)], { all_workspaces: true }),
      // acc-b is in a second pool as well: the same account, counted once.
      pool("video", [entry("acc-b", 1)], { revision: 3 }),
      pool("codex", [entry("cx-a", 1)], { runtime_type: "codex" }),
    ],
  };
}

function profile(id: string, extra: Partial<RuntimeProfile> = {}): RuntimeProfile {
  return {
    id, workspace_id: "ws-1", display_name: id, protocol_family: "claude", runtime_type: "claude", command_name: "claude",
    description: null, fixed_args: [], visibility: "workspace", created_by: null, enabled: true, created_at: "", updated_at: "",
    subscriptions: [], spend_order: "first", share_key: null, all_workspaces: false, pool_id: null, ...extra,
  };
}

function renderPage(search = "", locale: "en" | "ru" = "en") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const replace = vi.fn();
  const navigation: NavigationAdapter = {
    push: vi.fn(), replace, back: vi.fn(), pathname: "/acme/usage", searchParams: new URLSearchParams(search), hash: "",
    getShareableUrl: (path) => path,
  };
  const view = renderWithI18n(
    <NavigationProvider value={navigation}>
      <QueryClientProvider client={client}>
        <SubscriptionsContent />
      </QueryClientProvider>
    </NavigationProvider>,
    { locale },
  );
  return { ...view, replace };
}

describe("SubscriptionsPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    api.listSubscriptions.mockResolvedValue(subscriptionList(true));
    api.listPools.mockResolvedValue(poolList());
    api.listSubscriptionDiscrepancies.mockResolvedValue([]);
    api.listRuntimeProfiles.mockResolvedValue([profile("env-claude", { pool_id: "claude" })]);
    api.listWorkspaces.mockResolvedValue([{ id: "ws-1", name: "STT" }, { id: "ws-2", name: "Other" }]);
    api.getSubscriptionLogin.mockResolvedValue(null);
  });
  afterEach(() => vi.restoreAllMocks());

  it("redirects the old subscriptions page to the Analytics tab", async () => {
    const replace = vi.fn();
    renderWithI18n(
      <NavigationProvider value={{
        pathname: "/acme/subscriptions", searchParams: new URLSearchParams(), hash: "",
        push: vi.fn(), replace, back: vi.fn(), getShareableUrl: (path) => path,
      }}>
        <SubscriptionsPage />
      </NavigationProvider>,
    );
    await waitFor(() => expect(replace).toHaveBeenCalledWith("/acme/usage?tab=subscriptions"));
    expect(api.listSubscriptions).not.toHaveBeenCalled();
  });

  it("groups accounts by pool, counts a shared account once and keeps accounts without a pool visible", async () => {
    renderPage();
    const claude = await screen.findByTestId("pool-group-claude");
    expect(within(claude).getByText("Plan A")).toBeTruthy();
    expect(within(claude).getByText("Plan B")).toBeTruthy();
    // The same account in the second pool is a reference, not another account.
    const video = screen.getByTestId("pool-group-video");
    expect(within(video).getByText("same account")).toBeTruthy();
    // No pool: its own group, and counted in the totals.
    const none = screen.getByTestId("pool-group-none");
    expect(within(none).getByText("Loose key")).toBeTruthy();
    const summary = screen.getByTestId("subscriptions-summary");
    // 4 accounts although acc-b appears in two pools.
    expect(within(summary).getByText("Accounts").parentElement!.textContent).toContain("4");
    expect(within(summary).getByText("In no pool").parentElement!.textContent).toContain("1");
    expect(within(summary).getByText("Available to agents").parentElement!.textContent).toContain("2");
  });

  it("shows the server's reason and the time an account frees up, from its code", async () => {
    renderPage();
    const row = await screen.findByTestId("account-row-acc-a");
    expect(row.textContent).toMatch(/Agent limit reached · until /);
    expect(row.textContent).toMatch(/available from /);
    expect(within(row).getByRole("img", { name: /Agent limit reached/ })).toBeTruthy();
    expect(screen.getByRole("status").textContent).toContain("Plan A");
  });

  it("does not present an estimate as a reported reset", async () => {
    api.listPools.mockResolvedValue({
      can_manage: true,
      pools: [pool("claude", [entry("acc-a", 1, { ...limitedVerdict, reason_code: "provider_limit", until_estimated: true }), entry("acc-b", 2)])],
    });
    renderPage();
    const row = await screen.findByTestId("account-row-acc-a");
    expect(row.textContent).toMatch(/Provider limit · about .*, estimate/);
    expect(row.textContent).toMatch(/available about .* \(estimate\)/);
  });

  it("says the time is unknown when the server has none", async () => {
    api.listPools.mockResolvedValue({
      can_manage: true,
      pools: [pool("claude", [entry("acc-a", 1, { blocked: true, usable: false, reason_code: "login_required", until: null }), entry("acc-b", 2)])],
    });
    renderPage();
    const row = await screen.findByTestId("account-row-acc-a");
    expect(row.textContent).toContain("Sign-in required · end unknown");
    expect(row.textContent).toContain("availability time unknown");
  });

  it("is read-only without can_manage", async () => {
    api.listSubscriptions.mockResolvedValue(subscriptionList(false));
    api.listPools.mockResolvedValue(poolList(false));
    renderPage("account=acc-a");
    expect(await screen.findByText("Only an instance admin changes accounts and pools.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Add subscription" })).toBeNull();
    expect(screen.queryByRole("button", { name: "New pool" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Actions for “Plan A”" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Sign in again" })).toBeNull();
  });

  it("lists discrepancies by kind and opens the pool of an environment", async () => {
    api.listSubscriptionDiscrepancies.mockResolvedValue([
      { kind: "credential_missing", message: "учётка на хосте /private/token", subscription_key: "acc-a", device_name: "vps", runtime_profile_id: null, runtime_name: null, workspace_id: null, agent_id: null, agent_name: null },
      { kind: "runtime_without_subscriptions", message: "", subscription_key: null, device_name: null, runtime_profile_id: "env-claude", runtime_name: "Claude env", workspace_id: "ws-1", agent_id: null, agent_name: null },
    ]);
    const { replace } = renderPage();
    const block = (await screen.findByRole("heading", { name: "Discrepancies" })).closest("section")!;
    expect(within(block as HTMLElement).getByText("No credential on the host")).toBeTruthy();
    expect(within(block as HTMLElement).getByText("Plan A · vps")).toBeTruthy();
    expect(block.textContent).not.toContain("учётка");
    expect(block.textContent).not.toContain("/private/token");
    fireEvent.click(within(block as HTMLElement).getByRole("button", { name: "Open runtime" }));
    expect(replace).toHaveBeenCalledWith("/acme/usage?pool=claude");
  });

  it.each(["en", "ru"] as const)("uses localized workspace plural forms in %s", async (locale) => {
    api.listPools.mockResolvedValue({ pools: [pool("one", [entry("acc-a", 1)]), pool("two", [entry("acc-b", 1)], { workspace_ids: ["ws-1", "ws-2"] })], can_manage: true });
    renderPage("", locale);
    expect(await screen.findByText(locale === "en" ? / · 1 workspace$/ : / · 1 пространство$/)).toBeTruthy();
    expect(screen.getByText(locale === "en" ? / · 2 workspaces$/ : / · 2 пространства$/)).toBeTruthy();
  });

  it("filters accounts and keeps the filter in the address", async () => {
    const { replace } = renderPage();
    await screen.findByTestId("pool-group-claude");
    fireEvent.click(screen.getByRole("button", { name: "Unavailable" }));
    expect(replace).toHaveBeenLastCalledWith("/acme/usage?status=unavailable");
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "codex" } });
    expect(replace).toHaveBeenLastCalledWith("/acme/usage?q=codex");
  });

  it("applies the filters from the address and resets them all at once", async () => {
    const { replace } = renderPage("status=available&family=codex&q=plan&tab=subscriptions");
    await screen.findByText(/Found \d+ of 4/);
    expect(screen.getByRole("button", { name: "Codex" })).toHaveAttribute("aria-pressed", "true");
    fireEvent.click(screen.getByRole("button", { name: "Reset" }));
    expect(replace).toHaveBeenLastCalledWith("/acme/usage?tab=subscriptions");
  });

  it("ignores an unknown filter value in the address", async () => {
    renderPage("status=nonsense&family=gemini&range=5");
    await screen.findByTestId("pool-group-claude");
    expect(within(screen.getByRole("group", { name: "State" })).getByRole("button", { name: "All" })).toHaveAttribute("aria-pressed", "true");
    expect(within(screen.getByRole("group", { name: "CLI" })).getByRole("button", { name: "All" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "2 days" })).toHaveAttribute("aria-pressed", "true");
  });

  it("shows the agents' limit with what is left for people", async () => {
    renderPage("account=acc-a");
    const detail = await screen.findByTestId("account-detail-acc-a");
    expect(within(detail).getByTestId("agent-limit-summary").textContent).toBe("Agents up to: 5 hours 70% (people 30%)");
    expect(within(detail).getByText("Pools:").parentElement!.textContent).toContain("claude (#1)");
  });

  it("saves one limit per window and refuses a value outside 0…100", async () => {
    api.updateSubscription.mockResolvedValue(sub("acc-a"));
    renderPage("account=acc-a");
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
    renderPage("account=acc-a");
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
    renderPage("account=acc-a");
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
    renderPage("account=acc-a");
    fireEvent.click(await screen.findByRole("button", { name: "Actions for “Plan A”" }));
    fireEvent.click(await screen.findByRole("menuitem", { name: "Delete subscription…" }));
    await waitFor(() => expect(api.deleteSubscription).toHaveBeenCalledWith("acc-a"));
    expect(api.updatePool).not.toHaveBeenCalled();
  });

  it("reorders a pool with the revision it was read at and sends nothing else", async () => {
    api.updatePool.mockResolvedValue(pool("claude", [entry("acc-b", 1), entry("acc-a", 2)], { revision: 13 }));
    renderPage("pool=claude");
    const dialog = await screen.findByRole("dialog");
    const editor = await within(dialog).findByTestId("pool-editor");
    expect(editor.getAttribute("data-revision")).toBe("12");
    fireEvent.click(within(dialog).getByRole("button", { name: "Up: acc-b" }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.updatePool).toHaveBeenCalledWith("claude", { revision: 12, subscriptions: ["acc-b", "acc-a"] }));
    expect(api.setPoolGrants).not.toHaveBeenCalled();
  });

  it("changes the spend order of a pool", async () => {
    api.updatePool.mockResolvedValue(pool("claude", [], { revision: 13 }));
    renderPage("pool=claude");
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(await within(dialog).findByRole("radio", { name: /evenly over the week/ }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.updatePool).toHaveBeenCalledWith("claude", { revision: 12, spend_order: "balanced" }));
  });

  it("explains a refused edit of a pool that changed meanwhile and takes the current pool on request", async () => {
    api.updatePool.mockRejectedValue(new ApiError("pool changed", 409, "Conflict", { code: "pool_revision_conflict", current_revision: 14 }));
    renderPage("pool=claude");
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(await within(dialog).findByRole("button", { name: "Up: acc-b" }));
    api.listPools.mockResolvedValue({ can_manage: true, pools: [pool("claude", [entry("acc-b", 1), entry("acc-a", 2)], { revision: 14 }), pool("video", [entry("acc-b", 1)]), pool("codex", [entry("cx-a", 1)], { runtime_type: "codex" })] });
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.updatePool).toHaveBeenCalledWith("claude", expect.objectContaining({ revision: 12 })));
    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toContain("version 14");
    fireEvent.click(within(alert).getByRole("button", { name: "Take the current pool" }));
    expect(within(dialog).getByTestId("pool-editor").getAttribute("data-revision")).toBe("14");
    expect(within(dialog).queryByRole("alert")).toBeNull();
  });

  it("grants a pool to chosen workspaces with the new revision of the same save", async () => {
    api.updatePool.mockResolvedValue(pool("video", [entry("acc-b", 1)], { revision: 4 }));
    api.setPoolGrants.mockResolvedValue(pool("video", [entry("acc-b", 1)], { revision: 4, workspace_ids: ["ws-1", "ws-2"] }));
    renderPage("pool=video");
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(await within(dialog).findByRole("checkbox", { name: "Other" }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.setPoolGrants).toHaveBeenCalledWith("video", { revision: 3, workspace_ids: ["ws-1", "ws-2"] }));
  });
});

describe("Connect an account", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    api.listSubscriptions.mockResolvedValue(subscriptionList(true));
    api.listPools.mockResolvedValue(poolList());
    api.listSubscriptionDiscrepancies.mockResolvedValue([]);
    api.listRuntimeProfiles.mockResolvedValue([]);
    api.listWorkspaces.mockResolvedValue([{ id: "ws-1", name: "STT" }, { id: "ws-2", name: "Other" }]);
    api.getSubscriptionLogin.mockResolvedValue(null);
    api.createSubscription.mockResolvedValue(sub("plan-c", { title: "Plan C" }));
    api.startSubscriptionLogin.mockResolvedValue({ id: "l1", state: "done", url: null, code: null, message: "", started_at: null });
  });

  async function openWizard() {
    const view = renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "Add subscription" }));
    const dialog = await screen.findByRole("dialog");
    return { ...view, dialog };
  }

  it("adds the account to an existing pool of the same CLI at the chosen place and signs in once", async () => {
    api.updatePool.mockResolvedValue(pool("claude", [], { revision: 13 }));
    const { dialog } = await openWizard();
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Plan C" } });
    fireEvent.change(within(dialog).getByLabelText("Token"), { target: { value: "sk-ant-oat01-secret" } });
    fireEvent.change(within(dialog).getByLabelText("Agents' limit, 5 hours, %"), { target: { value: "70" } });
    expect(within(dialog).getByText("People keep 30%")).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("button", { name: "Next" }));
    // Pools of the other CLI are not offered.
    await userEvent.click(within(dialog).getByRole("combobox", { name: "Pool" }));
    expect(screen.queryByRole("option", { name: "codex" })).toBeNull();
    await userEvent.click(await screen.findByRole("option", { name: "claude" }));
    await userEvent.click(within(dialog).getByRole("combobox", { name: "Place in the order" }));
    await userEvent.click(await screen.findByRole("option", { name: "first" }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Next" }));
    expect(within(dialog).getByTestId("connect-usage").textContent).toContain("claude env");
    fireEvent.click(within(dialog).getByRole("button", { name: "Connect and sign in" }));

    await waitFor(() => expect(api.startSubscriptionLogin).toHaveBeenCalledWith("plan-c", { token: "sk-ant-oat01-secret" }));
    expect(api.createSubscription).toHaveBeenCalledWith({ provider: "claude", title: "Plan C", windows: ["5h", "week"], caps: { "5h": 70 } });
    expect(api.updatePool).toHaveBeenCalledWith("claude", { revision: 12, subscriptions: ["plan-c", "acc-a", "acc-b"] });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("can stop after the account and sign-in: the account stays without a pool", async () => {
    const { dialog, replace } = await openWizard();
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Plan C" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Connect without a pool" }));
    await waitFor(() => expect(api.createSubscription).toHaveBeenCalledTimes(1));
    expect(api.updatePool).not.toHaveBeenCalled();
    expect(api.createPool).not.toHaveBeenCalled();
    expect(api.startSubscriptionLogin).not.toHaveBeenCalled();
    await waitFor(() => expect(replace).toHaveBeenCalledWith("/acme/usage?account=plan-c"));
  });

  it("connects with no pool when the person picks that on the pool step", async () => {
    const { dialog } = await openWizard();
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Plan C" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Next" }));
    fireEvent.click(within(dialog).getByRole("radio", { name: "No pool yet" }));
    expect(within(dialog).getByText(/listed under “No pool”/)).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("button", { name: "Connect" }));
    await waitFor(() => expect(api.createSubscription).toHaveBeenCalledTimes(1));
    expect(api.updatePool).not.toHaveBeenCalled();
    expect(api.createPool).not.toHaveBeenCalled();
  });

  it("does not create a second account when a later step fails and is retried", async () => {
    api.updatePool.mockRejectedValueOnce(new ApiError("pool changed", 409, "Conflict", { code: "pool_revision_conflict", current_revision: 13 }));
    api.updatePool.mockResolvedValueOnce(pool("claude", [], { revision: 14 }));
    const { dialog } = await openWizard();
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Plan C" } });
    fireEvent.change(within(dialog).getByLabelText("Token"), { target: { value: "sk-ant-oat01-secret" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Next" }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Next" }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Connect and sign in" }));
    await waitFor(() => expect(api.updatePool).toHaveBeenCalledTimes(1));
    expect(screen.getByRole("dialog")).toBeTruthy();
    expect(api.startSubscriptionLogin).not.toHaveBeenCalled();

    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Connect and sign in" }));
    await waitFor(() => expect(api.startSubscriptionLogin).toHaveBeenCalledWith("plan-c", { token: "sk-ant-oat01-secret" }));
    expect(api.createSubscription).toHaveBeenCalledTimes(1);
    expect(api.updatePool).toHaveBeenCalledTimes(2);
  });

  it("creates a new pool granted to the current workspace only, which creates no environment for the others", async () => {
    api.createPool.mockResolvedValue(pool("team", [entry("plan-c", 1)]));
    const { dialog } = await openWizard();
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Plan C" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Next" }));
    fireEvent.click(within(dialog).getByRole("radio", { name: "New pool" }));
    const name = within(dialog).getAllByLabelText("Name").at(-1)!;
    fireEvent.change(name, { target: { value: "Claude · team" } });
    expect(within(dialog).getByRole("checkbox", { name: "All workspaces, including new ones" })).not.toBeChecked();
    expect(await within(dialog).findByRole("checkbox", { name: "STT" })).toBeChecked();
    expect(within(dialog).getByRole("checkbox", { name: "Other" })).not.toBeChecked();
    fireEvent.click(within(dialog).getByRole("button", { name: "Next" }));
    expect(within(dialog).getByTestId("connect-usage").textContent).toContain("A new pool “Claude · team” is created");
    fireEvent.click(within(dialog).getByRole("button", { name: "Connect" }));
    await waitFor(() =>
      expect(api.createPool).toHaveBeenCalledWith({
        name: "Claude · team", runtime_type: "claude", subscriptions: ["plan-c"], spend_order: "first", workspace_ids: ["ws-1"],
      }),
    );
    expect(api.createSubscription).toHaveBeenCalledTimes(1);
  });

  it("writes an optional model of a key subscription into its settings", async () => {
    api.createSubscription.mockResolvedValue(sub("zai-a", { title: "GLM", provider: "zai", auth: "api_key" }));
    const { dialog } = await openWizard();
    fireEvent.click(within(dialog).getByRole("radio", { name: /API key/ }));
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "GLM" } });
    fireEvent.change(within(dialog).getByLabelText(/^Model/), { target: { value: "glm-4.6" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Connect without a pool" }));
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
    fireEvent.click(within(dialog).getByRole("button", { name: "Connect without a pool" }));
    expect(await screen.findByRole("link", { name: "https://auth.test/device" })).toBeTruthy();
    expect(screen.getByText("AB-12")).toBeTruthy();
    expect(api.startSubscriptionLogin).toHaveBeenCalledWith("cx-b", {});
  });
});
