// @vitest-environment jsdom
import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { installKeys } from "../lib/keys";
import { checked, dismissReader, resetSelection } from "../lib/store";
import { TodayView } from "./TodayView";
import { BoardView } from "./BoardView";

const fixtures = vi.hoisted(() => {
  const thread = { account: "account", message_id: "mail", thread_id: "thread", subject: "Mail to select", from: "sender@example.test", received_at: "2026-10-01", preview: "Test" };
  return {
    today: { brief: { needs_you: [thread], waiting_on: [], feed_unread: 0, paper_unread: 0, screener: 0 }, senders: [] },
    board: { board: { needs_you: [{ ...thread, card_id: "pinned-card", read: true, bucket: "feed" }], waiting_on: [], done: [] }, snoozed: [] },
  };
});
vi.mock("../lib/useLoad", () => ({
  useLoad: (key: string) => ({ data: key.startsWith("today") ? fixtures.today : fixtures.board, loading: false, error: null, reload: vi.fn() }),
}));
vi.mock("../lib/actions", () => ({
  markDone: vi.fn(), markRead: vi.fn(), moveTo: vi.fn(), openThread: vi.fn(), pinThreads: vi.fn(),
  snooze: vi.fn(), decide: vi.fn(), sendMail: vi.fn(), addCard: vi.fn(), removeCard: vi.fn(), setCardDone: vi.fn(),
}));

let host: HTMLDivElement;
let off: () => void;
beforeEach(() => {
  dismissReader(); resetSelection();
  HTMLElement.prototype.scrollIntoView = vi.fn();
  host = document.createElement("div"); document.body.append(host);
  off = installKeys();
});
afterEach(() => {
  off(); act(() => render(null, host)); host.remove(); resetSelection();
});

describe("selection feedback on summary surfaces", () => {
  it.each(["Today", "Board"])("shows and clears existing x selection on %s", async (surface) => {
    await act(async () => { render(surface === "Today" ? <TodayView /> : <BoardView />, host); });
    act(() => {
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true }));
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "x", bubbles: true }));
    });
    expect(checked.value.size).toBe(1);
    expect(host.querySelector(".picked .chip")?.textContent).toBe("Selected");
    const buttons = [...host.querySelectorAll<HTMLButtonElement>(".bulkbar button")];
    if (surface === "Board") expect(buttons).toHaveLength(1);
    const clear = buttons.find((button) => button.textContent?.includes("Clear"))!;
    act(() => { clear.click(); });
    expect(checked.value.size).toBe(0);
    expect(host.querySelector(".picked")).toBeNull();
    expect(host.querySelector(".bulkbar")).toBeNull();
  });
});
