// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Subscription } from "@multica/core/types";
import { renderWithI18n } from "../test/i18n";

const api = vi.hoisted(() => ({
  startSubscriptionLogin: vi.fn(),
  getSubscriptionLogin: vi.fn(),
  cancelSubscriptionLogin: vi.fn(),
  listSubscriptions: vi.fn(),
}));
vi.mock("@multica/core/api", () => ({ api, ApiError: class ApiError extends Error {} }));

import { SubscriptionLoginStatus } from "./subscription-login";

function sub(extra: Partial<Subscription>): Subscription {
  return {
    id: "s1", key: "acc-a", provider: "claude", family: "claude", title: "Plan A", owner_note: "", auth: "claude_oauth",
    credential_path: "", windows: [], caps: {}, effective_caps: {}, account_group: "", disabled_until: null,
    disabled: false, usage: [], used_by: [], hosts: [], login_state: "unchecked", login: null, created_at: "", updated_at: "",
    ...extra,
  };
}

const session = (state: string, extra: Record<string, unknown> = {}) => ({
  id: "l1", state, url: null, code: null, message: "", started_at: null, ...extra,
});

function renderStatus(subscription: Subscription, canManage = true) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithI18n(
    <QueryClientProvider client={client}>
      <SubscriptionLoginStatus sub={subscription} canManage={canManage} />
    </QueryClientProvider>,
  );
}

describe("SubscriptionLoginStatus", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    api.getSubscriptionLogin.mockResolvedValue(null);
    api.listSubscriptions.mockResolvedValue({ subscriptions: [], can_manage: true });
  });

  it("shows the state and hosts to members without the sign-in action", () => {
    renderStatus(
      sub({ login_state: "revoked", hosts: [{ daemon_id: "d1", device_name: "vps", credential_state: "missing", checked_at: null, error: "" }] }),
      false,
    );
    expect(screen.getByText("Sign-in revoked")).toBeTruthy();
    expect(screen.getByText("vps: no credential")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Sign in again" })).toBeNull();
  });

  it("shows nothing when the server does not report sign-in", () => {
    const { container } = renderStatus(sub({ login_state: null }));
    expect(container.textContent).toBe("");
  });

  it("offers no web sign-in for an interactive Claude login", () => {
    renderStatus(sub({ auth: "claude_login" }));
    expect(screen.queryByRole("button", { name: "Sign in again" })).toBeNull();
  });

  it("starts a device sign-in, shows the link and code, then cancels", async () => {
    api.startSubscriptionLogin.mockResolvedValue(session("waiting", { url: "https://auth.test/device", code: "ABCD-12345" }));
    api.getSubscriptionLogin.mockResolvedValue(null);
    api.cancelSubscriptionLogin.mockResolvedValue(undefined);
    renderStatus(sub({ key: "cx-a", auth: "codex_auth", provider: "codex", family: "codex" }));
    fireEvent.click(screen.getByRole("button", { name: "Sign in again" }));
    fireEvent.click(await screen.findByRole("button", { name: "Get link" }));
    expect(await screen.findByRole("link", { name: "https://auth.test/device" })).toBeTruthy();
    expect(screen.getByText("ABCD-12345")).toBeTruthy();
    expect(api.startSubscriptionLogin).toHaveBeenCalledWith("cx-a", {});
    fireEvent.click(screen.getByRole("button", { name: "Cancel sign-in" }));
    await waitFor(() => expect(api.cancelSubscriptionLogin).toHaveBeenCalledWith("cx-a"));
  });

  it("sends a pasted token, clears the field and closes once the host has it", async () => {
    api.startSubscriptionLogin.mockResolvedValue(session("waiting"));
    renderStatus(sub({}));
    fireEvent.click(screen.getByRole("button", { name: "Sign in again" }));
    const send = await screen.findByRole("button", { name: "Send" });
    expect(send.hasAttribute("disabled")).toBe(true);
    const field = screen.getByLabelText("Token") as HTMLInputElement;
    fireEvent.change(field, { target: { value: "sk-ant-oat01-x" } });
    // The daemon reports the outcome on the next poll.
    api.getSubscriptionLogin.mockResolvedValue(session("done"));
    fireEvent.click(send);
    expect(await screen.findByText("Waiting for the daemon to write the credential…")).toBeTruthy();
    expect(api.startSubscriptionLogin).toHaveBeenCalledWith("acc-a", { token: "sk-ant-oat01-x" });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull(), { timeout: 4000 });
  });

  it("keeps the dialog and shows the reason when a key is rejected", async () => {
    api.startSubscriptionLogin.mockResolvedValue(session("failed", { message: "ключ отклонён" }));
    renderStatus(sub({ auth: "api_key", provider: "openrouter" }));
    fireEvent.click(screen.getByRole("button", { name: "Sign in again" }));
    fireEvent.change(await screen.findByLabelText("API key"), { target: { value: "bad" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    expect((await screen.findByRole("alert")).textContent).toBe("ключ отклонён");
    expect((screen.getByLabelText("API key") as HTMLInputElement).value).toBe("");
    expect(screen.getByRole("dialog")).toBeTruthy();
  });
});
