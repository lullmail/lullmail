import { chromium } from "playwright";
import { mkdir } from "node:fs/promises";

const baseURL = process.env.E2E_BASE_URL;
const setupToken = process.env.E2E_SETUP_TOKEN;
const output = process.env.E2E_OUTPUT || "/tmp/lullmail-e2e";
if (!baseURL || !setupToken) throw new Error("E2E_BASE_URL and E2E_SETUP_TOKEN are required");
await mkdir(output, { recursive: true });

const browser = await chromium.launch({ headless: true });
const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
const page = await context.newPage();
const errors = [];
page.on("console", (message) => { if (message.type() === "error") errors.push(message.text()); });
page.on("pageerror", (error) => errors.push(error.message));

await page.goto(baseURL, { waitUntil: "networkidle" });
await page.getByRole("button", { name: "Get started" }).click();
await page.getByPlaceholder("Setup code").fill(setupToken);
await page.getByRole("button", { name: "Continue" }).click();
await page.getByPlaceholder("Your name").fill("Owner");
await page.getByPlaceholder("Password", { exact: true }).fill("staple horse correct battery");
await page.getByRole("button", { name: "Create account" }).click();
await page.getByRole("heading", { name: "Save your recovery codes" }).waitFor();
const recoveryCode = await page.locator(".recovery-grid code").first().innerText();
await page.screenshot({ path: output + "/recovery.png", fullPage: true });
await page.getByRole("button", { name: /saved them/ }).click();
await page.getByRole("button", { name: "Connect a mailbox" }).waitFor();

await page.goto(baseURL + "/settings/security", { waitUntil: "networkidle" });
await page.getByRole("heading", { name: "Security" }).waitFor();
await page.getByRole("button", { name: "Change password" }).waitFor();
await page.screenshot({ path: output + "/security-desktop.png", fullPage: true });

await page.getByRole("button", { name: "Sign out here" }).click();
await page.getByRole("heading", { name: "Welcome back" }).waitFor();
await page.getByLabel("Email or name").fill("Owner");
await page.getByLabel("Password").fill("staple horse correct battery");
await page.locator("form:has(#gate-password) button[type=submit]").click();
await page.getByRole("button", { name: "Connect a mailbox" }).waitFor();

await page.goto(baseURL + "/settings/security", { waitUntil: "networkidle" });
await page.getByRole("button", { name: "Sign out here" }).click();
await page.getByRole("button", { name: "Other ways to sign in" }).click();
await page.getByRole("button", { name: "Use a recovery code" }).click();
await page.getByPlaceholder("Recovery code").fill(recoveryCode);
await page.locator("form:has(#fallback-code) button[type=submit]").click();
await page.getByRole("button", { name: "Connect a mailbox" }).waitFor();

await page.setViewportSize({ width: 390, height: 844 });
await page.goto(baseURL + "/settings/security", { waitUntil: "networkidle" });
await page.screenshot({ path: output + "/security-mobile.png", fullPage: true });

const deleteField = page.getByLabel("Type your email to confirm deletion");
await deleteField.fill((await deleteField.getAttribute("placeholder")) || "");
await page.getByRole("button", { name: "Delete my account" }).click();
await page.getByRole("heading", { name: "Set up your mailbox" }).waitFor();

if (errors.length) throw new Error("Browser errors:\n" + errors.join("\n"));
await browser.close();
console.log(JSON.stringify({ passwordSetup: true, passwordLogin: true, recoveryLogin: true, fullDeletion: true, screenshots: output }));
