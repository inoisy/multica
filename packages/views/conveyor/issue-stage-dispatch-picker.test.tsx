// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderWithI18n } from "../test/i18n";

const apiMock = vi.hoisted(() => ({
  getIssueStageDispatch: vi.fn(),
  listSquads: vi.fn(),
  setIssueStageDispatch: vi.fn(),
}));
vi.mock("@multica/core/api", () => ({ api: apiMock }));

import { IssueStageDispatchPicker } from "./issue-stage-dispatch-picker";

function renderPicker(hasChildren: boolean) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={client}>
      <div className="grid grid-cols-[auto_1fr]">
        <IssueStageDispatchPicker wsId="ws-1" issueId="epic" hasChildren={hasChildren} />
      </div>
    </QueryClientProvider>,
  );
}

describe("IssueStageDispatchPicker", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    apiMock.listSquads.mockResolvedValue([{ id: "sq-1", name: "Разработка", archived_at: null }]);
  });

  it("shows the squad the stages go to", async () => {
    apiMock.getIssueStageDispatch.mockResolvedValue({ issue_id: "epic", squad_id: "sq-1", dispatch_by: "u" });
    renderPicker(true);
    expect(await screen.findByText("Stages")).toBeTruthy();
    await waitFor(() => expect(screen.getByRole("combobox", { name: "Stages" }).textContent).toContain("Разработка"));
  });

  it("stays hidden on an issue without sub-issues while dispatch is off", async () => {
    apiMock.getIssueStageDispatch.mockResolvedValue({ issue_id: "epic", squad_id: null, dispatch_by: null });
    const { container } = renderPicker(false);
    await waitFor(() => expect(apiMock.getIssueStageDispatch).toHaveBeenCalled());
    expect(container.textContent).toBe("");
  });
});
