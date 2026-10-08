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

vi.mock("./store", () => ({
  accountCount: { value: null },
  accountFilter: { value: "" },
  accountQS: (p: string) => p,
  accounts: { value: [] },
  closeReader: vi.fn(),
  counts: { value: {} },
  list: { value: { kind: "rows", rows: [], origin: null } },
  mailboxes: { value: [] },
  openCompose: vi.fn(),
  reader: { value: { threadId: null, account: null } },
  refreshCounts: vi.fn(),
  rememberListScroll: vi.fn(),
  resetSelection: vi.fn(),
  screeningEnabled: { value: true },
  setAccountFilter: vi.fn(),
  showError: vi.fn(),
  showToast: vi.fn(),
  undoSeconds: { value: 5 },
}));

import { markDone, markRead } from "./actions";
import { api } from "./api";
import { showError, showToast } from "./store";
import type { Row } from "./types";

const row = (id: string, read: boolean): Row => ({
  account: "acct-a",
  thread_id: "t-" + id,
  message_id: id,
  subject: "s",
  from: "f",
  received_at: "",
  read,
  preview: "",
});

/** What the UI asked the server to do, in order: (message, action). Calls
    without a body (count refreshes) are not mutations. */
function requests(): Array<[string, string]> {
  return vi
    .mocked(api)
    .mock.calls.filter(([, opts]) => !!(opts as { body?: unknown } | undefined)?.body)
    .map(([url, opts]) => [
      decodeURIComponent(url!.split("/")[2]!.split("?")[0]!),
      (opts!.body as { action: string }).action,
    ]);
}

function lastToastUndo(): () => void {
  const calls = vi.mocked(showToast).mock.calls;
  const undo = calls[calls.length - 1]?.[1];
  if (!undo) throw new Error("no toast with an undo was shown");
  return undo;
}

describe("bulk action undo and partial-success reconciliation", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api).mockReset();
    vi.mocked(api).mockResolvedValue({undo_token:"receipt"} as never);
  });

  it("restores per-row read state on undo, not a blanket inverse (lullmail-17)", async () => {
    const readRow = row("m1", true);
    const unreadRow = row("m2", false);
    await markRead([readRow, unreadRow], true);
    expect(requests()).toEqual([
      ["m1", "read"],
      ["m2", "read"],
    ]);

    await lastToastUndo()();
    // The already-read row must keep its read state; only the row the
    // action flipped goes back to unread.
    expect(requests()).toEqual([
      ["m1", "read"],
      ["m2", "read"],
      ["m1", "restore"],
      ["m2", "restore"],
    ]);
    expect(showError).not.toHaveBeenCalled();
  });

  it("reconciles partial success and undoes only the changed subset (lullmail-18)", async () => {
    vi.mocked(api).mockImplementation(async (url: string) => {
      if (url.includes("m1")) throw new Error("boom");
      return {undo_token:"receipt"} as never;
    });
    const a = row("m1", false);
    const b = row("m2", false);
    await markDone([a, b]);

    expect(showError).toHaveBeenCalledWith(expect.stringMatching(/1 of 2/));
    expect(showToast).toHaveBeenCalled();

    await lastToastUndo()();
    // The failed row was never marked, so undo must not touch it; the
    // succeeded row flips back exactly once.
    expect(requests()).toEqual([
      ["m1", "read"],
      ["m2", "read"],
      ["m2", "restore"],
    ]);
  });

  it("waits for a held sibling before reporting the settled outcome (lullmail-18)", async () => {
    vi.mocked(api).mockImplementation(async (url: string) => {
      if (url.includes("m1")) {
        await new Promise((r) => setTimeout(r, 25));
        return {undo_token:"receipt"} as never;
      }
      throw new Error("boom");
    });
    const a = row("m1", false);
    const b = row("m2", false);
    await markRead([a, b], true);

    // The late success still landed: it gets the toast and an exact undo,
    // and the error names only the failed subset.
    expect(showError).toHaveBeenCalledWith(expect.stringMatching(/1 of 2/));
    await lastToastUndo()();
    expect(requests()).toEqual([
      ["m1", "read"],
      ["m2", "read"],
      ["m1", "restore"],
    ]);
  });

  it("reports a total failure without offering an undo", async () => {
    vi.mocked(api).mockImplementation(async () => {
      throw new Error("down");
    });
    await markRead([row("m1", false)], true);
    expect(showToast).not.toHaveBeenCalled();
    expect(showError).toHaveBeenCalled();
    expect(requests()).toEqual([["m1", "read"]]);
  });
});

it("uses the server's mixed-message snapshot even when the representative was already read", async () => {
 const undo = [{id: "m1", before: {bucket:"feed",read_at:"2026-01-01T00:00:00Z",until:null},after:{bucket:"feed",read_at:"2026-01-02T00:00:00Z",until:null}}, {id:"m2",before:{bucket:"set_aside",read_at:null,until:"2026-10-20T09:30:00.123456Z"},after:{bucket:"set_aside",read_at:"2026-01-02T00:00:00Z",until:"2026-10-20T09:30:00.123456Z"}}];
 vi.clearAllMocks(); vi.mocked(api).mockReset(); vi.mocked(api).mockResolvedValue({undo,undo_token:"mixed-receipt"} as never);
 await markDone([row("m1", true)]);
 await lastToastUndo()();
 expect(vi.mocked(api).mock.calls.find(([,opts]) => (opts?.body as {action?: string})?.action === "restore")?.[1]?.body).toEqual({action:"restore",undo_token:"mixed-receipt"});
});

it("loads an older envelope page once and discards it after another thread opens", async () => {
 const { reader } = await import("./store");
 const { loadOlderMessages } = await import("./actions");
 let resolve!: (value: unknown) => void;
 vi.mocked(api).mockReset(); vi.mocked(api).mockImplementation(() => new Promise(r => {resolve=r}) as never);
 reader.value = {threadId:"first",account:"acct-a",nextCursor:"older",messages:[],loadingOlder:false} as never;
 const pending = loadOlderMessages();
 await loadOlderMessages();
 expect(vi.mocked(api).mock.calls).toHaveLength(1);
 reader.value = {threadId:"second",account:"acct-a",messages:[]} as never;
 resolve({rows:[{id:"late"}],has_more:false}); await pending;
 expect((reader.value as unknown as {messages: unknown[]}).messages).toEqual([]);
});
