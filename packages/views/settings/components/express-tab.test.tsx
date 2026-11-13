// @vitest-environment jsdom

import { type ReactNode } from "react";
import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enSettings from "../../locales/en/settings.json";

type MemberRole = "owner" | "admin" | "member" | "guest";

const membersRef = vi.hoisted(() => ({
  current: [{ user_id: "user-1", role: "owner" as MemberRole }],
}));
const installationsRef = vi.hoisted(() => ({
  current: {
    installations: [] as unknown[],
    configured: true,
    install_supported: true,
  },
}));
const mockRegister = vi.hoisted(() => vi.fn());
const mockDeleteInstallation = vi.hoisted(() => vi.fn());
const mockInvalidate = vi.hoisted(() => vi.fn());
const mockToastError = vi.hoisted(() => vi.fn());
const queryErrorRef = vi.hoisted(() => ({ current: false }));
const queryLoadingRef = vi.hoisted(() => ({ current: false }));

vi.mock("@tanstack/react-query", () => ({
  useQuery: (opts: { queryKey: unknown[]; enabled?: boolean }) => {
    if (opts.enabled === false) return { data: undefined, isLoading: false };
    const key = JSON.stringify(opts.queryKey);
    if (key.includes("members")) return { data: membersRef.current, isLoading: false };
    if (key.includes("installations")) {
      return {
        data: queryLoadingRef.current ? undefined : installationsRef.current,
        isLoading: queryLoadingRef.current,
        isError: queryErrorRef.current,
      };
    }
    return { data: undefined, isLoading: false };
  },
  useQueryClient: () => ({ invalidateQueries: mockInvalidate }),
  queryOptions: <T,>(opts: T) => opts,
}));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace-1" }));

vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({ queryKey: ["members"], queryFn: vi.fn() }),
}));

vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({
    getAgentName: (agentId: string) => `Agent ${agentId}`,
    getMemberName: () => "Unknown",
    getSquadName: () => "Unknown Squad",
    getActorName: () => "Unknown",
    getActorInitials: () => "??",
    getActorAvatarUrl: () => null,
  }),
}));

vi.mock("../../common/actor-avatar", () => ({
  ActorAvatar: ({ actorId }: { actorId: string }) => (
    <span data-testid="actor-avatar" data-actor-id={actorId} />
  ),
}));

vi.mock("@multica/core/express", () => ({
  expressInstallationsOptions: () => ({
    queryKey: ["express", "installations"],
    queryFn: vi.fn(),
  }),
  expressKeys: { installations: (wsId: string) => ["express", "installations", wsId] },
}));

vi.mock("@multica/core/api", () => ({
  api: {
    registerExpressBot: mockRegister,
    deleteExpressInstallation: mockDeleteInstallation,
    getBaseUrl: () => "https://api.example.test",
  },
}));

vi.mock("@multica/core/auth", () => {
  const useAuthStore = Object.assign(
    (sel?: (s: { user: { id: string } }) => unknown) =>
      sel ? sel({ user: { id: "user-1" } }) : { user: { id: "user-1" } },
    { getState: () => ({ user: { id: "user-1" } }) },
  );
  return { useAuthStore };
});

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: mockToastError, message: vi.fn() },
}));


import { ExpressAgentBindButton, ExpressTab } from "./express-tab";

const TEST_RESOURCES = { en: { common: enCommon, settings: enSettings } };

function renderUI(children: ReactNode) {
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      {children}
    </I18nProvider>,
  );
}

function resetFixtures() {
  vi.clearAllMocks();
  membersRef.current = [{ user_id: "user-1", role: "owner" }];
  installationsRef.current = { installations: [], configured: true, install_supported: true };
  queryErrorRef.current = false;
  queryLoadingRef.current = false;
}

describe("ExpressAgentBindButton", () => {
  beforeEach(resetFixtures);

  it("shows the bot URL for CTS and submits the trimmed registration", async () => {
    mockRegister.mockResolvedValue({ id: "i1", agent_id: "agent-1", status: "active" });
    renderUI(<ExpressAgentBindButton agentId="agent-1" agentName="Bot" />);
    await userEvent.click(screen.getByTestId("express-agent-connect"));
    expect(await screen.findByTestId("express-bot-url")).toHaveValue(
      "https://api.example.test/api/express",
    );
    const submit = screen.getByTestId("express-connect-submit");
    expect(submit).toBeDisabled();
    await userEvent.type(screen.getByTestId("express-cts-url"), " https://cts.example.com ");
    await userEvent.type(screen.getByTestId("express-bot-id"), "bot-uuid");
    expect(screen.getByTestId("express-secret-key")).toHaveAttribute("type", "password");
    await userEvent.type(screen.getByTestId("express-secret-key"), "secret");
    await userEvent.click(submit);
    await waitFor(() =>
      expect(mockRegister).toHaveBeenCalledWith("workspace-1", "agent-1", {
        cts_url: "https://cts.example.com",
        bot_id: "bot-uuid",
        secret_key: "secret",
        bot_name: "",
      }),
    );
  });

  it("does not report success for a malformed install response", async () => {
    mockRegister.mockResolvedValue({ id: "", status: "revoked" });
    renderUI(<ExpressAgentBindButton agentId="agent-1" />);
    await userEvent.click(screen.getByTestId("express-agent-connect"));
    await userEvent.type(await screen.findByTestId("express-cts-url"), "cts.example.com");
    await userEvent.type(screen.getByTestId("express-bot-id"), "b");
    await userEvent.type(screen.getByTestId("express-secret-key"), "s");
    await userEvent.click(screen.getByTestId("express-connect-submit"));
    await waitFor(() => expect(mockToastError).toHaveBeenCalled());
  });

  it("renders nothing for a non-manager", () => {
    membersRef.current = [{ user_id: "user-1", role: "member" }];
    const { container } = renderUI(<ExpressAgentBindButton agentId="agent-1" />);
    expect(container).toBeEmptyDOMElement();
  });
});

describe("ExpressTab", () => {
  beforeEach(resetFixtures);

  it("surfaces the not-enabled notice when the deployment has no eXpress key", () => {
    installationsRef.current = { installations: [], configured: false, install_supported: false };
    renderUI(<ExpressTab />);
    expect(screen.getByText("MULTICA_EXPRESS_SECRET_KEY")).toBeInTheDocument();
  });

  it("lists a connected bot with its CTS host", () => {
    installationsRef.current = {
      installations: [
        {
          id: "i1",
          agent_id: "agent-1",
          status: "active",
          cts_url: "https://cts.example.com",
          bot_name: "",
          installed_at: "2026-11-13T09:00:00Z",
        },
      ],
      configured: true,
      install_supported: true,
    };
    renderUI(<ExpressTab />);
    expect(screen.getByText("Agent agent-1")).toBeInTheDocument();
    expect(screen.getByText("cts.example.com")).toBeInTheDocument();
  });
});
