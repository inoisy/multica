#!/usr/bin/env node
import { createHash } from "node:crypto";
import { createRequire } from "node:module";
import { copyFile, mkdir, readFile, readdir, writeFile } from "node:fs/promises";
import path from "node:path";

const [mode, source, destination, provenance] = process.argv.slice(2);
if (!["capture", "archive"].includes(mode) || !source || !destination) {
  console.error("Usage: node tools/product/capture.mjs capture CONFIG.json OUT | archive DIRECTORY OUT PROVENANCE.json");
  process.exit(2);
}
await mkdir(destination, { recursive: true, mode: 0o700 });
const sha256 = bytes => createHash("sha256").update(bytes).digest("hex");
const manifest = { version: 1, captured_at: new Date().toISOString(), mode, images: [] };
if (mode === "archive") {
  if (!provenance) throw new Error("Archive requires provenance: issue, code_sha and evidence_kind");
  const metadata = JSON.parse(await readFile(provenance, "utf8"));
  if (!metadata.issue || !metadata.evidence_kind || !(metadata.source_revision || metadata.code_sha)) throw new Error("Provenance needs issue, evidence_kind and source_revision or code_sha");
  if (["version", "captured_at", "mode", "images"].some(key => key in metadata)) throw new Error("Provenance cannot override manifest fields");
  Object.assign(manifest, metadata);
  for (const name of (await readdir(source)).sort()) {
    if (!/\.(png|webp)$/.test(name)) continue;
    await copyFile(path.join(source, name), path.join(destination, name));
    manifest.images.push({ file: name, sha256: sha256(await readFile(path.join(destination, name))) });
  }
  if (!manifest.images.length) throw new Error("Archive contains no screenshots");
} else {
  const config = JSON.parse(await readFile(source, "utf8"));
  if (!config.evidence_kind || !config.source_revision) throw new Error("Set evidence_kind and source_revision; do not label a mockup as deployed UI");
  manifest.evidence_kind = config.evidence_kind;
  manifest.source_revision = config.source_revision;
  const require = createRequire(import.meta.url);
  const { chromium } = require(process.env.PRODUCT_PLAYWRIGHT_MODULE || "playwright");
  const browser = await chromium.launch({ headless: true, ...(process.env.PRODUCT_CHROMIUM_EXECUTABLE ? { executablePath: process.env.PRODUCT_CHROMIUM_EXECUTABLE } : {}) });
  try {
    for (const entry of config.pages) {
      if (!/^[a-z0-9-]+$/.test(entry.name)) throw new Error("Page name must be a safe filename");
      for (const theme of ["light", "dark"]) for (const width of [1440, 375]) {
        const context = await browser.newContext({ viewport: { width, height: 1000 }, colorScheme: theme,
          ...(process.env.PRODUCT_STORAGE_STATE ? { storageState: process.env.PRODUCT_STORAGE_STATE } : {}) });
        try {
          await context.addInitScript(({ theme, themeStorageKey }) => {
            if (themeStorageKey) localStorage.setItem(themeStorageKey, theme);
            const apply = () => {
              if (!document.documentElement) return;
              document.documentElement.classList.toggle("dark", theme === "dark");
              document.documentElement.dataset.theme = theme;
              document.documentElement.style.colorScheme = theme;
            };
            new MutationObserver(apply).observe(document, { childList: true });
            apply();
          }, { theme, themeStorageKey: config.theme_storage_key });
          const page = await context.newPage();
          const errors = [];
          page.on("pageerror", err => errors.push(err.message));
          const response = await page.goto(new URL(entry.path, config.base_url).href, { waitUntil: "load" });
          if (response && !response.ok()) throw new Error(`${entry.name}: HTTP ${response.status()}`);
          await page.locator(entry.ready || "main").waitFor({ state: "visible" });
          for (const action of entry.actions || []) {
            if (action.click) await page.locator(action.click).click();
            else if (action.select) await page.locator(action.select).selectOption(action.value);
            else throw new Error("Unknown capture action");
          }
          await page.evaluate(theme => {
            document.documentElement.classList.toggle("dark", theme === "dark");
            document.documentElement.dataset.theme = theme;
            document.documentElement.style.colorScheme = theme;
          }, theme);
          await page.evaluate(() => document.fonts.ready);
          const audit = await page.evaluate(() => ({
            document_overflow: document.documentElement.scrollWidth > innerWidth,
            controls: [...document.querySelectorAll("select,input,button,[role=combobox]")].map(el => {
              const rect = el.getBoundingClientRect(), style = getComputedStyle(el);
              return { tag: el.tagName.toLowerCase(), role: el.getAttribute("role"), width: rect.width,
                height: rect.height, right: rect.right, padding_right: style.paddingRight,
                appearance: style.appearance, background_image: style.backgroundImage !== "none" };
            }),
          }));
          const file = `${entry.name}-${theme}-${width}.png`;
          const bytes = await page.screenshot({ path: path.join(destination, file), fullPage: true, animations: "disabled" });
          manifest.images.push({ file, theme, width, sha256: sha256(bytes), audit, errors });
          console.log(file);
        } finally { await context.close(); }
      }
    }
  } finally { await browser.close(); }
}
await writeFile(path.join(destination, "manifest.json"), JSON.stringify(manifest, null, 2) + "\n", { mode: 0o600 });
console.log(`${manifest.images.length} images; manifest.json`);
