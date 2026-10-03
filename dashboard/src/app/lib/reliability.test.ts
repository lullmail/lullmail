// @vitest-environment jsdom
import "fake-indexeddb/auto";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, clearMemoryCache, StaleOwnerError } from "./api";
import { checkEarlierSubmission, sendMail, sendMailOutcome, submissionLookup, type SendInput } from "./actions";
import {
  cacheResponse, cachedResponse, clearOfflineData, loadDrafts, offlineGeneration,
  offlineOwner, offlineStorageSuspended, prepareOfflineOwner, purgeAccountSnapshots,
  saveDraftFields, snapshotGeneration,
} from "./offline";
import {
  closeCompose, composeOpen, draftStack, editedSinceUnconfirmed, hydrateDrafts, openCompose, prepareDraftSend,
  resetPrivateState, setUnconfirmedSend, toast, updateDraft,
} from "./store";

const owner = { installation_id: "inst", user_id: "owner-a", email: "a@example.test" };
const other = { installation_id: "inst", user_id: "owner-b", email: "b@example.test" };
const input: SendInput = { to: "recipient@example.test", subject: "Private subject", text: "Private body" };
const accepted = () => new Response(JSON.stringify({ queued: "q1", undo_seconds: 5 }));
const settle = () => new Promise((resolve) => setTimeout(resolve, 300));
const notFound = () => new Response(JSON.stringify({ title: "Not Found" }), { status: 404 });
const entry = (status: string, undoMs = 0) => new Response(JSON.stringify({ id: "row-1", account_id: "a", status, filing_status: "not_started", created_at: new Date().toISOString(), undo_until: new Date(Date.now() + undoMs).toISOString(), recoverable: true, saved_sent_copy: false }));

/** Routes submission lookups (GET /api/outbox?key=) to lookups, everything
 *  else to sends, so each mock sees only its own requests. */
function routeFetch(sends: ReturnType<typeof vi.fn>, lookups: ReturnType<typeof vi.fn>) {
  vi.stubGlobal("fetch", (url: string, init: RequestInit) => (String(url).startsWith("/api/outbox?key=") ? (lookups as unknown as typeof fetch)(url, init) : (sends as unknown as typeof fetch)(url, init)));
}

async function cacheRows(): Promise<Array<{ key: string; owner: string; account: string; value: unknown }>> {
  return new Promise((resolve, reject) => {
    const open = indexedDB.open("lullmail-offline-v1");
    open.onerror = () => reject(open.error);
    open.onsuccess = () => {
      const db = open.result;
      const tx = db.transaction("responses", "readonly");
      const request = tx.objectStore("responses").getAll();
      tx.oncomplete = () => { db.close(); resolve(request.result); };
    };
  });
}

beforeEach(async () => {
  await clearOfflineData();
  await prepareOfflineOwner(owner);
  clearMemoryCache();
  submissionLookup.backoffMs = [];
});
afterEach(async () => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  await clearOfflineData();
});

describe("send submission identity", () => {
  it("honors a replayed zero-second undo window without suggesting another cancel", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ queued: "q1", undo_seconds: 0 }))));
    expect(await sendMail(input, "previously-queued")).toBe(true);
    expect(toast.value?.message).toContain("undo window has ended");
    expect(toast.value?.undo).toBeUndefined();
  });
  it("recognizes a submitted completion receipt without claiming a new undo window", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ queued: "q1", undo_seconds: 0, status: "submitted" }))));
    expect(await sendMail(input, "completed-submission")).toBe(true);
    expect(toast.value?.message).toBe("Message submitted");
    expect(toast.value?.undo).toBeUndefined();
  });

  it("transports one draft key and identical bytes across an ambiguous network retry", async () => {
    openCompose({ to: input.to, subject: input.subject, body: input.text });
    const id = draftStack.value[0].id;
    const firstKey = (await prepareDraftSend(id))!;
    const fetcher = vi.fn().mockRejectedValueOnce(new TypeError("lost acknowledgment")).mockResolvedValueOnce(accepted());
    routeFetch(fetcher, vi.fn().mockImplementation(async () => notFound()));
    expect(await sendMail(input, firstKey)).toBe(false);
    closeCompose();
    openCompose();
    const retryKey = (await prepareDraftSend(id))!;
    expect(await sendMail(input, retryKey)).toBe(true);
    expect(retryKey).toBe(firstKey);
    expect(fetcher).toHaveBeenCalledTimes(2);
    const first = fetcher.mock.calls[0][1];
    const retry = fetcher.mock.calls[1][1];
    expect(first.headers["Idempotency-Key"]).toBeTruthy();
    expect(retry.headers["Idempotency-Key"]).toBe(first.headers["Idempotency-Key"]);
    expect(retry.body).toBe(first.body);
    expect(fetcher.mock.calls[0][0]).toBe("/api/send");
  });

  it("persists the key before fetch and restores it with an unchanged parked draft", async () => {
    openCompose({ to: input.to, subject: input.subject, body: input.text });
    const id = draftStack.value[0].id;
    const key = await prepareDraftSend(id);
    expect((await loadDrafts())[0].sendKey).toBe(key);
    resetPrivateState(); // simulate losing only this tab's live state
    await hydrateDrafts();
    expect(draftStack.value[0].id).toBe(id);
    expect(await prepareDraftSend(id)).toBe(key);
  });

  it.each([
    { to: "different@example.test" }, { cc: "cc@example.test" }, { bcc: "bcc@example.test" },
    { subject: "Edited" }, { body: "Edited" }, { htmlMode: true },
    { accountId: "another-account" }, { replyToId: "another-parent" },
    { attachments: [{ filename: "a.txt", contentType: "text/plain", dataBase64: "YQ==" }] },
  ])("changes identity when send content changes: %j", async (patch) => {
    openCompose({ to: input.to, subject: input.subject, body: input.text });
    const id = draftStack.value[0].id;
    const first = await prepareDraftSend(id);
    updateDraft(patch);
    expect(await prepareDraftSend(id)).not.toBe(first);
  });

  it("does not deduplicate a new draft that happens to have identical content", async () => {
    openCompose({ to: input.to, subject: input.subject, body: input.text });
    const first = await prepareDraftSend(draftStack.value[0].id);
    openCompose({ to: input.to, subject: input.subject, body: input.text });
    expect(await prepareDraftSend(draftStack.value[1].id)).not.toBe(first);
  });

  it("coalesces same-key simultaneous sends and snapshots attachment data for undo", async () => {
    let resolve!: (response: Response) => void;
    const fetcher = vi.fn().mockImplementation(() => new Promise<Response>((r) => { resolve = r; }));
    vi.stubGlobal("fetch", fetcher);
    const draft = { ...input, attachments: [{ filename: "a.txt", contentType: "text/plain", dataBase64: "YQ==" }] };
    const first = sendMail(draft, "submission-a");
    const second = sendMail(draft, "submission-a");
    expect(first).toBe(second);
    expect(fetcher).toHaveBeenCalledTimes(1);
    draft.attachments[0].dataBase64 = "Yg==";
    resolve(accepted());
    expect(await first).toBe(true);
    fetcher.mockResolvedValue(new Response(null, { status: 204 }));
    await toast.value!.undo!();
    expect(draftStack.value[0].attachments![0].dataBase64).toBe("YQ==");
    expect(draftStack.value[0].sendKey).toBeUndefined();
  });

  it("cannot restore an old send undo into the next owner", async () => {
    const fetcher = vi.fn().mockResolvedValue(accepted());
    vi.stubGlobal("fetch", fetcher);
    await sendMail(input, "submission-a");
    const oldUndo = toast.value!.undo!;
    await prepareOfflineOwner(other);
    expect(toast.value).toBeNull();
    await oldUndo();
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(draftStack.value).toEqual([]);
  });
});

describe("account snapshot removal", () => {
  it("purges product, mirror, and unified snapshots while retaining another mailbox", async () => {
    await cacheResponse("/buckets/imbox?account=product-a", "private list");
    await cacheResponse("/threads/t?account=mirror-a", "private body");
    await cacheResponse("/buckets/imbox", "unified mail");
    await cacheResponse("/threads/t?account=mirror-b", "other mailbox");
    await purgeAccountSnapshots("product-a", "mirror-a");
    expect(await cachedResponse("/buckets/imbox?account=product-a")).toBeUndefined();
    expect(await cachedResponse("/threads/t?account=mirror-a")).toBeUndefined();
    expect(await cachedResponse("/buckets/imbox")).toBeUndefined();
    expect(await cachedResponse("/threads/t?account=mirror-b")).toBe("other mailbox");
  });

  it("fails closed on old servers without a mirror identity mapping", async () => {
    await cacheResponse("/threads/t?account=unknown-mirror", "private body");
    await purgeAccountSnapshots("product-a");
    expect(await cacheRows()).toHaveLength(0);
  });

  it("never deletes another owner's rows even when mailbox ids match", async () => {
    await cacheResponse("/threads/t?account=mirror-a", "owner-a");
    // Preserve a second namespace directly: switching owners normally wipes it.
    await new Promise<void>((resolve) => {
      const open = indexedDB.open("lullmail-offline-v1");
      open.onsuccess = () => {
        const db = open.result;
        const tx = db.transaction("responses", "readwrite");
        tx.objectStore("responses").put({ key: "other-namespace", owner: "inst/other", account: "mirror-a", value: "owner-b" });
        tx.oncomplete = () => { db.close(); resolve(); };
      };
    });
    await purgeAccountSnapshots("product-a", "mirror-a");
    expect(await cacheRows()).toEqual([expect.objectContaining({ owner: "inst/other", value: "owner-b" })]);
  });

  it("fences a stale write waiting for IndexedDB to open", async () => {
    const gen = offlineGeneration(), snapshots = snapshotGeneration();
    const stale = cacheResponse("/threads/t?account=mirror-a", "stale", gen, snapshots);
    await purgeAccountSnapshots("product-a", "mirror-a");
    await stale;
    await cacheResponse("/threads/t?account=mirror-a", "late explicit write", gen, snapshots);
    expect(await cachedResponse("/threads/t?account=mirror-a")).toBeUndefined();
  });

  it("rejects a late private response instead of republishing or recaching removed mail", async () => {
    let resolve!: (r: Response) => void;
    vi.stubGlobal("fetch", vi.fn(() => new Promise<Response>((r) => { resolve = r; })));
    const response = api("/threads/t?account=mirror-a");
    const rejected = expect(response).rejects.toBeInstanceOf(StaleOwnerError);
    await purgeAccountSnapshots("product-a", "mirror-a");
    resolve(new Response(JSON.stringify({ body: "private" })));
    await rejected;
    expect(await cachedResponse("/threads/t?account=mirror-a")).toBeUndefined();
  });
});

describe("live private-state teardown", () => {
  it("tears down memory even when writing the localStorage generation throws", async () => {
    openCompose({ to: input.to, body: input.text });
    vi.spyOn(Storage.prototype, "setItem").mockImplementationOnce(() => { throw new Error("localStorage blocked"); });
    await expect(clearOfflineData()).rejects.toThrow("localStorage blocked");
    expect(draftStack.value).toEqual([]);
    expect(composeOpen.value).toBe(false);
    expect(offlineStorageSuspended()).toBe(true);
  });
  it("clears live drafts synchronously and cancels autosave before logout's disk wipe", async () => {
    openCompose({ to: input.to, subject: input.subject, body: input.text });
    const before = offlineGeneration();
    const wipe = clearOfflineData();
    expect(draftStack.value).toEqual([]);
    expect(composeOpen.value).toBe(false);
    expect(offlineGeneration()).toBeGreaterThan(before);
    await wipe;
    await prepareOfflineOwner(other);
    updateDraft({ subject: "must not revive the prior draft" });
    window.dispatchEvent(new Event("pagehide"));
    await settle();
    expect(await loadDrafts()).toEqual([]);
  });

  it("a direct owner switch removes parked draft state and pending autosaves", async () => {
    openCompose({ to: input.to, subject: input.subject, body: input.text });
    closeCompose();
    await prepareOfflineOwner(other);
    expect(offlineOwner()).toBe("inst/owner-b");
    expect(draftStack.value).toEqual([]);
    await settle();
    expect(await loadDrafts()).toEqual([]);
  });

  it("fences in-flight draft writes and hydration, even when the same owner signs back in", async () => {
    await saveDraftFields("old", { to: input.to, subject: input.subject, body: input.text });
    const oldGen = offlineGeneration();
    const hydration = hydrateDrafts();
    const staleWrite = saveDraftFields("late", { body: "old private data" }, oldGen);
    await clearOfflineData();
    await prepareOfflineOwner(owner);
    await Promise.all([hydration, staleWrite]);
    await saveDraftFields("late-explicit", { body: "old private data" }, oldGen);
    expect(draftStack.value).toEqual([]);
    expect(await loadDrafts()).toEqual([]);
  });

  it("fails closed on a failed wipe and retries erasure before preparing an owner again", async () => {
    await saveDraftFields("old", { to: input.to, body: input.text });
    openCompose({ to: input.to, body: input.text });
    vi.spyOn(indexedDB, "open").mockImplementationOnce(() => { throw new Error("storage blocked"); });
    await expect(clearOfflineData()).rejects.toThrow("storage blocked");
    expect(draftStack.value).toEqual([]);
    expect(offlineStorageSuspended()).toBe(true);
    expect(await loadDrafts()).toEqual([]);
    await prepareOfflineOwner(owner);
    expect(offlineStorageSuspended()).toBe(false);
    expect(await loadDrafts()).toEqual([]);
  });
});

describe("durable outbox recovery boundaries", () => {
  it.each(["ambiguous", "failed", "cancelled"])("keeps a draft when a retained key reports %s", async (status) => {
    openCompose({ to: input.to, subject: input.subject, body: input.text });
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ queued: "old", undo_seconds: 0, durable: true, status }))));
    expect(await sendMail(input, "retained-" + status)).toBe(false);
    expect(draftStack.value).toHaveLength(1);
    expect(toast.value?.undo).toBeUndefined();
  });
  it("stores normal no-store mailbox responses but never outbox recovery data", async () => {
    const fetcher = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify([{ subject: "offline mail" }]), { headers: { "Cache-Control": "no-store" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ request: { text: "sensitive outbox copy" } }), { headers: { "Cache-Control": "no-store" } }));
    vi.stubGlobal("fetch", fetcher);
    await api("/buckets/imbox", { fresh: true }); await api("/outbox/job", { fresh: true });
    await settle();
    expect(await cachedResponse("/buckets/imbox")).toEqual([{ subject: "offline mail" }]);
    expect(await cachedResponse("/outbox/job")).toBeUndefined();
    fetcher.mockResolvedValueOnce(new Response(JSON.stringify({ id: "row", status: "pending" })));
    await api("/outbox?key=k", { fresh: true });
    await settle();
    expect(await cachedResponse("/outbox?key=k")).toBeUndefined();
    fetcher.mockRejectedValue(new Error("offline"));
    await expect(api("/outbox/job")).rejects.toThrow("offline");
  });
});

describe("unknown send outcomes are resolved, not reported as failures", () => {
  it("treats a lost acknowledgment as queued when the key's row exists", async () => {
    const lookups = vi.fn().mockImplementation(async () => entry("pending", 4000));
    routeFetch(vi.fn().mockRejectedValue(new TypeError("lost acknowledgment")), lookups);
    expect(await sendMailOutcome(input, "lost-pending")).toBe("accepted");
    expect(lookups.mock.calls[0][0]).toBe("/api/outbox?key=lost-pending");
    expect(toast.value?.message).toMatch(/^Sending in [34]s$/);
    expect(toast.value?.undo).toBeDefined();
  });
  it("shows the real state of a row found after a 5xx", async () => {
    routeFetch(vi.fn().mockResolvedValue(new Response(JSON.stringify({ detail: "acceptance could not be confirmed" }), { status: 503 })), vi.fn().mockImplementation(async () => entry("submitted")));
    expect(await sendMailOutcome(input, "lost-submitted")).toBe("accepted");
    expect(toast.value?.message).toBe("Message submitted");
    routeFetch(vi.fn().mockRejectedValue(new TypeError("timeout")), vi.fn().mockImplementation(async () => entry("ambiguous")));
    expect(await sendMailOutcome(input, "lost-ambiguous")).toBe("kept");
    expect(toast.value?.message).toContain("may already have reached the recipient");
  });
  it("retries the lookup with backoff before giving up", async () => {
    submissionLookup.backoffMs = [5, 5];
    const lookups = vi.fn().mockRejectedValueOnce(new TypeError("still offline")).mockImplementationOnce(async () => notFound()).mockImplementation(async () => entry("pending", 4000));
    routeFetch(vi.fn().mockRejectedValue(new TypeError("lost acknowledgment")), lookups);
    expect(await sendMailOutcome(input, "late-commit")).toBe("accepted");
    expect(lookups).toHaveBeenCalledTimes(3);
  });
  it("says the outcome could not be confirmed when no row is found", async () => {
    submissionLookup.backoffMs = [1, 1];
    const lookups = vi.fn().mockImplementation(async () => notFound());
    routeFetch(vi.fn().mockRejectedValue(new TypeError("lost acknowledgment")), lookups);
    expect(await sendMailOutcome(input, "never-arrived")).toBe("unconfirmed");
    expect(lookups).toHaveBeenCalledTimes(3);
    expect(toast.value?.tone).toBe("error");
    expect(toast.value?.message).toContain("could not be confirmed");
  });
  it("does not look up a definite refusal", async () => {
    const lookups = vi.fn();
    routeFetch(vi.fn().mockResolvedValue(new Response(JSON.stringify({ detail: "you already have 4 sends waiting" }), { status: 429 })), lookups);
    expect(await sendMailOutcome(input, "refused")).toBe("kept");
    expect(lookups).not.toHaveBeenCalled();
    expect(toast.value?.message).toContain("Could not send");
  });
  it("classifies an earlier submission before an edited draft is re-sent", async () => {
    for (const [response, verdict] of [
      [() => entry("pending", 4000), "exists"], [() => entry("submitted"), "exists"], [() => entry("ambiguous"), "exists"],
      [() => entry("failed"), "clear"], [() => entry("cancelled"), "clear"], [() => notFound(), "clear"],
    ] as const) {
      routeFetch(vi.fn(), vi.fn().mockImplementation(async () => response()));
      expect(await checkEarlierSubmission("earlier")).toBe(verdict);
    }
    routeFetch(vi.fn(), vi.fn().mockRejectedValue(new TypeError("offline")));
    expect(await checkEarlierSubmission("earlier")).toBe("unknown");
  });
  it("keeps the unconfirmed key across edits and reloads, and flags only an edited draft", async () => {
    openCompose({ to: input.to, subject: input.subject, body: input.text });
    const id = draftStack.value[0].id;
    const key = (await prepareDraftSend(id))!;
    setUnconfirmedSend(id, key);
    // Unchanged: the next send replays the same key, nothing to check.
    expect(editedSinceUnconfirmed(id)).toBeUndefined();
    expect(await prepareDraftSend(id)).toBe(key);
    // Edited: a new key would be minted, so the old one must be checked.
    updateDraft({ body: "Edited after the lost acknowledgment" });
    expect(draftStack.value[0].sendKey).toBeUndefined();
    expect(editedSinceUnconfirmed(id)).toBe(key);
    await settle();
    resetPrivateState();
    await hydrateDrafts();
    expect(draftStack.value[0].unconfirmedKey).toBe(key);
    expect(editedSinceUnconfirmed(id)).toBe(key);
    setUnconfirmedSend(id, undefined);
    expect(editedSinceUnconfirmed(id)).toBeUndefined();
  });
});
