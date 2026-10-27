// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Pool, PoolAccount, Subscription } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { NavigationProvider, type NavigationAdapter } from "../../navigation";

const api = vi.hoisted(() => ({
  listSubscriptions: vi.fn(),
  listPools: vi.fn(),
  listWorkspaces: vi.fn(),
  listRuntimes: vi.fn(),
  updatePool: vi.fn(),
  setPoolGrants: vi.fn(),
}));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/paths", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/paths")>()),
  useWorkspacePaths: () => ({ runtimes: () => "/acme/runtimes" }),
}));
vi.mock("@multica/core/api", () => ({
  api,
  ApiError: class ApiError extends Error {},
}));

import { PoolAccountsSection } from "./pool-accounts-section";

function sub(key: string): Subscription {
  return {
    id: `id-${key}`, key, provider: "claude", family: "claude", title: key, owner_note: "", auth: "claude_oauth", credential_path: "",
    windows: ["5h"], caps: {}, effective_caps: {}, account_group: "", disabled_until: null, disabled: false, usage: [], used_by: [],
    hosts: [], login_state: "logged_in", login: null, created_at: "", updated_at: "",
  };
}
const entry = (key: string, position: number): PoolAccount => ({
  id: `id-${key}`, key, title: key, provider: "claude", account_group: "", position, blocked: false, usable: true,
  reason_code: null, until: null, until_estimated: false,
});
const pool = (keys: string[], revision: number): Pool => ({
  id: "pool-1", name: "Claude shared", runtime_type: "claude", description: "", spend_order: "first", all_workspaces: false, revision,
  subscriptions: keys.map((key, index) => entry(key, index + 1)), workspace_ids: ["ws-1"],
  environments: [{ runtime_profile_id: "env-1", workspace_id: "ws-1", display_name: "Claude env", enabled: true }],
  availability: { blocked: false, usable: true, reason_code: null, until: null, until_estimated: false },
  created_at: "", updated_at: "",
});

function renderWith(ui: React.ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const navigation: NavigationAdapter = {
    push: vi.fn(), replace: vi.fn(), back: vi.fn(), pathname: "/acme/runtimes",
    searchParams: new URLSearchParams(), hash: "", getShareableUrl: (path) => path,
  };
  return renderWithI18n(
    <NavigationProvider value={navigation}>
      <QueryClientProvider client={client}>{ui}</QueryClientProvider>
    </NavigationProvider>,
  );
}

describe("PoolAccountsSection", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    api.listSubscriptions.mockResolvedValue({ can_manage: true, subscriptions: [sub("a"), sub("b"), sub("c")] });
    api.listPools.mockResolvedValue({ can_manage: true, pools: [pool(["a", "b"], 5)] });
    api.listWorkspaces.mockResolvedValue([{ id: "ws-1", name: "STT" }]);
    api.listRuntimes.mockResolvedValue([]);
  });

  it("renders nothing for an environment without a pool", () => {
    const { container } = renderWith(<PoolAccountsSection poolId={null} />);
    expect(container.textContent).toBe("");
    expect(api.listPools).not.toHaveBeenCalled();
  });

  it("shows the accounts of the environment's pool in order with the pool's version", async () => {
    renderWith(<PoolAccountsSection poolId="pool-1" />);
    const section = await screen.findByTestId("runtime-pool");
    await within(section).findByTestId("pool-editor");
    expect(section.textContent).toContain("Pool “Claude shared”");
    expect(within(section).getAllByRole("listitem").map((li) => li.textContent)).toEqual(
      expect.arrayContaining([expect.stringContaining("a"), expect.stringContaining("b")]),
    );
    expect(within(section).getByTestId("pool-editor").getAttribute("data-revision")).toBe("5");
  });

  it("saves an edit with the revision it started from and sends the person to Accounts for limits", async () => {
    api.updatePool.mockResolvedValue(pool(["b", "a"], 6));
    renderWith(<PoolAccountsSection poolId="pool-1" />);
    const section = await screen.findByTestId("runtime-pool");
    fireEvent.click(await within(section).findByRole("button", { name: "Up: b" }));
    fireEvent.click(within(section).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.updatePool).toHaveBeenCalledWith("pool-1", { revision: 5, subscriptions: ["b", "a"] }));
    expect(within(section).getByRole("link", { name: /Accounts/ }).getAttribute("href")).toBe("/acme/runtimes?tab=accounts");
  });

  it("does not offer editing to a reader who cannot manage", async () => {
    api.listPools.mockResolvedValue({ can_manage: false, pools: [pool(["a"], 5)] });
    renderWith(<PoolAccountsSection poolId="pool-1" />);
    await screen.findByTestId("pool-editor");
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Up: a" })).toBeNull();
  });
});
