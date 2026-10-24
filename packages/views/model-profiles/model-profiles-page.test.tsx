// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ModelProfile } from "@multica/core/types";
import { renderWithI18n } from "../test/i18n";
import { NavigationProvider, type NavigationAdapter } from "../navigation";

const apiMock = vi.hoisted(() => ({
  listModelProfiles: vi.fn(),
  updateModelProfile: vi.fn(),
  createModelProfile: vi.fn(),
  deleteModelProfile: vi.fn(),
  previewModelProfile: vi.fn(),
  getIssueModelProfile: vi.fn(),
  setIssueModelProfile: vi.fn(),
}));
const role = vi.hoisted(() => ({ current: "owner" as string | null }));
vi.mock("@multica/core/api", () => ({ api: apiMock }));
vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "ws-1" }));
vi.mock("@multica/core/permissions", () => ({
  useCurrentMember: () => ({ userId: "u1", role: role.current, member: null, isLoading: false }),
}));
vi.mock("@multica/core/runtimes", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/runtimes")>()),
  runtimeListOptions: () => ({ queryKey: ["runtimes"], queryFn: () => Promise.resolve([{ id: "rt-1", name: "codex" }, { id: "rt-2", name: "claude" }]) }),
}));
vi.mock("@multica/core/workspace/queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/workspace/queries")>()),
  memberListOptions: () => ({ queryKey: ["members"], queryFn: () => Promise.resolve([]) }),
  agentListOptions: () => ({ queryKey: ["agents"], queryFn: () => Promise.resolve([{ id: "a1", name: "Dev" }]) }),
}));
vi.mock("@multica/core/paths", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/paths")>();
  return { ...actual, useWorkspacePaths: () => actual.paths.workspace("acme") };
});
vi.mock("./model-rows-editor", () => ({
  ModelRowsEditor: ({ rows, onChange }: { rows: unknown[]; onChange: (rows: unknown[]) => void }) => (
    <button type="button" onClick={() => onChange([...rows].reverse())}>
      reorder {rows.length}
    </button>
  ),
}));

import { ModelProfilesPage } from "./model-profiles-page";
import { IssueModelProfilePicker } from "./issue-model-profile-picker";

const opt = (position: number, runtime_name: string, model: string, thinking_level = "") => ({
  position, runtime_id: `rt-${position}`, runtime_name, provider: "", model, thinking_level,
  custom_args: null, state: "available", until: null, chosen: false,
});

const strong: ModelProfile = {
  id: "p1", name: "Strong", description: "hard work", when_exhausted_profile_id: "p2",
  options: [opt(1, "codex", "sol", "high"), opt(2, "claude", "opus")],
  agents: [{ id: "a1", name: "Dev", identifier: "" }], own_list_agents: [{ id: "a2", name: "Review", identifier: "" }],
  open_issues: [{ id: "i1", name: "x", identifier: "STT-7" }], fallback_of: [], updated_at: "",
};
const mid: ModelProfile = { ...strong, id: "p2", name: "Mid", description: "", when_exhausted_profile_id: null, options: [opt(1, "glm", "glm")], agents: [], own_list_agents: [], open_issues: [] };

function wrap(ui: React.ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const navigation: NavigationAdapter = {
    push: vi.fn(), replace: vi.fn(), back: vi.fn(), pathname: "/acme/profiles",
    searchParams: new URLSearchParams(), hash: "", getShareableUrl: (path) => path,
  };
  return renderWithI18n(
    <NavigationProvider value={navigation}>
      <QueryClientProvider client={client}>{ui}</QueryClientProvider>
    </NavigationProvider>,
  );
}

describe("ModelProfilesPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    role.current = "owner";
    apiMock.listModelProfiles.mockResolvedValue([strong, mid]);
  });

  it("lists profiles with models in order, the exhausted profile and who references them", async () => {
    wrap(<ModelProfilesPage />);
    const table = await screen.findByTestId("model-profiles-table");
    const strongRow = within(table).getByText("Strong").closest("tr")!;
    expect(strongRow.textContent).toContain("codex·sol·high → claude·opus");
    expect(strongRow.textContent).toContain("Mid");
    expect(strongRow.textContent).toContain("Dev");
    expect(strongRow.textContent).toContain("STT-7");
    expect(strongRow.textContent).toContain("Review");
    const rows = within(table).getAllByRole("row");
    expect(rows.find((r) => r.textContent?.startsWith("Mid"))?.textContent).toContain("wait");
  });

  it("previews whose primary model changes before saving an edit", async () => {
    apiMock.previewModelProfile.mockResolvedValue([
      { kind: "agent", ref: { id: "a1", name: "Dev", identifier: "" }, before: { position: 1, runtime_id: "rt-1", model: "sol", thinking_level: "high", custom_args: null }, after: { position: 1, runtime_id: "rt-2", model: "opus", thinking_level: "", custom_args: null }, reason: "primary" },
    ]);
    apiMock.updateModelProfile.mockResolvedValue(strong);
    wrap(<ModelProfilesPage />);
    fireEvent.click(await screen.findByRole("button", { name: "Edit Strong" }));
    fireEvent.click(await screen.findByRole("button", { name: "reorder 2" }));
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Preview" }));
    const preview = await screen.findByTestId("model-profile-preview");
    expect(preview.textContent).toContain("Primary model changes for 1:");
    expect(preview.textContent).toContain("codex·sol·high → claude·opus");
    expect(apiMock.previewModelProfile).toHaveBeenCalledWith("p1", expect.objectContaining({ options: [
      expect.objectContaining({ runtime_id: "rt-2" }), expect.objectContaining({ runtime_id: "rt-1" }),
    ] }));
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(apiMock.updateModelProfile).toHaveBeenCalledWith("p1", expect.objectContaining({ name: "Strong", when_exhausted_profile_id: "p2" })));
  });

  it("creates a profile from scratch with its rows", async () => {
    apiMock.createModelProfile.mockResolvedValue(mid);
    wrap(<ModelProfilesPage />);
    fireEvent.click(await screen.findByRole("button", { name: "Create profile" }));
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Copy" } });
    expect(screen.getByRole("button", { name: "Save" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(apiMock.createModelProfile).toHaveBeenCalledWith({ name: "Copy", description: "", when_exhausted_profile_id: null, options: [] }));
  });

  it("hides management from members who are not admins", async () => {
    role.current = "member";
    wrap(<ModelProfilesPage />);
    await screen.findByTestId("model-profiles-table");
    expect(screen.queryByRole("button", { name: "Create profile" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Edit Strong" })).toBeNull();
  });
});

describe("IssueModelProfilePicker", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    apiMock.listModelProfiles.mockResolvedValue([strong, mid]);
  });

  it("shows the issue's profile in the sidebar row", async () => {
    apiMock.getIssueModelProfile.mockResolvedValue({ issue_id: "i1", model_profile_id: "p2", profile_name: "Mid" });
    wrap(<div className="grid"><IssueModelProfilePicker wsId="ws-1" issueId="i1" /></div>);
    expect(await screen.findByText("Model profile")).toBeTruthy();
    expect(await screen.findByText("Mid")).toBeTruthy();
  });

  it("defaults to the assignee's profile and hides itself on an older backend", async () => {
    apiMock.getIssueModelProfile.mockResolvedValueOnce({ issue_id: "i1", model_profile_id: null, profile_name: "" });
    const first = wrap(<IssueModelProfilePicker wsId="ws-1" issueId="i1" />);
    expect(await screen.findByText("Assignee's profile")).toBeTruthy();
    first.unmount();
    apiMock.getIssueModelProfile.mockRejectedValueOnce(new Error("404"));
    const { container } = wrap(<IssueModelProfilePicker wsId="ws-1" issueId="i2" />);
    await waitFor(() => expect(apiMock.getIssueModelProfile).toHaveBeenCalledTimes(2));
    expect(container.textContent).toBe("");
  });
});
