// @vitest-environment jsdom
import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { BoardView } from "./BoardView";
import { KeyboardSnoozePicker } from "../ui/SnoozeMenu";
import { installKeys } from "../lib/keys";
import { checked, cursor, dismissReader, dismissToast, list, resetSelection, snoozePickerRows, toast } from "../lib/store";

const state = vi.hoisted(() => ({ data: null as any, api: vi.fn() }));
vi.mock("../lib/api", () => ({
  api: state.api, clearMemoryCache: vi.fn(),
  ApiError: class extends Error {}, QueuedOffline: class extends Error {}, StaleOwnerError: class extends Error {},
}));
vi.mock("../lib/useLoad", () => ({ useLoad: () => ({
  data: state.data,
  loading: false, error: null, reload: vi.fn(),
}) }));

const pin = {
  account: "account", card_id: "pin", thread_id: "thread", message_id: "message", subject: "Pinned message",
  from: "sender@example.test", received_at: "2026-10-01", preview: "Test", read: true, bucket: "feed",
};
let host: HTMLDivElement;
let off: () => void;
let revision = 0;
const mutations = () => state.api.mock.calls.filter(([path]) => String(path).includes("/action?"));
async function key(value: string) {
  await act(async () => { document.dispatchEvent(new KeyboardEvent("keydown", { key: value, bubbles: true, cancelable: true })); });
}
async function mount(cards: any[]) {
  state.data = { board: { needs_you: cards, waiting_on: [], done: [] }, snoozed: [] };
  await act(async () => { render(<><BoardView {...({ fixtureRevision: ++revision } as any)} /><KeyboardSnoozePicker /></>, host); });
}
async function undo() {
  await vi.waitFor(() => expect(toast.value?.undo).toBeTypeOf("function"));
  await act(async () => { toast.value?.undo?.(); });
}
beforeEach(() => {
  state.api.mockReset().mockResolvedValue({});
  dismissReader(); dismissToast(); resetSelection(); snoozePickerRows.value = [];
  HTMLElement.prototype.scrollIntoView = vi.fn();
  window.scrollTo = vi.fn();
  host = document.createElement("div"); document.body.append(host);
  off = installKeys();
});
afterEach(() => {
  off(); act(() => render(null, host)); host.remove();
  dismissToast(); resetSelection(); snoozePickerRows.value = [];
});

describe("Board keyboard undo state", () => {
  it("does not unread an already-read pin when Done is undone", async () => {
    await mount([pin]); await key("j"); await key("e"); await undo();
    expect(mutations().map(([, options]) => options.body)).toEqual([{ action: "read" }]);
  });

  it("restores a pin's real non-Inbox bucket on move undo", async () => {
    await mount([pin]); await key("j"); await key("i"); await undo();
    expect(mutations().map(([, options]) => options.body)).toEqual([{ action: "imbox" }, { action: "feed" }]);
  });

  it("restores the exact date and bucket of an already-snoozed pin", async () => {
    const until = "2027-01-15T09:45:00.123456Z";
    await mount([{ ...pin, bucket: "set_aside", snooze_until: until }]);
    await key("j"); await key("s");
    const tomorrow = [...host.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find((button) => button.textContent?.startsWith("Tomorrow"))!;
    await act(async () => { tomorrow.click(); });
    await undo();
    expect(mutations().at(-1)?.[1].body).toEqual({ action: "set_aside", until });
  });

  it("never publishes unknown-state or disconnected pins as keyboard mutation targets", async () => {
    const { read, bucket, ...unknown } = pin;
    await mount([unknown, { ...pin, card_id: "disconnected", message_id: undefined }, { ...pin, card_id: "missing-account", account: undefined }]);
    expect(list.value.rows).toEqual([]);
    await key("j"); await key("e"); await key("i");
    expect(mutations()).toEqual([]);
  });

  it.each([
    { bucket: "dropped" }, { bucket: "unknown" }, { read: "false" },
    { bucket: "set_aside" }, { bucket: "set_aside", snooze_until: "not-a-date" },
    { bucket: "set_aside", snooze_until: "2027-01-15" },
  ])("rejects an undo snapshot it cannot faithfully restore: %j", async (state) => {
    await mount([{ ...pin, ...state }]);
    expect(list.value.rows).toEqual([]);
    await key("j"); await key("e"); await key("i");
    expect(mutations()).toEqual([]);
  });

  it("keeps cursor indices aligned when unavailable pins and notes are interleaved", async () => {
    const { read, bucket, ...unknown } = pin;
    await mount([
      { ...unknown, card_id: "unknown", subject: "Unknown" }, pin,
      { card_id: "note", manual: true, subject: "Note" },
      { ...pin, card_id: "second", message_id: "second", thread_id: "second", subject: "Second" },
    ]);
    const cards = [...host.querySelectorAll<HTMLElement>(".board-card")];
    expect(cards.map((card) => card.getAttribute("data-cursor-index"))).toEqual([null, "0", null, "1"]);
    await key("j"); expect(document.activeElement).toBe(cards[1]);
    await key("j"); expect(document.activeElement).toBe(cards[3]);
    await act(async () => { cards[1].click(); });
    await key("x");
    expect(cards[1].classList.contains("picked")).toBe(true);
    expect(cards[3].classList.contains("picked")).toBe(false);
    await key("e");
    expect(mutations()[0]?.[0]).toBe("/messages/message/action?account=account");
  });

  it("prunes selected rows whose state becomes unavailable on refresh", async () => {
    await mount([pin]); await key("j"); await key("x");
    expect(checked.value.size).toBe(1);
    const { read, bucket, ...unknown } = pin;
    await mount([unknown]);
    expect(checked.value.size).toBe(0);
    expect(cursor.value).toBe(-1);
    expect(list.value.rows).toEqual([]);
  });

  it("unknown pins keep Open, card Done and Unpin without inventing a mail mutation", async () => {
    const { read, bucket, ...unknown } = pin;
    await mount([unknown]);
    const buttons = [...host.querySelectorAll<HTMLButtonElement>(".board-card-acts button")];
    expect(buttons.map((button) => button.textContent?.trim())).toEqual(["Open", "Done", "Unpin"]);
    await act(async () => { buttons[1].click(); });
    expect(state.api).toHaveBeenCalledWith("/board/cards/pin/done", { body: { done: true } });
    expect(mutations()).toEqual([]);
  });

  it("retains the known unread Inbox contract of old derived cards", async () => {
    const { card_id, read, bucket, ...derived } = pin;
    await mount([derived]); await key("j"); await key("e"); await undo();
    expect(mutations().map(([, options]) => options.body)).toEqual([{ action: "read" }, { action: "unread" }]);
  });

  it("opens once on a focused card Enter and ignores repetition, composition and handled events", async () => {
    state.api.mockImplementation(async (path) => String(path).startsWith("/threads/") ? [] : {});
    await mount([pin]);
    const card = host.querySelector<HTMLElement>(".board-card")!;
    await act(async () => {
      card.focus();
      for (const flags of [{ repeat: true }, { isComposing: true }, { keyCode: 229 }]) {
        card.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true, ...flags }));
      }
      const handled = new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true });
      handled.preventDefault(); card.dispatchEvent(handled);
    });
    expect(state.api).not.toHaveBeenCalled();
    await act(async () => { card.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true })); });
    expect(state.api.mock.calls.filter(([path]) => String(path).startsWith("/threads/"))).toHaveLength(1);
  });
});
