// @vitest-environment jsdom
import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { counts, moreSheet, overlayOpen, screeningEnabled } from "../lib/store";
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
  moreSheet.value = false;
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

  it("counts as an overlay while open, keeps Tab inside, and closes when the current tab is tapped", () => {
    mount(<TabBar />);
    const more = tabs().at(-1) as HTMLButtonElement;
    expect(overlayOpen.value).toBe(false);
    act(() => more.click());
    expect(overlayOpen.value).toBe(true);

    const links = [...host.querySelectorAll<HTMLAnchorElement>("[role='dialog'] a")];
    const tab = (shiftKey = false) => act(() => { document.dispatchEvent(new KeyboardEvent("keydown", { key: "Tab", shiftKey, bubbles: true, cancelable: true })); });
    links.at(-1)!.focus();
    tab();
    expect(document.activeElement).toBe(more);
    tab();
    expect(document.activeElement).toBe(links[0]);
    tab(true);
    expect(document.activeElement).toBe(more);

    // Inbox is the current place: no route change, so the tap itself closes.
    act(() => { tabs()[1].dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true })); });
    expect(host.querySelector("[role='dialog']")).toBeNull();
    expect(overlayOpen.value).toBe(false);
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
    expect(markDone).toHaveBeenCalledWith([row]);
    expect(openThread).not.toHaveBeenCalled();
    // Done does not remove the row, so it settles back rather than sliding off.
    expect(el.style.getPropertyValue("--swipe")).toBe("");
    vi.useRealTimers();
  });

  it("names a Sent row by who it was written to", () => {
    const sent: Row = { ...row, bucket: "sent", from: "Tyler <me@example.test>", to: "Ari Patel <ari@example.test>" };
    setList({ kind: "rows", key: "s", rows: [sent], senders: [], origin: "sent", loading: false, error: null });
    mount(<MsgList rows={[sent]} />);
    expect(host.querySelector(".row-sender")!.textContent).toBe("To Ari Patel");
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

    // A second finger landing mid-swipe neither takes over nor strands the row.
    pointer(el, "pointerdown", 300, 100);
    pointer(el, "pointermove", 250, 100);
    const second = new MouseEvent("pointerdown", { bubbles: true, cancelable: true, clientX: 200, clientY: 100 });
    Object.defineProperties(second, { pointerId: { value: 2 }, pointerType: { value: "touch" } });
    act(() => { el.dispatchEvent(second); });
    pointer(el, "pointerup", 250, 100);
    expect(el.style.getPropertyValue("--swipe")).toBe("");
    expect(el.classList.contains("swiping")).toBe(false);

    pointer(el, "pointerdown", 300, 100, "mouse");
    pointer(el, "pointermove", 100, 100, "mouse");
    pointer(el, "pointerup", 100, 100, "mouse");
    expect(markDone).not.toHaveBeenCalled();
  });
});
