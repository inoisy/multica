// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderWithI18n } from "../test/i18n";

const api = vi.hoisted(() => ({
  listSubscriptions: vi.fn(),
  listPools: vi.fn(),
  listSubscriptionDiscrepancies: vi.fn(),
  listRuntimes: vi.fn(),
  listWorkspaces: vi.fn(),
  getSubscriptionLogin: vi.fn(),
  listSubscriptionManualRuns: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({ api, ApiError: class ApiError extends Error {} }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("../navigation", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../navigation")>()),
  AppLink: ({ href, children, ...props }: { href: string; children?: React.ReactNode }) => <a href={href} {...props}>{children}</a>,
}));
vi.mock("@multica/core/paths", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/paths")>();
  return { ...actual, useWorkspacePaths: () => actual.paths.workspace("acme") };
});

import { NavigationProvider, type NavigationAdapter } from "../navigation";
import { entry, limitedVerdict, pool, poolList, sub, subscriptionList } from "../runtimes/accounts/accounts.fixtures";
import { SubscriptionsContent, SubscriptionsPage } from "./subscriptions-page";

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

describe("Analytics → Subscriptions", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    api.listSubscriptions.mockResolvedValue(subscriptionList(true));
    api.listPools.mockResolvedValue(poolList());
    api.listSubscriptionDiscrepancies.mockResolvedValue([]);
    api.listRuntimes.mockResolvedValue([{ id: "rt-claude", profile_id: "env-claude", name: "Claude env" }]);
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

  it("has no create or edit controls, whoever the reader is", async () => {
    renderPage("account=acc-a");
    expect(await screen.findByText(/View only/)).toBeTruthy();
    for (const name of ["Add subscription", "Add an environment through accounts", "New pool", "Actions for “Plan A”", "Sign in again", "Pool order"]) {
      expect(screen.queryByRole("button", { name })).toBeNull();
    }
    expect(screen.queryByTestId("manual-runs")).toBeNull();
    expect(api.listSubscriptionManualRuns).not.toHaveBeenCalled();
    expect(screen.getByRole("link", { name: "Open Environments → Accounts" }).getAttribute("href")).toBe("/acme/runtimes?tab=accounts");
  });

  it("links a pool to every environment that spends it, without picking one", async () => {
    api.listPools.mockResolvedValue({
      can_manage: true,
      pools: [pool("claude", [entry("acc-a", 1)], {
        environments: [
          { runtime_profile_id: "env-claude", workspace_id: "ws-1", display_name: "Claude env", enabled: true },
          { runtime_profile_id: "env-other", workspace_id: "ws-2", display_name: "Claude · Other", enabled: true },
        ],
      })],
    });
    renderPage();
    const group = await screen.findByTestId("pool-group-claude");
    const envs = within(group).getByRole("list", { name: "Environments of the pool" });
    expect(within(envs).getByRole("link", { name: "Open environment “Claude env”" }).getAttribute("href")).toBe("/acme/runtimes/rt-claude/runtime/rt-claude");
    // An environment of another workspace cannot be opened from here: named, not linked.
    expect(within(envs).queryByRole("link", { name: /Other/ })).toBeNull();
    expect(envs.textContent).toContain("Claude · Other");
    expect(envs.textContent).toContain("Other");
  });

  it("lists discrepancies by kind and links to the environment", async () => {
    api.listSubscriptionDiscrepancies.mockResolvedValue([
      { kind: "credential_missing", message: "учётка на хосте /private/token", subscription_key: "acc-a", device_name: "vps", runtime_profile_id: null, runtime_name: null, workspace_id: null, agent_id: null, agent_name: null },
      { kind: "runtime_without_subscriptions", message: "", subscription_key: null, device_name: null, runtime_profile_id: "env-claude", runtime_name: "Claude env", workspace_id: "ws-1", agent_id: null, agent_name: null },
    ]);
    renderPage();
    const block = (await screen.findByRole("heading", { name: "Discrepancies" })).closest("section")!;
    expect(within(block as HTMLElement).getByText("No credential on the host")).toBeTruthy();
    expect(within(block as HTMLElement).getByText("Plan A · vps")).toBeTruthy();
    expect(block.textContent).not.toContain("учётка");
    expect(block.textContent).not.toContain("/private/token");
    expect(within(block as HTMLElement).getByRole("link", { name: "Open runtime" }).getAttribute("href")).toBe("/acme/runtimes/rt-claude/runtime/rt-claude");
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

  it("shows the agents' limit with what is left for people, read-only", async () => {
    renderPage("account=acc-a");
    const detail = await screen.findByTestId("account-detail-acc-a");
    expect(within(detail).getByTestId("agent-limit-summary").textContent).toBe("Agents up to: 5 hours 70% (people 30%)");
    expect(within(detail).getByText("Pools:").parentElement!.textContent).toContain("claude (#1)");
    expect(within(detail).getByTestId("usage-measured-at").textContent).toContain("Quota measured");
  });
});
