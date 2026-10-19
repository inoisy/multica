// @vitest-environment node

import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("../api", () => ({ api: { getToken: () => "tok" } }));

import { getLoginAccounts, getLoginSession, postLogin, SubsLoginError } from "./index";

function respond(status: number, body: unknown) {
  const fetchMock = vi.fn().mockResolvedValue(
    new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }),
  );
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("subs-login client", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("reads accounts with the session token and workspace", async () => {
    const fetchMock = respond(200, {
      accounts: [{ id: "codex-a", provider: "codex", auth: "codex_auth", state: "logged_in", login: null }],
      can_login: true,
    });
    const out = await getLoginAccounts("stt");
    expect(out).toEqual({
      accounts: [{ id: "codex-a", provider: "codex", auth: "codex_auth", state: "logged_in", login: null }],
      can_login: true,
    });
    const [url, init] = fetchMock.mock.calls[0] ?? [];
    expect(url).toBe("/subs-login/api/accounts?ws=stt");
    expect(init.headers).toEqual({ Authorization: "Bearer tok" });
  });

  it("returns null when the helper is not installed", async () => {
    respond(404, {});
    expect(await getLoginAccounts("stt")).toBeNull();
  });

  it("keeps rendering on malformed responses", async () => {
    respond(200, {
      accounts: [{ id: "x", provider: 7, auth: "codex_auth", state: "something_new" }],
      can_login: "yes",
    });
    expect(await getLoginAccounts("stt")).toEqual({
      accounts: [{ id: "x", provider: "", auth: "codex_auth", state: "unchecked", login: null }],
      can_login: false,
    });
    respond(200, "nonsense");
    expect(await getLoginAccounts("stt")).toEqual({ accounts: [], can_login: false });
    respond(200, { state: "brand_new" });
    expect((await getLoginSession("stt", "x")).state).toBe("failed");
  });

  it("marks writes and surfaces server errors", async () => {
    const fetchMock = respond(403, { error: "only owner" });
    await expect(postLogin("stt", "codex-a", { action: "start" })).rejects.toEqual(new SubsLoginError(403, "only owner"));
    const [url, init] = fetchMock.mock.calls[0] ?? [];
    expect(url).toBe("/subs-login/api/login?ws=stt");
    expect(init.method).toBe("POST");
    expect(init.headers["X-Subs-Login-Request"]).toBe("1");
    expect(JSON.parse(init.body)).toEqual({ account: "codex-a", action: "start" });
  });

  it("puts the account into the query string of a session read", async () => {
    const fetchMock = respond(200, { session: "s1", state: "waiting", url: "https://x", code: "ABCD-12345" });
    expect(await getLoginSession("stt", "codex a")).toEqual({
      session: "s1",
      state: "waiting",
      url: "https://x",
      code: "ABCD-12345",
      message: "",
    });
    expect(fetchMock.mock.calls[0]?.[0]).toBe("/subs-login/api/login?account=codex%20a&ws=stt");
  });
});
