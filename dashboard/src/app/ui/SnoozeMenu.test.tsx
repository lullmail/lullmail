// @vitest-environment jsdom
import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, describe, expect, it } from "vitest";
import { installKeys } from "../lib/keys";
import { cursor, setList, showError, showToast, snoozePickerRows, toast } from "../lib/store";
import type { Row } from "../lib/types";
import { Overlays } from "../App";
import { daysUntilWeekend } from "./SnoozeMenu";
import { Toast } from "./Toast";

const host = document.createElement("div");
const row: Row = {
  account: "account", thread_id: "thread", message_id: "message", subject: "Subject",
  from: "sender@example.test", received_at: "2026-08-31T12:00:00Z", read: false, preview: "",
};

afterEach(() => {
  render(null, host);
  toast.value = null;
  snoozePickerRows.value = [];
  cursor.value = -1;
});

describe("snooze choices", () => {
  it("targets the next Saturday instead of a fixed delay", () => {
    expect(daysUntilWeekend(new Date(2026, 7, 31))).toBe(5); // Monday
    expect(daysUntilWeekend(new Date(2026, 8, 4))).toBe(1); // Friday
    expect(daysUntilWeekend(new Date(2026, 8, 5))).toBe(7); // Saturday
  });

  it("opens the date picker for the keyboard s command", () => {
    setList({ kind: "rows", key: "test", loading: false, error: null, rows: [row], senders: [], origin: "imbox" });
    cursor.value = 0;
    const uninstall = installKeys();
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "s", bubbles: true }));
    uninstall();

    expect(snoozePickerRows.value).toEqual([row]);
    render(<Overlays />, host);
    const dialog = host.querySelector('[role="dialog"]');
    const noticeStack = host.querySelector(".notice-stack");
    expect(dialog?.textContent).toContain("This weekend");
    expect(noticeStack).not.toBeNull();
    expect(noticeStack?.contains(dialog || null)).toBe(false);
  });
});

describe("toast announcements", () => {
  it("announces errors assertively and ordinary messages politely", () => {
    act(() => showError("Failed"));
    render(<Toast />, host);
    expect(host.querySelector('[role="alert"]')?.getAttribute("aria-live")).toBe("assertive");

    act(() => showToast("Saved"));
    render(<Toast />, host);
    expect(host.querySelector('[role="status"]')?.getAttribute("aria-live")).toBe("polite");
  });
});
