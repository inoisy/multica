import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { NavigationProvider, type NavigationAdapter } from "../../navigation";
import { renderWithI18n } from "../../test/i18n";
import { customPagesKey, type CustomPagesState } from "../../custom-pages/custom-pages-api";
import { CustomPagesSection } from "./custom-pages-section";

vi.mock("@multica/core/paths", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/paths")>()),
  useWorkspaceSlug: () => "ws",
  useWorkspacePaths: () => ({
    pages: () => "/ws/pages",
    customPage: (id: string) => `/ws/pages/${id}`,
  }),
}));

function mount(pathname: string, state: CustomPagesState) {
  const adapter: NavigationAdapter = {
    pathname,
    searchParams: new URLSearchParams(),
    hash: "",
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    getShareableUrl: (p) => p,
  };
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  client.setQueryData(customPagesKey("ws"), state);
  return renderWithI18n(
    <QueryClientProvider client={client}>
      <NavigationProvider value={adapter}>
        <CustomPagesSection pathname={pathname} />
      </NavigationProvider>
    </QueryClientProvider>,
  );
}

describe("CustomPagesSection", () => {
  it("lists workspace pages: embedded ones in the app, tab ones in a new tab", () => {
    mount("/ws/pages/takes", {
      can_edit: false,
      pages: [
        { id: "takes", title: "Takes", url: "/takes/", mode: "embed", description: "" },
        { id: "docs", title: "Docs", url: "https://docs.example.test", mode: "tab", description: "Team docs" },
      ],
    });
    const embedded = screen.getByRole("link", { name: "Takes" });
    expect(embedded).toHaveAttribute("href", "/ws/pages/takes");
    expect(embedded).toHaveAttribute("aria-current", "page");
    const tab = screen.getByRole("link", { name: "Docs" });
    expect(tab).toHaveAttribute("href", "https://docs.example.test");
    expect(tab).toHaveAttribute("target", "_blank");
    expect(tab).not.toHaveAttribute("aria-current");
  });

  it("renders nothing without pages", () => {
    const { container } = mount("/ws/issues", { can_edit: true, pages: [] });
    expect(container).toBeEmptyDOMElement();
  });
});
