// @vitest-environment jsdom
import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { navigate, path, startRouter } from "./router";
import { checked, closeReader, cursor, list, palette, query, reader, rememberListScroll, rowIdentity, setList } from "./store";
import { Palette } from "../ui/Palette";
import { installKeys } from "./keys";
import type { Row } from "./types";

vi.mock("./api", () => ({ api: vi.fn().mockResolvedValue([]) }));
vi.mock("./actions", () => ({ openThread: vi.fn() }));

const row: Row = { account: "a", message_id: "m", thread_id: "t", from: "a@example.test", subject: "Mail", received_at: "2026-09-29", read: false, preview: "" };
let host: HTMLDivElement;
let offRouter: (() => void) | undefined;
let offKeys: () => void;
function openReader() {
  reader.value = { ...reader.value, threadId: "t", account: "a" };
  cursor.value = 0;
  checked.value = new Set([rowIdentity(row)]);
}
beforeEach(() => {
  vi.stubGlobal("requestAnimationFrame", (fn: FrameRequestCallback) => { fn(0); return 1; });
  window.scrollTo = vi.fn();
  window.history.replaceState({}, "", "/");
  query.value = "";
  palette.value = false;
  offRouter = startRouter();
  offKeys = installKeys();
  setList({ key: "bucket:imbox", kind: "rows", rows: [row], senders: [], loading: false, error: null, origin: "imbox" });
  openReader();
  host = document.createElement("div"); document.body.append(host);
});
afterEach(() => {
  offRouter?.(); offKeys();
  act(() => render(null, host)); host.remove();
  query.value = ""; palette.value = false;
  reader.value = { ...reader.value, threadId: null, account: null };
  vi.unstubAllGlobals();
});

describe("reader navigation boundaries", () => {
  it("same-origin sidebar clicks close the reader and immediately fence old actions", () => {
    const a = document.createElement("a"); a.href = "/reading"; host.append(a);
    const ev = new MouseEvent("click", { bubbles: true, cancelable: true });
    act(() => { a.dispatchEvent(ev); });
    expect(ev.defaultPrevented).toBe(true);
    expect(path.value).toBe("/reading");
    expect(reader.value.threadId).toBeNull();
    expect(list.value.kind).toBe("none");
    expect(checked.value.size).toBe(0);
    expect(window.scrollTo).not.toHaveBeenCalled();
  });

  it("same-route navigation closes the reader without erasing its unchanged list", () => {
    navigate("/");
    expect(reader.value.threadId).toBeNull();
    expect(list.value.rows).toEqual([row]);
  });

  it("Back/Forward leave the old reader and clear old selection", () => {
    window.history.replaceState({}, "", "/receipts");
    window.dispatchEvent(new PopStateEvent("popstate"));
    expect(path.value).toBe("/receipts");
    expect(reader.value.threadId).toBeNull();
    expect(checked.value.size).toBe(0);
    expect(cursor.value).toBe(-1);
  });

  it("preserves modified links for native browser navigation", () => {
    const a = document.createElement("a"); a.href = "/reading"; host.append(a);
    const ev = new MouseEvent("click", { bubbles: true, cancelable: true, metaKey: true });
    a.dispatchEvent(ev);
    expect(ev.defaultPrevented).toBe(false);
    expect(path.value).toBe("/");
    expect(reader.value.threadId).toBe("t");
  });

  it("keyboard g navigation leaves the reader instead of changing only its Back label", () => {
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "g", bubbles: true }));
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "r", bubbles: true }));
    expect(path.value).toBe("/reading");
    expect(reader.value.threadId).toBeNull();
  });

  it("palette search closes the reader and shows the new query with no stale list", async () => {
    palette.value = true;
    await act(async () => { render(<Palette />, host); });
    const input = host.querySelector("input")!;
    act(() => { input.value = "rare-query"; input.dispatchEvent(new Event("input", { bubbles: true })); });
    const result = Array.from(host.querySelectorAll('[role="option"]')).find((el) => el.textContent?.includes("Search all mail"))!;
    act(() => { result.dispatchEvent(new MouseEvent("click", { bubbles: true })); });
    expect(query.value).toBe("rare-query");
    expect(reader.value.threadId).toBeNull();
    expect(palette.value).toBe(false);
    expect(list.value.kind).toBe("none");
  });

  it("unregisters router listeners when the app unmounts", () => {
    offRouter?.();
    window.history.replaceState({}, "", "/reading");
    window.dispatchEvent(new PopStateEvent("popstate"));
    expect(path.value).toBe("/");
  });

  it("only returning to the same list restores its scroll position", () => {
    Object.defineProperty(window, "scrollY", { configurable: true, value: 450 });
    rememberListScroll();
    closeReader();
    expect(window.scrollTo).toHaveBeenCalledExactlyOnceWith({ top: 450 });
    vi.mocked(window.scrollTo).mockClear();
    closeReader();
    navigate("/reading");
    expect(window.scrollTo).not.toHaveBeenCalled();
  });

  it("a navigation cancels a queued return-to-list scroll", () => {
    let callback: FrameRequestCallback = () => {};
    vi.stubGlobal("requestAnimationFrame", (fn: FrameRequestCallback) => { callback = fn; return 1; });
    closeReader();
    navigate("/reading");
    callback(0);
    expect(window.scrollTo).not.toHaveBeenCalled();
  });
});
