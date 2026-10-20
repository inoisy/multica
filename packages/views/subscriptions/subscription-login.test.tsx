// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { LoginAccount } from "@multica/core/subs-login";
import { renderWithI18n } from "../test/i18n";

vi.mock("@multica/core/api", () => ({ api: { getToken: () => null } }));

// The helper is reached with fetch; `postLogin` answers POSTs with queued bodies and records what was sent.
const loginMock = { postLogin: vi.fn() };
function stubHelper() {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_url: string, init: RequestInit) => {
      if (init.method !== "POST") return new Response(JSON.stringify({ state: "waiting" }), { status: 200 });
      const sent = JSON.parse(String(init.body)) as { account: string; [key: string]: unknown };
      const { account, ...action } = sent;
      const body: unknown = await loginMock.postLogin("stt", account, action);
      return new Response(JSON.stringify(body), { status: 200 });
    }),
  );
}

import { SubscriptionLogin } from "./subscription-login";

const codex: LoginAccount = { id: "codex-a", provider: "codex", auth: "codex_auth", state: "revoked", login: null };
const claude: LoginAccount = { id: "claude-a", provider: "claude", auth: "claude_oauth", state: "unchecked", login: null };

function renderLogin(account: LoginAccount, canLogin = true) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={client}>
      <SubscriptionLogin account={account} canLogin={canLogin} wsSlug="stt" />
    </QueryClientProvider>,
  );
}

describe("SubscriptionLogin", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    loginMock.postLogin.mockReset();
    stubHelper();
  });
  afterEach(() => vi.unstubAllGlobals());

  it("shows the status to members without the login action", () => {
    renderLogin(codex, false);
    expect(screen.getByText("Sign-in revoked")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Sign in again" })).toBeNull();
  });

  it("shows the device link and code, then cancels", async () => {
    loginMock.postLogin
      .mockResolvedValueOnce({ session: "s1", state: "waiting", url: "https://auth.test/device", code: "ABCD-12345", message: "" })
      .mockResolvedValueOnce({ session: "s1", state: "cancelled", url: null, code: null, message: "вход отменён" });
    renderLogin(codex);
    fireEvent.click(screen.getByRole("button", { name: "Sign in again" }));
    fireEvent.click(await screen.findByRole("button", { name: "Get link" }));
    expect(await screen.findByRole("link", { name: "https://auth.test/device" })).toBeTruthy();
    expect(screen.getByText("ABCD-12345")).toBeTruthy();
    expect(loginMock.postLogin).toHaveBeenCalledWith("stt", "codex-a", { action: "start" });
    fireEvent.click(screen.getByRole("button", { name: "Cancel sign-in" }));
    expect((await screen.findByRole("alert")).textContent).toBe("вход отменён");
    expect(loginMock.postLogin).toHaveBeenLastCalledWith("stt", "codex-a", { action: "cancel" });
  });

  it("saves a pasted token and closes on success", async () => {
    loginMock.postLogin.mockResolvedValue({ session: null, state: "done", url: null, code: null, message: "" });
    renderLogin(claude);
    fireEvent.click(screen.getByRole("button", { name: "Sign in again" }));
    const save = await screen.findByRole("button", { name: "Save" });
    expect(save.hasAttribute("disabled")).toBe(true);
    fireEvent.change(screen.getByLabelText("Token"), { target: { value: "sk-ant-oat01-x" } });
    fireEvent.click(save);
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(loginMock.postLogin).toHaveBeenCalledWith("stt", "claude-a", { action: "secret", value: "sk-ant-oat01-x" });
  });

  it("keeps the dialog and shows the reason when a key is rejected, also on retry", async () => {
    loginMock.postLogin.mockResolvedValue({ session: null, state: "failed", url: null, code: null, message: "ключ отклонён" });
    renderLogin({ ...claude, auth: "api_key", id: "or-a" });
    fireEvent.click(screen.getByRole("button", { name: "Sign in again" }));
    fireEvent.change(await screen.findByLabelText("API key"), { target: { value: "bad" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect((await screen.findByRole("alert")).textContent).toBe("ключ отклонён");
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(loginMock.postLogin).toHaveBeenCalledTimes(2));
    expect((await screen.findByRole("alert")).textContent).toBe("ключ отклонён");
    expect(screen.getByRole("dialog")).toBeTruthy();
  });
});
