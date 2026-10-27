// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderWithI18n } from "../../test/i18n";

const api = vi.hoisted(() => ({
  listWorkspaces: vi.fn(),
  createPool: vi.fn(),
}));
vi.mock("@multica/core/api", () => ({ api, ApiError: class ApiError extends Error {} }));

import { NewPoolDialog } from "./new-pool-dialog";
import { pool, sub } from "./accounts.fixtures";

function renderDialog() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={client}>
      <NewPoolDialog open onOpenChange={vi.fn()} wsId="ws-1" subscriptions={[sub("acc-a"), sub("acc-b")]} />
    </QueryClientProvider>,
  );
}

describe("Add an environment through accounts", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    api.listWorkspaces.mockResolvedValue([{ id: "ws-1", name: "STT" }, { id: "ws-2", name: "Other" }]);
  });

  it("creates the pool and its grant in one request, in the order the person set", async () => {
    api.createPool.mockResolvedValue(pool("team", []));
    renderDialog();
    const dialog = await screen.findByRole("dialog", { name: "New environment through accounts" });
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "  Claude · team  " } });
    expect(await within(dialog).findByRole("checkbox", { name: "STT" })).toBeChecked();
    fireEvent.click(within(dialog).getByRole("button", { name: "Create" }));
    await waitFor(() => expect(api.createPool).toHaveBeenCalledWith({
      name: "Claude · team", runtime_type: "claude", subscriptions: [], spend_order: "first", workspace_ids: ["ws-1"],
    }));
    expect(api.createPool).toHaveBeenCalledTimes(1);
  });

  it("can be created granted to no workspace, but not without a name", async () => {
    api.createPool.mockResolvedValue(pool("ungranted", [], { workspace_ids: [], environments: [] }));
    renderDialog();
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(await within(dialog).findByRole("checkbox", { name: "STT" }));
    expect(within(dialog).getByRole("button", { name: "Create" })).toBeDisabled();
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Ungranted" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Create" }));
    await waitFor(() => expect(api.createPool).toHaveBeenCalledWith({
      name: "Ungranted", runtime_type: "claude", subscriptions: [], spend_order: "first", workspace_ids: [],
    }));
  });
});
