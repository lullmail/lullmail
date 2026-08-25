import { chromium } from "playwright";
import { mkdir, writeFile } from "node:fs/promises";

const baseURL = process.env.E2E_BASE_URL;
const setupToken = process.env.E2E_SETUP_TOKEN;
const output = process.env.E2E_OUTPUT || "/tmp/email-soft-visual";
if (!baseURL || !setupToken) throw new Error("E2E_BASE_URL and E2E_SETUP_TOKEN are required");
await mkdir(output, { recursive: true });

const browser = await chromium.launch({ headless: true });
const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
const page = await context.newPage();
const errors = [];
page.on("console", (message) => { if (message.type() === "error") errors.push(message.text()); });
page.on("pageerror", (error) => errors.push(error.message));

const cdp = await context.newCDPSession(page);
await cdp.send("WebAuthn.enable");
await cdp.send("WebAuthn.addVirtualAuthenticator", { options: {
  protocol: "ctap2", transport: "internal", hasResidentKey: true,
  hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true,
} });

await page.goto(baseURL, { waitUntil: "networkidle" });
await page.getByPlaceholder("Owner email").fill("owner@example.test");
await page.getByPlaceholder("One-time setup token").fill(setupToken);
await page.getByRole("button", { name: "Create passkey" }).click();
await page.getByRole("heading", { name: "Save your recovery codes" }).waitFor();
await page.getByRole("button", { name: /I saved them/ }).click();
await page.getByRole("link", { name: "Today", exact: true }).waitFor();

const routes = [
  ["today", "/today"], ["imbox", "/"], ["board", "/board"],
  ["calendar", "/calendar"], ["notes", "/notes"], ["people", "/people"],
  ["reading", "/reading"], ["receipts", "/receipts"],
  ["screener", "/screener"], ["snoozed", "/snoozed"],
  ["accounts", "/settings/accounts"], ["security", "/settings/security"],
];
const audits = [];

async function capture(mode, name, route) {
  const started = Date.now();
  await page.goto(baseURL + route, { waitUntil: "networkidle" });
  await page.screenshot({ path: `${output}/${mode}-${name}.png`, fullPage: true });
  const metrics = await page.evaluate(() => ({
    width: document.documentElement.scrollWidth,
    viewport: document.documentElement.clientWidth,
    height: document.documentElement.scrollHeight,
    buttonsWithoutName: [...document.querySelectorAll("button")].filter((el) =>
      !(el.textContent || "").trim() && !el.getAttribute("aria-label") && !el.getAttribute("title")
    ).length,
    tinyTargets: [...document.querySelectorAll("button, a, input, textarea, select")].filter((el) => {
      const box = el.getBoundingClientRect();
      return box.width > 0 && box.height > 0 && (box.width < 32 || box.height < 32);
    }).length,
  }));
  audits.push({ mode, name, route, elapsedMs: Date.now() - started, ...metrics });
}

for (const [name, route] of routes) await capture("desktop", name, route);

await page.goto(baseURL + "/today", { waitUntil: "networkidle" });
await page.getByRole("button", { name: "Compose" }).click();
await page.getByPlaceholder("Write something worth reading.").waitFor();
await page.screenshot({ path: `${output}/desktop-compose.png`, fullPage: true });
await page.keyboard.press("Escape");
await page.getByRole("button", { name: "Search and jump" }).click();
await page.getByPlaceholder("Search the mail, or jump anywhere…").fill("studio");
await page.screenshot({ path: `${output}/desktop-search.png`, fullPage: true });
await page.keyboard.press("Escape");
await page.getByRole("button", { name: "Settings and shortcuts" }).click();
await page.getByRole("menu").waitFor();
await page.screenshot({ path: `${output}/desktop-menu.png`, fullPage: true });
await page.keyboard.press("Escape");

await page.goto(baseURL, { waitUntil: "networkidle" });
await page.getByText("Studio review: a calmer first screen", { exact: true }).click();
await page.getByRole("heading", { name: "Studio review: a calmer first screen" }).waitFor();
await page.getByText("1 remote image blocked", { exact: false }).waitFor();
await page.screenshot({ path: `${output}/desktop-thread.png`, fullPage: true });

await page.evaluate(() => localStorage.setItem("es-theme", "sepia"));
await page.goto(baseURL + "/today", { waitUntil: "networkidle" });
await page.screenshot({ path: `${output}/desktop-today-sepia.png`, fullPage: true });
await page.evaluate(() => localStorage.setItem("es-theme", "dark"));
await page.reload({ waitUntil: "networkidle" });
await page.screenshot({ path: `${output}/desktop-today-dark.png`, fullPage: true });
await page.evaluate(() => { localStorage.setItem("es-theme", "light"); localStorage.setItem("es-layout", "classic"); });
await page.goto(baseURL, { waitUntil: "networkidle" });
await page.screenshot({ path: `${output}/desktop-classic.png`, fullPage: true });
await page.getByText("Dinner on Thursday?", { exact: true }).click();
await page.getByRole("heading", { name: "Dinner on Thursday?" }).waitFor();
await page.screenshot({ path: `${output}/desktop-classic-thread.png`, fullPage: true });
await page.goto(baseURL + "/board", { waitUntil: "networkidle" });
await page.screenshot({ path: `${output}/desktop-classic-board.png`, fullPage: true });
await page.evaluate(() => localStorage.setItem("es-layout", "document"));

await page.setViewportSize({ width: 390, height: 844 });
for (const [name, route] of routes) await capture("mobile", name, route);

await writeFile(`${output}/audit.json`, JSON.stringify({ errors, audits }, null, 2));
await browser.close();
if (errors.length) throw new Error("Browser errors:\n" + errors.join("\n"));
console.log(JSON.stringify({ screenshots: routes.length * 2 + 9, output }));
