// @vitest-environment node
import { QueryClient } from "@tanstack/react-query";
import { afterEach, expect, it, vi } from "vitest";
import { customPagesOptions } from "./custom-pages-api";

const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
afterEach(() => { vi.unstubAllGlobals(); client.clear(); });

it("keeps an unknown page mode outside the app and malformed permissions read-only", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
    pages: [{ id: "page-1", title: "Tools", url: "https://example.test", mode: "future-mode" }],
    can_edit: "true",
  }))));
  const state = await client.fetchQuery(customPagesOptions("stt"));
  expect(state).toEqual({
    pages: [{ id: "page-1", title: "Tools", url: "https://example.test", mode: "tab", description: "" }],
    can_edit: false,
  });
});

it("reports a malformed pages list rather than presenting it as empty", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
    pages: [{ title: "Missing page identity and address" }], can_edit: true,
  }))));
  await expect(client.fetchQuery(customPagesOptions("stt"))).rejects.toThrow("Could not read workspace pages");
});
