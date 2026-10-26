import { test, expect } from "@playwright/test";
import { TestApiClient } from "./fixtures";

test("subscription forms and run diagnostics fit both themes and mobile", async ({ page, request }) => {
  test.setTimeout(180_000);
  page.setDefaultTimeout(15_000);
  const api = new TestApiClient();
  const suffix = `${Date.now()}-${process.pid}`;
  // The isolated server explicitly grants this fixture instance-admin access.
  await api.login(process.env.MULTICA_QA_ADMIN_EMAIL ?? "qa-424-admin@example.test", "Layout QA");
  const ws = await api.createWorkspace("Acceptance layout", `layout-${suffix}`);
  await api.markUserOnboarded();
  const runtime = await api.seedRuntime("Diagnostic runtime");
  const issue = await api.createIssue("Diagnostic run");
  await api.seedCompletedRun(issue.id, runtime);
  // A run on plain agent fields stores no choice, only the confirmed account.
  const fieldsIssue = await api.createIssue("Run without a choice");
  await api.seedCompletedRun(fieldsIssue.id, runtime, { withChoice: false });
  const apiBase = process.env.NEXT_PUBLIC_API_URL || "http://localhost:8080";
  const headers = { Authorization: `Bearer ${api.getToken()}`, "X-Workspace-ID": ws.id };
  const sub = await request.post(`${apiBase}/api/subscriptions`, { headers, data: { key: `layout-${suffix}`, title: "Layout account", provider: "claude" } });
  expect(sub.status()).toBe(201);
  const account = await sub.json();
  const pool = await request.post(`${apiBase}/api/runtime-pools`, { headers, data: { display_name: `Layout pool ${suffix}`, runtime_type: "claude", subscriptions: [account.key], workspace_ids: [ws.id] } });
  expect(pool.status()).toBe(201);
  const poolId = (await pool.json()).pool_id;

  await page.addInitScript((token) => {
    localStorage.setItem("multica_token", token!);
    localStorage.setItem("multica:chat:isOpen", "false");
  }, api.getToken());
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  try {
    for (const colorScheme of ["light", "dark"] as const) {
      for (const width of [1440, 375]) {
        const variant = `${colorScheme}-${width}`;
        await page.setViewportSize({ width, height: 1000 });
        await page.emulateMedia({ colorScheme, reducedMotion: "reduce" });
        await page.goto(`/${ws.slug}/usage?tab=subscriptions`, { waitUntil: "domcontentloaded" });
        await expect(page.getByTestId(`pool-group-${poolId}`).getByText(/ · 1 workspace$/)).toBeVisible();
        await page.screenshot({ path: test.info().outputPath(`subscriptions-${variant}.png`), fullPage: true, animations: "disabled" });
        await page.getByRole("button", { name: "Add subscription", exact: true }).click();
        const dialog = page.getByRole("dialog", { name: "Add subscription", exact: true });
        await expect(dialog.getByPlaceholder("Claude account", { exact: true })).toBeVisible();
        expect(await dialog.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
        await page.screenshot({ path: test.info().outputPath(`subscription-form-${variant}.png`), fullPage: true, animations: "disabled" });
        await dialog.evaluate((element) => { element.scrollTop = element.scrollHeight; });
        await page.screenshot({ path: test.info().outputPath(`subscription-form-bottom-${variant}.png`), fullPage: true, animations: "disabled" });
        await dialog.evaluate((element) => { element.scrollTop = 0; });
        await dialog.getByRole("radio", { name: /API key/ }).check();
        const provider = dialog.getByRole("combobox", { name: "Provider", exact: true });
        expect(await provider.evaluate((element) => parseFloat(getComputedStyle(element).paddingRight))).toBeGreaterThanOrEqual(28);
        await provider.click();
        await expect(page.getByRole("option").first()).toBeVisible();
        await page.screenshot({ path: test.info().outputPath(`subscription-select-${variant}.png`), fullPage: true, animations: "disabled" });
        await page.keyboard.press("Escape");
        await page.keyboard.press("Escape");

        await page.goto(`/${ws.slug}/issues/${issue.id}`, { waitUntil: "domcontentloaded" });
        await expect(page.getByRole("heading", { name: "Activity", exact: true })).toBeVisible();
        // Wait for the responsive layout to replace its SSR desktop tree.
        if (width === 375) await expect(page.getByRole("button", { name: "Execution log", exact: true })).toHaveCount(0);
        await page.getByRole("button", { name: "View activity", exact: true }).first().click();
        await page.getByRole("button", { name: "Open full log", exact: true }).first().click();
        await page.getByRole("button", { name: "Run details", exact: true }).click();
        await expect(page.getByText("confirmed by the daemon", { exact: false })).toBeVisible();
        const accountRow = page.getByText("diagnostic-account · confirmed by the daemon", { exact: false });
        expect(await accountRow.evaluate((element) => getComputedStyle(element).wordBreak)).toBe("normal");
        await page.screenshot({ path: test.info().outputPath(`run-details-${variant}.png`), fullPage: true, animations: "disabled" });
        expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(width);

        await page.goto(`/${ws.slug}/issues/${fieldsIssue.id}`, { waitUntil: "domcontentloaded" });
        await expect(page.getByRole("heading", { name: "Activity", exact: true })).toBeVisible();
        if (width === 375) await expect(page.getByRole("button", { name: "Execution log", exact: true })).toHaveCount(0);
        await page.getByRole("button", { name: "View activity", exact: true }).first().click();
        await page.getByRole("button", { name: "Open full log", exact: true }).first().click();
        await page.getByRole("button", { name: "Run details", exact: true }).click();
        await expect(page.getByText("diagnostic-account · confirmed by the daemon", { exact: false })).toBeVisible();
        await expect(page.getByText("Choice", { exact: true })).toHaveCount(0);
        await page.screenshot({ path: test.info().outputPath(`run-details-fields-${variant}.png`), fullPage: true, animations: "disabled" });
      }
    }
    expect(errors).toEqual([]);
  } finally {
    await request.delete(`${apiBase}/api/pools/${poolId}`, { headers });
    await request.delete(`${apiBase}/api/subscriptions/${account.key}`, { headers });
    await api.deleteWorkspace();
  }
});
