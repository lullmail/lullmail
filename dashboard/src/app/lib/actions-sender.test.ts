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
  rowIdentity: (r: { account: string; message_id: string }) => r.account + "\u0000" + r.message_id,
  screeningEnabled: { value: true },
  setAccountFilter: vi.fn(),
  setList: vi.fn(),
  showError: vi.fn(),
  showToast: vi.fn(),
  trashing: { value: new Set<string>() },
  undoSeconds: { value: 5 },
}));

import { routeSender, senderRoute } from "./actions";
import { api } from "./api";
import { closeReader, reader, showError, showToast } from "./store";
import type { Row } from "./types";

const row = (bucket: string): Row => ({
  account: "acct-a", thread_id: "t-1", message_id: "m-1", subject: "s", from: "The Letter <news@letters.test>",
  received_at: "", read: true, preview: "", bucket,
});

/** Every request that carried a body, as (path, body). */
function writes(): Array<[string, unknown]> {
  return vi.mocked(api).mock.calls
    .filter(([, opts]) => !!(opts as { body?: unknown } | undefined)?.body)
    .map(([url, opts]) => [url!.split("?")[0]!, opts!.body]);
}

function lastUndo(): () => Promise<void> {
  const call = vi.mocked(showToast).mock.calls.at(-1)!;
  return call[1] as () => Promise<void>;
}

beforeEach(() => {
  vi.clearAllMocks();
  reader.value = { threadId: null, account: null } as typeof reader.value;
  vi.mocked(api).mockImplementation(async (url: string) => {
    if (url.includes("/action")) return { undo_token: "tok" } as never;
    return {} as never;
  });
});

describe("where a sender's mail goes today", () => {
  it("answers the allowed route, matching the address whatever its case", async () => {
    vi.mocked(api).mockResolvedValueOnce([
      { sender: "other@x.test", route: "imbox", allowed: true },
      { sender: "news@letters.test", route: "feed", allowed: true },
    ] as never);
    expect(await senderRoute(" News@Letters.test ")).toBe("feed");
    expect(vi.mocked(api).mock.calls[0]).toEqual(["/people", { fresh: true }]);
  });

  it("answers null for a stranger and for a blocked sender: no rule lets them in", async () => {
    vi.mocked(api).mockResolvedValue([{ sender: "news@letters.test", route: "blocked", allowed: false }] as never);
    expect(await senderRoute("news@letters.test")).toBeNull();
    expect(await senderRoute("nobody@x.test")).toBeNull();
  });
});

describe("always send this sender to…", () => {
  it("sets the rule, moves the thread in hand, closes the reader on it, and says so once", async () => {
    reader.value = { threadId: "t-1", account: "acct-a" } as typeof reader.value;
    await routeSender("News@Letters.test", "The Letter", "feed", [row("imbox")], "imbox");
    expect(writes()).toEqual([
      ["/screener/decide", { sender: "news@letters.test", allow: true, route: "feed" }],
      ["/messages/m-1/action", { action: "feed" }],
    ]);
    expect(closeReader).toHaveBeenCalledTimes(1);
    expect(vi.mocked(showToast).mock.calls.map((c) => c[0])).toEqual(["Mail from The Letter now goes to Reading"]);
  });

  it("undo puts the rule back to what it was and the thread back where it came from", async () => {
    await routeSender("news@letters.test", "The Letter", "paper_trail", [row("imbox")], "imbox");
    vi.mocked(api).mockClear();
    await lastUndo()();
    expect(writes()).toEqual([
      ["/screener/decide", { sender: "news@letters.test", allow: true, route: "imbox" }],
      ["/messages/m-1/action", { action: "restore", undo_token: "tok" }],
    ]);
  });

  it("undo for a sender who had no rule takes the new one away again", async () => {
    await routeSender("news@letters.test", "", "feed", [row("screener")], null);
    expect(vi.mocked(showToast).mock.calls.at(-1)![0]).toBe("Mail from news@letters.test now goes to Reading");
    vi.mocked(api).mockClear();
    await lastUndo()();
    expect(writes()[0]).toEqual(["/screener/undecide", { sender: "news@letters.test" }]);
  });

  it("undo leaves the rule alone when the old one was never learned, rather than guess", async () => {
    await routeSender("news@letters.test", "The Letter", "feed", [row("imbox")], undefined);
    vi.mocked(api).mockClear();
    await lastUndo()();
    expect(writes()).toEqual([["/messages/m-1/action", { action: "restore", undo_token: "tok" }]]);
  });

  it("a thread already in that place stays open: only the rule changes", async () => {
    reader.value = { threadId: "t-1", account: "acct-a" } as typeof reader.value;
    await routeSender("news@letters.test", "The Letter", "feed", [row("feed")], "imbox");
    expect(writes()).toEqual([["/screener/decide", { sender: "news@letters.test", allow: true, route: "feed" }]]);
    expect(closeReader).not.toHaveBeenCalled();
  });

  it("never tries to file sent mail", async () => {
    await routeSender("news@letters.test", "The Letter", "feed", [row("sent"), row("outreach")], "imbox");
    expect(writes().map(([path]) => path)).toEqual(["/screener/decide"]);
  });

  it("moves nothing and promises nothing when the rule could not be saved", async () => {
    vi.mocked(api).mockRejectedValueOnce(new Error("server said no"));
    await routeSender("news@letters.test", "The Letter", "feed", [row("imbox")], "imbox");
    expect(writes()).toHaveLength(1);
    expect(showToast).not.toHaveBeenCalled();
    expect(vi.mocked(showError).mock.calls[0][0]).toContain("Could not save that rule");
  });
});
