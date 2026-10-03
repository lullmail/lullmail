// @vitest-environment jsdom
import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { installKeys } from "../lib/keys";
import { checked, clearChecked, closeReader, cursor, layout, list, palette, reader, resetSelection, rowIdentity, setAccountFilter, setList, shortcuts, snoozePickerRows, targetRows, toast } from "../lib/store";
import type { Row } from "../lib/types";
import { markDone, markRead, moveTo, openThread, pinThreads } from "../lib/actions";
import { MsgList } from "./MsgRow";
import { BulkBar } from "./BulkBar";

vi.mock("../lib/actions", () => ({
  markDone: vi.fn(), markRead: vi.fn(), moveTo: vi.fn(), openThread: vi.fn(),
  pinThreads: vi.fn(), snooze: vi.fn(), decide: vi.fn(),
}));

const rows: Row[] = Array.from({ length: 6 }, (_, n) => ({
  account: n === 5 ? "other" : "account", message_id: n === 5 ? "0" : String(n),
  thread_id: "thread-" + n, from: "sender@example.test", subject: "Subject " + n,
  received_at: "2026-09-29T12:00:00Z", preview: "Preview", read: n % 2 === 0,
}));
let host: HTMLDivElement;
let uninstall: () => void;
const ids = (...indices: number[]) => indices.map((n) => rowIdentity(rows[n]));
const selected = () => [...checked.value].sort();
const expectSelected = (...indices: number[]) => expect(selected()).toEqual(ids(...indices).sort());
const row = (index: number) => host.querySelector<HTMLElement>('[data-cursor-index="' + index + '"]')!;
function click(el: Element, modifiers: MouseEventInit = {}) {
  const ev = new MouseEvent("click", { bubbles: true, cancelable: true, ...modifiers });
  act(() => { el.dispatchEvent(ev); });
  return ev;
}
function key(value: string, modifiers: KeyboardEventInit = {}, target: EventTarget = document) {
  const ev = new KeyboardEvent("keydown", { key: value, bubbles: true, cancelable: true, ...modifiers });
  act(() => { target.dispatchEvent(ev); });
  return ev;
}
function publish(nextRows = rows, key = "test") {
  act(() => {
    setList({ kind: "rows", key, rows: nextRows, senders: [], origin: "imbox", loading: false, error: null });
    render(<><BulkBar /><MsgList rows={list.value.rows} /></>, host);
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.stubGlobal("requestAnimationFrame", (fn: FrameRequestCallback) => { fn(0); return 0; });
  vi.stubGlobal("matchMedia", () => ({ matches: true }));
  window.scrollTo = vi.fn();
  HTMLElement.prototype.scrollIntoView = vi.fn();
  resetSelection();
  closeReader();
  layout.value = "document";
  toast.value = null;
  palette.value = false;
  shortcuts.value = false;
  snoozePickerRows.value = [];
  host = document.createElement("div");
  document.body.append(host);
  publish();
  uninstall = installKeys();
});
afterEach(() => {
  uninstall();
  act(() => render(null, host));
  host.remove();
  resetSelection();
  closeReader();
  vi.unstubAllGlobals();
});

describe("message pointer selection", () => {
  it.each(["ctrlKey", "metaKey"])("toggles individual rows with %s without opening or marking read", (modifier) => {
    click(row(0), { [modifier]: true });
    click(row(5), { [modifier]: true });
    expectSelected(0, 5);
    expect(cursor.value).toBe(5);
    expect(document.activeElement).toBe(row(5));
    click(row(0), { [modifier]: true });
    expectSelected(5);
    expect(openThread).not.toHaveBeenCalled();
    expect(markRead).not.toHaveBeenCalled();
  });

  it("replaces an anchored range, including backwards and shrinking ranges", () => {
    click(row(2), { ctrlKey: true });
    click(row(5), { shiftKey: true });
    expectSelected(2, 3, 4, 5);
    click(row(3), { shiftKey: true });
    expectSelected(2, 3);
    click(row(0), { shiftKey: true });
    expectSelected(0, 1, 2);
    expect(openThread).not.toHaveBeenCalled();
  });

  it.each(["ctrlKey", "metaKey"])("adds an inclusive range with Shift + %s", (modifier) => {
    click(row(0), { [modifier]: true });
    click(row(3), { [modifier]: true });
    click(row(5), { shiftKey: true, [modifier]: true });
    expectSelected(0, 3, 4, 5);
  });

  it("selects only the clicked row when no anchor or cursor exists", () => {
    click(row(4), { shiftKey: true });
    expectSelected(4);
  });

  it("checkboxes establish focus and anchor and shift-checkbox selects exactly once", () => {
    click(row(1).querySelector(".row-check")!);
    expect(cursor.value).toBe(1);
    key("x");
    expectSelected();
    click(row(4).querySelector(".row-check")!, { shiftKey: true });
    expectSelected(1, 2, 3, 4);
    expect(openThread).not.toHaveBeenCalled();
  });

  it("extends selection with arrows while the checkbox has actual DOM focus", () => {
    const button = row(1).querySelector<HTMLButtonElement>(".row-check")!;
    act(() => { button.focus(); });
    click(button);
    expect(document.activeElement).toBe(button);
    expect(key("ArrowDown", { shiftKey: true }, document.activeElement!).defaultPrevented).toBe(true);
    expectSelected(1, 2);
    expect(document.activeElement).toBe(row(2));
  });

  it("plain clicks clear old selections and open just the clicked thread", () => {
    click(row(0), { ctrlKey: true });
    click(row(1), { ctrlKey: true });
    click(row(4));
    expectSelected();
    expect(cursor.value).toBe(4);
    expect(openThread).toHaveBeenCalledExactlyOnceWith(rows[4].thread_id, rows[4].account, "imbox");
    click(row(5), { shiftKey: true });
    expectSelected(4, 5);
  });

  it("quick actions target only their own row and never change the selection", () => {
    click(row(0), { ctrlKey: true });
    click(row(4).querySelector('[aria-label="Mark done"]')!, { shiftKey: true });
    expect(markDone).toHaveBeenCalledExactlyOnceWith([rows[4]]);
    expectSelected(0);
    expect(openThread).not.toHaveBeenCalled();
  });

  it("leaves nested links and non-primary/Alt clicks alone", () => {
    const a = document.createElement("a");
    a.href = "#attachment";
    row(1).append(a);
    expect(click(a, { ctrlKey: true }).defaultPrevented).toBe(false);
    click(row(1), { button: 1 });
    click(row(1), { altKey: true });
    expectSelected();
    expect(openThread).not.toHaveBeenCalled();
  });
});

describe("message keyboard selection and bulk scope", () => {
  it("keeps j/k focus separate from selected rows, and x toggles the focused row", () => {
    click(row(1), { metaKey: true });
    key("j");
    expect(cursor.value).toBe(2);
    expectSelected(1);
    key("x");
    expectSelected(1, 2);
    key("k");
    expect(cursor.value).toBe(1);
    expectSelected(1, 2);
  });

  it("extends, shrinks, and adds keyboard ranges", () => {
    key("j");
    key("ArrowDown", { shiftKey: true });
    key("ArrowDown", { shiftKey: true });
    expectSelected(0, 1, 2);
    key("ArrowUp", { shiftKey: true });
    expectSelected(0, 1);
    key("End", { ctrlKey: true });
    expect(cursor.value).toBe(5);
    expectSelected(0, 1);
    key("ArrowUp", { ctrlKey: true, shiftKey: true });
    expectSelected(0, 1, 2, 3, 4);
    key("Home");
    expect(cursor.value).toBe(0);
  });

  it.each(["ctrlKey", "metaKey"])("selects only loaded messages with %s+A", (modifier) => {
    publish(rows.slice(0, 3));
    expect(key("a", { [modifier]: true }).defaultPrevented).toBe(true);
    expectSelected(0, 1, 2);
    expect(cursor.value).toBe(0);
  });

  it("Escape and Clear remove selection while retaining keyboard focus", () => {
    click(row(2), { ctrlKey: true });
    key("Escape");
    expectSelected();
    expect(cursor.value).toBe(2);
    key("x");
    click(Array.from(host.querySelectorAll(".bulkbar button")).find((button) => button.textContent?.includes("Clear"))!);
    expectSelected();
    expect(cursor.value).toBe(2);
  });

  it("Space toggles only the focused row and never opens it", () => {
    key("j");
    expect(key(" ").defaultPrevented).toBe(true);
    expectSelected(0);
    key(" ", { repeat: true });
    expectSelected(0);
    key(" ", { ctrlKey: true });
    expectSelected();
    expect(openThread).not.toHaveBeenCalled();
  });

  it("local snooze-menu Escape closes just the menu and preserves bulk selection", () => {
    click(row(1), { ctrlKey: true });
    click(row(3).querySelector('[aria-label="Set aside"]')!);
    const item = host.querySelector('[role="menuitem"]')!;
    expect(item).not.toBeNull();
    key("e", {}, item);
    expect(markDone).not.toHaveBeenCalled();
    key("Escape", {}, item);
    expect(host.querySelector('[role="menu"]')).toBeNull();
    expectSelected(1);
  });

  it("Ctrl+A on a sidebar link retains native page selection", () => {
    const link = document.createElement("a"); link.href = "/reading"; host.append(link);
    expect(key("a", { ctrlKey: true }, link).defaultPrevented).toBe(false);
    expectSelected();
  });

  it("uses exactly checked rows for both the bulk bar and keyboard verbs", () => {
    click(row(0), { metaKey: true });
    click(row(5), { metaKey: true });
    key("k");
    key("e");
    key("i");
    key("p");
    key("s");
    expect(markDone).toHaveBeenCalledWith([rows[0], rows[5]]);
    expect(moveTo).toHaveBeenCalledWith([rows[0], rows[5]], "imbox");
    expect(pinThreads).toHaveBeenCalledWith([rows[0], rows[5]]);
    expect(snoozePickerRows.value).toEqual([rows[0], rows[5]]);
    snoozePickerRows.value = [];
    click(host.querySelector(".bulkbar button")!);
    expect(markDone).toHaveBeenLastCalledWith([rows[0], rows[5]]);
  });

  it("Enter opens focus and clears bulk selection", () => {
    click(row(0), { ctrlKey: true });
    key("j");
    key("Enter");
    expectSelected();
    expect(openThread).toHaveBeenCalledExactlyOnceWith(rows[1].thread_id, rows[1].account, "imbox");
  });

  it.each(["input", "textarea", "select", "div"])("preserves editing and select-all in %s", (tag) => {
    const editor = document.createElement(tag);
    if (tag === "div") editor.setAttribute("contenteditable", "");
    host.append(editor);
    expect(key("a", { ctrlKey: true }, editor).defaultPrevented).toBe(false);
    key("j", {}, editor);
    key("x", {}, editor);
    expectSelected();
    expect(cursor.value).toBe(-1);
  });

  it("preserves button/link activation and locally handled events", () => {
    click(row(0), { ctrlKey: true });
    const button = row(3).querySelector(".row-check")!;
    expect(key("Enter", {}, button).defaultPrevented).toBe(false);
    expect(key(" ", {}, button).defaultPrevented).toBe(false);
    const link = document.createElement("a"); link.href = "#"; host.append(link);
    expect(key("Enter", {}, link).defaultPrevented).toBe(false);
    const handled = new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true });
    handled.preventDefault();
    act(() => { document.dispatchEvent(handled); });
    expect(openThread).not.toHaveBeenCalled();
  });

  it("does not act during IME composition or repeat destructive shortcuts", () => {
    click(row(0), { ctrlKey: true });
    key("e", { isComposing: true });
    key("e", { repeat: true });
    key("x", { repeat: true });
    expect(markDone).not.toHaveBeenCalled();
    expectSelected(0);
    key("j", { repeat: true });
    expect(cursor.value).toBe(1);
  });

  it.each(["document", "classic"] as const)("cannot operate a hidden list in %s reader mode", (mode) => {
    layout.value = mode;
    vi.stubGlobal("matchMedia", () => ({ matches: false }));
    click(row(0), { ctrlKey: true });
    reader.value = { ...reader.value, threadId: "visible", account: "account", messages: [{
      id: "visible-message", account: "account", subject: "Visible", from: "s@example.test", received_at: "2026-09-29",
    } as any] };
    key("j"); key("x"); key("ArrowDown", { shiftKey: true }); key("Enter"); key("a", { ctrlKey: true });
    expect(cursor.value).toBe(0);
    expectSelected(0);
    expect(openThread).not.toHaveBeenCalled();
    expect(key("ArrowDown").defaultPrevented).toBe(false);
    key("e");
    expect(markDone).toHaveBeenCalledWith([expect.objectContaining({ thread_id: "visible", message_id: "visible-message" })]);
    key("Escape");
    expect(reader.value.threadId).toBeNull();
  });

  it("keeps the visible classic list actionable with the reader beside it", () => {
    layout.value = "classic";
    reader.value = { ...reader.value, threadId: "visible", account: "account" };
    click(row(1), { ctrlKey: true });
    key("ArrowDown", { shiftKey: true });
    expectSelected(1, 2);
    key("e");
    expect(markDone).toHaveBeenCalledExactlyOnceWith([rows[1], rows[2]]);
  });

  it("reader verbs work when opened from the Screener's palette", () => {
    setList({ kind: "senders", key: "screener", rows: [] });
    reader.value = { ...reader.value, threadId: "visible", account: "account", messages: [{
      id: "visible-message", account: "account", subject: "Visible", from: "s@example.test", received_at: "2026-09-29",
    } as any] };
    key("e");
    expect(markDone).toHaveBeenCalledWith([expect.objectContaining({ thread_id: "visible" })]);
  });
});

describe("selection reconciliation", () => {
  it("keeps focus and selection attached to identities on reorder and uses current range order", () => {
    click(row(1), { ctrlKey: true });
    publish([rows[5], rows[2], rows[1], rows[0], rows[3], rows[4]]);
    expect(cursor.value).toBe(2);
    expectSelected(1);
    click(row(0), { shiftKey: true });
    expectSelected(5, 2, 1);
  });

  it("preserves surviving selections and anchor on append, prunes removed rows", () => {
    publish(rows.slice(0, 3));
    click(row(1), { ctrlKey: true });
    publish(rows);
    click(row(5), { shiftKey: true });
    expectSelected(1, 2, 3, 4, 5);
    publish([rows[0], rows[2], rows[4]]);
    expectSelected(2, 4);
    expect(cursor.value).toBe(-1);
    click(row(0), { shiftKey: true });
    expectSelected(0);
  });

  it("resets selection, cursor and anchor on query/folder key or kind change", () => {
    click(row(1), { ctrlKey: true });
    publish(rows, "search:new");
    expectSelected(); expect(cursor.value).toBe(-1);
    click(row(4), { shiftKey: true });
    expectSelected(4);
    act(() => setList({ kind: "none" }));
    expectSelected(); expect(cursor.value).toBe(-1);
  });

  it("fences old actions immediately on account filter change", () => {
    click(row(1), { ctrlKey: true });
    act(() => setAccountFilter("another-account"));
    expectSelected(); expect(cursor.value).toBe(-1);
    expect(targetRows()).toEqual([]);
    expect(list.value.kind).toBe("none");
    act(() => setAccountFilter(""));
  });
});
