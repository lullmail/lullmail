// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("./api", () => ({
  ApiError: class ApiError extends Error {
    status: number;
    constructor(message: string, status: number) {
      super(message);
      this.status = status;
    }
  },
  QueuedOffline: class QueuedOffline extends Error {},
  StaleOwnerError: class StaleOwnerError extends Error {},
  clearMemoryCache: vi.fn(),
  api: vi.fn(),
}));

// LUL-F04: both reader row constructors dropped snooze_until (and the
// thread API did not return it), so an Undo of a single-message dated
// snooze sent {action:"set_aside"} with no deadline — the server answered
// with its three-day default and silently changed a known deadline.

import { moveTo, snooze } from "./actions";
import { api } from "./api";
import { dismissToast, layout, reader, targetRows, toast } from "./store";
import type { Message } from "./types";

const until = "2026-10-20T09:30:00.123456789Z";

function openSnoozedThread(snoozeUntil?: string): void {
  layout.value = "document";
  const message: Message = {
    id: "m1", account: "acct-a", subject: "s", from: "Sender <s@example.test>", to: "",
    received_at: "2026-10-01T10:00:00Z", bucket: "set_aside", body: "",
  };
  if (snoozeUntil !== undefined) message.snooze_until = snoozeUntil;
  reader.value = {
    threadId: "t1", account: "acct-a", bucket: "snoozed", loading: false, error: null,
    messages: [message], imagesOk: new Set(),
  };
}

/** The mutation bodies the UI asked the server to apply, in order. */
function sentBodies(): Array<Record<string, unknown>> {
  return vi
    .mocked(api)
    .mock.calls.filter(([, opts]) => !!(opts as { body?: unknown } | undefined)?.body)
    .map(([, opts]) => (opts as { body: Record<string, unknown> }).body);
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api).mockReset();
  vi.mocked(api).mockResolvedValue({} as never);
  dismissToast();
});

describe("reader snooze undo keeps the exact deadline (LUL-F04)", () => {
  it("restores a dated snooze verbatim after a reader move", async () => {
    openSnoozedThread(until);
    const rows = targetRows();
    // The reader row carries the deadline through (baseline dropped it).
    expect(rows[0].snooze_until).toBe(until);

    await moveTo(rows, "imbox");
    expect(sentBodies()).toEqual([{ action: "imbox" }]);
    await toast.value!.undo!();
    // The undo restores the EXACT microsecond instant — never the
    // server's three-day default.
    expect(sentBodies()[1]).toEqual({ action: "set_aside", until });
  });

  it("restores the original deadline after a reader snooze change", async () => {
    openSnoozedThread(until);
    const rows = targetRows();
    await snooze(rows, 1);
    const applied = sentBodies()[0] as { action: string; until: string };
    expect(applied.action).toBe("set_aside");
    // The new deadline is about one day out — and not the original.
    expect(Date.parse(applied.until)).toBeGreaterThan(Date.now());
    expect(applied.until).not.toBe(until);
    await toast.value!.undo!();
    expect(sentBodies()[1]).toEqual({ action: "set_aside", until });
  });

  it("declines the undo instead of applying the default when the deadline is unknown (mixed version)", async () => {
    // An older server's thread endpoint returns no snooze_until.
    openSnoozedThread(undefined);
    const rows = targetRows();
    expect(rows[0].snooze_until).toBeUndefined();
    await moveTo(rows, "imbox");
    expect(sentBodies()).toEqual([{ action: "imbox" }]);
    await toast.value!.undo!();
    // No set_aside was issued: an exact restore is impossible, so the
    // undo declines visibly rather than silently applying the default
    // three days.
    expect(sentBodies()).toEqual([{ action: "imbox" }]);
    expect(toast.value?.tone).toBe("error");
    expect(toast.value?.message).toMatch(/return date/i);
  });
});
