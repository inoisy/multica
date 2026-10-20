// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderWithI18n } from "../test/i18n";
import type { Subscription, SubscriptionsState } from "./subscription-types";

const control = vi.hoisted(() => ({ request: vi.fn() }));
const login = vi.hoisted(() => ({ post: vi.fn() }));

vi.mock("../conveyor/control-api", () => ({
  ControlError: class ControlError extends Error {
    constructor(
      readonly status: number,
      message: string,
    ) {
      super(message);
    }
  },
  controlRequest: control.request,
}));
vi.mock("@multica/core/paths", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/paths")>()),
  useWorkspaceSlug: () => "stt",
}));
vi.mock("@multica/core/subs-login", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/subs-login")>()),
  loginAccountsOptions: () => ({ queryKey: ["subs-login", "stt"], queryFn: async () => ({ accounts: [], can_login: true }) }),
  postLogin: login.post,
}));

import { SubscriptionsPage } from "./subscriptions-page";

function sub(id: string, provider: string, title: string, runtimes: string[]): Subscription {
  return {
    id, provider, title, quota_row: title, windows: ["5h", "week"], caps: {}, reserve: 0, effective: {},
    credential: `~agents/.config/${id}/token`, auth: provider === "codex" ? "codex_auth" : "claude_oauth", cli: "",
    changed_at: null, changed_by: null, usage: [], error: null, available: true, runtimes, agents: [],
  };
}

function state(): SubscriptionsState {
  const subscriptions = [
    sub("claude-main", "claude", "Claude main", ["Claude · 2"]),
    sub("claude-second", "claude", "Claude second", ["Claude · 2"]),
    sub("codex-main", "codex", "Codex main", ["Codex · 2"]),
  ];
  return {
    subscriptions, stale: false, updated: null, can_edit: true, events: [{ at: "2026-10-20T06:00:00Z", kind: "subscriptions", text: "evgeniy: saved" }],
    window_names: { "5h": "5 hours", week: "week" },
    runtimes: [
      { name: "Claude · 2", command: "claude-reserve", subscriptions: ["claude-second", "claude-main"], order: "first" },
      { name: "Codex · 2", command: "codex-reserve", subscriptions: ["codex-main"], order: "balanced" },
    ],
    windows: [{ key: "5h" }, { key: "week" }], auths: ["claude_oauth", "codex_auth", "api_key"], orders: ["first", "balanced"],
  };
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={client}>
      <SubscriptionsPage />
    </QueryClientProvider>,
  );
}

/** Table writes the page sent, in order. */
function writes() {
  return control.request.mock.calls
    .filter(([path]) => path === "subscriptions/tables")
    .map(([, body]) => body as { table: string; key: string; row: Record<string, unknown> | null });
}

describe("SubscriptionsPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    control.request.mockImplementation(async (path: string) => (path === "subscriptions" ? state() : { message: "Saved" }));
    login.post.mockResolvedValue({ session: null, state: "done", url: null, code: null, message: "" });
  });
  afterEach(() => vi.restoreAllMocks());

  it("groups accounts by provider and has no raw tables", async () => {
    renderPage();
    expect(await screen.findByRole("heading", { name: "Claude" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Codex" })).toBeTruthy();
    expect(screen.queryByRole("table")).toBeNull();
    expect(screen.getByRole("button", { name: "Add subscription" })).toBeTruthy();
  });

  it("adds a Claude subscription to a runtime and signs it in", async () => {
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "Add subscription" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("2. Name"), { target: { value: "Claude third" } });
    fireEvent.change(within(dialog).getByLabelText("Token"), { target: { value: "sk-ant-secret" } });
    fireEvent.click(within(dialog).getByRole("button", { name: /Pick runtimes/ }));
    fireEvent.click(await screen.findByRole("option", { name: /Claude · 2/ }));
    expect(screen.queryByRole("option", { name: /Codex · 2/ })).toBeNull();
    fireEvent.click(within(dialog).getByRole("button", { name: "Add and sign in" }));

    await waitFor(() => expect(login.post).toHaveBeenCalledWith("stt", "claude-2", { action: "secret", value: "sk-ant-secret" }));
    expect(writes()).toEqual([
      {
        table: "items", key: "claude-2",
        row: { provider: "claude", title: "Claude third", quota_row: "Claude third", windows: ["5h", "week"], auth: "claude_oauth", credential: "~agents/.config/claude-2/token", cli: "" },
      },
      { table: "runtimes", key: "Claude · 2", row: { command: "claude-reserve", subscriptions: ["claude-second", "claude-main", "claude-2"], order: "first" } },
    ]);
    // Default limits (100%, no reserve) send no caps request.
    expect(control.request.mock.calls.some(([path, body]) => path === "subscriptions" && body)).toBe(false);
  });

  it("takes a subscription out of its runtimes before deleting it", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "Actions for “Claude main”" }));
    fireEvent.click(await screen.findByRole("menuitem", { name: "Delete subscription…" }));
    await waitFor(() => expect(writes()).toHaveLength(2));
    expect(writes()).toEqual([
      { table: "runtimes", key: "Claude · 2", row: { command: "claude-reserve", subscriptions: ["claude-second"], order: "first" } },
      { table: "items", key: "claude-main", row: null },
    ]);
  });

  it("reorders a runtime from the card's spent-by link", async () => {
    renderPage();
    const cards = await screen.findAllByRole("button", { name: "Claude · 2" });
    fireEvent.click(cards[0]!);
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Move up: Claude main" }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(writes()).toHaveLength(1));
    expect(writes()[0]).toEqual({
      table: "runtimes", key: "Claude · 2", row: { command: "claude-reserve", subscriptions: ["claude-main", "claude-second"], order: "first" },
    });
  });
});
