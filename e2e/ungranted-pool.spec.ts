import { test, expect } from "@playwright/test";
import { TestApiClient } from "./fixtures";

test("both pool forms create ungranted pools without environments", async ({ page, request }) => {
  test.setTimeout(120_000);
  const api = new TestApiClient();
  const suffix = `${Date.now()}-${process.pid}`;
  await api.login(process.env.MULTICA_QA_ADMIN_EMAIL ?? "qa-424-admin@example.test", "Pool QA");
  const ws = await api.createWorkspace("Ungranted pool QA", `pool-qa-${suffix}`);
  await api.markUserOnboarded();
  const base = process.env.NEXT_PUBLIC_API_URL || "http://localhost:8080";
  const headers = { Authorization: `Bearer ${api.getToken()}`, "X-Workspace-ID": ws.id };
  const poolIds: string[] = [], accountKeys: string[] = [];
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.addInitScript((token) => {
    localStorage.setItem("multica_token", token!);
    localStorage.setItem("multica:chat:isOpen", "false");
  }, api.getToken());
  const environments = async () => {
    const response = await request.get(`${base}/api/workspaces/${ws.id}/runtime-profiles`, { headers });
    expect(response.ok()).toBe(true);
    return (await response.json()).runtime_profiles ?? [];
  };
  const before = await environments();
  try {
    for (const colorScheme of ["light", "dark"] as const) {
      for (const width of [1440, 375]) {
        const variant = `${colorScheme}-${width}`;
        await page.setViewportSize({ width, height: 1000 });
        await page.emulateMedia({ colorScheme, reducedMotion: "reduce" });
        await page.goto(`/${ws.slug}/usage?tab=subscriptions`, { waitUntil: "domcontentloaded" });
        const capture = async (name: string) => {
          expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(width);
          await page.screenshot({ path: test.info().outputPath(`${name}-${variant}.png`), fullPage: true, animations: "disabled" });
        };
        await page.getByRole("button", { name: "New pool", exact: true }).click();
        let dialog = page.getByRole("dialog", { name: "New pool", exact: true });
        await dialog.getByRole("checkbox", { name: ws.name, exact: true }).uncheck();
        await expect(dialog.getByRole("button", { name: "Create", exact: true })).toBeDisabled();
        await dialog.getByRole("textbox", { name: "Name", exact: true }).fill(`Standalone ${variant}`);
        await expect(dialog.getByRole("button", { name: "Create", exact: true })).toBeEnabled();
        await capture("standalone");
        const standaloneResponse = page.waitForResponse((r) => r.url().endsWith("/api/pools") && r.request().method() === "POST");
        await dialog.getByRole("button", { name: "Create", exact: true }).click();
        const created = await standaloneResponse;
        expect(created.status()).toBe(201);
        const standalone = await created.json();
        poolIds.push(standalone.id);
        expect(standalone.workspace_ids).toEqual([]);
        expect(standalone.environments).toEqual([]);
        await expect(dialog).toBeHidden();

        await page.getByRole("button", { name: "Add subscription", exact: true }).click();
        dialog = page.getByRole("dialog", { name: "Add subscription", exact: true });
        await dialog.getByRole("radio", { name: /API key/ }).check();
        await dialog.getByRole("textbox", { name: "Name", exact: true }).fill(`Account ${variant}`);
        await dialog.getByRole("button", { name: "Next", exact: true }).click();
        await dialog.getByRole("radio", { name: "New pool", exact: true }).check();
        await dialog.getByRole("checkbox", { name: ws.name, exact: true }).uncheck();
        await expect(dialog.getByRole("button", { name: "Next", exact: true })).toBeDisabled();
        await dialog.getByRole("textbox", { name: "Name", exact: true }).fill(`Wizard ${variant}`);
        await expect(dialog.getByRole("checkbox", { name: "All workspaces, including new ones" })).not.toBeChecked();
        await expect(dialog.getByRole("button", { name: "Next", exact: true })).toBeEnabled();
        await capture("wizard-pool");
        await dialog.getByRole("button", { name: "Next", exact: true }).click();
        await expect(dialog.getByTestId("connect-usage")).toContainText("no environment is created");
        await capture("wizard-usage");
        const connectedResponse = page.waitForResponse((r) => r.url().endsWith("/api/pools") && r.request().method() === "POST");
        await dialog.getByRole("button", { name: "Connect", exact: true }).click({ clickCount: 2 });
        const connected = await connectedResponse;
        expect(connected.status()).toBe(201);
        const pool = await connected.json();
        poolIds.push(pool.id);
        accountKeys.push(pool.subscriptions[0].key);
        expect(pool.workspace_ids).toEqual([]);
        expect(pool.environments).toEqual([]);
        expect(pool.subscriptions).toHaveLength(1);
        await expect(dialog).toBeHidden();
        const pools = await request.get(`${base}/api/pools`, { headers });
        expect((await pools.json()).pools.filter((p: { name: string }) => p.name === `Wizard ${variant}`)).toHaveLength(1);
        expect(await environments()).toEqual(before);
      }
    }
    expect(errors).toEqual([]);
  } finally {
    for (const id of poolIds) await request.delete(`${base}/api/pools/${id}`, { headers });
    for (const key of accountKeys) await request.delete(`${base}/api/subscriptions/${key}`, { headers });
    await api.deleteWorkspace();
  }
});
