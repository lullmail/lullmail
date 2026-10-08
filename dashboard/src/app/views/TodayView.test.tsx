// @vitest-environment jsdom
import "fake-indexeddb/auto";
import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { TodayView } from "./TodayView";
import { compose, composeOpen, draftStack, resetPrivateState, resetSelection, setList } from "../lib/store";
import { prepareOfflineOwner } from "../lib/offline";
import { clearMemoryCache } from "../lib/api";

// LUL-F02: Today's inline replies used to address the split of the row's
// From line, bypassing the server-computed reply defaults (Reply-To
// honored, own sent mail followed up to its recipients). The parent is
// now resolved through the thread endpoint before the inline reply is
// exposed, and an "ask" default opens the standard composer with a blank
// To instead of guessing.

const host = document.createElement("div");
const settle = (ms = 30) => new Promise((r) => setTimeout(r, ms));

const brief = {
  needs_you: [{
    account: "mirror-a", thread_id: "thread-9", message_id: "m-9",
    from: "Sender <sender@example.test>", subject: "Needs you",
    received_at: new Date().toISOString(), preview: "preview",
  }],
  waiting_on: [], feed_unread: 0, paper_unread: 0, screener: 0,
};

const jsonResponse = (body: unknown) =>
  new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });

let threadResponse: () => Response = () => jsonResponse([]);

beforeEach(async () => {
  await new Promise<void>((resolve, reject) => {
    const request = indexedDB.deleteDatabase("lullmail-offline-v1");
    request.onsuccess = () => resolve();
    request.onerror = request.onblocked = () => reject(request.error ?? new Error("database blocked"));
  });
  localStorage.clear();
  await prepareOfflineOwner({ installation_id: "inst", user_id: "u1", email: "owner@example.test" });
  clearMemoryCache();
  resetSelection();
  resetPrivateState();
  setList({ kind: "none", key: "", loading: false, error: null, rows: [], senders: [], origin: null });
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes("/api/briefing")) return jsonResponse(brief);
    if (url.includes("/api/screener")) return jsonResponse([]);
    if (url.includes("/body?account=")) return threadResponse();
    return jsonResponse([]);
  }));
});

afterEach(() => {
  render(null, host);
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

const replyButton = () => [...host.querySelectorAll("button")].find((b) => /reply/i.test(b.textContent || ""))!;

describe("Today inline reply recipients", () => {
  it("sends to the server-computed Reply-To, resolved before the inline reply opens", async () => {
    threadResponse = () => jsonResponse([{
      id: "m-9", account: "mirror-a", subject: "Needs you", from: "Sender <sender@example.test>",
      to: "owner@example.test", received_at: new Date().toISOString(), bucket: "imbox",
      body: "", reply_to: "replies@example.test",
    }]);
    render(<TodayView />, host);
    await act(async () => { await settle(); });
    await act(async () => { replyButton().click(); await settle(); });

    const area = host.querySelector("textarea.inline-ta") as HTMLTextAreaElement;
    expect(area).not.toBeNull();
    await act(async () => {
      area.value = "inline answer";
      area.dispatchEvent(new Event("input", { bubbles: true }));
      await settle();
    });
    const sends: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/api/outbox")) return new Response(JSON.stringify({ title: "Not Found" }), { status: 404 });
      if (url.includes("/api/send")) {
        sends.push(String(init?.body));
        return jsonResponse({ queued: "q1", undo_seconds: 0, status: "submitted" });
      }
      return jsonResponse({});
    }));
    await act(async () => {
      ([...host.querySelectorAll("button")].find((b) => b.textContent === "Send")!).click();
      await settle(80);
    });
    expect(sends).toHaveLength(1);
    expect(JSON.parse(sends[0]).to).toBe("replies@example.test");
  });

  it("opens the standard composer with a blank To when the server's default is ask", async () => {
    threadResponse = () => jsonResponse([{
      id: "m-9", account: "mirror-a", subject: "Needs you", from: "Sender <sender@example.test>",
      to: "owner@example.test", received_at: new Date().toISOString(), bucket: "imbox",
      body: "", reply_to: "",
    }]);
    render(<TodayView />, host);
    await act(async () => { await settle(); });
    await act(async () => { replyButton().click(); await settle(); });

    expect(host.querySelector("textarea.inline-ta")).toBeNull();
    expect(composeOpen.value).toBe(true);
    expect(compose.value?.to).toBe("");
    expect(compose.value?.replyToId).toBe("m-9");
  });

  it("falls back to the composer with a blank To when the parent cannot be read", async () => {
    threadResponse = () => new Response(JSON.stringify({ title: "Not Found" }), { status: 404 });
    render(<TodayView />, host);
    await act(async () => { await settle(); });
    await act(async () => { replyButton().click(); await settle(); });

    expect(host.querySelector("textarea.inline-ta")).toBeNull();
    expect(composeOpen.value).toBe(true);
    expect(compose.value?.to).toBe("");
    expect(draftStack.value[draftStack.value.length - 1]?.accountId).toBe("mirror-a");
  });

  it("does not publish a late seed after an owner reset", async () => {
    let resolveThread!: (response: Response) => void;
    const pending = new Promise<Response>((resolve) => { resolveThread = resolve; });
    threadResponse = () => pending as unknown as Response;
    render(<TodayView />, host);
    await act(async () => { await settle(); });
    await act(async () => { replyButton().click(); });
    // The owner changes while the thread read is in flight.
    await prepareOfflineOwner({ installation_id: "inst", user_id: "u2", email: "next@example.test" });
    resolveThread(jsonResponse([{
      id: "m-9", account: "mirror-a", subject: "Needs you", from: "Sender <sender@example.test>",
      to: "owner@example.test", received_at: new Date().toISOString(), bucket: "imbox",
      body: "", reply_to: "replies@example.test",
    }]));
    await act(async () => { await settle(); });
    expect(host.querySelector("textarea.inline-ta")).toBeNull();
    expect(composeOpen.value).toBe(false);
  });
});
