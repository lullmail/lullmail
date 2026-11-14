// @vitest-environment jsdom
import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { counts, screeningEnabled } from "../lib/store";
import { path } from "../lib/router";
import { markDone, openThread } from "../lib/actions";
import { setList } from "../lib/store";
import type { Row } from "../lib/types";
import { MsgList } from "./MsgRow";
import { TabBar } from "./TabBar";

vi.mock("../lib/actions", () => ({
  markDone: vi.fn(), markRead: vi.fn(), moveTo: vi.fn(), openThread: vi.fn(),
  pinThreads: vi.fn(), snooze: vi.fn(), trashThreads: vi.fn(),
}));

let host: HTMLDivElement;
const mount = (node: preact.ComponentChild) => act(() => render(node, host));
const tabs = () => [...host.querySelectorAll<HTMLElement>(".tabbar .tab")];

beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  path.value = "/";
  counts.value = { imbox: 3, screener: 2 };
  screeningEnabled.value = true;
});
afterEach(() => { act(() => render(null, host)); host.remove(); vi.clearAllMocks(); });

describe("phone tab bar", () => {
  it("marks the current place and carries the counts", () => {
    mount(<TabBar />);
    expect(tabs().map((t) => t.querySelector(".tab-label")!.textContent)).toEqual(["Today", "Inbox", "Screener", "Reading", "More"]);
    const inbox = tabs()[1];
    expect(inbox.getAttribute("aria-current")).toBe("page");
    expect(inbox.querySelector(".tab-n")!.textContent).toBe("3");
    expect(tabs()[2].querySelector(".tab-n")!.classList.contains("loud")).toBe(true);
  });

  it("drops the Screener tab when screening is off", () => {
    screeningEnabled.value = false;
    mount(<TabBar />);
    expect(tabs().map((t) => t.querySelector(".tab-label")!.textContent)).toEqual(["Today", "Inbox", "Reading", "More"]);
  });

  it("lights More for a place that lives in the sheet, and the sheet closes on navigation and on Escape", () => {
    path.value = "/board";
    mount(<TabBar />);
    const more = tabs().at(-1) as HTMLButtonElement;
    expect(more.classList.contains("active")).toBe(true);

    act(() => more.click());
    const sheet = host.querySelector<HTMLElement>("[role='dialog']")!;
    expect(more.getAttribute("aria-expanded")).toBe("true");
    expect(sheet.querySelector("a[aria-current='page']")!.textContent).toContain("Board");
    expect(document.activeElement).toBe(sheet.querySelector("a"));

    act(() => { document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true })); });
    expect(host.querySelector("[role='dialog']")).toBeNull();
    expect(document.activeElement).toBe(more);

    act(() => more.click());
    act(() => { path.value = "/notes"; });
    expect(host.querySelector("[role='dialog']")).toBeNull();
  });
});

describe("swipe a row to mark it done", () => {
  const row: Row = {
    account: "a", message_id: "1", thread_id: "t1", from: "s@example.test", subject: "S",
    received_at: "2026-10-01T12:00:00Z", preview: "", read: false,
  };
  const pointer = (el: Element, type: string, x: number, y: number, pointerType = "touch") => {
    const ev = new MouseEvent(type, { bubbles: true, cancelable: true, clientX: x, clientY: y });
    Object.defineProperties(ev, { pointerId: { value: 1 }, pointerType: { value: pointerType } });
    act(() => { el.dispatchEvent(ev); });
  };
  const mountRow = () => {
    setList({ kind: "rows", key: "t", rows: [row], senders: [], origin: "imbox", loading: false, error: null });
    mount(<MsgList rows={[row]} />);
    return host.querySelector<HTMLElement>(".msg-row")!;
  };

  it("commits past the threshold, and the tail of the gesture does not open the thread", () => {
    vi.useFakeTimers();
    const el = mountRow();
    pointer(el, "pointerdown", 300, 100);
    pointer(el, "pointermove", 260, 102);
    expect(el.classList.contains("swiping")).toBe(true);
    pointer(el, "pointermove", 180, 103);
    expect(el.classList.contains("swipe-armed")).toBe(true);
    pointer(el, "pointerup", 180, 103);
    act(() => el.click());
    act(() => { vi.advanceTimersByTime(200); });
    expect(markDone).toHaveBeenCalledWith([row]);
    expect(openThread).not.toHaveBeenCalled();
    expect(el.style.getPropertyValue("--swipe")).toBe("");
    vi.useRealTimers();
  });

  it("snaps back from a short drag, ignores a vertical one, and ignores a mouse", () => {
    const el = mountRow();
    pointer(el, "pointerdown", 300, 100);
    pointer(el, "pointermove", 250, 100);
    pointer(el, "pointerup", 250, 100);
    expect(el.classList.contains("swiping")).toBe(false);
    expect(el.style.getPropertyValue("--swipe")).toBe("");

    pointer(el, "pointerdown", 300, 100);
    pointer(el, "pointermove", 290, 160);
    pointer(el, "pointermove", 150, 160);
    expect(el.classList.contains("swiping")).toBe(false);
    pointer(el, "pointerup", 150, 160);

    pointer(el, "pointerdown", 300, 100, "mouse");
    pointer(el, "pointermove", 100, 100, "mouse");
    pointer(el, "pointerup", 100, 100, "mouse");
    expect(markDone).not.toHaveBeenCalled();
  });
});
