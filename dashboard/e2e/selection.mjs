import { chromium, expect } from "@playwright/test";
import { createServer } from "vite";
import preact from "@preact/preset-vite";
import { existsSync } from "node:fs";
import { mkdir, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("../", import.meta.url));
// Ctrl on Linux/Windows, Cmd on macOS: Ctrl+click is a context-menu click and
// Ctrl+A is "start of line" in a native text field there.
const mod = process.platform === "darwin" ? "Meta" : "Control";
const output = process.env.E2E_OUTPUT || "/tmp/lullmail-selection-browser";
const server = await createServer({
  root, configFile: false, plugins: [preact(), {
    name: "selection-fixture",
    configureServer(server) {
      server.middlewares.use("/__selection_test", (_req, res) => {
        res.setHeader("Content-Type", "text/html");
        res.end('<!doctype html><html data-theme="light"><head><link rel="stylesheet" href="/styles.css"></head><body><div id="app"></div><script type="module" src="/e2e/selection-fixture.tsx"></script></body></html>');
      });
    },
  }], server: { host: "127.0.0.1", port: 0 },
  // Pre-bundle the fixture's deps before the first request; otherwise a cold
  // cache makes Vite re-optimize and reload the page mid-test.
  optimizeDeps: { entries: ["e2e/selection-fixture.tsx"] },
});
let browser, page;
const requests = [];
const errors = [];
let completed = false;
let failure = null;
try {
  await server.listen();
  const address = server.httpServer.address();
  browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || (existsSync("/usr/bin/chromium") ? "/usr/bin/chromium" : undefined),
  });
  page = await browser.newPage({ viewport: { width: 1280, height: 1100 } });
  page.on("pageerror", (error) => errors.push(error.message));
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    requests.push({ path: url.pathname, method: route.request().method() });
    let result = {};
    if (url.pathname.startsWith("/api/threads/")) result = [{
      id: "opened", account: "test-account", subject: "Opened synthetic mail", from: "sender@example.test",
      to: "owner@example.test", received_at: "2026-10-01T12:00:00Z", bucket: "imbox", body: "Synthetic only",
    }];
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(result) });
  });
  await page.goto(`http://127.0.0.1:${address.port}/__selection_test`);
  const rows = page.locator(".msg-row");
  const selected = page.getByTestId("selected");
  const clickSubject = (n, modifiers = []) => rows.nth(n).locator(".row-subject").click({ modifiers });
  await expect(rows).toHaveCount(8);
  await clickSubject(1, [mod]);
  await clickSubject(4, ["Shift"]);
  await expect(selected).toHaveText("thread-1,thread-2,thread-3,thread-4");
  await clickSubject(2, ["Shift"]);
  await expect(selected).toHaveText("thread-1,thread-2");
  await clickSubject(7, ["Meta"]);
  await expect(selected).toHaveText("thread-1,thread-2,thread-7");
  expect(requests).toHaveLength(0);
  await page.keyboard.press("Escape");
  await expect(selected).toHaveText("");
  await rows.nth(2).hover();
  await rows.nth(2).locator(".row-check").click();
  await expect(rows.nth(2).locator(".row-check")).toBeFocused();
  await page.keyboard.press("Shift+ArrowDown");
  await expect(selected).toHaveText("thread-2,thread-3");
  await expect(rows.nth(3)).toBeFocused();
  await page.keyboard.press(`${mod}+a`);
  await expect(page.locator(".msg-row.picked")).toHaveCount(8);
  await page.keyboard.press("Escape");
  await expect(page.getByTestId("cursor")).toHaveText("3");
  await page.keyboard.press("Space");
  await expect(selected).toHaveText("thread-3");
  await page.getByRole("textbox", { name: "Typing field" }).fill("untouched input");
  await page.keyboard.press(`${mod}+a`);
  await expect(selected).toHaveText("thread-3");
  await page.keyboard.type("x");
  await expect(page.getByRole("textbox", { name: "Typing field" })).toHaveValue("x");
  await clickSubject(5, [mod]);
  await page.evaluate(() => window.selectionFixture.reorder());
  await expect(page.getByTestId("cursor")).toHaveText("2");
  await expect(selected).toHaveText("thread-5,thread-3");
  await clickSubject(0, ["Shift"]);
  await expect(selected).toHaveText("thread-7,thread-6,thread-5");
  await rows.nth(0).hover();
  await rows.nth(0).getByRole("button", { name: "Set aside", exact: true }).click();
  // The menu moves focus into itself in an effect; Escape only closes it once it holds focus.
  await expect(page.getByRole("menu").getByRole("menuitem").first()).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("menu")).toHaveCount(0);
  await expect(selected).toHaveText("thread-7,thread-6,thread-5");
  await mkdir(output, { recursive: true });
  await page.screenshot({ path: output + "/selection-light.png", fullPage: true });
  await page.evaluate(() => document.documentElement.setAttribute("data-theme", "dark"));
  await page.screenshot({ path: output + "/selection-dark.png", fullPage: true });
  await clickSubject(4);
  await expect(selected).toHaveText("");
  await expect(page.getByTestId("reader")).toHaveText("thread-3");
  expect(requests.filter((request) => request.path.startsWith("/api/threads/"))).toHaveLength(1);
  expect(errors).toEqual([]);
  completed = true;
  console.log("Chromium selection checks passed: real pointer modifiers, checkbox focus, ranges, select-all, native input, reorder, menu Escape, plain-open, light/dark screenshots; synthetic API only.");
} catch (error) {
  failure = error instanceof Error ? error.message : String(error);
  throw error;
} finally {
  // Preserve both early launch failures and native-UI assertion failures.
  // All requests contain synthetic fixture paths only, never mailbox data.
  await mkdir(output, { recursive: true });
  await writeFile(output + "/selection-report.json", JSON.stringify({
    completed, browserCreated: !!browser, pageCreated: !!page, failure, requests, errors,
  }, null, 2) + "\n");
  if (!completed && page) {
    await page.screenshot({ path: output + "/selection-failure.png", fullPage: true }).catch(() => {});
  }
  await browser?.close();
  await server.close();
}
