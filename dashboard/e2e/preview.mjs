// Opens a persistent local review window with a disposable virtual passkey.
// Closing the window ends this process; production sign-in is unaffected.
import { chromium } from "playwright";

const baseURL = process.env.E2E_BASE_URL || "http://localhost:8080";
const setupToken = process.env.E2E_SETUP_TOKEN;
if (!setupToken) throw new Error("E2E_SETUP_TOKEN is required");

const browser = await chromium.launch({ headless: false });
const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
const page = await context.newPage();
const cdp = await context.newCDPSession(page);
await cdp.send("WebAuthn.enable");
await cdp.send("WebAuthn.addVirtualAuthenticator", { options: {
  protocol: "ctap2", transport: "internal", hasResidentKey: true,
  hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true,
} });

await page.goto(baseURL, { waitUntil: "networkidle" });
await page.getByRole("button", { name: "Get started" }).click();
await page.getByPlaceholder("Setup code").fill(setupToken);
await page.getByRole("button", { name: "Continue" }).click();
await page.getByPlaceholder("Your name").fill("Owner");
await page.getByRole("button", { name: "Create my passkey" }).click();
await page.getByRole("heading", { name: "Save your recovery codes" }).waitFor();
await page.getByRole("button", { name: /saved them/ }).click();
await page.goto(baseURL + "/today", { waitUntil: "networkidle" });
console.log(JSON.stringify({ ready: true, url: page.url() }));

await new Promise((resolve) => browser.once("disconnected", resolve));
