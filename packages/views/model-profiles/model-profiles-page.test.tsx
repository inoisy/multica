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
  resetAgentPersonalProfile: vi.fn(),
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
  agentListOptions: () => ({ queryKey: ["agents"], queryFn: () => Promise.resolve([{ id: "a1", name: "Dev" }, { id: "a2", name: "Review" }]) }),
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
  custom_args: null, state: "available", until: null, reason_code: null, until_estimated: false, chosen: false, effective_model: model, model_at_run: false,
});

const strong: ModelProfile = {
  id: "p1", name: "Strong", description: "hard work", when_exhausted_profile_id: "p2",
  options: [opt(1, "codex", "sol", "high"), opt(2, "claude", "opus")],
  agents: [{ id: "a1", name: "Dev", identifier: "" }],
  personal_profiles: [{ profile_id: "pp1", agent: { id: "a2", name: "Review", identifier: "" }, matches_base: false, diff_rows: [1] }],
  open_issues: [{ id: "i1", name: "x", identifier: "STT-7" }], fallback_of: [], updated_at: "",
  personal: false, owner_agent: null, based_on: null, matches_base: null, diff_rows: [],
};
const mid: ModelProfile = { ...strong, id: "p2", name: "Mid", description: "", when_exhausted_profile_id: null, options: [opt(1, "glm", "glm")], agents: [], personal_profiles: [], open_issues: [] };
const personalOfReview: ModelProfile = {
  ...strong, id: "pp1", name: "Review · personal", description: "", when_exhausted_profile_id: null, agents: [], personal_profiles: [], open_issues: [],
  options: [opt(1, "codex", "sol", "xhigh"), opt(2, "claude", "opus")],
  personal: true, owner_agent: { id: "a2", name: "Review", identifier: "" }, based_on: { id: "p1", name: "Strong", identifier: "" }, matches_base: false, diff_rows: [1],
};

function wrap(ui: React.ReactElement, search = "") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const navigation: NavigationAdapter = {
    push: vi.fn(), replace: vi.fn(), back: vi.fn(), pathname: "/acme/profiles",
    searchParams: new URLSearchParams(search), hash: "", getShareableUrl: (path) => path,
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
    apiMock.listModelProfiles.mockImplementation((kind?: string) =>
      Promise.resolve(kind === "personal" ? [personalOfReview] : [strong, mid]),
    );
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
    expect(strongRow.textContent).toContain("differs: rows 1");
    const rows = within(table).getAllByRole("row");
    expect(rows.find((r) => r.textContent?.startsWith("Mid"))?.textContent).toContain("wait");
  });

  it("does not show the personal profile of an agent the reader cannot see", async () => {
    const hidden: ModelProfile = {
      ...personalOfReview, id: "pp9", name: "Secret · personal", owner_agent: { id: "a9", name: "Secret", identifier: "" },
    };
    apiMock.listModelProfiles.mockImplementation((kind?: string) =>
      Promise.resolve(kind === "personal" ? [personalOfReview, hidden] : [{ ...strong, personal_profiles: [...strong.personal_profiles, { profile_id: "pp9", agent: { id: "a9", name: "Secret", identifier: "" }, matches_base: true, diff_rows: [] }] }, mid]),
    );
    wrap(<ModelProfilesPage />);
    const table = await screen.findByTestId("personal-profiles-table");
    expect(within(table).getByText("Review")).toBeInTheDocument();
    expect(screen.queryByText("Secret")).toBeNull();
  });

  it("disables deleting a profile that agents, open issues or other profiles still use", async () => {
    wrap(<ModelProfilesPage />);
    const table = await screen.findByTestId("model-profiles-table");
    expect((within(table).getByRole("button", { name: "Delete Strong" }) as HTMLButtonElement).disabled).toBe(true);
    expect(within(table).getByRole("button", { name: "Delete Mid" }).hasAttribute("disabled")).toBe(false);
  });

  it("lists personal profiles with their base and differing rows, apart from the shared ones", async () => {
    wrap(<ModelProfilesPage />);
    const table = await screen.findByTestId("personal-profiles-table");
    const row = within(table).getByText("Review").closest("tr")!;
    expect(row.textContent).toContain("Strong");
    expect(row.textContent).toContain("differs: rows 1");
    expect(row.textContent).toContain("codex·sol·xhigh → claude·opus");
    // Personal profiles are never in the shared list and never offered as a fallback.
    expect(within(screen.getByTestId("model-profiles-table")).queryByText("Review · personal")).toBeNull();
    expect(apiMock.listModelProfiles).toHaveBeenCalledWith("shared");
    expect(apiMock.listModelProfiles).toHaveBeenCalledWith("personal");
  });

  it("lists profiles as cards on a phone instead of a table that scrolls sideways", async () => {
    const width = window.innerWidth;
    window.innerWidth = 375;
    try {
      wrap(<ModelProfilesPage />);
      const list = await screen.findByTestId("model-profiles-list");
      expect(screen.queryByTestId("model-profiles-table")).toBeNull();
      const card = within(list).getByText("Strong").closest("li")!;
      expect(card.textContent).toContain("codex·sol·high → claude·opus");
      expect(card.textContent).toContain("differs: rows 1");
      expect(screen.getByTestId("personal-profiles-list").textContent).toContain("Review");
    } finally {
      window.innerWidth = width;
    }
  });

  it("filters by kind from the address and writes the filter back to it", async () => {
    wrap(<ModelProfilesPage />, "kind=personal");
    await screen.findByTestId("personal-profiles-table");
    expect(screen.queryByTestId("model-profiles-table")).toBeNull();
    expect(screen.getByRole("button", { name: "Personal" })).toHaveAttribute("aria-pressed", "true");
  });

  it("returns a personal profile to the shared one only after a confirmation", async () => {
    apiMock.resetAgentPersonalProfile.mockResolvedValue({ agent_id: "a2" });
    wrap(<ModelProfilesPage />);
    const table = await screen.findByTestId("personal-profiles-table");
    fireEvent.click(within(table).getByRole("button", { name: /Return to shared/ }));
    expect(apiMock.resetAgentPersonalProfile).not.toHaveBeenCalled();
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog.textContent).toContain("Return to “Strong”?");
    fireEvent.click(within(dialog).getByRole("button", { name: "Return to shared" }));
    await waitFor(() => expect(apiMock.resetAgentPersonalProfile).toHaveBeenCalledWith("a2"));
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

  it("offers only shared profiles to an issue", async () => {
    apiMock.getIssueModelProfile.mockResolvedValue({ issue_id: "i1", model_profile_id: null, profile_name: "" });
    wrap(<IssueModelProfilePicker wsId="ws-1" issueId="i1" />);
    await screen.findByText("Assignee's profile");
    expect(apiMock.listModelProfiles).toHaveBeenCalledWith("shared");
    expect(apiMock.listModelProfiles).not.toHaveBeenCalledWith("personal");
    expect(apiMock.listModelProfiles).not.toHaveBeenCalledWith("all");
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
