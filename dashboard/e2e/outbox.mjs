// Real-browser recovery acceptance for the durable outbox.
//
// Boots a DISPOSABLE stack on loopback only: the loopback SMTP/IMAP sink
// (tools/outbox-sink, fault-injecting), a PostgreSQL schema of its own inside
// the database named by DATABASE_URL, and the built lullmail binary on a free
// port. Chromium then drives the real dashboard. No real mailbox, SMTP, IMAP
// or provider is ever contacted: the seeded account points at the sink.
//
//   DATABASE_URL=postgres://u@host:port/db?sslmode=disable npm run test:outbox
//
// Requirements: a built dashboard (npm run build, the Go binary embeds
// dashboard/dist), Go (unless LULL_E2E_BIN and OUTBOX_SINK_BIN are given),
// psql (PSQL=/path to override) and Chromium (CHROMIUM_PATH to override).
// Everything it creates lives in one schema (outbox_e2e_*) that is dropped at
// the end, and every child process it starts is killed.
import { chromium } from "@playwright/test";
import { spawn, execFileSync } from "node:child_process";
import { createCipheriv, createHash, randomBytes, randomUUID } from "node:crypto";
import { existsSync, mkdtempSync, openSync, readFileSync, rmSync } from "node:fs";
import { mkdir, writeFile } from "node:fs/promises";
import net from "node:net";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const dashboard = fileURLToPath(new URL("../", import.meta.url));
const repo = path.resolve(dashboard, "..");
const output = process.env.E2E_OUTPUT || "/tmp/lullmail-outbox-browser";
const databaseURL = process.env.DATABASE_URL;
if (!databaseURL) throw new Error("DATABASE_URL is required (a disposable PostgreSQL database; the run uses its own schema inside it)");
const psqlBin = process.env.PSQL || "psql";
const SECRET_KEY = "outbox-e2e-secret-key-0123456789abcdef";
const PASSWORD = "staple horse correct battery";
const schema = "outbox_e2e_" + randomBytes(4).toString("hex");
const work = mkdtempSync(path.join(tmpdir(), "outbox-e2e-"));
const setupToken = "setup-" + randomBytes(8).toString("hex");

// ---------------------------------------------------------------- utilities
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const stamp = () => new Date().toISOString().slice(11, 19);
const log = (...args) => console.log(stamp(), ...args);
const tag = () => randomBytes(3).toString("hex");

async function poll(what, fn, { timeout = 90_000, interval = 250 } = {}) {
  const end = Date.now() + timeout;
  let lastError;
  for (;;) {
    try { const value = await fn(); if (value) return value; } catch (error) { lastError = error; }
    if (Date.now() > end) throw new Error(`timed out after ${timeout} ms waiting for ${what}${lastError ? ` (last error: ${lastError.message})` : ""}`);
    await sleep(interval);
  }
}

// Holds an invariant for a wall-clock window by sampling it, instead of sleeping blind.
async function observe(what, ms, invariant, interval = 1000) {
  const end = Date.now() + ms;
  let samples = 0;
  for (;;) {
    const problem = await invariant();
    samples++;
    if (problem) throw new Error(`${what}: ${problem} (sample ${samples})`);
    if (Date.now() >= end) return samples;
    await sleep(Math.min(interval, Math.max(0, end - Date.now())));
  }
}

function freePort() {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.once("error", reject);
    srv.listen(0, "127.0.0.1", () => { const { port } = srv.address(); srv.close(() => resolve(port)); });
  });
}

// ---------------------------------------------------------------- database
const psqlEnv = { ...process.env, PGOPTIONS: `-c search_path=${schema}` };
function psql(statement, { raw = false } = {}) {
  const args = ["-X", "-q", "-A", "-t", "-v", "ON_ERROR_STOP=1", "-d", databaseURL, "-c", statement];
  return execFileSync(psqlBin, args, { env: raw ? process.env : psqlEnv, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] }).trim();
}
const rows = (select) => JSON.parse(psql(`SELECT coalesce(json_agg(t), '[]'::json) FROM (${select}) t`) || "[]");
const safe = (value) => { if (!/^[\w.@-]+$/.test(value)) throw new Error("unsafe SQL literal " + value); return value; };
const jobByKey = (key) => rows(`SELECT id, state, filing_state, error_code, started_at, undo_until, payload_bytes, (payload_ciphertext <> '') AS has_payload, (sent_ciphertext <> '') AS has_sent, now() > undo_until AS window_over FROM outbox_jobs WHERE submission_key = '${safe(key)}'`)[0];
const jobCount = (key) => Number(psql(`SELECT count(*) FROM outbox_jobs WHERE submission_key = '${safe(key)}'`));
const setUndoExtra = (seconds) => psql(`UPDATE ob_knob SET extra_seconds = ${Number(seconds)}`);
const clearOutbox = () => psql("DELETE FROM outbox_jobs");
// A claim older than the two-minute timeout is what turns an interrupted
// submission into 'ambiguous'. The disposable database lets the test age it
// instead of waiting; nothing else about the row is touched.
const ageClaims = () => psql("UPDATE outbox_jobs SET started_at = now() - interval '3 minutes' WHERE state = 'submitting'");

// SECRET_KEY-sealed credential, same scheme as secretbox.go sealSecret.
function sealSecret(plain) {
  const iv = randomBytes(12);
  const cipher = createCipheriv("aes-256-gcm", createHash("sha256").update(SECRET_KEY).digest(), iv);
  const body = Buffer.concat([cipher.update(plain, "utf8"), cipher.final()]);
  return Buffer.concat([iv, body, cipher.getAuthTag()]).toString("base64");
}

// ---------------------------------------------------------------- processes
const children = new Set();
function launch(name, command, args, env, logFile) {
  const fd = openSync(path.join(work, logFile), "a");
  const child = spawn(command, args, { env, stdio: ["ignore", fd, fd] });
  children.add(child);
  child.exited = new Promise((resolve) => child.once("exit", (code, signal) => { children.delete(child); resolve({ code, signal }); }));
  child.name = name;
  return child;
}
function killAll() { for (const child of children) { try { child.kill("SIGKILL"); } catch { /* already gone */ } } }
let schemaCreated = false;
function dropSchemaSync() {
  if (!schemaCreated) return;
  schemaCreated = false;
  try { psql(`DROP SCHEMA ${schema} CASCADE`, { raw: true }); } catch (error) { log("could not drop schema", schema, error.message); }
}
process.on("exit", killAll);
for (const signal of ["SIGINT", "SIGTERM"]) process.on(signal, () => { killAll(); dropSchemaSync(); process.exit(130); });

function buildBinaries() {
  const bins = { server: process.env.LULL_E2E_BIN, sink: process.env.OUTBOX_SINK_BIN };
  if (!existsSync(path.join(dashboard, "dist", "index.html"))) throw new Error("dashboard/dist is missing: run npm run build first (the Go binary embeds it)");
  if (!bins.server) {
    bins.server = path.join(work, "lullmail");
    log("building lullmail");
    execFileSync("go", ["build", "-o", bins.server, "."], { cwd: repo, stdio: "inherit" });
  }
  if (!bins.sink) {
    bins.sink = path.join(work, "outbox-sink");
    log("building outbox-sink");
    execFileSync("go", ["build", "-o", bins.sink, "./tools/outbox-sink"], { cwd: repo, stdio: "inherit" });
  }
  return bins;
}

let ports, bins, sinkProcess, serverProcess, baseURL;
const sinkURL = () => `http://127.0.0.1:${ports.control}`;

async function startSink() {
  sinkProcess = launch("sink", bins.sink, ["-control", `127.0.0.1:${ports.control}`], { PATH: process.env.PATH }, "sink.log");
  const line = await poll("sink ports", () => {
    const text = readFileSync(path.join(work, "sink.log"), "utf8");
    return text.split("\n").find((l) => l.startsWith("{"));
  }, { timeout: 30_000 });
  Object.assign(ports, JSON.parse(line)); // { smtp, imap }
  await poll("sink control API", async () => (await fetch(sinkURL() + "/state")).ok, { timeout: 30_000 });
}

function startServer() {
  serverProcess = launch("lullmail", bins.server, ["serve"], {
    PATH: process.env.PATH, HOME: process.env.HOME || work,
    DATABASE_URL: databaseURL + (databaseURL.includes("?") ? "&" : "?") + "search_path=" + schema,
    SECRET_KEY, LULL_TOKEN: setupToken, DATA_DIR: path.join(work, "data"),
    PORT: String(ports.web), PUBLIC_URL: baseURL,
  }, "server.log");
  return poll("server health", async () => (await fetch(baseURL + "/health")).ok && serverProcess.exitCode === null, { timeout: 60_000 });
}
async function killServer() {
  const dying = serverProcess;
  dying.kill("SIGKILL"); // no shutdown hooks, no drain: a crash
  await dying.exited;
}
async function restartServerCrash() { await killServer(); await startServer(); }

// ---------------------------------------------------------------- sink control
async function sink(pathAndQuery, method = "POST") {
  const res = await fetch(sinkURL() + pathAndQuery, { method });
  if (!res.ok) throw new Error(`sink ${pathAndQuery}: ${res.status}`);
  return method === "GET" ? res.json() : res.text();
}
const sinkState = () => sink("/state", "GET");
const sinkReset = () => sink("/reset");
const fault = (server, step, action, { times = 1, after = 0, hold = 0 } = {}) =>
  sink(`/fault?server=${server}&step=${step}&action=${action}&times=${times}&after=${after}&hold_ms=${hold}`);
const smtpCount = async (marker) => (await sinkState()).smtp.delivered.filter((m) => m.includes(marker)).length;
const imapCount = async (marker) => (await sinkState()).imap.stored.filter((m) => m.includes(marker)).length;
const smtpSteps = async () => (await sinkState()).smtp.steps;

// ---------------------------------------------------------------- results
const results = [];
let current;
function scenario(id, name) { current = { id, name, status: "running", checks: [], notes: [], screenshots: [] }; results.push(current); log(`--- ${id} ${name}`); return current; }
function check(name, ok, detail = "") {
  current.checks.push({ name, ok: !!ok, detail: String(detail) });
  log(ok ? "  ok  " : "  FAIL", name, ok ? "" : detail);
  return !!ok;
}
const note = (text) => { current.notes.push(text); log("  note", text); };
async function shot(page, name) {
  const file = `${current.id}-${name}.png`;
  await page.screenshot({ path: path.join(output, file), fullPage: true }).catch(() => {});
  current.screenshots.push(file);
}

// ---------------------------------------------------------------- browser helpers
let browser;
const pageErrors = [];
async function newContext() {
  const context = await browser.newContext({ viewport: { width: 1280, height: 1000 } });
  context.on("page", (page) => page.on("pageerror", (error) => pageErrors.push(`${current?.id}: ${error.message}`)));
  if (process.env.OUTBOX_DEBUG) {
    await context.addInitScript(() => {
      const abort = IDBTransaction.prototype.abort;
      IDBTransaction.prototype.abort = function (...args) { console.log("IDB-ABORT " + new Error().stack.split("\n").slice(1, 5).join(" <- ")); return abort.apply(this, args); };
    });
    let pageNo = 0;
    context.on("page", (page) => { const no = ++pageNo; page.on("console", (message) => { if (message.text().startsWith("IDB-ABORT")) log(`  debug [page ${no}]`, message.text().slice(0, 200)); }); });
  }
  return context;
}

// Records every POST /api/send this page issues, with the key it carried.
function trackSends(page) {
  const sends = [];
  const byRequest = new Map();
  page.on("request", (request) => {
    if (request.method() !== "POST" || new URL(request.url()).pathname !== "/api/send") return;
    const record = { key: request.headers()["idempotency-key"], body: request.postData(), status: null, replay: null, queued: null, failed: null };
    sends.push(record); byRequest.set(request, record);
  });
  page.on("requestfailed", (request) => { const r = byRequest.get(request); if (r) r.failed = request.failure()?.errorText || "failed"; });
  page.on("response", async (response) => {
    const r = byRequest.get(response.request());
    if (!r) return;
    r.status = response.status(); r.replay = response.headers()["x-idempotent-replay"] || null;
    try { const body = await response.json(); r.queued = body.queued || null; r.resultStatus = body.status || null; } catch { /* navigated away */ }
  });
  return sends;
}

async function api(page, method, apiPath, { key, body } = {}) {
  return page.evaluate(async ({ method, apiPath, key, body }) => {
    const headers = {};
    if (key) headers["Idempotency-Key"] = key;
    if (body !== undefined) headers["Content-Type"] = "application/json";
    const res = await fetch("/api" + apiPath, { method, headers, body, credentials: "same-origin" });
    return { status: res.status, replay: res.headers.get("x-idempotent-replay"), cache: res.headers.get("cache-control"), text: await res.text() };
  }, { method, apiPath, key, body });
}
const apiJSON = async (page, method, apiPath, options) => { const r = await api(page, method, apiPath, options); return { ...r, json: (() => { try { return JSON.parse(r.text); } catch { return null; } })() }; };

async function login(page, name) {
  await page.goto(baseURL + "/", { waitUntil: "domcontentloaded" });
  await page.getByRole("heading", { name: "Welcome back" }).waitFor({ timeout: 60_000 });
  const submit = page.locator("form:has(#gate-password) button[type=submit]");
  // The prerendered form can be replaced while the client hydrates, which empties the inputs: refill until the button enables.
  await poll("login form hydrated", async () => {
    await page.getByLabel("Email or name").fill(name);
    await page.getByLabel("Password").fill(PASSWORD);
    return submit.isEnabled();
  }, { timeout: 60_000, interval: 300 });
  await submit.click();
  await appReady(page);
}
const appReady = (page) => page.getByRole("button", { name: "Settings and shortcuts" }).waitFor({ timeout: 60_000 });
async function logout(page) {
  await page.goto(baseURL + "/settings/security", { waitUntil: "domcontentloaded" });
  await page.getByRole("button", { name: "Sign out here" }).click();
  await page.getByRole("heading", { name: "Welcome back" }).waitFor({ timeout: 60_000 });
}
async function openCompose(page, fields) {
  if (!(await page.getByRole("dialog", { name: "Compose" }).isVisible())) {
    await page.getByRole("button", { name: "Compose" }).first().click();
  }
  await page.getByRole("dialog", { name: "Compose" }).waitFor();
  if (fields) await fillCompose(page, fields);
}
async function fillCompose(page, { to, subject, body }) {
  await page.getByPlaceholder("To — comma-separated").fill(to);
  await page.getByPlaceholder("Subject").fill(subject);
  await page.getByPlaceholder("Write something worth reading.").fill(body);
}
const sendButton = (page) => page.getByRole("dialog", { name: "Compose" }).getByRole("button", { name: /^Send$/ });
async function composeAndSend(page, sends, fields) {
  const before = sends.length;
  await openCompose(page, fields);
  await sendButton(page).click();
  const record = await poll("send request", () => sends.length > before && sends[before] && sends[before].status !== null ? sends[before] : null, { timeout: 60_000 });
  return record;
}
const outboxRows = (page) => page.locator("section.settings-section ul > li");
async function gotoOutbox(page, { client = false } = {}) {
  if (client) {
    await page.getByRole("button", { name: "Settings and shortcuts" }).click();
    await page.getByRole("menuitem", { name: "Outbox" }).click();
  }
  else await page.goto(baseURL + "/outbox", { waitUntil: "domcontentloaded" });
  await page.getByRole("heading", { name: "Outbox" }).waitFor();
  await page.getByText("Loading saved sends…").waitFor({ state: "detached", timeout: 60_000 });
}
const rowFor = (page, status) => outboxRows(page).filter({ has: page.locator("strong", { hasText: new RegExp(`^${status}$`) }) });
// The list repolls every 3 s, so wait for the row to carry the status before asserting on it.
async function rowReady(page, status) {
  await rowFor(page, status).first().waitFor({ timeout: 20_000 }).catch(() => {});
  return rowFor(page, status);
}

// Everything a page can have persisted: localStorage, sessionStorage, every
// IndexedDB store, and Cache Storage (the service worker's cache).
async function scanStorage(page) {
  const dump = await page.evaluate(async () => {
    const out = { local: {}, session: {}, idb: {}, caches: {}, serviceWorkers: 0 };
    for (let i = 0; i < localStorage.length; i++) { const k = localStorage.key(i); out.local[k] = localStorage.getItem(k); }
    for (let i = 0; i < sessionStorage.length; i++) { const k = sessionStorage.key(i); out.session[k] = sessionStorage.getItem(k); }
    const infos = indexedDB.databases ? await indexedDB.databases() : [];
    for (const info of infos) {
      const db = await new Promise((resolve, reject) => { const r = indexedDB.open(info.name); r.onsuccess = () => resolve(r.result); r.onerror = () => reject(r.error); });
      out.idb[info.name] = {};
      for (const store of [...db.objectStoreNames]) {
        out.idb[info.name][store] = await new Promise((resolve, reject) => { const q = db.transaction(store, "readonly").objectStore(store).getAll(); q.onsuccess = () => resolve(q.result); q.onerror = () => reject(q.error); });
      }
      db.close();
    }
    for (const name of await caches.keys()) {
      const cache = await caches.open(name);
      out.caches[name] = [];
      for (const request of await cache.keys()) {
        const response = await cache.match(request);
        const apiUrl = request.url.includes("/api/");
        out.caches[name].push({ url: request.url, body: apiUrl && response ? await response.clone().text() : "" });
      }
    }
    out.serviceWorkers = (await navigator.serviceWorker?.getRegistrations?.())?.length || 0;
    return out;
  });
  const text = JSON.stringify(dump);
  return { text, dump, has: (needle) => text.includes(needle), idbText: JSON.stringify(dump.idb) };
}

async function expectAbsent(page, label, needles) {
  const scan = await scanStorage(page);
  for (const needle of needles) check(`${label}: browser storage has no "${needle}"`, !scan.has(needle), "found in local/session/IndexedDB/Cache Storage");
  const apiCached = Object.values(scan.dump.caches).flat().filter((e) => e.url.includes("/api/outbox"));
  check(`${label}: service-worker caches hold no /api/outbox response`, apiCached.length === 0, apiCached.map((e) => e.url).join(","));
  return scan;
}

// ---------------------------------------------------------------- fixtures
async function createOwnerA(page) {
  await page.goto(baseURL, { waitUntil: "domcontentloaded" });
  await page.getByRole("button", { name: "Get started" }).click();
  await page.getByPlaceholder("Setup code").fill(setupToken);
  await page.getByRole("button", { name: "Continue" }).click();
  await page.getByPlaceholder("Your name").fill("Owner");
  await page.getByPlaceholder("Password", { exact: true }).fill(PASSWORD);
  await page.getByRole("button", { name: "Create account" }).click();
  await page.getByRole("heading", { name: "Save your recovery codes" }).waitFor();
  await page.getByRole("button", { name: /saved them/ }).click();
}
function seedAccount(email, mirror, address) {
  psql(`INSERT INTO mail_accounts (id, provider, email, name, needs_reauth) VALUES ('${mirror}', 'imap', '${address}', '${mirror}', false)`);
  // sync_enabled=false: the scheduler never touches the sink; only the outbox's own SMTP and Sent APPEND do.
  psql(`INSERT INTO email_accounts (user_id, mirror_account_id, provider, address, label, username, host, port, smtp_host, smtp_port, cred_ciphertext, backfill_days, retention_days, sync_enabled)
        SELECT id, '${mirror}', 'imap', '${address}', 'Sink ${mirror}', 'sink-user', '127.0.0.1', ${ports.imap}, '127.0.0.1', ${ports.smtp}, '${sealSecret("sink-pass")}', 90, 0, false FROM users WHERE email = '${email}'`);
  psql(`INSERT INTO mail_mailboxes (account_id, id, name, role, native) VALUES ('${mirror}', 'inbox', 'Inbox', 'inbox', 'INBOX'), ('${mirror}', 'sent', 'Sent', 'sent', 'Sent')`);
}
function createOwnerB() {
  psql("INSERT INTO users (email, display_name) VALUES ('second@owner.local', 'Second')");
  psql("INSERT INTO auth_passwords (user_id, hash, updated_at) SELECT b.id, p.hash, now() FROM users b, auth_passwords p JOIN users a ON a.id = p.user_id WHERE b.email = 'second@owner.local' AND a.email = 'owner@owner.local'");
}
function installUndoKnob() {
  psql("CREATE TABLE ob_knob (extra_seconds int NOT NULL DEFAULT 0); INSERT INTO ob_knob VALUES (0)");
  psql(`CREATE FUNCTION ob_widen() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.undo_until := NEW.undo_until + (SELECT extra_seconds FROM ob_knob LIMIT 1) * interval '1 second'; RETURN NEW; END $$`);
  psql("CREATE TRIGGER ob_widen BEFORE INSERT ON outbox_jobs FOR EACH ROW EXECUTE FUNCTION ob_widen()");
}
async function freshState() { await sinkReset(); clearOutbox(); setUndoExtra(0); }

// ---------------------------------------------------------------- scenarios
// 1. Owner switch in one browser profile.
async function s1OwnerSwitch() {
  scenario("S1", "owner switch: no cross-owner outbox, composition or local data");
  await freshState();
  const m = { sent: "S1A-SENT-" + tag(), failed: "S1A-FAILED-" + tag(), failedBody: "S1A-SECRETBODY-" + tag(), draft: "S1A-PARKED-" + tag(), b: "S1B-SENT-" + tag() };
  const context = await newContext();
  const page = await context.newPage();
  const sends = trackSends(page);
  await login(page, "Owner");
  // A: one delivered send, one failed (composition retained), one parked unsent draft.
  const sentRecord = await composeAndSend(page, sends, { to: "a-rcpt@example.test", subject: m.sent, body: "body " + m.sent });
  await poll("A submitted", () => jobByKey(sentRecord.key)?.state === "submitted");
  await fault("smtp", "rcpt", "reject");
  const failedRecord = await composeAndSend(page, sends, { to: "a-rcpt@example.test", subject: m.failed, body: m.failedBody });
  await poll("A failed", () => jobByKey(failedRecord.key)?.state === "failed");
  await openCompose(page, { to: "a-park@example.test", subject: m.draft, body: "parked " + m.draft });
  await page.keyboard.press("Escape");
  await poll("parked draft reached IndexedDB", async () => (await scanStorage(page)).idbText.includes(m.draft));
  const aList = await apiJSON(page, "GET", "/outbox");
  const aIds = (aList.json || []).map((e) => e.id);
  check("owner A sees its two outbox entries", aIds.length === 2, JSON.stringify(aList.json));
  const failedId = jobByKey(failedRecord.key).id;
  check("A's list response is no-store", aList.cache === "no-store", aList.cache);
  // Reading the failed composition once puts private text on screen while A is signed in.
  await gotoOutbox(page);
  await rowFor(page, "failed").getByRole("button", { name: "Review saved composition" }).click();
  await page.getByRole("region", { name: "Saved composition review" }).getByText(m.failedBody).waitFor();
  await shot(page, "A-review");
  await expectAbsent(page, "A after viewing its own composition", [m.failedBody]);
  const detail = await apiJSON(page, "GET", "/outbox/" + failedId);
  check("A's detail response is no-store", detail.cache === "no-store", detail.cache);

  await logout(page);
  await expectAbsent(page, "after A signs out", [m.failedBody, m.failed, m.sent, m.draft, "sender-a@example.test", failedId]);
  await login(page, "Second");
  await gotoOutbox(page);
  check("owner B's Outbox view is empty", await page.getByText("No saved sends").isVisible(), await page.locator("body").innerText());
  await shot(page, "B-outbox-empty");
  const bList = await apiJSON(page, "GET", "/outbox");
  check("GET /api/outbox as B returns [] ", Array.isArray(bList.json) && bList.json.length === 0, bList.text);
  // B's view of A's ids must be indistinguishable from an id that never existed.
  const probe = async (id) => {
    const out = [];
    for (const [method, suffix] of [["GET", ""], ["GET", "?format=eml"], ["DELETE", ""], ["DELETE", "/payload"]]) {
      const r = await api(page, method, "/outbox/" + id + suffix);
      out.push(r.status + ":" + r.text.replace(id, "<id>").slice(0, 200));
    }
    return out;
  };
  const nonexistent = await probe(randomUUID());
  check("a never-existing id gives 404 on GET and ?format=eml to B", nonexistent[0].startsWith("404:") && nonexistent[1].startsWith("404:"), nonexistent.join(" | "));
  for (const id of aIds) {
    const seen = await probe(id);
    check(`A's outbox ${id.slice(0, 8)} is indistinguishable from a nonexistent id to B (GET/eml/cancel/discard)`, seen.every((r, i) => r === nonexistent[i]), seen.join(" | ") + "  vs  " + nonexistent.join(" | "));
    check(`A's outbox ${id.slice(0, 8)} is 404 to B on GET and ?format=eml`, seen[0].startsWith("404:") && seen[1].startsWith("404:"), seen.slice(0, 2).join(" | "));
  }
  const intact = jobByKey(failedRecord.key);
  check("A's failed job was not altered by B's attempts", intact.state === "failed" && intact.has_payload, JSON.stringify(intact));
  // B reusing A's submission key must not replay or collide with A's row.
  const replayAsB = await apiJSON(page, "POST", "/send", { key: failedRecord.key, body: JSON.stringify({ to: "b-rcpt@example.test", cc: "", bcc: "", subject: m.b, text: "body " + m.b, html: "", account_id: "", reply_to_message_id: "", attachments: [] }) });
  check("B reusing A's key creates B's own job (no replay of A's, no 409)", replayAsB.status === 200 && !replayAsB.replay && replayAsB.json && !aIds.includes(replayAsB.json.queued), JSON.stringify([replayAsB.status, replayAsB.replay, replayAsB.text]));
  await poll("B's own send delivered", async () => (await smtpCount(m.b)) === 1, { timeout: 60_000 });
  check("A's failed composition was never delivered by B's activity", (await smtpCount(m.failed)) === 0 && (await smtpCount(m.failedBody)) === 0);
  await expectAbsent(page, "as B", [m.failedBody, m.failed, m.sent, m.draft, "sender-a@example.test", failedId, ...aIds]);
  await logout(page);
  await login(page, "Owner");
  await gotoOutbox(page);
  check("A signing back in sees exactly A's two entries and not B's", (await outboxRows(page).count()) === 2, await page.locator("section.settings-section").innerText());
  await shot(page, "A-back");
  await context.close();
  current.status = current.checks.every((c) => c.ok) ? "pass" : "fail";
}

// 2. Sign out during the undo window.
async function s2LogoutPending() {
  scenario("S2", "sign out inside the undo window: delivered exactly once, nothing lingers locally");
  await freshState();
  setUndoExtra(10); // keeps "pending" long enough that the sign-out is provably inside the window
  const m = { sent: "S2-SENT-" + tag(), draft: "S2-PARKED-" + tag() };
  const context = await newContext();
  const page = await context.newPage();
  const sends = trackSends(page);
  await login(page, "Owner");
  await openCompose(page, { to: "park@example.test", subject: m.draft, body: "parked " + m.draft });
  await page.getByRole("button", { name: "+ New draft" }).click();
  await fillCompose(page, { to: "s2-rcpt@example.test", subject: m.sent, body: "body " + m.sent });
  await poll("parked draft reached IndexedDB", async () => (await scanStorage(page)).idbText.includes(m.draft));
  await sendButton(page).click();
  const record = await poll("send accepted", () => sends[0]?.status ? sends[0] : null);
  check("send returned durable 200", record.status === 200, JSON.stringify(record));
  const rowAtSend = await poll("job row", () => jobByKey(record.key));
  await logout(page);
  const atLogout = jobByKey(record.key);
  check("sign-out completed while the job was still pending (inside the undo window)", atLogout.state === "pending" && !atLogout.window_over, JSON.stringify(atLogout));
  check("nothing was delivered at sign-out time", (await smtpCount(m.sent)) === 0);
  await expectAbsent(page, "after sign-out", [m.draft, m.sent, "s2-rcpt@example.test", "park@example.test"]);
  await poll("delivered after the window", async () => (await smtpCount(m.sent)) === 1, { timeout: 90_000 });
  await poll("job submitted", () => jobByKey(record.key)?.state === "submitted");
  await observe("no second delivery", 8000, async () => ((await smtpCount(m.sent)) === 1 ? null : `delivered ${await smtpCount(m.sent)}`));
  check("exactly one delivery to the sink", (await smtpCount(m.sent)) === 1);
  check("exactly one server row for the key", jobCount(record.key) === 1);
  await login(page, "Owner");
  await gotoOutbox(page);
  await rowReady(page, "submitted");
  check("after signing in again the Outbox shows 'submitted'", (await rowFor(page, "submitted").count()) === 1, await page.locator("section.settings-section").innerText());
  await shot(page, "outbox-submitted");
  check("parked draft did not return after sign-out/in (ratified logout semantics)", !(await scanStorage(page)).has(m.draft));
  await context.close();
  current.status = current.checks.every((c) => c.ok) ? "pass" : "fail";
  setUndoExtra(0);
}

// 3. kill -9 recovery.
async function sendViaUi(page, sends, marker) {
  return composeAndSend(page, sends, { to: "rcpt-" + marker.toLowerCase() + "@example.test", subject: marker, body: "body " + marker });
}
async function assertNeverResent(label, key, marker, expectedDelivered) {
  await observe(`${label}: no resend while idle`, 8000, async () => {
    const j = jobByKey(key); const n = await smtpCount(marker);
    return n === expectedDelivered && j.state === "ambiguous" ? null : `state=${j.state} delivered=${n}`;
  });
  for (let i = 1; i <= 2; i++) {
    await restartServerCrash();
    await observe(`${label}: no resend after crash restart #${i}`, 6000, async () => {
      const j = jobByKey(key); const n = await smtpCount(marker);
      return n === expectedDelivered && j.state === "ambiguous" ? null : `state=${j.state} delivered=${n}`;
    });
  }
}

async function s3KillRecovery() {
  scenario("S3", "kill -9 recovery: ambiguous never resent; pending sent once; filing never re-appended");
  const context = await newContext();
  const page = await context.newPage();
  const sends = trackSends(page);
  await login(page, "Owner");

  // 3a: killed while the sink holds the SMTP session mid-DATA.
  await freshState();
  const a = "S3A-" + tag();
  await fault("smtp", "body", "continue", { hold: 40_000 });
  let record = await sendViaUi(page, sends, a);
  await poll("sink reached DATA body", async () => (await smtpSteps()).includes("body"), { timeout: 60_000 });
  check("3a: job is 'submitting' when killed", jobByKey(record.key).state === "submitting", JSON.stringify(jobByKey(record.key)));
  await killServer();
  check("3a: sink has not kept the message at kill time", (await smtpCount(a)) === 0);
  await startServer();
  await observe("3a: claim younger than 2 minutes is left alone", 3000, () => (jobByKey(record.key).state === "submitting" ? null : "state changed early: " + jobByKey(record.key).state));
  ageClaims();
  await poll("3a: ambiguous", () => jobByKey(record.key)?.state === "ambiguous", { timeout: 60_000 });
  check("3a: error code says interrupted submission", jobByKey(record.key).error_code === "interrupted_submission", jobByKey(record.key).error_code);
  check("3a: private payload retained for recovery", jobByKey(record.key).has_payload);
  await page.goto(baseURL + "/outbox", { waitUntil: "domcontentloaded" });
  await gotoOutbox(page);
  await rowReady(page, "ambiguous");
  check("3a: UI shows ambiguous with the duplicate warning", (await rowFor(page, "ambiguous").count()) === 1 && (await page.getByText("The provider may already have sent this message").isVisible()), await page.locator("section.settings-section").innerText());
  await shot(page, "3a-ambiguous");
  await assertNeverResent("3a", record.key, a, 0);
  check("3a: at most one delivery (zero here: connection died before commit)", (await smtpCount(a)) <= 1);

  // 3b: killed while still pending (inside the undo window).
  await freshState();
  setUndoExtra(20);
  const b = "S3B-" + tag();
  record = await sendViaUi(page, sends, b);
  check("3b: job is 'pending' when killed", jobByKey(record.key).state === "pending" && !jobByKey(record.key).window_over, JSON.stringify(jobByKey(record.key)));
  await killServer();
  check("3b: nothing delivered while the server is dead", (await smtpCount(b)) === 0);
  await startServer();
  await poll("3b: delivered once after restart", async () => (await smtpCount(b)) === 1, { timeout: 90_000 });
  await poll("3b: submitted", () => jobByKey(record.key)?.state === "submitted");
  await observe("3b: no second delivery", 6000, async () => ((await smtpCount(b)) === 1 ? null : "delivered " + (await smtpCount(b))));
  await restartServerCrash();
  await observe("3b: no second delivery after another crash restart", 6000, async () => ((await smtpCount(b)) === 1 && jobByKey(record.key).state === "submitted" ? null : "delivered " + (await smtpCount(b))));
  check("3b: exactly one delivery and one row", (await smtpCount(b)) === 1 && jobCount(record.key) === 1);
  setUndoExtra(0);

  // 3c: killed after the sink kept the message but before the client saw 250.
  await freshState();
  const c = "S3C-" + tag();
  await fault("smtp", "committed", "continue", { hold: 40_000 });
  record = await sendViaUi(page, sends, c);
  await poll("3c: sink kept the message", async () => (await smtpCount(c)) === 1, { timeout: 60_000 });
  check("3c: job is 'submitting' when killed", jobByKey(record.key).state === "submitting", JSON.stringify(jobByKey(record.key)));
  await killServer();
  await startServer();
  ageClaims();
  await poll("3c: ambiguous", () => jobByKey(record.key)?.state === "ambiguous", { timeout: 60_000 });
  await gotoOutbox(page);
  await rowReady(page, "ambiguous");
  check("3c: UI shows ambiguous", (await rowFor(page, "ambiguous").count()) === 1, await page.locator("section.settings-section").innerText());
  await shot(page, "3c-ambiguous");
  await assertNeverResent("3c", record.key, c, 1);
  check("3c: delivered exactly once, never twice", (await smtpCount(c)) === 1);

  // 3d: killed while the Sent-copy APPEND is in flight (message submitted, copy kept by IMAP, OK not yet seen).
  await freshState();
  const d = "S3D-" + tag();
  await fault("imap", "committed", "continue", { hold: 40_000 });
  record = await sendViaUi(page, sends, d);
  await poll("3d: IMAP kept the Sent copy", async () => (await imapCount(d)) === 1, { timeout: 60_000 });
  check("3d: SMTP submission already recorded, filing 'submitting' when killed", jobByKey(record.key).state === "submitted" && jobByKey(record.key).filing_state === "submitting", JSON.stringify(jobByKey(record.key)));
  await killServer();
  await startServer();
  psql("UPDATE outbox_jobs SET updated_at = now() - interval '3 minutes' WHERE filing_state = 'submitting'");
  await poll("3d: filing becomes ambiguous", () => jobByKey(record.key)?.filing_state === "ambiguous", { timeout: 60_000 });
  check("3d: the submission outcome is untouched by the filing interruption", jobByKey(record.key).state === "submitted");
  await observe("3d: the Sent copy is never appended again and the message never resent", 8000, async () => ((await imapCount(d)) === 1 && (await smtpCount(d)) === 1 ? null : `imap=${await imapCount(d)} smtp=${await smtpCount(d)}`));
  await restartServerCrash();
  await observe("3d: still once each after another crash restart", 6000, async () => ((await imapCount(d)) === 1 && (await smtpCount(d)) === 1 && jobByKey(record.key).filing_state === "ambiguous" ? null : `imap=${await imapCount(d)} smtp=${await smtpCount(d)}`));
  await context.close();
  current.status = current.checks.every((c2) => c2.ok) ? "pass" : "fail";
}

// 4. Two tabs, offline, replay.
async function s4MultiTabOffline() {
  scenario("S4", "two tabs + offline: one row, one delivery; replay is idempotent");
  await freshState();
  const m = "S4-" + tag();
  const context = await newContext();
  const tab1 = await context.newPage();
  const tab2 = await context.newPage();
  const sends1 = trackSends(tab1), sends2 = trackSends(tab2);
  await login(tab1, "Owner");
  await tab2.goto(baseURL + "/today", { waitUntil: "domcontentloaded" });
  await appReady(tab2);
  await openCompose(tab1, { to: "s4-rcpt@example.test", subject: m, body: "body " + m });
  await poll("draft persisted", async () => (await scanStorage(tab1)).idbText.includes(m));
  await context.setOffline(true);
  await sendButton(tab1).click();
  const attempt = await poll("offline send attempt", () => sends1[0]?.failed ? sends1[0] : null, { timeout: 30_000 });
  check("offline send fails at the network and carried an Idempotency-Key", !!attempt.key, JSON.stringify(attempt));
  const toast = await poll("error toast", async () => (await tab1.locator('[role="alert"].toast, .toast.error').first().innerText().catch(() => "")) || null, { timeout: 15_000 }).catch(() => "");
  note("offline toast text: " + JSON.stringify(toast));
  check("nothing reached the server while offline", jobCount(attempt.key) === 0);
  const offlineScan = await scanStorage(tab1);
  const draftKeyKept = offlineScan.idbText.includes(attempt.key);
  check("the draft (with the same send key) is retained in IndexedDB, nothing lost", draftKeyKept && offlineScan.idbText.includes(m), "key or content missing from IndexedDB");
  check("composer still holds the unsent draft", await tab1.getByRole("dialog", { name: "Compose" }).isVisible());
  await shot(tab1, "offline-send-failed");
  await context.setOffline(false);
  // Reload BOTH tabs: the failed send's draft (with its key) must come back from IndexedDB in each.
  await tab1.reload({ waitUntil: "domcontentloaded" }); await appReady(tab1);
  if (!(await tab1.getByRole("dialog", { name: "Compose" }).isVisible())) await tab1.getByRole("button", { name: "Compose" }).first().click();
  await tab1.getByRole("dialog", { name: "Compose" }).waitFor();
  check("after a reload the failed send's draft is restored in tab 1", (await tab1.getByPlaceholder("Subject").inputValue()) === m);
  await tab2.reload({ waitUntil: "domcontentloaded" });
  await appReady(tab2);
  const dialog2 = tab2.getByRole("dialog", { name: "Compose" });
  if (!(await dialog2.isVisible())) await tab2.getByRole("button", { name: "Compose" }).first().click();
  await dialog2.waitFor();
  const restored = await tab2.getByPlaceholder("Subject").inputValue();
  check("second tab restores the same draft from shared storage", restored === m, restored);
  await shot(tab2, "tab2-draft");
  // Variant 1: tab 1's request reaches the server but its answer is held back, so the
  // draft is still live when tab 2 presses Send on the same draft (same key).
  let release;
  const held = new Promise((resolve) => { release = resolve; });
  await tab1.route("**/api/send", async (route) => { const response = await route.fetch(); await held; await route.fulfill({ response }); });
  const before1 = sends1.length;
  await sendButton(tab1).click();
  await poll("tab 1's request created the row", () => jobCount(attempt.key) === 1, { timeout: 60_000 });
  await sendButton(tab2).click();
  const second = await poll("tab 2 answered", () => sends2.find((s) => s.status), { timeout: 60_000 });
  check("tab 2 sent under the draft's ORIGINAL key", second.key === attempt.key, second.key + " vs " + attempt.key);
  check("the server answered tab 2 as an idempotent replay (X-Idempotent-Replay: true)", second.status === 200 && second.replay === "true", JSON.stringify([second.status, second.replay]));
  release();
  const first = await poll("tab 1 answered", () => sends1.slice(before1).find((s) => s.status), { timeout: 60_000 });
  await tab1.unroute("**/api/send");
  check("tab 1's answer is the original (non-replay) 200 with the same queued id", first.status === 200 && !first.replay && first.queued === second.queued, JSON.stringify([first.status, first.replay, first.queued, second.queued]));
  check("exactly one server row for the key", jobCount(attempt.key) === 1);
  await poll("delivered", async () => (await smtpCount(m)) === 1, { timeout: 90_000 });
  await observe("single delivery holds", 6000, async () => ((await smtpCount(m)) === 1 ? null : "delivered " + (await smtpCount(m))));
  check("exactly one delivery after both tabs sent the same draft", (await smtpCount(m)) === 1);
  await shot(tab2, "two-tabs-same-key");
  // Raw replays: same key + same body is idempotent; a changed body under the key is refused.
  const body = first.body;
  const again = await apiJSON(tab1, "POST", "/send", { key: attempt.key, body });
  check("same key + same request replays with X-Idempotent-Replay and the original id", again.status === 200 && again.replay === "true" && again.json.queued === first.queued, JSON.stringify([again.status, again.replay, again.text]));
  const changed = await apiJSON(tab1, "POST", "/send", { key: attempt.key, body: body.replace(m, m + "-CHANGED") });
  check("same key + changed request is 409", changed.status === 409, JSON.stringify([changed.status, changed.text]));
  check("replays created no second row or delivery", jobCount(attempt.key) === 1 && (await smtpCount(m)) === 1);

  // Variant 2: both tabs press Send at the same instant on a fresh draft.
  const m2 = "S4B-" + tag();
  await tab1.reload({ waitUntil: "domcontentloaded" }); await appReady(tab1);
  await openCompose(tab1, { to: "s4b-rcpt@example.test", subject: m2, body: "body " + m2 });
  await poll("second draft persisted", async () => (await scanStorage(tab1)).idbText.includes(m2));
  await tab2.reload({ waitUntil: "domcontentloaded" }); await appReady(tab2);
  if (!(await tab2.getByRole("dialog", { name: "Compose" }).isVisible())) await tab2.getByRole("button", { name: "Compose" }).first().click();
  await tab2.getByRole("dialog", { name: "Compose" }).waitFor();
  check("tab 2 hydrates the second draft", (await tab2.getByPlaceholder("Subject").inputValue()) === m2);
  const n1 = sends1.length, n2 = sends2.length;
  await Promise.all([sendButton(tab1).click(), sendButton(tab2).click()]);
  await poll("some tab answered", () => sends1.slice(n1).some((s) => s.status) || sends2.slice(n2).some((s) => s.status), { timeout: 60_000 });
  await poll("tab 2 settled", async () => sends2.length > n2 || (await tab2.locator("text=drafts are NOT saved").count()) > 0 || !(await tab2.getByRole("dialog", { name: "Compose" }).isVisible()), { timeout: 30_000 });
  const race = [...sends1.slice(n1), ...sends2.slice(n2)];
  note("simultaneous send: tab1 " + JSON.stringify(sends1.slice(n1).map((s) => [s.status, s.replay])) + ", tab2 " + JSON.stringify(sends2.slice(n2).map((s) => [s.status, s.replay])) + ", tab2 banner: " + JSON.stringify(await tab2.locator(".compose-ring-count[role=status]").allInnerTexts()));
  await shot(tab2, "simultaneous-tab2");
  // A tab that did not send must say why. (Its autosave also fails on the retired draft, so the
  // "drafts are NOT saved" marker may appear there; that comes from autosave, not from Send.)
  for (const [label, tab, sent] of [["tab 1", tab1, sends1.length > n1], ["tab 2", tab2, sends2.length > n2]]) {
    if (!sent) {
      const toastText = await poll(`${label} explains why it did not send`, async () => (await tab.locator(".toast.error").first().innerText().catch(() => "")) || null, { timeout: 10_000 }).catch(() => "");
      check(`simultaneous press: ${label} did not send and says another tab sent/changed the draft`, /another tab/.test(toastText), JSON.stringify(toastText));
    }
  }
  check("simultaneous press: every request used one key and got the same queued id", new Set(race.filter((s) => s.status).map((s) => s.key)).size === 1 && new Set(race.filter((s) => s.status).map((s) => s.queued)).size === 1, JSON.stringify(race.map((s) => [s.key, s.status, s.queued])));
  const key2 = race.find((s) => s.status).key;
  check("simultaneous press: exactly one server row", jobCount(key2) === 1);
  await poll("delivered", async () => (await smtpCount(m2)) === 1, { timeout: 90_000 });
  await observe("single delivery holds", 5000, async () => ((await smtpCount(m2)) === 1 ? null : "delivered " + (await smtpCount(m2))));
  check("simultaneous press: exactly one delivery", (await smtpCount(m2)) === 1);

  // Offline queue + Web Locks: a queueable mutation done offline, replayed by two tabs.
  const card = "S4-CARD-" + tag();
  const made = await apiJSON(tab1, "POST", "/board/cards", { body: JSON.stringify({ title: card, note: "" }) });
  const cardId = made.json?.card_id;
  await Promise.all([tab1.goto(baseURL + "/board", { waitUntil: "domcontentloaded" }), tab2.goto(baseURL + "/board", { waitUntil: "domcontentloaded" })]);
  const doneRequests = [];
  for (const tab of [tab1, tab2]) tab.on("request", (r) => { if (r.method() === "POST" && r.url().includes(`/api/board/cards/${cardId}/done`)) doneRequests.push({ key: r.headers()["idempotency-key"] }); });
  await tab1.getByRole("group", { name: card }).waitFor();
  await tab2.getByRole("group", { name: card }).waitFor();
  await context.setOffline(true);
  await tab1.getByRole("group", { name: card }).getByRole("button", { name: /Done/ }).click();
  await poll("mutation queued in IndexedDB", async () => (await scanStorage(tab1)).idbText.includes(`/board/cards/${cardId}/done`), { timeout: 20_000 });
  const queuedKey = doneRequests[0]?.key;
  check("offline board action is queued locally with its key", !!queuedKey, JSON.stringify(doneRequests));
  doneRequests.length = 0;
  await context.setOffline(false);
  await poll("card marked done on the server", () => psql(`SELECT done_at IS NOT NULL FROM board_cards WHERE id = '${safe(cardId)}'`) === "t", { timeout: 60_000 });
  await observe("no further replays", 5000, () => null, 1000);
  const mutationRows = Number(psql(`SELECT count(*) FROM api_mutations WHERE mutation_key = '${safe(queuedKey || "none")}'`));
  check("queued mutation applied exactly once (one api_mutations receipt)", mutationRows === 1, "receipts=" + mutationRows);
  note(`replay requests observed across both tabs after reconnect: ${doneRequests.length} (Web Locks admit one pass; idempotency covers the rest)`);
  check("at most one replay request left the two tabs for the queued mutation", doneRequests.length <= 1, JSON.stringify(doneRequests));
  await context.close();
  current.status = current.checks.every((c) => c.ok) ? "pass" : "fail";
}

// 5. Ambiguous, failed and unconfirmed-filing UI, recovery, and cancel.
async function s5AmbiguousUi() {
  scenario("S5", "ambiguous send: warning, review, new draft, remove, deliberate re-send; nothing auto-resends");
  await freshState();
  const m = { subject: "S5A-SUBJECT-" + tag(), body: "S5A-BODYTEXT-" + tag() };
  const context = await newContext();
  const page = await context.newPage();
  const sends = trackSends(page);
  const dialogs = [];
  page.on("dialog", async (dialog) => { dialogs.push({ type: dialog.type(), message: dialog.message() }); await (dialogs.length === 1 ? dialog.dismiss() : dialog.accept()); });
  await login(page, "Owner");
  await fault("smtp", "committed", "drop");
  const first = await composeAndSend(page, sends, { to: "s5-rcpt@example.test", subject: m.subject, body: m.body });
  await poll("ambiguous", () => jobByKey(first.key)?.state === "ambiguous", { timeout: 90_000 });
  const t0 = Date.now();
  check("the sink really kept the message that the client never saw acknowledged", (await smtpCount(m.subject)) === 1);
  await gotoOutbox(page, { client: true });
  const row = await rowReady(page, "ambiguous");
  check("Outbox shows the ambiguous entry", (await row.count()) === 1, await page.locator("section.settings-section").innerText());
  check("duplicate-delivery warning is on the row", await row.getByText("The provider may already have sent this message").isVisible());
  check("row offers Review and Remove, and no Cancel", (await row.getByRole("button", { name: "Review saved composition" }).isVisible()) && (await row.getByRole("button", { name: "Remove saved composition" }).isVisible()) && (await row.getByRole("button", { name: "Cancel send" }).count()) === 0);
  await shot(page, "ambiguous-row");
  await row.getByRole("button", { name: "Review saved composition" }).click();
  const review = page.getByRole("region", { name: "Saved composition review" });
  await review.getByText(m.body).waitFor();
  check("review shows the saved subject, recipient and text", (await review.getByRole("heading", { name: m.subject }).isVisible()) && (await review.getByText("s5-rcpt@example.test").isVisible()));
  check("review carries the explicit duplicate-message alert", await review.getByRole("alert").getByText("Creating a new draft can result in a duplicate message").isVisible());
  await shot(page, "ambiguous-review");
  await expectAbsent(page, "after viewing the review", [m.body]);
  await review.getByRole("button", { name: "Create a new draft from this copy" }).click();
  const compose = page.getByRole("dialog", { name: "Compose" });
  await compose.waitFor();
  check("new draft opens with the saved recipient, subject and body", (await compose.getByPlaceholder("To — comma-separated").inputValue()).includes("s5-rcpt@example.test") && (await compose.getByPlaceholder("Subject").inputValue()) === m.subject && (await compose.getByPlaceholder("Write something worth reading.").inputValue()) === m.body);
  await shot(page, "ambiguous-new-draft");
  note("the duplicate warning is shown on the review panel; the compose dialog opened from it carries no warning of its own");
  const samples = await observe("S5 nothing auto-resends", Math.max(1000, 31_000 - (Date.now() - t0)), async () => {
    const j = jobByKey(first.key); const n = await smtpCount(m.subject);
    return j.state === "ambiguous" && n === 1 && sends.length === 1 ? null : `state=${j.state} delivered=${n} sends=${sends.length}`;
  });
  check(`delivery count stayed at 1 and the entry stayed ambiguous for >= 30 s (${samples} samples)`, Date.now() - t0 >= 30_000);

  // Deliberate re-send from the new draft: a NEW key, a second delivery, by the user's own action.
  await sendButton(page).click();
  const second = await poll("second send answered", () => (sends[1]?.status ? sends[1] : null), { timeout: 60_000 });
  check("deliberate re-send uses a NEW idempotency key", second.key && second.key !== first.key, `${first.key} vs ${second.key}`);
  await poll("second delivery", async () => (await smtpCount(m.subject)) === 2, { timeout: 90_000 });
  check("second delivery happened only because the user sent again", (await smtpCount(m.subject)) === 2);
  await observe("no third delivery", 6000, async () => ((await smtpCount(m.subject)) === 2 ? null : "delivered " + (await smtpCount(m.subject))));

  // Remove the saved composition from the ambiguous entry: dismissed confirm first, then accepted.
  await gotoOutbox(page, { client: true });
  const amb = await rowReady(page, "ambiguous");
  const originalId = jobByKey(first.key).id;
  await amb.getByRole("button", { name: "Remove saved composition" }).click();
  check("a confirm dialog guards removal", dialogs.length === 1 && dialogs[0].type === "confirm" && /Permanently remove/.test(dialogs[0].message), JSON.stringify(dialogs));
  check("dismissing the confirm removed nothing", jobByKey(first.key).has_payload && (await amb.getByRole("button", { name: "Review saved composition" }).isVisible()));
  await amb.getByRole("button", { name: "Remove saved composition" }).click();
  await poll("payload erased", () => !jobByKey(first.key).has_payload, { timeout: 30_000 });
  const gone = jobByKey(first.key);
  check("accepting erased the payload but kept the receipt and the ambiguous outcome", gone.state === "ambiguous" && gone.payload_bytes === 0, JSON.stringify(gone));
  await poll("buttons vanish", async () => (await rowFor(page, "ambiguous").getByRole("button", { name: "Review saved composition" }).count()) === 0, { timeout: 15_000 });
  const detail = await api(page, "GET", "/outbox/" + originalId);
  check("erased composition is no longer retrievable", detail.status >= 400, `${detail.status} ${detail.text.slice(0, 80)}`);
  await shot(page, "removed");
  await context.close();
  current.status = current.checks.every((c) => c.ok) ? "pass" : "fail";
}

async function s5bFailed() {
  scenario("S5b", "failed (provably not sent) entries: not-sent semantics, no duplicate warning, composition recoverable");
  const context = await newContext();
  const page = await context.newPage();
  const sends = trackSends(page);
  await login(page, "Owner");
  const cases = [
    { label: "rcpt rejected", step: "rcpt", action: "reject", expect: "failed", delivered: 0 },
    { label: "connection dropped at EHLO", step: "ehlo", action: "drop", expect: "failed", delivered: 0 },
  ];
  for (const c of cases) {
    await freshState();
    await fault("smtp", c.step, c.action, { times: 1 });
    const m = { subject: "S5B-" + c.step.toUpperCase() + "-" + tag(), body: "BODY-S5B-" + tag() };
    const rec = await composeAndSend(page, sends, { to: "s5b@example.test", subject: m.subject, body: m.body });
    await poll(`${c.label}: settled`, () => ["failed", "ambiguous", "submitted"].includes(jobByKey(rec.key)?.state), { timeout: 90_000 });
    const job = jobByKey(rec.key);
    check(`${c.label}: classified ${c.expect}`, job.state === c.expect, `state=${job.state} code=${job.error_code}`);
    check(`${c.label}: sink kept no message`, (await smtpCount(m.subject)) === c.delivered);
    if (job.state !== "failed") continue;
    check(`${c.label}: composition retained`, job.has_payload);
    await gotoOutbox(page);
    const row = await rowReady(page, "failed");
    check(`${c.label}: failed row shown, no duplicate-delivery warning`, (await row.count()) === 1 && (await row.getByText("may already have sent").count()) === 0, await page.locator("section.settings-section").innerText());
    await row.getByRole("button", { name: "Review saved composition" }).click();
    const review = page.getByRole("region", { name: "Saved composition review" });
    await review.getByText(m.body).waitFor();
    check(`${c.label}: review has no duplicate alert`, (await review.getByRole("alert").count()) === 0);
    await shot(page, "failed-" + c.step);
    await review.getByRole("button", { name: "Create a new draft from this copy" }).click();
    const compose = page.getByRole("dialog", { name: "Compose" });
    await compose.waitFor();
    check(`${c.label}: recovered draft carries the content`, (await compose.getByPlaceholder("Subject").inputValue()) === m.subject && (await compose.getByPlaceholder("Write something worth reading.").inputValue()) === m.body);
    await compose.getByRole("button", { name: "Discard" }).click();
    await compose.waitFor({ state: "detached" }).catch(() => {});
    await observe(`${c.label}: nothing auto-resends`, 8000, async () => (jobByKey(rec.key).state === "failed" && (await smtpCount(m.subject)) === 0 ? null : "state changed or delivered"));
  }
  await context.close();
  current.status = current.checks.every((c) => c.ok) ? "pass" : "fail";
}

async function s5cFiling() {
  scenario("S5c", "submitted but Sent-copy filing failed: 'Sent copy unconfirmed' with download, never re-appended");
  const context = await newContext();
  const page = await context.newPage();
  const sends = trackSends(page);
  page.on("dialog", (dialog) => dialog.accept());
  await login(page, "Owner");
  for (const c of [{ label: "IMAP dropped at APPEND", step: "append", stored: 0 }, { label: "IMAP dropped after the copy was kept", step: "committed", stored: 1 }]) {
    await freshState();
    await fault("imap", c.step, "drop", { times: 1 });
    const m = { subject: "S5C-" + c.step.toUpperCase() + "-" + tag(), body: "BODY-S5C-" + tag() };
    const rec = await composeAndSend(page, sends, { to: "s5c@example.test", subject: m.subject, body: m.body });
    await poll(`${c.label}: filing settled`, () => { const j = jobByKey(rec.key); return j?.state === "submitted" && j.filing_state === "ambiguous"; }, { timeout: 90_000 });
    const job = jobByKey(rec.key);
    check(`${c.label}: message was submitted once`, job.state === "submitted" && (await smtpCount(m.subject)) === 1);
    check(`${c.label}: the exact submitted copy is retained`, job.has_sent);
    await gotoOutbox(page);
    const row = await rowReady(page, "submitted");
    await row.getByText("its Sent copy is unconfirmed").first().waitFor({ timeout: 20_000 }).catch(() => {});
    check(`${c.label}: row says the Sent copy is unconfirmed and not to resend`, await row.getByText("its Sent copy is unconfirmed. Do not resend it.").isVisible(), await page.locator("section.settings-section").innerText());
    const link = row.getByRole("link", { name: "Download the submitted copy" });
    check(`${c.label}: download link present`, await link.isVisible());
    check(`${c.label}: row offers no Review/Cancel for a submitted send`, (await row.getByRole("button", { name: "Review saved composition" }).count()) === 0 && (await row.getByRole("button", { name: "Cancel send" }).count()) === 0);
    const [download] = await Promise.all([page.waitForEvent("download"), link.click()]);
    const file = path.join(work, "download-" + c.step + ".eml");
    await download.saveAs(file);
    const eml = readFileSync(file, "utf8");
    check(`${c.label}: downloaded .eml is the submitted message`, eml.includes(m.subject) && eml.includes("s5c@example.test"), eml.slice(0, 200));
    await shot(page, "filing-" + c.step);
    await observe(`${c.label}: filing is never retried and the message never resent`, 8000, async () => {
      const stored = await imapCount(m.subject), sent = await smtpCount(m.subject);
      return stored === c.stored && sent === 1 && jobByKey(rec.key).filing_state === "ambiguous" ? null : `imap stored=${stored} smtp=${sent} filing=${jobByKey(rec.key).filing_state}`;
    });
    if (c.step === "committed") {
      check(`${c.label}: row offers 'Remove saved Sent copy'`, await row.getByRole("button", { name: "Remove saved Sent copy" }).isVisible());
      await row.getByRole("button", { name: "Remove saved Sent copy" }).click();
      await poll(`${c.label}: saved Sent copy erased`, () => !jobByKey(rec.key).has_sent, { timeout: 30_000 });
      check(`${c.label}: removal kept the submitted outcome and the unconfirmed-filing state`, jobByKey(rec.key).state === "submitted" && jobByKey(rec.key).filing_state === "ambiguous");
      await poll(`${c.label}: download link gone`, async () => (await row.getByRole("link", { name: "Download the submitted copy" }).count()) === 0, { timeout: 15_000 });
    }
  }
  await context.close();
  current.status = current.checks.every((c) => c.ok) ? "pass" : "fail";
}

async function s6Cancel() {
  scenario("S6", "cancel inside the undo window; a cancelled key cannot be revived by replay");
  await freshState();
  setUndoExtra(10);
  const context = await newContext();
  const page = await context.newPage();
  const sends = trackSends(page);
  await login(page, "Owner");
  const m = "S6-" + tag();
  const rec = await composeAndSend(page, sends, { to: "s6@example.test", subject: m, body: "body " + m });
  await gotoOutbox(page, { client: true });
  const pending = await rowReady(page, "pending");
  check("pending row offers Cancel send", await pending.getByRole("button", { name: "Cancel send" }).isVisible(), await page.locator("section.settings-section").innerText());
  await shot(page, "pending");
  await pending.getByRole("button", { name: "Cancel send" }).click();
  await poll("cancelled in the database", () => jobByKey(rec.key)?.state === "cancelled", { timeout: 30_000 });
  check("UI flips to cancelled and offers the saved composition", (await rowFor(page, "cancelled").getByRole("button", { name: "Review saved composition" }).count()) === 1);
  await shot(page, "cancelled");
  await poll("undo window over", () => jobByKey(rec.key).window_over, { timeout: 60_000 });
  await observe("nothing is sent after the window", 4000, async () => ((await smtpCount(m)) === 0 && jobByKey(rec.key).state === "cancelled" ? null : "delivered or state changed"));
  const replay = await apiJSON(page, "POST", "/send", { key: rec.key, body: rec.body });
  check("replaying the cancelled key answers the original cancelled row (replay header, same id)", replay.status === 200 && replay.replay === "true" && replay.json.status === "cancelled" && replay.json.queued === rec.queued, JSON.stringify([replay.status, replay.replay, replay.text]));
  await observe("replay did not revive it", 5000, async () => ((await smtpCount(m)) === 0 && jobByKey(rec.key).state === "cancelled" && jobCount(rec.key) === 1 ? null : "revived or duplicated"));
  check("never delivered, one row", (await smtpCount(m)) === 0 && jobCount(rec.key) === 1);

  // The toast's Undo is the same cancel path and puts the draft back.
  const u = "S6U-" + tag();
  const recU = await composeAndSend(page, sends, { to: "s6u@example.test", subject: u, body: "body " + u });
  await page.getByRole("status").getByRole("button", { name: /Undo/ }).click();
  await poll("toast Undo cancelled", () => jobByKey(recU.key)?.state === "cancelled", { timeout: 30_000 });
  await page.getByRole("dialog", { name: "Compose" }).waitFor();
  check("toast Undo restores the draft", (await page.getByPlaceholder("Subject").inputValue()) === u);
  await context.close();
  current.status = current.checks.every((c) => c.ok) ? "pass" : "fail";
  setUndoExtra(0);
}

// ---------------------------------------------------------------- main
let exitCode = 1;
try {
  await mkdir(output, { recursive: true });
  bins = buildBinaries();
  ports = { web: await freePort(), control: await freePort() };
  baseURL = `http://127.0.0.1:${ports.web}`;
  psql(`CREATE SCHEMA ${schema}`, { raw: true }); schemaCreated = true;
  await startSink();
  await startServer();
  browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || (existsSync("/usr/bin/chromium") ? "/usr/bin/chromium" : undefined),
  });
  const setup = await newContext();
  const setupPage = await setup.newPage();
  await createOwnerA(setupPage);
  await setup.close();
  installUndoKnob();
  seedAccount("owner@owner.local", "acct-a", "sender-a@example.test");
  createOwnerB();
  seedAccount("second@owner.local", "acct-b", "sender-b@example.test");
  // OUTBOX_ONLY=S3,S5 runs a subset while debugging; CI runs everything.
  const only = (process.env.OUTBOX_ONLY || "").split(",").map((v) => v.trim().toLowerCase()).filter(Boolean);
  const all = [["s1", s1OwnerSwitch], ["s2", s2LogoutPending], ["s3", s3KillRecovery], ["s4", s4MultiTabOffline], ["s5", s5AmbiguousUi], ["s5b", s5bFailed], ["s5c", s5cFiling], ["s6", s6Cancel]];
  for (const [id, run] of all) {
    if (only.length && !only.includes(id)) continue;
    try { await run(); }
    catch (error) {
      if (current && current.status === "running") { current.status = "error"; current.error = error instanceof Error ? error.stack || error.message : String(error); }
      log("  ERROR", current?.id, error instanceof Error ? error.message : error);
      let n = 0;
      for (const context of browser.contexts()) for (const page of context.pages()) await page.screenshot({ path: path.join(output, `${current?.id}-error-${++n}.png`), fullPage: true }).catch(() => {});
    }
    if (current.status === "running") current.status = current.checks.every((c) => c.ok) ? "pass" : "fail";
  }
  const ok = results.every((r) => r.status === "pass") && pageErrors.length === 0;
  exitCode = ok ? 0 : 1;
} catch (error) {
  log("FATAL", error instanceof Error ? error.stack : error);
  results.push({ id: "setup", name: "stack setup", status: "error", checks: [], notes: [], screenshots: [], error: String(error?.stack || error) });
} finally {
  const summary = {
    passed: results.length > 0 && results.every((r) => r.status === "pass") && pageErrors.length === 0,
    schema, results, pageErrors,
    logs: { server: path.join(work, "server.log"), sink: path.join(work, "sink.log") },
  };
  await mkdir(output, { recursive: true });
  await writeFile(path.join(output, "outbox-report.json"), JSON.stringify(summary, null, 2) + "\n");
  for (const name of ["server.log", "sink.log"]) {
    try { await writeFile(path.join(output, "outbox-" + name), readFileSync(path.join(work, name))); } catch { /* not created */ }
  }
  await browser?.close().catch(() => {});
  killAll();
  dropSchemaSync();
  if (!process.env.OUTBOX_E2E_KEEP) rmSync(work, { recursive: true, force: true });
  for (const r of results) log(`${r.status.toUpperCase().padEnd(5)} ${r.id} ${r.name}${r.checks.some((c) => !c.ok) ? "  [" + r.checks.filter((c) => !c.ok).map((c) => c.name).join("; ") + "]" : ""}`);
  if (pageErrors.length) log("page errors:", pageErrors.join(" | "));
  process.exitCode = exitCode;
}
