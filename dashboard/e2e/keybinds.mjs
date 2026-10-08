// Real-Chromium acceptance for the keyboard layer (lib/keys.ts) and the
// in-app shortcuts help. The real App is mounted by e2e/keybinds-fixture.tsx
// against a synthetic, stateful API: every /api request is intercepted, there
// are no credentials, and any request that leaves 127.0.0.1 fails the run.
// Assertions are on observable outcomes: DOM state and the API calls (with
// bodies) the app actually sends.
import { chromium, expect } from "@playwright/test";
import { createServer } from "vite";
import preact from "@preact/preset-vite";
import { existsSync } from "node:fs";
import { mkdir, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("../", import.meta.url));
// Ctrl on Linux/Windows, Cmd on macOS (see selection.mjs): the handler accepts
// either, but Ctrl+click is a context-menu click on macOS.
const mod = process.platform === "darwin" ? "Meta" : "Control";
const output = process.env.E2E_OUTPUT || "/tmp/lullmail-keybinds-browser";
const ACCOUNT = "test-account";

/* ---- synthetic API ---- */

function makeApi({ twoAccounts = false } = {}) {
  const now = Date.now();
  const iso = (minutesAgo) => new Date(now - minutesAgo * 60000).toISOString();
  const msg = (n, bucket, read, from, subject) => ({
    id: "m" + n, thread: "t" + n, account: ACCOUNT, bucket, read, from, subject,
    received_at: iso(n * 7 + 1),
  });
  const msgs = [
    msg(0, "imbox", false, "Ada Lovelace <ada@example.test>", "Quarterly numbers"),
    msg(1, "imbox", false, "Grace Hopper <grace@example.test>", "Lunch on Friday"),
    msg(2, "imbox", true, "Alan Turing <alan@example.test>", "Re: Contract draft"),
    msg(3, "imbox", false, "Billing <billing@example.test>", "Invoice 1042"),
    msg(4, "imbox", false, "Edsger Dijkstra <edsger@example.test>", "Trip photos"),
    msg(5, "feed", false, "Digest <digest@example.test>", "Weekly digest"),
    msg(6, "feed", false, "Digest <digest@example.test>", "Another digest"),
  ];
  const sender = (name, n) => ({ sender: `${name} <${name.toLowerCase()}@example.test>`, waiting: n, newest: iso(3), sample_subject: "Hello from " + name });
  const senders = [sender("Newcomer", 2), sender("Second", 1), sender("Third", 4)];
  const calls = [];
  const unhandled = [];
  const row = (m) => ({
    account: m.account, thread_id: m.thread, message_id: m.id, from: m.from, subject: m.subject,
    received_at: m.received_at, read: m.read, preview: "Synthetic mail. No real mailbox is connected.",
    bucket: m.bucket, ...(m.snooze_until ? { snooze_until: m.snooze_until } : {}),
  });
  const inBucket = (name) => msgs.filter((m) => name === "snoozed" ? m.bucket === "set_aside" || m.bucket === "later" : m.bucket === name);

  const undoReceipts = new Map();
  let undoSequence = 0;
  async function handle(route) {
    const req = route.request();
    const url = new URL(req.url());
    const path = url.pathname.replace(/^\/api/, "");
    const method = req.method();
    if (path === "/events") {
      await route.fulfill({ status: 200, contentType: "text/event-stream", body: ": synthetic\n\n" });
      return;
    }
    const raw = req.postData();
    const body = raw ? JSON.parse(raw) : undefined;
    const query = Object.fromEntries(url.searchParams);
    calls.push({ method, path, ...(Object.keys(query).length ? { query } : {}), ...(body !== undefined ? { body } : {}) });
    const ok = (value) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(value) });
    let m;
    if (method === "GET") {
      if (path === "/auth/status") return ok({ configured: true, authenticated: true, email: "owner@example.test", bootstrap_available: false, passkey_supported: false, installation_id: "inst-1", user_id: "user-1" });
      if (path === "/counts") return ok({ imbox: inBucket("imbox").filter((r) => !r.read).length, screener: senders.length });
      if (path === "/prefs") return ok({ screening_enabled: true });
      if (path === "/accounts") return ok([{ id: ACCOUNT, address: "owner@example.test" }, ...(twoAccounts ? [{ id: "second-account", address: "second@example.test" }] : [])]);
      if (path === "/mailboxes") return ok([{ name: "inbox" }, { name: "sent" }]);
      if (path === "/recent") return ok(inBucket("imbox").slice(0, 3).map(row));
      if (path === "/screener") return ok(senders.map((s) => ({ ...s })));
      if (path === "/briefing") return ok({ needs_you: [], waiting_on: [], feed_unread: 0, paper_unread: 0, screener: 0 });
      if (path === "/board") return ok({ needs_you: [], waiting_on: [], done: [] });
      if (path === "/notes" || path === "/people") return ok([]);
      if ((m = path.match(/^\/buckets\/([a-z_]+)$/))) return ok({ rows: inBucket(m[1]).map(row), has_more: false });
      const message = (x) => ({
        id: x.id, account: x.account, subject: x.subject, from: x.from, to: "owner@example.test",
        received_at: x.received_at, bucket: x.bucket, body: "Synthetic body only.", body_status: "ready",
        reply_to: x.from.match(/<(.+)>/)[1],
      });
      if ((m = path.match(/^\/threads\/([^/]+)$/))) {
        const found = msgs.filter((x) => x.thread === decodeURIComponent(m[1]));
        return ok({rows: found.map(message), has_more: false});
      }
      if ((m = path.match(/^\/messages\/([^/]+)\/body$/))) {
        const found = msgs.find((x) => x.id === decodeURIComponent(m[1]) && x.account === query.account);
        return ok(found ? message(found) : {});
      }
    } else if (method === "POST") {
      if ((m = path.match(/^\/messages\/([^/]+)\/action$/))) {
        const target = msgs.find((x) => x.id === decodeURIComponent(m[1]));
        if (!target) return ok({});
        if (body.action === "restore") {
          const receipt = undoReceipts.get(body.undo_token);
          if (!receipt || receipt.id !== target.id) throw new Error("invalid synthetic undo authority");
          Object.assign(target, receipt.before);
          if (!("snooze_until" in receipt.before)) delete target.snooze_until;
          undoReceipts.delete(body.undo_token);
          return ok({});
        }
        const before = {...target};
        if (body.action === "read") target.read = true;
        else if (body.action === "unread") target.read = false;
        else {
          target.bucket = body.action;
          if (body.until) target.snooze_until = body.until; else delete target.snooze_until;
        }
        const undo_token = "receipt-" + (++undoSequence);
        undoReceipts.set(undo_token, {id:target.id,before});
        return ok({undo_token});
      }
      if (path === "/board/pin") return ok({ card_id: "card-" + body.thread_id, created: true, subject: "pinned" });
      if (path === "/board/unpin") return ok({});
      if (path === "/screener/decide") {
        const at = senders.findIndex((s) => s.sender === body.sender);
        if (at >= 0) senders.splice(at, 1);
        return ok({});
      }
      if (path === "/screener/undecide") {
        senders.push(sender(body.sender.split(" ")[0], 1));
        return ok({});
      }
      if (path === "/send") return ok({ queued: "q1", undo_seconds: 5 });
    } else if (method === "DELETE" && path === "/outbox/q1") {
      return ok({});
    }
    unhandled.push(method + " " + path);
    await route.fulfill({ status: 404, contentType: "application/json", body: JSON.stringify({ title: "unhandled synthetic route" }) });
  }

  return {
    handle, calls, unhandled,
    mark: () => calls.length,
    since: (mark) => calls.slice(mark),
    mutations: (mark) => calls.slice(mark).filter((c) => c.method !== "GET")
      // A restore carries the server-issued one-use receipt; assertions
      // match the action and the presence of a receipt, not a specific one.
      .map((c) => c.body?.action === "restore" && c.body.undo_token ? {...c, body: {...c.body, undo_token: "receipt"}} : c),
  };
}

const act = (id, action, extra = {}) => ({ method: "POST", path: `/messages/${id}/action`, query: { account: ACCOUNT }, body: { action, ...(action === "restore" ? { undo_token: "receipt" } : {}), ...extra } });
const decide = (sender, allow, route) => ({ method: "POST", path: "/screener/decide", body: { sender, allow, route } });
const byPath = (a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0);

/* ---- runner ---- */

const server = await createServer({
  root, configFile: false, plugins: [preact(), {
    name: "keybinds-fixture",
    configureServer(server) {
      server.middlewares.use("/__keybinds_test", (_req, res) => {
        res.setHeader("Content-Type", "text/html");
        res.end('<!doctype html><html data-theme="light"><head><meta name="viewport" content="width=device-width, initial-scale=1"><link rel="stylesheet" href="/styles.css"></head><body><div id="app"></div><script type="module" src="/e2e/keybinds-fixture.tsx"></script></body></html>');
      });
    },
  }], server: { host: "127.0.0.1", port: 0 },
  // Pre-bundle the fixture's deps before the first request; otherwise a cold
  // cache makes Vite re-optimize and reload the page mid-test.
  optimizeDeps: { entries: ["e2e/keybinds-fixture.tsx"] },
});

let browser;
let port = 0;
const results = [];
const covered = new Set();
let helpLabels = [];

const only = process.env.KEYBINDS_ONLY;
async function scenario(name, covers, fn, { layout = "document", width = 1280, path = "/", twoAccounts = false } = {}) {
  if (only && !name.includes(only)) return;
  const context = await browser.newContext({ viewport: { width, height: 900 }, serviceWorkers: "block" });
  const page = await context.newPage();
  const api = makeApi({ twoAccounts });
  const errors = [];
  const external = [];
  page.on("pageerror", (error) => errors.push(error.message));
  page.on("console", (message) => { if (message.type() === "error") errors.push("console: " + message.text()); });
  // Anything that is neither the local fixture server nor the intercepted API
  // would be a real network request; refuse it and fail the scenario.
  await context.route((url) => url.protocol.startsWith("http") && url.hostname !== "127.0.0.1", async (route) => {
    external.push(route.request().url());
    await route.abort();
  });
  await context.route("**/api/**", api.handle);
  if (layout === "classic") await context.addInitScript(() => localStorage.setItem("es-layout", "classic"));
  const started = Date.now();
  try {
    await page.goto(`http://127.0.0.1:${port}/__keybinds_test?path=${encodeURIComponent(path)}`);
    await fn(makeKit(page, api));
    expect(errors, "browser errors").toEqual([]);
    expect(external, "non-local requests").toEqual([]);
    expect(api.unhandled, "unhandled synthetic API routes").toEqual([]);
    covers.forEach((label) => covered.add(label));
    results.push({ name, ok: true, ms: Date.now() - started });
    console.log("ok   " + name);
  } catch (error) {
    const where = error instanceof Error ? (error.stack || "").split("\n").find((line) => line.includes("keybinds.mjs")) : "";
    const failure = (error instanceof Error ? error.message : String(error)) + (where ? "\n" + where.trim() : "");
    results.push({ name, ok: false, failure });
    console.log("FAIL " + name + "\n     " + failure.split("\n").slice(-14).join("\n     "));
    await mkdir(output, { recursive: true });
    await page.screenshot({ path: output + "/" + name.replace(/\W+/g, "-") + ".png", fullPage: true }).catch(() => {});
  } finally {
    await context.close();
  }
}

function makeKit(page, api) {
  const kit = {
    page, api,
    rows: page.locator(".msg-row"),
    toast: page.locator(".toast"),
    dialog: (name) => page.getByRole("dialog", { name }),
    press: (key) => page.keyboard.press(key),
    // A negative assertion needs the app to have had time to react.
    settle: (ms = 200) => page.waitForTimeout(ms),
    cursor: () => page.evaluate(() => {
      const el = document.querySelector(".msg-row.cursor, .screener.cursor");
      return el ? Number(el.getAttribute("data-cursor-index")) : -1;
    }),
    picked: () => page.evaluate(() => [...document.querySelectorAll(".msg-row")]
      .map((el, i) => (el.classList.contains("picked") ? i : -1)).filter((i) => i >= 0)),
    path: () => page.evaluate(() => location.pathname),
    // The rows are on screen AND published to the store the key layer reads.
    async ready(count = 5) {
      await expect(page.locator(".msg-row, .screener")).toHaveCount(count);
      await expect.poll(() => page.evaluate(() => window.keybindsFixture.published())).toBe(count);
      // Views reset selection in a mount effect; let it run before keys arrive.
      await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => setTimeout(resolve, 20)))));
    },
    async expectCursor(n) { await expect.poll(kit.cursor).toBe(n); },
    async expectPicked(list) { await expect.poll(kit.picked).toEqual(list); },
    async expectNoOverlay() { await expect(page.getByRole("dialog")).toHaveCount(0); },
    async expectMutations(mark, expected, sort = false) {
      await expect.poll(() => {
        const got = api.mutations(mark);
        return sort ? got.sort(byPath) : got;
      }).toEqual(sort ? [...expected].sort(byPath) : expected);
    },
    async expectNoMutations(mark) {
      await kit.settle();
      expect(api.mutations(mark)).toEqual([]);
    },
    // A plain input that lives outside the app's tree, for focus-in-a-field cases.
    async field(kind) {
      await page.evaluate((k) => {
        document.getElementById("probe")?.remove();
        const el = k === "textarea" ? document.createElement("textarea")
          : k === "select" ? document.createElement("select")
          : k === "input" ? document.createElement("input")
          : document.createElement("div");
        if (k === "contenteditable") el.setAttribute("contenteditable", "true");
        if (k === "textbox") { el.setAttribute("role", "textbox"); el.setAttribute("tabindex", "0"); }
        if (k === "select") el.innerHTML = "<option>one</option><option>two</option>";
        el.id = "probe";
        el.setAttribute("aria-label", "Probe " + k);
        document.body.appendChild(el);
        el.focus();
      }, kind);
      await expect(page.locator("#probe")).toBeFocused();
    },
  };
  return kit;
}

const LABEL = {
  nav: "j / k or ↓ / ↑",
  range: "Shift + click / ↑ / ↓",
  toggleClick: "Ctrl/⌘ + click",
  addRange: "Ctrl/⌘ + Shift + click",
  edges: "Home / End",
  all: "Ctrl/⌘ + A",
  enter: "Enter / o",
  back: "u",
  select: "x / Space",
  done: "e",
  snooze: "s",
  inbox: "i",
  pin: "p",
  reply: "r",
  compose: "c",
  screener: "1 2 3 0",
  goto: "g then t b d n i r z s c p",
  palette: "/ or Ctrl/⌘K",
  calView: "y / m / w",
  calToday: "t",
  calMove: "← / →",
  esc: "Esc",
  help: "?",
};

let failure = null;
let completed = false;
try {
  await server.listen();
  port = server.httpServer.address().port;
  browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROMIUM_PATH || (existsSync("/usr/bin/chromium") ? "/usr/bin/chromium" : undefined),
  });

  /* ---- movement and selection ---- */

  await scenario("movement: j k arrows Home End; moving keeps selection", [LABEL.nav, LABEL.edges], async (t) => {
    await t.ready();
    expect(await t.cursor()).toBe(-1);
    await t.press("k");
    await t.expectCursor(4);
    await t.press("Home");
    await t.expectCursor(0);
    await t.press("j");
    await t.expectCursor(1);
    await expect(t.rows.nth(1)).toBeFocused();
    await t.press("ArrowDown");
    await t.expectCursor(2);
    await t.press("ArrowUp");
    await t.expectCursor(1);
    await t.press("k");
    await t.expectCursor(0);
    await t.press("k");
    await t.expectCursor(0);
    await t.press("End");
    await t.expectCursor(4);
    await t.press("j");
    await t.expectCursor(4);
    await t.press("x");
    await t.expectPicked([4]);
    await t.press("k");
    await t.expectCursor(3);
    await t.expectPicked([4]);
    await t.press("ArrowUp");
    await t.expectCursor(2);
    await t.expectPicked([4]);
  });

  await scenario("selection: x, Space, ranges, select-all, clicks", [LABEL.select, LABEL.range, LABEL.edges, LABEL.all, LABEL.toggleClick, LABEL.addRange, LABEL.esc], async (t) => {
    await t.ready();
    await t.press("j");
    await t.press("x");
    await t.expectPicked([0]);
    await expect(t.page.locator(".bulkbar-count")).toHaveText("1 thread");
    await t.press("x");
    await t.expectPicked([]);
    await expect(t.page.locator(".bulkbar")).toHaveCount(0);
    await t.press("Space");
    await t.expectPicked([0]);
    await t.press("Space");
    await t.expectPicked([]);
    // Shift range from the anchor (row 0): arrows, End, Home.
    await t.press("Shift+ArrowDown");
    await t.press("Shift+ArrowDown");
    await t.expectPicked([0, 1, 2]);
    await t.press("Shift+End");
    await t.expectPicked([0, 1, 2, 3, 4]);
    await t.press("Shift+Home");
    await t.expectPicked([0]);
    await t.press("Escape");
    await t.expectPicked([]);
    await t.press(`${mod}+a`);
    await t.expectPicked([0, 1, 2, 3, 4]);
    await t.press("Escape");
    await t.expectPicked([]);
    // Pointer modifiers on rows (the full matrix lives in selection.mjs).
    const subject = (n) => t.rows.nth(n).locator(".row-subject");
    await subject(1).click({ modifiers: [mod] });
    await t.expectPicked([1]);
    await subject(3).click({ modifiers: ["Shift"] });
    await t.expectPicked([1, 2, 3]);
    await t.press("Escape");
    await subject(0).click({ modifiers: [mod] });
    await subject(2).click({ modifiers: [mod, "Shift"] });
    await t.expectPicked([0, 1, 2]);
  });

  /* ---- reader ---- */

  await scenario("reader: Enter and o open, u and Escape close, reader owns the page", [LABEL.enter, LABEL.back, LABEL.esc], async (t) => {
    await t.ready();
    await t.press("Enter");
    await t.settle();
    expect(t.api.since(0).filter((c) => c.path.startsWith("/threads/"))).toEqual([]);
    await t.press("j");
    await t.press("j");
    await t.expectCursor(1);
    const mark = t.api.mark();
    await t.press("Enter");
    await expect(t.page.locator(".thread-title")).toHaveText("Lunch on Friday");
    await expect(t.page.locator(".msg-list")).toHaveCount(0);
    expect(t.api.since(mark).filter((c) => c.method === "GET" && c.path === "/threads/t1")).toEqual([
      { method: "GET", path: "/threads/t1", query: { account: ACCOUNT, page: "1" } },
    ]);
    await t.expectMutations(mark, [act("m1", "read")]);
    // With the reader owning the page, list keys do nothing.
    const open = t.api.mark();
    for (const key of ["j", "k", "ArrowDown", "Enter", "o", "x", "Space"]) await t.press(key);
    await t.settle();
    expect(t.api.since(open).filter((c) => c.method !== "GET" || c.path.startsWith("/threads/"))).toEqual([]);
    await expect(t.page.locator(".thread-title")).toHaveText("Lunch on Friday");
    await t.press("u");
    await expect(t.page.locator(".thread-title")).toHaveCount(0);
    await t.ready();
    await t.press("j");
    await t.press("o");
    await expect(t.page.locator(".thread-title")).toHaveText("Quarterly numbers");
    await t.press("Escape");
    await expect(t.page.locator(".thread-title")).toHaveCount(0);
    await t.ready();
  });

  await scenario("reader verbs: e, i, p, s and r act on the open thread", [LABEL.done, LABEL.inbox, LABEL.pin, LABEL.snooze, LABEL.reply], async (t) => {
    await t.ready();
    await t.press("j");
    await t.press("Enter");
    await expect(t.page.locator(".thread-title")).toHaveText("Quarterly numbers");
    let mark = t.api.mark();
    await t.press("p");
    await t.expectMutations(mark, [{ method: "POST", path: "/board/pin", body: { account: ACCOUNT, thread_id: "t0" } }]);
    await expect(t.toast).toContainText("Pinned to the board");
    mark = t.api.mark();
    await t.press("i");
    await t.expectMutations(mark, [act("m0", "imbox")]);
    await expect(t.page.locator(".thread-title")).toHaveCount(0);
    await t.ready();
    // s: picker, then a date; closes the reader.
    await t.press("j");
    await t.press("Enter");
    await expect(t.page.locator(".thread-title")).toHaveText("Quarterly numbers");
    mark = t.api.mark();
    await t.press("s");
    await expect(t.dialog("Choose when to snooze")).toBeVisible();
    await t.page.getByRole("menuitem", { name: /^Next week/ }).click();
    await expect.poll(() => t.api.mutations(mark).length).toBe(1);
    const [snoozed] = t.api.mutations(mark);
    expect(snoozed.path).toBe("/messages/m0/action");
    expect(snoozed.body.action).toBe("set_aside");
    expect(Math.abs(Date.parse(snoozed.body.until) - (Date.now() + 7 * 86400000))).toBeLessThan(60000);
    await expect(t.page.locator(".thread-title")).toHaveCount(0);
    await t.ready(4);
    // e: marks done and closes the reader.
    await t.press("j");
    await t.press("Enter");
    await expect(t.page.locator(".thread-title")).toHaveText("Lunch on Friday");
    mark = t.api.mark();
    await t.press("e");
    await t.expectMutations(mark, [act("m1", "read")]);
    await expect(t.page.locator(".thread-title")).toHaveCount(0);
    await expect(t.toast).toContainText("Done");
    await t.ready(4);
    // r: replies to the open thread's last message (m0 is snoozed; m1 is first now).
    await t.press("j");
    await t.press("Enter");
    await expect(t.page.locator(".thread-title")).toHaveText("Lunch on Friday");
    await t.press("r");
    await expect(t.dialog("Compose")).toBeVisible();
    await expect(t.page.locator(".compose-kicker")).toHaveText("Replying to Grace Hopper");
    await expect(t.page.getByPlaceholder("To — comma-separated")).toHaveValue("grace@example.test");
    await expect(t.page.getByPlaceholder("Subject")).toHaveValue("Re: Lunch on Friday");
  });

  /* ---- verbs on rows, with undo ---- */

  await scenario("done: e on cursor and selection, undo toast is honest", [LABEL.done, LABEL.select, LABEL.esc], async (t) => {
    await t.ready();
    let mark = t.api.mark();
    await t.press("e");
    await t.expectNoMutations(mark);
    // Two selected rows: one request per row, one combined toast.
    await t.press("j");
    await t.press("x");
    await t.press("j");
    await t.press("x");
    await t.expectPicked([0, 1]);
    mark = t.api.mark();
    await t.press("e");
    await t.expectMutations(mark, [act("m0", "read"), act("m1", "read")], true);
    await expect(t.toast).toContainText("2 threads done");
    await t.expectPicked([]);
    mark = t.api.mark();
    await t.toast.getByRole("button", { name: "Undo" }).click();
    await t.expectMutations(mark, [act("m0", "restore"), act("m1", "restore")], true);
    await expect(t.toast).toHaveCount(0);
    // A row that was already read uses its receipt to restore that exact preimage.
    await t.ready();
    await t.press("j");
    await t.press("j");
    await t.press("j");
    await t.expectCursor(2);
    mark = t.api.mark();
    await t.press("e");
    await t.expectMutations(mark, [act("m2", "read")]);
    await expect(t.toast).toContainText("Done");
    mark = t.api.mark();
    await t.toast.getByRole("button", { name: "Undo" }).click();
    await t.expectMutations(mark, [act("m2", "restore")]);
    // Escape dismisses a toast without undoing anything.
    await t.ready();
    await t.press("j");
    mark = t.api.mark();
    await t.press("e");
    await t.expectMutations(mark, [act("m0", "read")]);
    await expect(t.toast).toBeVisible();
    mark = t.api.mark();
    await t.press("Escape");
    await expect(t.toast).toHaveCount(0);
    await t.expectNoMutations(mark);
  });

  await scenario("snooze: s opens the picker, keys move within it, dates and undo", [LABEL.snooze, LABEL.esc], async (t) => {
    await t.ready();
    await t.press("j");
    await t.press("s");
    const picker = t.dialog("Choose when to snooze");
    await expect(picker).toBeVisible();
    await expect(picker.getByRole("menuitem")).toHaveCount(5);
    await expect(picker.getByRole("menuitem").first()).toBeFocused();
    await t.press("ArrowDown");
    await expect(picker.getByRole("menuitem").nth(1)).toBeFocused();
    await t.press("ArrowUp");
    await expect(picker.getByRole("menuitem").first()).toBeFocused();
    let mark = t.api.mark();
    await t.press("Escape");
    await expect(picker).toHaveCount(0);
    await t.expectNoMutations(mark);
    // Enter on the focused first choice: tomorrow.
    await t.press("s");
    await expect(picker.getByRole("menuitem").first()).toBeFocused();
    mark = t.api.mark();
    await t.press("Enter");
    await expect.poll(() => t.api.mutations(mark).length).toBe(1);
    const [tomorrow] = t.api.mutations(mark);
    expect(tomorrow.path).toBe("/messages/m0/action");
    expect(tomorrow.query).toEqual({ account: ACCOUNT });
    expect(tomorrow.body.action).toBe("set_aside");
    expect(Math.abs(Date.parse(tomorrow.body.until) - (Date.now() + 86400000))).toBeLessThan(60000);
    await expect(t.toast).toContainText("Snoozed until tomorrow");
    await t.ready(4);
    // Undo returns the row to where it came from, with no deadline.
    mark = t.api.mark();
    await t.toast.getByRole("button", { name: "Undo" }).click();
    await t.expectMutations(mark, [act("m0", "restore")]);
    await t.ready(5);
    // Someday carries no date. A selection is snoozed as a whole.
    await t.press("j");
    await t.press("x");
    await t.press("j");
    await t.press("x");
    await t.expectPicked([0, 1]);
    await t.press("s");
    mark = t.api.mark();
    await t.page.getByRole("menuitem", { name: /^Someday/ }).click();
    await t.expectMutations(mark, [act("m0", "later"), act("m1", "later")], true);
    await expect(t.toast).toContainText("2 threads snoozed for someday");
  });

  await scenario("inbox: i files into the Inbox from Reading, with undo", [LABEL.inbox], async (t) => {
    await t.ready(2);
    await t.press("j");
    let mark = t.api.mark();
    await t.press("i");
    await t.expectMutations(mark, [act("m5", "imbox")]);
    await expect(t.toast).toContainText("Moved to Inbox");
    await t.ready(1);
    mark = t.api.mark();
    await t.toast.getByRole("button", { name: "Undo" }).click();
    await t.expectMutations(mark, [act("m5", "restore")]);
    await t.ready(2);
  }, { path: "/reading" });

  await scenario("pin: p pins the cursor row and a selection, with undo", [LABEL.pin], async (t) => {
    await t.ready();
    await t.press("j");
    let mark = t.api.mark();
    await t.press("p");
    await t.expectMutations(mark, [{ method: "POST", path: "/board/pin", body: { account: ACCOUNT, thread_id: "t0" } }]);
    await expect(t.toast).toContainText("Pinned to the board");
    mark = t.api.mark();
    await t.toast.getByRole("button", { name: "Undo" }).click();
    await t.expectMutations(mark, [{ method: "POST", path: "/board/unpin", body: { card_id: "card-t0" } }]);
    await t.ready();
    await t.press("j");
    await t.press("x");
    await t.press("j");
    await t.press("x");
    mark = t.api.mark();
    await t.press("p");
    await t.expectMutations(mark, [
      { method: "POST", path: "/board/pin", body: { account: ACCOUNT, thread_id: "t0" } },
      { method: "POST", path: "/board/pin", body: { account: ACCOUNT, thread_id: "t1" } },
    ]);
  });

  /* ---- compose ---- */

  await scenario("compose: c opens, Escape parks, c stacks a draft only outside fields", [LABEL.compose, LABEL.esc], async (t) => {
    await t.ready();
    await t.press("c");
    const compose = t.dialog("Compose");
    await expect(compose).toBeVisible();
    await expect(t.page.locator(".compose-kicker")).toHaveText("New message");
    const to = t.page.getByPlaceholder("To — comma-separated");
    await expect(to).toBeFocused();
    // Typing c into the field is typing, not a command.
    await t.press("c");
    await expect(to).toHaveValue("c");
    await expect(t.page.locator(".compose-ring-count")).toHaveCount(0);
    // Focus outside any field: c stacks another draft.
    await t.page.getByRole("button", { name: "Cc/Bcc" }).focus();
    await t.press("c");
    await expect(t.page.locator(".compose-ring-count").first()).toHaveText("2 of 2");
    // The new draft takes focus (To is empty), not just the first one ever opened.
    await expect(to).toBeFocused();
    // Escape parks the window; the drafts stay.
    await t.press("Escape");
    await expect(compose).toHaveCount(0);
    await t.press("c");
    await expect(compose).toBeVisible();
    await expect(t.page.locator(".compose-ring-count").first()).toHaveText("2 of 2");
    await expect(compose.locator(":focus")).toHaveCount(1);
  });

  await scenario("compose: c is inert while a field outside the app has focus", [LABEL.compose], async (t) => {
    await t.ready();
    for (const kind of ["input", "textarea", "contenteditable", "textbox"]) {
      await t.field(kind);
      await t.page.keyboard.type("c");
      await t.settle(50);
      await expect(t.dialog("Compose")).toHaveCount(0);
    }
  });

  await scenario("compose: Mod+Enter sends with undo; r on a row replies", [LABEL.compose, LABEL.reply], async (t) => {
    await t.ready();
    await t.press("c");
    await t.page.getByPlaceholder("To — comma-separated").fill("friend@example.test");
    await t.page.getByPlaceholder("Subject").fill("Hello");
    await t.page.getByPlaceholder("Write something worth reading.").fill("Body text");
    let mark = t.api.mark();
    await t.press(`${mod}+Enter`);
    await expect.poll(() => t.api.mutations(mark).length).toBe(1);
    expect(t.api.mutations(mark)[0]).toEqual({
      method: "POST", path: "/send",
      body: {
        to: "friend@example.test", cc: "", bcc: "", subject: "Hello", text: "Body text", html: "",
        account_id: ACCOUNT, reply_to_message_id: "", attachments: [],
      },
    });
    await expect(t.toast).toContainText("Sending in 5s");
    await expect(t.dialog("Compose")).toHaveCount(0);
    mark = t.api.mark();
    await t.toast.getByRole("button", { name: "Undo" }).click();
    await t.expectMutations(mark, [{ method: "DELETE", path: "/outbox/q1" }]);
    // Undo brings the draft back, complete.
    await expect(t.dialog("Compose")).toBeVisible();
    await expect(t.page.getByPlaceholder("To — comma-separated")).toHaveValue("friend@example.test");
    await expect(t.page.getByPlaceholder("Subject")).toHaveValue("Hello");
    await t.press("Escape");
    // r with no focused row does nothing; with one, it replies to that row.
    await t.press("r");
    await t.settle(100);
    await expect(t.dialog("Compose")).toHaveCount(0);
    await t.press("j");
    await t.press("j");
    await t.press("j");
    await t.press("r");
    await expect(t.dialog("Compose")).toBeVisible();
    await expect(t.page.locator(".compose-kicker")).toHaveText("Replying to Alan Turing");
    await expect(t.page.getByPlaceholder("To — comma-separated")).toHaveValue("alan@example.test");
    // "Re:" is not doubled.
    await expect(t.page.getByPlaceholder("Subject")).toHaveValue("Re: Contract draft");
    await t.page.getByPlaceholder("Write something worth reading.").fill("Thanks");
    mark = t.api.mark();
    await t.press(`${mod}+Enter`);
    await expect.poll(() => t.api.mutations(mark).length).toBe(1);
    expect(t.api.mutations(mark)[0].body).toMatchObject({
      to: "alan@example.test", subject: "Re: Contract draft", text: "Thanks", reply_to_message_id: "m2", account_id: ACCOUNT,
    });
  });

  /* ---- palette and help ---- */

  await scenario("palette: / and Ctrl/Meta+K open, toggle, are global, and take typing", [LABEL.palette], async (t) => {
    await t.ready();
    const palette = t.dialog("Browse");
    const input = t.page.getByRole("combobox");
    await t.press("/");
    await expect(palette).toBeVisible();
    await expect(input).toBeFocused();
    await expect(input).toHaveValue("");
    await t.press("Escape");
    await expect(palette).toHaveCount(0);
    // Every later opening takes focus too, not just the first.
    await t.press("/");
    await expect(palette).toBeVisible();
    await expect(input).toBeFocused();
    // Letters that are commands elsewhere are just text here.
    const mark = t.api.mark();
    await t.page.keyboard.type("ejcxs");
    await expect(input).toHaveValue("ejcxs");
    expect(t.api.mutations(mark)).toEqual([]);
    await expect(t.dialog("Compose")).toHaveCount(0);
    await t.press("Escape");
    await expect(palette).toHaveCount(0);
    await t.press("Control+k");
    await expect(palette).toBeVisible();
    await t.press("Control+k");
    await expect(palette).toHaveCount(0);
    await t.press("Meta+k");
    await expect(palette).toBeVisible();
    await t.press("Meta+k");
    await expect(palette).toHaveCount(0);
    // Wins even while focus is in a text field.
    await t.field("textarea");
    await t.press(`${mod}+k`);
    await expect(palette).toBeVisible();
    await t.press("Escape");
    await t.page.evaluate(() => document.getElementById("probe").remove());
    // Jump by typing and Enter.
    await t.press("/");
    await expect(input).toBeFocused();
    await t.page.keyboard.type("board");
    await t.press("Enter");
    await expect(palette).toHaveCount(0);
    expect(await t.path()).toBe("/board");
  });

  await scenario("help: ? lists every documented shortcut and makes the rest inert", [LABEL.help, LABEL.esc], async (t) => {
    await t.ready();
    await t.press("?");
    const help = t.dialog("Keyboard shortcuts");
    await expect(help).toBeVisible();
    const labels = await t.page.locator(".shortcut-grid .kbd").allTextContents();
    const source = await t.page.evaluate(() => window.keybindsFixture.shortcuts.map((s) => s[0]));
    expect(labels).toEqual(source);
    helpLabels = labels;
    // The overlay owns the keyboard: no command leaks through.
    const mark = t.api.mark();
    for (const key of ["j", "e", "c", "/", "x", "s", "p", "u", "o"]) await t.press(key);
    await t.settle();
    expect(t.api.mutations(mark)).toEqual([]);
    await expect(t.dialog("Compose")).toHaveCount(0);
    await expect(t.dialog("Browse")).toHaveCount(0);
    await expect(help).toBeVisible();
    await t.press("Escape");
    await expect(help).toHaveCount(0);
    await t.press("?");
    await expect(help).toBeVisible();
    await help.getByRole("button", { name: "Close keyboard shortcuts" }).click();
    await expect(help).toHaveCount(0);
  });

  /* ---- screener ---- */

  await scenario("screener: j k move, 1 2 3 0 decide, undo, row verbs inert", [LABEL.screener, LABEL.nav], async (t) => {
    const cards = t.page.locator(".screener");
    await t.ready(3);
    await t.press("j");
    await t.expectCursor(0);
    await t.press("j");
    await t.expectCursor(1);
    await t.press("k");
    await t.expectCursor(0);
    let mark = t.api.mark();
    for (const key of ["e", "x", "p", "i", "s", "Enter", "Space"]) await t.press(key);
    await t.expectNoMutations(mark);
    await expect(t.dialog("Choose when to snooze")).toHaveCount(0);
    mark = t.api.mark();
    await t.press("1");
    await t.expectMutations(mark, [decide("Newcomer <newcomer@example.test>", true, "imbox")]);
    await expect(t.toast).toContainText("→ Inbox");
    await expect(cards).toHaveCount(2);
    mark = t.api.mark();
    await t.toast.getByRole("button", { name: "Undo" }).click();
    await t.expectMutations(mark, [{ method: "POST", path: "/screener/undecide", body: { sender: "Newcomer <newcomer@example.test>" } }]);
    await expect(cards).toHaveCount(3);
    // After a decision the cursor resets; the next key starts at the top again.
    let left = 3;
    for (const [key, allow, route] of [["2", true, "feed"], ["3", true, "paper_trail"], ["0", false, "blocked"]]) {
      await t.ready(left);
      await t.press("j");
      await t.expectCursor(0);
      mark = t.api.mark();
      const first = (await cards.first().locator(".screener-name").innerText()).trim();
      await t.press(key);
      await expect.poll(() => t.api.mutations(mark).length).toBe(1);
      expect(t.api.mutations(mark)[0]).toMatchObject({ path: "/screener/decide", body: { allow, route } });
      expect(t.api.mutations(mark)[0].body.sender).toContain(first.split(" ")[0]);
      await expect(t.page.locator(".toast").first()).toBeVisible();
      left--;
    }
    await t.ready(0);
  }, { path: "/screener" });

  /* ---- go-to ---- */

  await scenario("go to: g then t b d n i r z s c p, timeout and cancel", [LABEL.goto], async (t) => {
    await t.ready();
    for (const [key, to] of [["t", "/today"], ["b", "/board"], ["d", "/calendar"], ["n", "/notes"], ["i", "/"],
      ["r", "/reading"], ["z", "/snoozed"], ["s", "/screener"], ["c", "/receipts"], ["p", "/people"]]) {
      await t.press("g");
      await t.press(key);
      await expect.poll(t.path, { message: "g " + key }).toBe(to);
    }
    // Neither the timeout nor a non-destination key leaves the jump armed.
    await t.press("g");
    await t.page.waitForTimeout(1400);
    await t.press("t");
    await t.settle(100);
    expect(await t.path()).toBe("/people");
    await t.press("g");
    await t.press("q");
    await t.press("t");
    await t.settle(100);
    expect(await t.path()).toBe("/people");
    // g typed into a field is text, and does not arm the jump.
    await t.field("input");
    await t.press("g");
    await t.press("t");
    await expect(t.page.locator("#probe")).toHaveValue("gt");
    expect(await t.path()).toBe("/people");
  });

  /* ---- guards ---- */

  await scenario("modifiers: no shortcut fires with Ctrl, Meta or Alt held", [], async (t) => {
    await t.ready();
    await t.press("j");
    await t.press("j");
    await t.expectCursor(1);
    const mark = t.api.mark();
    for (const held of ["Control", "Meta", "Alt"]) {
      for (const key of ["e", "s", "i", "p", "c", "r", "u", "x", "o", "j", "k", "/", "?", "Enter", "1", "0"]) {
        // Ctrl/Cmd+K is the palette's own binding; Alt+K is not.
        if (key === "k" && held !== "Alt") continue;
        await t.press(`${held}+${key}`);
      }
      await t.press(`${held}+g`);
      await t.press("t");
    }
    await t.settle();
    expect(t.api.since(mark).filter((c) => c.method !== "GET" || c.path.startsWith("/threads/"))).toEqual([]);
    await t.expectNoOverlay();
    await t.expectCursor(1);
    await t.expectPicked([]);
    await expect(t.page.locator(".thread-title")).toHaveCount(0);
    expect(await t.path()).toBe("/");
    // Synthetic IME composition is ignored too.
    await t.page.evaluate(() => {
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "e", isComposing: true, bubbles: true }));
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "e", keyCode: 229, bubbles: true }));
    });
    await t.expectNoMutations(mark);
  });

  await scenario("fields: nothing fires while typing in input, textarea, select, contenteditable or textbox", [], async (t) => {
    await t.ready();
    await t.press("j");
    await t.press("j");
    await t.press("x");
    await t.expectCursor(1);
    await t.expectPicked([1]);
    const mark = t.api.mark();
    const typed = "esipcruxjkogt/?10";
    for (const kind of ["input", "textarea", "contenteditable", "textbox", "select"]) {
      await t.field(kind);
      await t.page.keyboard.type(typed);
      await t.press("Enter");
      await t.press("Space");
      await t.settle(100);
      await t.expectNoOverlay();
      await t.expectCursor(1);
      await t.expectPicked([1]);
      expect(await t.path()).toBe("/");
      await expect(t.page.locator(".thread-title")).toHaveCount(0);
      const content = await t.page.evaluate(() => {
        const el = document.getElementById("probe");
        return "value" in el && el.tagName !== "SELECT" ? el.value : el.textContent;
      });
      if (kind === "input") expect(content).toBe(typed + " ");
      if (kind === "textarea") expect(content).toBe(typed + "\n ");
      if (kind === "contenteditable") expect(content.replace(/\s/g, "")).toBe(typed);
    }
    expect(t.api.mutations(mark)).toEqual([]);
  });

  await scenario("repeat: held keys act once, except movement", [LABEL.done, LABEL.select, LABEL.enter, LABEL.compose, LABEL.pin, LABEL.snooze, LABEL.nav], async (t) => {
    await t.ready();
    const hold = async (key, times = 4) => {
      for (let n = 0; n < times; n++) await t.page.keyboard.down(key);
      await t.page.keyboard.up(key);
    };
    await hold("j");
    await t.expectCursor(3);
    await hold("k", 2);
    await t.expectCursor(1);
    await t.press("Home");
    await t.press("j");
    await t.expectCursor(1);
    await hold("x");
    await t.expectPicked([1]);
    await hold("Space");
    await t.expectPicked([]);
    await hold("Space");
    await t.expectPicked([1]);
    let mark = t.api.mark();
    await hold("p");
    await t.settle();
    expect(t.api.mutations(mark).filter((c) => c.path === "/board/pin")).toHaveLength(1);
    await t.ready();
    await t.press("j");
    mark = t.api.mark();
    await hold("e");
    await t.settle();
    expect(t.api.mutations(mark).filter((c) => c.path.endsWith("/action"))).toHaveLength(1);
    mark = t.api.mark();
    await t.press("Home");
    await hold("s");
    await expect(t.dialog("Choose when to snooze")).toBeVisible();
    await t.press("Escape");
    await hold("c");
    await expect(t.dialog("Compose")).toBeVisible();
    await expect(t.page.locator(".compose-ring-count")).toHaveCount(0);
    await t.press("Escape");
    await t.page.getByRole("button", { name: "Settings and shortcuts" }).focus();
    await t.page.evaluate(() => document.activeElement.blur());
    mark = t.api.mark();
    await t.press("Home");
    await hold("Enter");
    await expect(t.page.locator(".thread-title")).toBeVisible();
    await t.settle();
    expect(t.api.since(mark).filter((c) => c.method === "GET" && c.path.startsWith("/threads/"))).toHaveLength(1);
    expect(t.api.mutations(mark).filter((c) => c.path.endsWith("/action"))).toHaveLength(1);
  });

  /* ---- Escape layering ---- */

  await scenario("escape: palette, compose, toast, then reader, one layer per press", [LABEL.esc], async (t) => {
    await t.ready();
    await t.press("j");
    await t.press("Enter");
    await expect(t.page.locator(".thread-title")).toBeVisible();
    await t.press("p");
    await expect(t.toast).toContainText("Pinned");
    await t.press("r");
    await expect(t.dialog("Compose")).toBeVisible();
    await t.press(`${mod}+k`);
    await expect(t.dialog("Browse")).toBeVisible();
    await t.press("Escape");
    await expect(t.dialog("Browse")).toHaveCount(0);
    await expect(t.dialog("Compose")).toBeVisible();
    await expect(t.toast).toBeVisible();
    await t.press("Escape");
    await expect(t.dialog("Compose")).toHaveCount(0);
    await expect(t.toast).toBeVisible();
    await expect(t.page.locator(".thread-title")).toBeVisible();
    await t.press("Escape");
    await expect(t.toast).toHaveCount(0);
    await expect(t.page.locator(".thread-title")).toBeVisible();
    await t.press("Escape");
    await expect(t.page.locator(".thread-title")).toHaveCount(0);
    await t.ready();
  });

  await scenario("escape: help over the reader, picker and menu over a selection", [LABEL.esc], async (t) => {
    await t.ready();
    await t.press("j");
    await t.press("Enter");
    await expect(t.page.locator(".thread-title")).toBeVisible();
    await t.press("?");
    await expect(t.dialog("Keyboard shortcuts")).toBeVisible();
    await t.press("Escape");
    await expect(t.dialog("Keyboard shortcuts")).toHaveCount(0);
    await expect(t.page.locator(".thread-title")).toBeVisible();
    await t.press("Escape");
    await expect(t.page.locator(".thread-title")).toHaveCount(0);
    await t.ready();
    // Picker over a selection: the picker goes first, the selection survives.
    await t.press("j");
    await t.press("x");
    await t.press("s");
    await expect(t.dialog("Choose when to snooze").getByRole("menuitem").first()).toBeFocused();
    await t.press("Escape");
    await expect(t.dialog("Choose when to snooze")).toHaveCount(0);
    await t.expectPicked([0]);
    // The bulk bar's own menu consumes its Escape too.
    await t.page.locator(".bulkbar").getByRole("button", { name: "Snooze" }).click();
    // The menu moves focus into itself in an effect; Escape only closes it once it holds focus.
    await expect(t.page.getByRole("menu").getByRole("menuitem").first()).toBeFocused();
    await t.press("Escape");
    await expect(t.page.getByRole("menu")).toHaveCount(0);
    await t.expectPicked([0]);
    await t.press("Escape");
    await t.expectPicked([]);
  });

  await scenario("escape: the settings menu closes before the selection and the reader", [LABEL.esc], async (t) => {
    await t.ready();
    await t.press("j");
    await t.press("x");
    await t.expectPicked([0]);
    await t.page.getByRole("button", { name: "Settings and shortcuts" }).click();
    await expect(t.page.getByRole("menu").getByRole("menuitem").first()).toBeFocused();
    await t.press("Escape");
    await expect(t.page.getByRole("menu")).toHaveCount(0);
    await t.expectPicked([0]);
    await t.press("Escape");
    await t.expectPicked([]);
    await t.press("j");
    await t.press("Enter");
    await expect(t.page.locator(".thread-title")).toBeVisible();
    await t.page.getByRole("button", { name: "Settings and shortcuts" }).click();
    await expect(t.page.getByRole("menu").getByRole("menuitem").first()).toBeFocused();
    await t.press("Escape");
    await expect(t.page.getByRole("menu")).toHaveCount(0);
    await expect(t.page.locator(".thread-title")).toBeVisible();
  });

  await scenario("escape: the mailbox picker closes before the selection and the reader", [LABEL.esc], async (t) => {
    await t.ready();
    const picker = t.page.getByRole("button", { name: "Choose mailbox" });
    await t.press("j");
    await t.press("x");
    await t.expectPicked([0]);
    await picker.click();
    // Like the settings menu, it takes focus into itself and arrows move within it.
    const items = t.page.getByRole("menu").getByRole("menuitem");
    await expect(items.first()).toBeFocused();
    await t.press("ArrowDown");
    await expect(items.nth(1)).toBeFocused();
    await t.press("Escape");
    await expect(t.page.getByRole("menu")).toHaveCount(0);
    await expect(picker).toBeFocused();
    await t.expectPicked([0]);
    await t.press("Escape");
    await t.expectPicked([]);
    await t.press("j");
    await t.press("Enter");
    await expect(t.page.locator(".thread-title")).toBeVisible();
    await picker.click();
    await expect(t.page.getByRole("menu").getByRole("menuitem").first()).toBeFocused();
    await t.press("Escape");
    await expect(t.page.getByRole("menu")).toHaveCount(0);
    await expect(t.page.locator(".thread-title")).toBeVisible();
  }, { twoAccounts: true });

  await scenario("calendar: y m w t and arrows drive the view and are guarded", [LABEL.calView, LABEL.calToday, LABEL.calMove], async (t) => {
    const title = t.page.locator(".page-head h1");
    const zoom = (name) => t.page.locator(".cal-zoom-btn.active", { hasText: name });
    const now = new Date();
    const month = (offset) => {
      const d = new Date(now.getFullYear(), now.getMonth() + offset, 1);
      return d.toLocaleDateString("en-US", { month: "long", year: "numeric" });
    };
    await expect(title).toHaveText(month(0));
    await expect(zoom("Month")).toBeVisible();
    // The view's key listener is attached in a mount effect; retry the first key until it is.
    await expect(async () => {
      await t.press("y");
      await expect(zoom("Year")).toBeVisible({ timeout: 500 });
    }).toPass();
    await expect(title).toHaveText(String(now.getFullYear()));
    await t.press("w");
    await expect(zoom("Week")).toBeVisible();
    await t.press("m");
    await expect(zoom("Month")).toBeVisible();
    // The listener is re-attached in an effect after each zoom change; let it land.
    await t.settle(150);
    await t.press("ArrowRight");
    await expect(title).toHaveText(month(1));
    await t.press("ArrowRight");
    await expect(title).toHaveText(month(2));
    await t.press("ArrowLeft");
    await expect(title).toHaveText(month(1));
    await t.press("t");
    await expect(title).toHaveText(month(0));
    // Guards: held modifiers, fields outside the app, and overlays leave the view alone.
    await t.press("ArrowRight");
    await expect(title).toHaveText(month(1));
    for (const held of ["Control", "Meta", "Alt"]) {
      for (const key of ["y", "w", "t", "ArrowRight", "ArrowLeft"]) await t.press(`${held}+${key}`);
    }
    await t.settle();
    await expect(title).toHaveText(month(1));
    await expect(zoom("Month")).toBeVisible();
    for (const kind of ["input", "textarea", "select", "contenteditable", "textbox"]) {
      await t.field(kind);
      await t.press("y");
      await t.press("ArrowLeft");
      await t.press("t");
    }
    await t.settle();
    await expect(title).toHaveText(month(1));
    await expect(zoom("Month")).toBeVisible();
    await t.page.evaluate(() => document.getElementById("probe").remove());
    await t.press("?");
    await expect(t.dialog("Keyboard shortcuts")).toBeVisible();
    for (const key of ["y", "w", "t", "ArrowLeft"]) await t.press(key);
    await t.settle();
    await t.press("Escape");
    await expect(title).toHaveText(month(1));
    await expect(zoom("Month")).toBeVisible();
    // The g-jump's own t goes to Today and does not also move the calendar.
    await t.press("g");
    await t.press("t");
    await expect.poll(t.path).toBe("/today");
  }, { path: "/calendar" });

  await scenario("classic layout: list stays live beside the reader; selection clears before the reader", [LABEL.esc, LABEL.enter, LABEL.back, LABEL.nav], async (t) => {
    await t.ready();
    await t.press("j");
    await t.press("x");
    await t.press("j");
    await t.press("x");
    await t.expectPicked([0, 1]);
    // Enter opens the focused thread and clears the explicit selection.
    await t.press("Enter");
    await expect(t.page.locator(".reader-pane .thread-title")).toHaveText("Lunch on Friday");
    await t.expectPicked([]);
    // The list still answers j/k with the reader open.
    await t.press("j");
    await t.expectCursor(2);
    await expect(t.page.locator(".reader-pane .thread-title")).toHaveText("Lunch on Friday");
    await t.press("x");
    await t.expectPicked([2]);
    await t.press("Escape");
    await t.expectPicked([]);
    await expect(t.page.locator(".reader-pane .thread-title")).toBeVisible();
    await t.press("Escape");
    await expect(t.page.locator(".reader-pane .thread-title")).toHaveCount(0);
    await t.press("o");
    await expect(t.page.locator(".reader-pane .thread-title")).toBeVisible();
    await t.press("u");
    await expect(t.page.locator(".reader-pane .thread-title")).toHaveCount(0);
    await t.ready();
  }, { layout: "classic" });

  /* ---- help vs implementation ---- */

  if (only) {
    console.log("KEYBINDS_ONLY set: skipped the help coverage check");
    helpLabels = [];
  } else expect(helpLabels.length, "help scenario must have run").toBeGreaterThan(0);
  const uncovered = helpLabels.filter((label) => !covered.has(label));
  if (uncovered.length) {
    results.push({ name: "help coverage", ok: false, failure: "documented in help but not exercised: " + uncovered.join(" | ") });
    console.log("FAIL help coverage\n     documented in help but not exercised: " + uncovered.join(" | "));
  } else if (!only) {
    console.log("ok   every shortcut row in the help is exercised (" + helpLabels.length + " rows)");
  }

  const failed = results.filter((r) => !r.ok);
  completed = failed.length === 0;
  if (failed.length) throw new Error(failed.length + " of " + results.length + " scenarios failed: " + failed.map((r) => r.name).join("; "));
  console.log(`Chromium keybind checks passed: ${results.length} scenarios, ${helpLabels.length} help rows covered; synthetic API only.`);
} catch (error) {
  failure = error instanceof Error ? error.message : String(error);
  throw error;
} finally {
  await mkdir(output, { recursive: true });
  await writeFile(output + "/keybinds-report.json", JSON.stringify({ completed, failure, results }, null, 2) + "\n");
  await browser?.close();
  await server.close();
}
