// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Issue } from "@multica/core/types";
import { renderWithI18n } from "../test/i18n";
import { NavigationProvider, type NavigationAdapter } from "../navigation";

const apiMock = vi.hoisted(() => ({
  listStageDispatch: vi.fn(),
  listChildIssues: vi.fn(),
  listSquads: vi.fn(),
  setIssueStageDispatch: vi.fn(),
  rerunIssue: vi.fn(),
  createComment: vi.fn(),
}));
vi.mock("@multica/core/api", () => ({ api: apiMock }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/paths", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/paths")>();
  return { ...actual, useWorkspacePaths: () => actual.paths.workspace("acme") };
});
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

import { buildStages, cardState, ConveyorPage, parseRef } from "./conveyor-page";

function issue(over: Partial<Issue>): Issue {
  return {
    id: over.identifier ?? "i", workspace_id: "ws-1", number: 1, identifier: "STT-1", title: "t", description: null,
    status: "todo", priority: "none", assignee_type: null, assignee_id: null, creator_type: "member", creator_id: "u",
    parent_issue_id: "epic", project_id: null, position: 0, stage: 1, start_date: null, due_date: null,
    created_at: "", updated_at: "", ...over,
  } as Issue;
}

describe("conveyor helpers", () => {
  it("reads a card from the issue status first, then from its route", () => {
    expect(cardState(issue({ status: "done" }))).toBe("success");
    expect(cardState(issue({ status: "cancelled" }))).toBe("cancelled");
    expect(cardState(issue({ status: "waiting" }))).toBe("held");
    expect(cardState(issue({ status: "in_progress", route: { step: "dev", status: "waiting_provider", round: 1 } }))).toBe("quota");
    expect(cardState(issue({ status: "qa", route: { step: "qa", status: "awaiting_coordinator", round: 1 } }))).toBe("blocked");
    expect(cardState(issue({ status: "in_review", route: { step: "close", status: "completed", round: 1 } }))).toBe("review");
    expect(cardState(issue({ status: "in_progress", route: { step: "dev", status: "active", round: 1 } }))).toBe("running");
    expect(cardState(issue({ assignee_id: "squad" }))).toBe("queued");
    expect(cardState(issue({}))).toBe("created");
  });

  it("opens the first stage with work left, past one that only waits for the customer", () => {
    const stages = buildStages([
      issue({ identifier: "A", stage: 1, status: "done" }),
      issue({ identifier: "B", stage: 2, status: "waiting" }),
      issue({ identifier: "C", stage: 3 }),
      issue({ identifier: "D", stage: 4 }),
      issue({ identifier: "E", stage: null }),
    ]);
    expect(stages.map((s) => [s.stage, s.state])).toEqual([[1, "done"], [2, "done"], [3, "active"], [4, "waiting"], [null, "waiting"]]);
  });

  it("accepts a key or a Multica link, not a bare number", () => {
    expect(parseRef(" STT-239 ")).toBe("STT-239");
    expect(parseRef("https://multica.example/stt/issues/STT-12")).toBe("STT-12");
    expect(parseRef("239")).toBeNull();
  });
});

describe("ConveyorPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    apiMock.listSquads.mockResolvedValue([{ id: "sq-1", name: "Разработка", archived_at: null }]);
    apiMock.listStageDispatch.mockResolvedValue([
      { issue: issue({ id: "epic", identifier: "STT-239", title: "Эпик", parent_issue_id: null, stage: null }), squad_id: "sq-1", dispatch_by: "u" },
    ]);
    apiMock.listChildIssues.mockResolvedValue({
      issues: [
        issue({ id: "c1", identifier: "STT-240", stage: 1, status: "done" }),
        issue({ id: "c2", identifier: "STT-241", stage: 2, status: "qa", assignee_id: "sq-1", route: { step: "qa", status: "waiting_provider", round: 1, waiting_reason: "limit until 15:53" } }),
      ],
    });
    apiMock.setIssueStageDispatch.mockResolvedValue({ issue_id: "STT-300", squad_id: "sq-1", dispatch_by: "u" });
  });

  function renderPage() {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const navigation: NavigationAdapter = {
      push: vi.fn(), replace: vi.fn(), back: vi.fn(), pathname: "/acme/conveyor",
      searchParams: new URLSearchParams(), hash: "", getShareableUrl: (path) => path,
    };
    return renderWithI18n(
      <NavigationProvider value={navigation}>
        <QueryClientProvider client={client}>
          <ConveyorPage />
        </QueryClientProvider>
      </NavigationProvider>,
    );
  }

  it("shows dispatched epics with their stages and the route step of each card", async () => {
    renderPage();
    expect(await screen.findByText("STT-239")).toBeTruthy();
    expect(await screen.findByText("Stages go to Разработка")).toBeTruthy();
    expect(screen.getByText(/In progress · stage 2/)).toBeTruthy();
    expect(screen.getByText("waits for the provider · qa")).toBeTruthy();
    expect(screen.getByText(/limit until 15:53/)).toBeTruthy();
  });

  it("stops dispatching after a confirmation", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "Stop dispatching" }));
    await waitFor(() => expect(apiMock.setIssueStageDispatch).toHaveBeenCalledWith("epic", null));
  });

  it("refuses a bare number before calling the server", async () => {
    renderPage();
    await screen.findByText("STT-239");
    const input = screen.getByLabelText(/Epic key or Multica link/);
    fireEvent.change(input, { target: { value: "300" } });
    fireEvent.submit(input.closest("form")!);
    const { toast } = await import("sonner");
    expect(toast.error).toHaveBeenCalledWith("Enter a key like STT-239 or a Multica link");
    expect(apiMock.setIssueStageDispatch).not.toHaveBeenCalled();
  });
});
