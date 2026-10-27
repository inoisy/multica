// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderWithI18n } from "../../test/i18n";

const api = vi.hoisted(() => ({
  listSubscriptions: vi.fn(),
  listPools: vi.fn(),
  listWorkspaces: vi.fn(),
  listRuntimes: vi.fn(),
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
vi.mock("@multica/core/paths", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/paths")>();
  return { ...actual, useWorkspacePaths: () => actual.paths.workspace("acme") };
});

import { ApiError } from "@multica/core/api";
import { NavigationProvider, type NavigationAdapter } from "../../navigation";
import { PoolAccountsSection } from "./pool-accounts-section";
import { entry, pool, poolList, subscriptionList } from "./accounts.fixtures";

function renderEditor(canManage = true, poolId = "claude") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const navigation: NavigationAdapter = {
    push: vi.fn(), replace: vi.fn(), back: vi.fn(), pathname: "/acme/runtimes", searchParams: new URLSearchParams(), hash: "",
    getShareableUrl: (path) => path,
  };
  api.listPools.mockResolvedValue(poolList(canManage));
  return renderWithI18n(
    <NavigationProvider value={navigation}>
      <QueryClientProvider client={client}>
        <PoolAccountsSection poolId={poolId} />
      </QueryClientProvider>
    </NavigationProvider>,
  );
}

describe("The pool editor on the environment page", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    api.listSubscriptions.mockResolvedValue(subscriptionList(true));
    api.listWorkspaces.mockResolvedValue([{ id: "ws-1", name: "STT" }, { id: "ws-2", name: "Other" }]);
    api.listRuntimes.mockResolvedValue([]);
  });

  it("reorders a pool with the revision it was read at and sends nothing else", async () => {
    api.updatePool.mockResolvedValue(pool("claude", [entry("acc-b", 1), entry("acc-a", 2)], { revision: 13 }));
    renderEditor();
    const editor = await screen.findByTestId("pool-editor");
    expect(editor.getAttribute("data-revision")).toBe("12");
    fireEvent.click(within(editor).getByRole("button", { name: "Up: acc-b" }));
    fireEvent.click(within(editor).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.updatePool).toHaveBeenCalledWith("claude", { revision: 12, subscriptions: ["acc-b", "acc-a"] }));
    expect(api.setPoolGrants).not.toHaveBeenCalled();
  });

  it("changes the spend order of a pool", async () => {
    api.updatePool.mockResolvedValue(pool("claude", [], { revision: 13 }));
    renderEditor();
    const editor = await screen.findByTestId("pool-editor");
    fireEvent.click(within(editor).getByRole("radio", { name: /evenly over the week/ }));
    fireEvent.click(within(editor).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.updatePool).toHaveBeenCalledWith("claude", { revision: 12, spend_order: "balanced" }));
  });

  it("removes a draft with Cancel without sending anything", async () => {
    renderEditor();
    const editor = await screen.findByTestId("pool-editor");
    fireEvent.click(within(editor).getByRole("button", { name: "Up: acc-b" }));
    fireEvent.click(within(editor).getByRole("button", { name: "Discard changes" }));
    expect(within(editor).queryByRole("button", { name: "Discard changes" })).toBeNull();
    expect(api.updatePool).not.toHaveBeenCalled();
  });

  it("keeps the draft when the pool moved on, says so and takes the current pool on request", async () => {
    api.updatePool.mockRejectedValue(new ApiError("pool changed", 409, "Conflict", { code: "pool_revision_conflict", current_revision: 14 }));
    renderEditor();
    const editor = await screen.findByTestId("pool-editor");
    fireEvent.click(within(editor).getByRole("button", { name: "Up: acc-b" }));
    api.listPools.mockResolvedValue({ can_manage: true, pools: [pool("claude", [entry("acc-b", 1), entry("acc-a", 2)], { revision: 14 })] });
    fireEvent.click(within(editor).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.updatePool).toHaveBeenCalledWith("claude", expect.objectContaining({ revision: 12 })));
    const alert = await within(editor).findByRole("alert");
    expect(alert.textContent).toContain("version 14");
    // The edit in progress is still on screen.
    expect(within(editor).getByRole("button", { name: "Save" })).not.toBeDisabled();
    fireEvent.click(within(alert).getByRole("button", { name: "Take the current pool" }));
    expect(screen.getByTestId("pool-editor").getAttribute("data-revision")).toBe("14");
    expect(within(editor).queryByRole("alert")).toBeNull();
  });

  it("grants a pool to chosen workspaces with the new revision of the same save", async () => {
    api.setPoolGrants.mockResolvedValue(pool("video", [entry("acc-b", 1)], { revision: 4, workspace_ids: ["ws-1", "ws-2"] }));
    renderEditor(true, "video");
    const editor = await screen.findByTestId("pool-editor");
    fireEvent.click(await within(editor).findByRole("checkbox", { name: "Other" }));
    fireEvent.click(within(editor).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api.setPoolGrants).toHaveBeenCalledWith("video", { revision: 3, workspace_ids: ["ws-1", "ws-2"] }));
  });

  it("shows a member the composition and grants without any control", async () => {
    renderEditor(false);
    const editor = await screen.findByTestId("pool-editor");
    expect(within(editor).queryByRole("button", { name: "Save" })).toBeNull();
    expect(within(editor).queryByRole("button", { name: "Up: acc-b" })).toBeNull();
    expect(within(editor).getAllByText("acc-a").length).toBeGreaterThan(0);
  });
});
