import { test, expect } from "@playwright/test";
import { TestApiClient } from "./fixtures";

test("profiles route renders its client exports and opens the profile editor", async ({ page }) => {
  test.setTimeout(120_000);
  const api = new TestApiClient();
  const suffix = `${Date.now()}-${process.pid}`;
  await api.login(`profiles-${suffix}@multica.ai`, "Profiles E2E");
  const workspace = await api.ensureWorkspace("Profiles E2E", `profiles-${suffix}`);
  await api.markUserOnboarded();
  await api.seedRuntime("Profiles test runtime");
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
        const response = await page.goto(`/${workspace.slug}/profiles`, { waitUntil: "domcontentloaded" });
        expect(response?.status()).toBe(200);
        await expect(page.getByRole("heading", { name: "Model profiles", exact: true })).toBeVisible();
        await page.screenshot({ path: test.info().outputPath(`profiles-${variant}.png`), fullPage: true, animations: "disabled" });
        await page.getByRole("button", { name: "Create profile", exact: true }).click();
        const editor = page.getByRole("dialog", { name: "New profile" });
        await expect(editor).toBeVisible();
        await editor.getByRole("textbox", { name: "Name", exact: true }).fill("Profile smoke");
        await editor.getByRole("button", { name: "Add model", exact: true }).click();
        await expect(editor.getByText("Primary", { exact: true })).toBeVisible();
        expect(await editor.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
        // Pickers of the rows sit at the height of the form's Input and Select.
        const height = (name: RegExp | string) =>
          editor.getByRole(typeof name === "string" ? "combobox" : "button", { name, exact: typeof name === "string" }).first()
            .evaluate((element) => element.getBoundingClientRect().height);
        const selectHeight = await height("When everything is on a limit");
        expect(await editor.getByRole("textbox", { name: "Name", exact: true }).evaluate((element) => element.getBoundingClientRect().height)).toBe(selectHeight);
        expect(await height(/^Runtime · /)).toBe(selectHeight);
        await page.screenshot({ path: test.info().outputPath(`profile-editor-${variant}.png`), fullPage: true, animations: "disabled" });
        await editor.getByRole("combobox", { name: "When everything is on a limit", exact: true }).click();
        await expect(page.getByRole("option", { name: "wait", exact: true })).toBeVisible();
        await page.screenshot({ path: test.info().outputPath(`profile-select-${variant}.png`), fullPage: true, animations: "disabled" });
        expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(width);
      }
    }
    expect(errors).toEqual([]);
  } finally {
    await api.deleteWorkspace();
  }
});
