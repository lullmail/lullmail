// @vitest-environment jsdom
import "fake-indexeddb/auto";
import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Compose } from "./Compose";
import { InlineReply } from "../views/TodayView";
import { submissionLookup } from "../lib/actions";
import { clearMemoryCache } from "../lib/api";
import { clearOfflineData, loadDrafts, prepareOfflineOwner } from "../lib/offline";
import { accounts, draftStack, hydrateDrafts, openCompose, pendingDraftReads, resetPrivateState, sendingDrafts, toast, updateDraftById } from "../lib/store";
import type { BriefThread } from "../lib/types";

const host = document.createElement("div");
const wait = (ms = 20) => new Promise((resolve) => setTimeout(resolve, ms));
const settle = async () => { await act(async () => { await wait(); }); };
const button = (label: string) => [...host.querySelectorAll("button")].find((b) => b.textContent === label)!;
const accepted = () => new Response(JSON.stringify({ queued: "q1", undo_seconds: 5 }));
const seed = { to: "person@example.test", subject: "A private subject", body: "A private body" };
const notFound = () => new Response(JSON.stringify({ title: "Not Found" }), { status: 404 });
const entry = (status: string) => new Response(JSON.stringify({ id: "row-1", account_id: "product-a", status, filing_status: "not_started", created_at: new Date().toISOString(), undo_until: new Date().toISOString(), recoverable: true, saved_sent_copy: false }));
// fetcher sees sends; lookups sees GET /api/outbox?key= submission lookups.
let fetcher: ReturnType<typeof vi.fn>;
let lookups: ReturnType<typeof vi.fn>;

async function edit(selector: string, value: string) {
  await act(async () => {
    const el = host.querySelector<HTMLInputElement | HTMLTextAreaElement>(selector)!;
    el.value = value;
    el.dispatchEvent(new Event("input", { bubbles: true }));
    await wait();
  });
}

async function clickSend() {
  await act(async () => {
    button("Send").click();
    await wait(35);
    // A send that ends without an acknowledgment also runs its lookup.
    for (let i = 0; i < 100 && sendingDrafts.value.size > 0; i++) await wait(20);
  });
}

beforeEach(async () => {
  await clearOfflineData();
  await prepareOfflineOwner({ installation_id: "inst", user_id: "a", email: "a@example.test" });
  clearMemoryCache();
  accounts.value = [{ id: "product-a", address: "a@example.test" }];
  fetcher = vi.fn().mockRejectedValue(new TypeError("acknowledgment lost"));
  lookups = vi.fn().mockImplementation(async () => notFound());
  vi.stubGlobal("fetch", (url: string, init: RequestInit) => (String(url).startsWith("/api/outbox?key=") ? (lookups as unknown as typeof fetch)(url, init) : (fetcher as unknown as typeof fetch)(url, init)));
  submissionLookup.backoffMs = [];
});
afterEach(async () => {
  await act(async () => { render(null, host); });
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  await clearOfflineData();
});

describe("composer submission retries", () => {
  it("retains key and exact sender across park/remount and default-account reorder", async () => {
    openCompose(seed);
    render(<Compose />, host);
    await settle();
    await clickSend();
    expect(fetcher).toHaveBeenCalledTimes(1);
    const original = fetcher.mock.calls[0][1];
    expect(JSON.parse(original.body).account_id).toBe("product-a");
    expect((await loadDrafts())[0].sendKey).toBe(original.headers["Idempotency-Key"]);
    await act(async () => { render(null, host); });
    accounts.value = [{ id: "product-b", address: "b@example.test" }, { id: "product-a", address: "a@example.test" }];
    render(<Compose />, host);
    await settle();
    await clickSend();
    expect(fetcher).toHaveBeenCalledTimes(2);
    expect(fetcher.mock.calls[1][1].headers["Idempotency-Key"]).toBe(original.headers["Idempotency-Key"]);
    expect(fetcher.mock.calls[1][1].body).toBe(original.body);
  });

  it("sends the authoritative draft when an edit and keyboard send share a render tick", async () => {
    openCompose(seed);
    render(<Compose />, host);
    await settle();
    await act(async () => {
      const body = host.querySelector<HTMLTextAreaElement>("textarea")!;
      body.value = "Latest same-tick edit";
      body.dispatchEvent(new Event("input", { bubbles: true }));
      body.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true, bubbles: true }));
      await wait(35);
    });
    expect(JSON.parse(fetcher.mock.calls[0][1].body).text).toBe("Latest same-tick edit");
    expect((await loadDrafts())[0].body).toBe("Latest same-tick edit");
  });

  it("keeps an ambiguous 409 retry on the same key and leaves the draft available", async () => {
    fetcher.mockImplementation(async () => new Response(JSON.stringify({ detail: "delivery outcome uncertain; check Sent before a new submission" }), { status: 409 }));
    openCompose(seed);
    render(<Compose />, host);
    await settle();
    await clickSend();
    const key = fetcher.mock.calls[0][1].headers["Idempotency-Key"];
    expect(draftStack.value).toHaveLength(1);
    await clickSend();
    expect(fetcher.mock.calls[1][1].headers["Idempotency-Key"]).toBe(key);
    expect(draftStack.value).toHaveLength(1);
  });

  it("mints a new key for edited content without losing the first retry key", async () => {
    openCompose(seed);
    render(<Compose />, host);
    await settle();
    await clickSend();
    const originalKey = fetcher.mock.calls[0][1].headers["Idempotency-Key"];
    await edit("textarea.compose-body", "A deliberate new version");
    await clickSend();
    expect(fetcher.mock.calls[1][1].headers["Idempotency-Key"]).not.toBe(originalKey);
    expect(JSON.parse(fetcher.mock.calls[1][1].body).text).toBe("A deliberate new version");
  });

  it("treats a lost acknowledgment whose row exists as sent", async () => {
    lookups.mockImplementation(async () => entry("submitted"));
    openCompose(seed);
    render(<Compose />, host);
    await settle();
    await clickSend();
    expect(lookups).toHaveBeenCalledTimes(1);
    expect(lookups.mock.calls[0][0]).toBe("/api/outbox?key=" + encodeURIComponent(fetcher.mock.calls[0][1].headers["Idempotency-Key"]));
    expect(draftStack.value).toEqual([]);
    expect(toast.value?.message).toBe("Message submitted");
  });

  it("keeps an unconfirmed send's key and re-sends it unchanged without asking", async () => {
    const confirm = vi.spyOn(window, "confirm");
    openCompose(seed);
    render(<Compose />, host);
    await settle();
    await clickSend();
    const key = fetcher.mock.calls[0][1].headers["Idempotency-Key"];
    expect(toast.value?.message).toContain("could not be confirmed");
    expect(draftStack.value[0].unconfirmedKey).toBe(key);
    await act(async () => { await wait(300); }); // autosave debounce
    expect((await loadDrafts())[0].unconfirmedKey).toBe(key);
    fetcher.mockResolvedValue(new Response(JSON.stringify({ queued: "q1", undo_seconds: 0, status: "submitted" })));
    await clickSend();
    expect(fetcher.mock.calls[1][1].headers["Idempotency-Key"]).toBe(key);
    expect(confirm).not.toHaveBeenCalled();
    expect(draftStack.value).toEqual([]);
  });

  it("asks before an edited draft could send a second copy of an unconfirmed send", async () => {
    openCompose(seed);
    render(<Compose />, host);
    await settle();
    await clickSend();
    const key = fetcher.mock.calls[0][1].headers["Idempotency-Key"];
    await edit("textarea.compose-body", "Edited after the lost acknowledgment");
    // The earlier send did reach the server after all.
    lookups.mockImplementation(async () => entry("pending"));
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    await clickSend();
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(confirm.mock.calls[0][0]).toContain("This message may already have been sent");
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(draftStack.value).toHaveLength(1);
    expect(draftStack.value[0].unconfirmedKey).toBe(key);
    // Sending anyway is a deliberate new submission under a new key.
    confirm.mockReturnValue(true);
    fetcher.mockResolvedValue(accepted());
    await clickSend();
    expect(fetcher).toHaveBeenCalledTimes(2);
    expect(fetcher.mock.calls[1][1].headers["Idempotency-Key"]).not.toBe(key);
    expect(JSON.parse(fetcher.mock.calls[1][1].body).text).toBe("Edited after the lost acknowledgment");
    expect(draftStack.value).toEqual([]);
  });

  it("asks too when the earlier send cannot be checked", async () => {
    openCompose(seed);
    render(<Compose />, host);
    await settle();
    await clickSend();
    await edit("textarea.compose-body", "Edited while the server is unreachable");
    lookups.mockRejectedValue(new TypeError("offline"));
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    await clickSend();
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it("asks before an edited draft could send a second copy of a known-ambiguous send", async () => {
    openCompose(seed);
    render(<Compose />, host);
    await settle();
    // The acknowledgment is lost and the lookup finds the row ambiguous:
    // the server itself says the first copy may have gone out.
    lookups.mockImplementation(async () => entry("ambiguous"));
    await clickSend();
    const key = fetcher.mock.calls[0][1].headers["Idempotency-Key"];
    expect(toast.value?.message).toContain("may already have reached the recipient");
    expect(draftStack.value[0].unconfirmedKey).toBe(key);
    await edit("textarea.compose-body", "Edited after the ambiguous answer");
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    await clickSend();
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(confirm.mock.calls[0][0]).toContain("This message may already have been sent");
    expect(fetcher).toHaveBeenCalledTimes(1); // Cancel: no second POST
    expect(draftStack.value).toHaveLength(1);
    expect(draftStack.value[0].unconfirmedKey).toBe(key); // still unresolved
    confirm.mockReturnValue(true); // sending anyway is a deliberate new submission
    fetcher.mockResolvedValue(accepted());
    await clickSend();
    expect(fetcher).toHaveBeenCalledTimes(2);
    expect(fetcher.mock.calls[1][1].headers["Idempotency-Key"]).not.toBe(key);
    expect(JSON.parse(fetcher.mock.calls[1][1].body).text).toBe("Edited after the ambiguous answer");
    expect(draftStack.value).toEqual([]);
  });

  it("still asks after a known-ambiguous draft is parked, reloaded and edited", async () => {
    fetcher.mockResolvedValue(new Response(JSON.stringify({ queued: "q9", undo_seconds: 0, durable: true, status: "ambiguous" })));
    openCompose(seed);
    render(<Compose />, host);
    await settle();
    await clickSend();
    const key = fetcher.mock.calls[0][1].headers["Idempotency-Key"];
    await edit("textarea.compose-body", "Edited before parking");
    await act(async () => { await wait(300); }); // the autosave persists marker and edit together
    expect((await loadDrafts())[0].unconfirmedKey).toBe(key);
    await act(async () => { render(null, host); });
    resetPrivateState();
    await hydrateDrafts();
    render(<Compose />, host);
    await settle();
    lookups.mockImplementation(async () => entry("ambiguous"));
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    await clickSend();
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(fetcher).toHaveBeenCalledTimes(1); // the reload did not forget the ambiguous send
  });

  it("guards button plus keyboard activation and a remount during an in-flight send", async () => {
    let resolve!: (value: Response) => void;
    fetcher.mockImplementation(() => new Promise<Response>((r) => { resolve = r; }));
    openCompose(seed);
    render(<Compose />, host);
    await settle();
    await act(async () => {
      button("Send").click();
      host.querySelector("textarea")!.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true, bubbles: true }));
      await wait(35);
    });
    expect(fetcher).toHaveBeenCalledTimes(1);
    await act(async () => { render(null, host); });
    render(<Compose />, host);
    await settle();
    expect(button("Sending…").disabled).toBe(true);
    expect(host.querySelector("fieldset")!.disabled).toBe(true);
    await act(async () => { resolve(accepted()); await wait(35); });
    expect(draftStack.value).toEqual([]);
    expect(await loadDrafts()).toEqual([]);
  });

  it("keeps pending attachment reads tied to a draft across park/remount", async () => {
    let finishRead!: (data: ArrayBuffer) => void;
    const file = new File(["private attachment"], "private.txt", { type: "text/plain" });
    Object.defineProperty(file, "arrayBuffer", { value: () => new Promise<ArrayBuffer>((resolve) => { finishRead = resolve; }) });
    openCompose(seed);
    render(<Compose />, host);
    await settle();
    await act(async () => {
      const chooser = host.querySelector<HTMLInputElement>('input[type="file"]')!;
      Object.defineProperty(chooser, "files", { value: [file] });
      chooser.dispatchEvent(new Event("change", { bubbles: true }));
      host.querySelector("textarea")!.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true, bubbles: true }));
      await wait();
    });
    expect(fetcher).not.toHaveBeenCalled();
    await act(async () => { render(null, host); });
    render(<Compose />, host);
    await settle();
    expect(button("Reading files…").disabled).toBe(true);
    await act(async () => { finishRead(new Uint8Array([97, 98, 99]).buffer); await wait(); });
    expect(host.textContent).toContain("private.txt");
    await clickSend();
    expect(JSON.parse(fetcher.mock.calls[0][1].body).attachments).toEqual([{ filename: "private.txt", content_type: "text/plain", data_base64: "YWJj" }]);
  });

  it("does not revive mounted fields or attachments after an owner wipe and unmount", async () => {
    openCompose(seed);
    render(<Compose />, host);
    await settle();
    await edit("textarea.compose-body", "Latest private field");
    const oldId = draftStack.value[0].id;
    await clearOfflineData();
    await prepareOfflineOwner({ installation_id: "inst", user_id: "b", email: "b@example.test" });
    await act(async () => { render(null, host); });
    updateDraftById(oldId, { attachments: [{ filename: "late.txt", contentType: "text/plain", dataBase64: "c2VjcmV0" }] });
    await wait(300);
    expect(draftStack.value).toEqual([]);
    expect(await loadDrafts()).toEqual([]);
  });
});

const thread = {
  thread_id: "thread-a", message_id: "message-a", account: "mirror-a",
  from: "Person <person@example.test>", subject: "Reply subject", received_at: "", preview: "",
} as BriefThread;

describe("Today inline reply retries", () => {
  it("coalesces same-tick activation, reuses the retry key, and changes it on edits", async () => {
    const onDone = vi.fn();
    render(<InlineReply thread={thread} to="person@example.test" onDone={onDone} />, host);
    await edit("textarea", "My reply");
    await act(async () => {
      button("Send").click();
      host.querySelector("textarea")!.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true, bubbles: true }));
      await wait(35);
    });
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(onDone).not.toHaveBeenCalled();
    const original = fetcher.mock.calls[0][1];
    await clickSend();
    expect(fetcher.mock.calls[1][1].headers["Idempotency-Key"]).toBe(original.headers["Idempotency-Key"]);
    expect(fetcher.mock.calls[1][1].body).toBe(original.body);
    await edit("textarea", "A new reply version");
    fetcher.mockResolvedValue(accepted());
    await clickSend();
    expect(fetcher.mock.calls[2][1].headers["Idempotency-Key"]).not.toBe(original.headers["Idempotency-Key"]);
    expect(onDone).toHaveBeenCalledTimes(1);
  });

  it("asks before an edited reply could send a second copy of an unconfirmed one", async () => {
    const onDone = vi.fn();
    render(<InlineReply thread={thread} to="person@example.test" onDone={onDone} />, host);
    await edit("textarea", "My reply");
    await clickSend();
    expect(fetcher).toHaveBeenCalledTimes(1);
    await edit("textarea", "My edited reply");
    lookups.mockImplementation(async () => entry("submitted"));
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    await clickSend();
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(onDone).not.toHaveBeenCalled();
  });

  it("asks before an edited reply could send a second copy of a known-ambiguous one", async () => {
    const onDone = vi.fn();
    fetcher.mockResolvedValue(new Response(JSON.stringify({ queued: "q9", undo_seconds: 0, durable: true, status: "ambiguous" })));
    render(<InlineReply thread={thread} to="person@example.test" onDone={onDone} />, host);
    await edit("textarea", "My reply");
    await clickSend();
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(onDone).not.toHaveBeenCalled();
    await edit("textarea", "My edited reply");
    lookups.mockImplementation(async () => entry("ambiguous"));
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    await clickSend();
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(fetcher).toHaveBeenCalledTimes(1); // Cancel keeps it to one copy
    expect(onDone).not.toHaveBeenCalled();
    confirm.mockReturnValue(true); // sending anyway is a deliberate new submission
    fetcher.mockResolvedValue(accepted());
    await clickSend();
    expect(fetcher).toHaveBeenCalledTimes(2);
    expect(fetcher.mock.calls[1][1].headers["Idempotency-Key"]).not.toBe(fetcher.mock.calls[0][1].headers["Idempotency-Key"]);
    expect(onDone).toHaveBeenCalledTimes(1);
  });

  it("a stale mounted inline reply cannot send into the new session", async () => {
    render(<InlineReply thread={thread} to="person@example.test" onDone={() => {}} />, host);
    await edit("textarea", "Old private reply");
    await prepareOfflineOwner({ installation_id: "inst", user_id: "b", email: "b@example.test" });
    await clickSend();
    expect(fetcher).not.toHaveBeenCalled();
  });
});


function chosenFile(name: string, size: number, read = vi.fn().mockResolvedValue(new Uint8Array([97]).buffer)) {
  const file = new File(["a"], name, { type: "text/plain" });
  Object.defineProperty(file, "size", { value: size });
  Object.defineProperty(file, "arrayBuffer", { value: read });
  return { file, read };
}

async function choose(files: File[]) {
  await act(async () => {
    const chooser = host.querySelector<HTMLInputElement>('input[type="file"]')!;
    Object.defineProperty(chooser, "files", { configurable: true, value: files });
    chooser.dispatchEvent(new Event("change", { bubbles: true }));
    await wait();
  });
}

describe("attachment read admission and release", () => {
  it("skips oversized and excess-total files before starting their reads, keeping valid files", async () => {
    const oversized = chosenFile("oversized.txt", (15 << 20) + 1);
    const first = chosenFile("first.txt", 15 << 20);
    const excess = chosenFile("excess.txt", 11 << 20);
    const valid = chosenFile("valid.txt", 10 << 20);
    openCompose(seed); render(<Compose />, host); await settle();
    await choose([oversized.file, first.file, excess.file, valid.file]);
    expect(oversized.read).not.toHaveBeenCalled();
    expect(excess.read).not.toHaveBeenCalled();
    expect(first.read).toHaveBeenCalledOnce();
    expect(valid.read).toHaveBeenCalledOnce();
    expect(draftStack.value[0].attachments?.map((file) => file.filename)).toEqual(["first.txt", "valid.txt"]);
    expect(pendingDraftReads.value.size).toBe(0);
  });

  it("counts all pending selections toward the 20-file limit before reading", async () => {
    let finish!: (bytes: ArrayBuffer) => void;
    const waiting = chosenFile("first.txt", 1, vi.fn(() => new Promise<ArrayBuffer>((resolve) => { finish = resolve; })));
    const rest = Array.from({ length: 19 }, (_, i) => chosenFile(`f${i}.txt`, 1));
    const excess = chosenFile("excess.txt", 1);
    openCompose(seed); render(<Compose />, host); await settle();
    await choose([waiting.file, ...rest.map((entry) => entry.file)]);
    await choose([excess.file]);
    expect(excess.read).not.toHaveBeenCalled();
    expect(pendingDraftReads.value.get(draftStack.value[0].id)).toBe(20);
    await act(async () => { finish(new Uint8Array([97]).buffer); await wait(); });
    expect(draftStack.value[0].attachments).toHaveLength(20);
    expect(pendingDraftReads.value.size).toBe(0);
  });

  it("reserves bytes across concurrent selections and releases failed reads for retry", async () => {
    let fail!: (error: Error) => void;
    const pending = chosenFile("pending.txt", 15 << 20, vi.fn(() => new Promise<ArrayBuffer>((_, reject) => { fail = reject; })));
    const excess = chosenFile("excess.txt", 11 << 20);
    openCompose(seed); render(<Compose />, host); await settle();
    await choose([pending.file]);
    await choose([excess.file]);
    expect(excess.read).not.toHaveBeenCalled();
    await act(async () => { fail(new Error("disk read failed")); await wait(); });
    expect(pendingDraftReads.value.size).toBe(0);
    expect(toast.value?.message).toContain('"pending.txt" could not be read');
    await choose([excess.file]);
    expect(excess.read).toHaveBeenCalledOnce();
    expect(draftStack.value[0].attachments?.[0].filename).toBe("excess.txt");
  });

  it("keeps successfully read files on either side of an individual read error", async () => {
    const first = chosenFile("first.txt", 1);
    const broken = chosenFile("broken.txt", 1, vi.fn().mockRejectedValue(new Error("unreadable")));
    const last = chosenFile("last.txt", 1);
    openCompose(seed); render(<Compose />, host); await settle();
    await choose([first.file, broken.file, last.file]);
    expect(draftStack.value[0].attachments?.map((file) => file.filename)).toEqual(["first.txt", "last.txt"]);
    expect(toast.value?.message).toContain('"broken.txt" could not be read');
    expect(pendingDraftReads.value.size).toBe(0);
  });

  it("bounds concurrent conversion memory across the entire draft ring", async () => {
    let finish!: (bytes: ArrayBuffer) => void;
    const first = chosenFile("first.txt", 15 << 20, vi.fn(() => new Promise<ArrayBuffer>((resolve) => { finish = resolve; })));
    const other = chosenFile("other.txt", 15 << 20);
    openCompose(seed); render(<Compose />, host); await settle();
    const firstId = draftStack.value[0].id;
    await choose([first.file]);
    await act(async () => { openCompose({ ...seed, subject: "Another draft" }); await wait(); });
    await choose([other.file]);
    expect(other.read).not.toHaveBeenCalled();
    expect(toast.value?.message).toContain("memory budget");
    expect(pendingDraftReads.value.size).toBe(1);
    expect(pendingDraftReads.value.get(firstId)).toBe(1);
    await act(async () => { finish(new Uint8Array([97]).buffer); await wait(); });
    await choose([other.file]);
    expect(other.read).toHaveBeenCalledOnce();
    expect(pendingDraftReads.value.size).toBe(0);
  });

  it("releases reservations on retirement without starting remaining queued reads", async () => {
    let finish!: (bytes: ArrayBuffer) => void;
    const first = chosenFile("pending.txt", 1, vi.fn(() => new Promise<ArrayBuffer>((resolve) => { finish = resolve; })));
    const never = chosenFile("never.txt", 1);
    openCompose(seed); render(<Compose />, host); await settle();
    await choose([first.file, never.file]);
    await act(async () => { button("Discard").click(); await wait(); });
    expect(pendingDraftReads.value.size).toBe(0);
    await act(async () => { finish(new Uint8Array([97]).buffer); await wait(); });
    expect(never.read).not.toHaveBeenCalled();
    expect(draftStack.value).toEqual([]);
  });

  it("an old read completion after reset cannot append to or release a new draft's reservation", async () => {
    let finishOld!: (bytes: ArrayBuffer) => void;
    let finishNew!: (bytes: ArrayBuffer) => void;
    const old = chosenFile("old.txt", 1, vi.fn(() => new Promise<ArrayBuffer>((resolve) => { finishOld = resolve; })));
    const never = chosenFile("never.txt", 1);
    const fresh = chosenFile("fresh.txt", 1, vi.fn(() => new Promise<ArrayBuffer>((resolve) => { finishNew = resolve; })));
    openCompose(seed); render(<Compose />, host); await settle();
    await choose([old.file, never.file]);
    await act(async () => { resetPrivateState(); openCompose(seed); await wait(); });
    const newId = draftStack.value[0].id;
    await choose([fresh.file]);
    await act(async () => { finishOld(new Uint8Array([97]).buffer); await wait(); });
    expect(never.read).not.toHaveBeenCalled();
    expect(draftStack.value[0].attachments).toBeUndefined();
    expect(pendingDraftReads.value.get(newId)).toBe(1);
    await act(async () => { finishNew(new Uint8Array([98]).buffer); await wait(); });
    expect(draftStack.value[0].attachments?.map((file) => file.filename)).toEqual(["fresh.txt"]);
    expect(pendingDraftReads.value.size).toBe(0);
  });
});

describe("composer focus", () => {
  // The autofocus attribute is honoured once per page load, so a draft opened
  // later has to take focus explicitly.
  it("focuses To for a blank draft and the body for a draft that has a recipient, every time", async () => {
    document.body.append(host);
    try {
      openCompose();
      render(<Compose />, host);
      await settle();
      expect(document.activeElement).toBe(host.querySelector(".compose-to"));
      await act(async () => { openCompose({ to: "person@example.test", subject: "Re: hello" }); await wait(); });
      expect(document.activeElement).toBe(host.querySelector(".compose-body"));
      await act(async () => { openCompose({ to: "second@example.test" }); await wait(); });
      expect(document.activeElement).toBe(host.querySelector(".compose-body"));
    } finally {
      await act(async () => { render(null, host); });
      host.remove();
    }
  });
});
