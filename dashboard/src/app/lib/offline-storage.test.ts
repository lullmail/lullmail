// @vitest-environment jsdom
import "fake-indexeddb/auto";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  cacheResponse, cachedResponse, clearOfflineData, loadDrafts, offlineGeneration,
  deleteDraft, offlineOwner, prepareOfflineOwner, queueMutation, saveDraftFields,
  replayMutations, withReplayLock,
} from "./offline";

// The offline stores behind the exported surface. The database name is
// the stable on-disk contract (the v2 migration depends on it), so the
// tests may open it directly to count rows.
const DB_NAME = "lullmail-offline-v1";

function countStore(name: string): Promise<number> {
  return new Promise((resolve, reject) => {
    const open = indexedDB.open(DB_NAME);
    open.onerror = () => reject(open.error);
    open.onsuccess = () => {
      const db = open.result;
      const tx = db.transaction(name, "readonly");
      const count = tx.objectStore(name).count();
      count.onsuccess = () => { db.close(); resolve(count.result); };
      count.onerror = () => { db.close(); reject(count.error); };
    };
  });
}

function dropOfflineDB(): Promise<void> {
  return new Promise((resolve) => {
    const req = indexedDB.deleteDatabase(DB_NAME);
    req.onsuccess = req.onerror = req.onblocked = () => resolve();
  });
}

beforeEach(async () => {
  localStorage.clear();
  await dropOfflineDB();
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("logout wipe (audit WEB-07, ratified semantics)", () => {
  it("clears caches, queue and drafts, drops the namespace marker, and keeps the generation counting", async () => {
    await prepareOfflineOwner({ installation_id: "inst1", user_id: "u1", email: "a@example.com" });
    const genAtSetup = offlineGeneration();
    expect(offlineOwner()).toBe("inst1/u1");

    await cacheResponse("/buckets/imbox", { rows: [1, 2, 3] });
    await queueMutation("/api/notes", "POST", { text: "x" }, "key-1");
    await saveDraftFields("d1", { to: "x@example.com", subject: "s", body: "b" });
    expect(await cachedResponse("/buckets/imbox")).toEqual({ rows: [1, 2, 3] });
    expect(await countStore("responses")).toBe(1);
    expect(await countStore("mutations")).toBe(1);
    expect((await loadDrafts()).length).toBe(1);

    await clearOfflineData();

    expect(await countStore("responses")).toBe(0);
    expect(await countStore("mutations")).toBe(0);
    expect(await countStore("drafts")).toBe(0);
    expect(await countStore("attachments")).toBe(0);
    expect(await cachedResponse("/buckets/imbox")).toBeUndefined();
    expect(await loadDrafts()).toEqual([]);
    expect(offlineOwner()).toBe("");
    // The generation counter is NEVER removed: it keeps counting up so a
    // pre-wipe async write can never publish into a post-wipe namespace.
    expect(offlineGeneration()).toBeGreaterThan(genAtSetup);
    expect(localStorage.getItem("lull-offline-gen")).not.toBeNull();
  });
});

describe("single-record drafts (audit F05)", () => {
  it("merges field edits into one record without clobbering its attachments", async () => {
    await prepareOfflineOwner({ installation_id: "inst1", user_id: "u1", email: "a@example.com" });

    await saveDraftFields("d1", { to: "x@example.com", subject: "one", body: "b" });
    await saveDraftFields("d1", { attachments: [{ filename: "a.txt", contentType: "text/plain", dataBase64: "QQ==" }] });
    await saveDraftFields("d1", { subject: "two" });

    const drafts = await loadDrafts();
    expect(drafts).toHaveLength(1);
    expect(drafts[0].subject).toBe("two");
    expect(drafts[0].to).toBe("x@example.com");
    expect(drafts[0].attachments).toEqual([{ filename: "a.txt", contentType: "text/plain", dataBase64: "QQ==" }]);
  });
});

describe("v1 migration failure keeps the only copies (audit 5 OFF-01)", () => {
  it("leaves legacy localStorage drafts and no completion marker when the copy fails", async () => {
    // Legacy v1 state: ring metadata + field slots under the old owner.
    localStorage.setItem("es-offline-owner", "a@example.com");
    localStorage.setItem("es-drafts", JSON.stringify([{ id: "d1" }]));
    localStorage.setItem("es-draft-d1", JSON.stringify({ to: "x@example.com", subject: "s", body: "private text" }));

    // A storage failure at copy time: every readwrite transaction on the
    // offline DB throws synchronously (a quota-style hard failure).
    const realIDB = indexedDB;
    const failingDB = {
      transaction() { throw new DOMException("simulated quota failure", "AbortError"); },
      close() { /* nothing to close */ },
    };
    const failingOpen = ((_name: string, _version?: number) => {
      const req: {
        onsuccess: ((ev: Event) => void) | null;
        onerror: ((ev: Event) => void) | null;
        onblocked: ((ev: Event) => void) | null;
        onupgradeneeded: ((ev: Event) => void) | null;
        result: unknown;
        error: DOMException | null;
      } = { onsuccess: null, onerror: null, onblocked: null, onupgradeneeded: null, result: failingDB, error: null };
      setTimeout(() => req.onsuccess?.({ target: req } as unknown as Event), 0);
      return req;
    }) as typeof indexedDB.open;
    (globalThis as { indexedDB: typeof indexedDB }).indexedDB = { open: failingOpen } as unknown as typeof indexedDB;

    try {
      await expect(prepareOfflineOwner({ installation_id: "inst1", user_id: "u1", email: "a@example.com" })).rejects.toThrow("simulated quota failure");
    } finally {
      (globalThis as { indexedDB: typeof indexedDB }).indexedDB = realIDB;
    }

    // The migration failed: the completion marker is UNSET, and every
    // legacy source key survives — the next boot can retry the copy.
    expect(localStorage.getItem("lull-offline-v2")).toBeNull();
    expect(localStorage.getItem("es-drafts")).not.toBeNull();
    expect(localStorage.getItem("es-draft-d1")).not.toBeNull();
    expect(localStorage.getItem("es-offline-owner")).not.toBeNull();
  });
});


describe("cross-tab lifecycle regression (OFF-02 / DRAFT-02)", () => {
  it("does not resurrect a discarded draft when another tab saves its stale copy", async () => {
    await prepareOfflineOwner({ installation_id: "inst1", user_id: "u1", email: "a@example.com" });
    await saveDraftFields("shared-draft", { body: "before discard" });
    await deleteDraft("shared-draft");
    await expect(saveDraftFields("shared-draft", { body: "late autosave from another tab" })).rejects.toThrow(/sent or discarded/);
    expect(await loadDrafts()).toEqual([]);
  });

  it("cannot insert queued work after a wipe wins the asynchronous database open", async () => {
    await prepareOfflineOwner({ installation_id: "inst1", user_id: "u1", email: "a@example.com" });
    const realOpen = indexedDB.open.bind(indexedDB);
    let release!: () => void;
    let opened!: () => void;
    const waiting = new Promise<void>((resolve) => { opened = resolve; });
    const spy = vi.spyOn(indexedDB, "open").mockImplementationOnce((...args) => {
      const request = realOpen(...args);
      Object.defineProperty(request, "onsuccess", { set(callback) {
        request.addEventListener("success", (event) => {
          release = () => callback.call(request, event);
          opened();
        });
      } });
      return request;
    });
    const queued = queueMutation("/notes/n", "PUT", { text: "old owner's note" });
    const outcome = queued.catch(() => undefined);
    await waiting;
    await clearOfflineData();
    release();
    await outcome;
    spy.mockRestore();
    expect(await countStore("mutations")).toBe(0);
  });
});

describe("authoritative metadata migration", () => {
  it.each([false, true])("upgrades v3 preserving drafts and honoring pending cache cleanup (%s)", async (pendingPurge) => {
    const draftId = "old-draft-" + pendingPurge;
    localStorage.setItem("lull-offline-v2", "1");
    localStorage.setItem("lull-offline-ns", "inst1/u1");
    localStorage.setItem("lull-offline-gen", "17");
    if (pendingPurge) localStorage.setItem('lull-offline-purge:["inst1/u1",17]:interrupted', "1");
    await new Promise<void>((resolve, reject) => {
      const request = indexedDB.open(DB_NAME, 3);
      request.onupgradeneeded = () => {
        for (const name of ["responses", "mutations", "attachments", "drafts"]) {
          request.result.createObjectStore(name, { keyPath: name === "responses" ? "key" : "id" });
        }
      };
      request.onsuccess = () => {
        const db = request.result;
        const tx = db.transaction(["responses", "drafts"], "readwrite");
        tx.objectStore("responses").put({ key: "inst1/u1\n\n/buckets/imbox", owner: "inst1/u1", account: "", value: "offline mail" });
        tx.objectStore("drafts").put({ id: draftId, ns: "inst1/u1", seq: 1, body: "unsent" });
        tx.oncomplete = () => { db.close(); resolve(); };
        tx.onabort = () => { db.close(); reject(tx.error); };
      };
    });
    await prepareOfflineOwner({ installation_id: "inst1", user_id: "u1", email: "a@example.com" });
    expect(offlineGeneration()).toBe(17);
    expect(await cachedResponse("/buckets/imbox")).toBe(pendingPurge ? undefined : "offline mail");
    expect((await loadDrafts())[0].body).toBe("unsent");
    expect(await saveDraftFields(draftId, { subject: "edit after upgrade" })).toBe(true);
  });

  it("migrates legacy fields, attachments and queue together before removing their sources", async () => {
    localStorage.setItem("es-offline-owner", "a@example.com");
    localStorage.setItem("es-drafts", JSON.stringify([{ id: "legacy" }]));
    localStorage.setItem("es-draft-legacy", JSON.stringify({ body: "unsent legacy" }));
    await new Promise<void>((resolve) => {
      const request = indexedDB.open(DB_NAME, 1);
      request.onupgradeneeded = () => {
        for (const name of ["responses", "mutations", "attachments"]) request.result.createObjectStore(name, { keyPath: name === "responses" ? "key" : "id" });
      };
      request.onsuccess = () => {
        const db = request.result;
        const tx = db.transaction(["attachments", "mutations"], "readwrite");
        tx.objectStore("attachments").put({ id: "legacy", owner: "a@example.com", files: [{ filename: "a.txt", dataBase64: "YQ==" }] });
        tx.objectStore("mutations").put({ id: "legacy-q", owner: "a@example.com", method: "PUT", path: "/notes/n", queuedAt: 1 });
        tx.oncomplete = () => { db.close(); resolve(); };
      };
    });
    await prepareOfflineOwner({ installation_id: "inst1", user_id: "u1", email: "a@example.com" });
    expect(await loadDrafts()).toEqual([expect.objectContaining({ id: "legacy", ns: "inst1/u1", body: "unsent legacy", attachments: [{ filename: "a.txt", dataBase64: "YQ==" }] })]);
    expect(await countStore("mutations")).toBe(1);
    expect(await countStore("attachments")).toBe(0);
    expect(localStorage.getItem("es-draft-legacy")).toBeNull();
    expect(localStorage.getItem("lull-offline-v2")).toBe("1");
  });
});

describe("metadata commit and interrupted mirror publication", () => {
  it("recovers committed owner metadata after its localStorage mirror failed", async () => {
    const set = Storage.prototype.setItem;
    const spy = vi.spyOn(Storage.prototype, "setItem").mockImplementation(function (this: Storage, key, value) {
      if (key === "lull-offline-gen") throw new Error("mirror quota");
      return set.call(this, key, value);
    });
    await expect(prepareOfflineOwner({ installation_id: "inst1", user_id: "u1", email: "a@example.com" })).rejects.toThrow("mirror quota");
    spy.mockRestore();
    await prepareOfflineOwner({ installation_id: "inst1", user_id: "u1", email: "a@example.com" });
    expect(await saveDraftFields("recovered", { body: "unsent" })).toBe(true);
    expect((await loadDrafts())[0].id).toBe("recovered");
  });

  it("never remigrates obsolete legacy fields over a committed draft when cleanup was interrupted", async () => {
    localStorage.setItem("es-offline-owner", "a@example.com");
    localStorage.setItem("es-drafts", JSON.stringify([{ id: "legacy" }]));
    localStorage.setItem("es-draft-legacy", JSON.stringify({ body: "legacy body" }));
    const set = Storage.prototype.setItem;
    const spy = vi.spyOn(Storage.prototype, "setItem").mockImplementation(function (this: Storage, key, value) {
      if (key === "lull-offline-v2") throw new Error("marker quota");
      return set.call(this, key, value);
    });
    await expect(prepareOfflineOwner({ installation_id: "inst1", user_id: "u1", email: "a@example.com" })).rejects.toThrow("marker quota");
    spy.mockRestore();
    // Model a committed draft edit in a different tab before cleanup retry.
    await new Promise<void>((resolve) => {
      const request = indexedDB.open(DB_NAME);
      request.onsuccess = () => {
        const db = request.result;
        const tx = db.transaction("drafts", "readwrite");
        tx.objectStore("drafts").put({ id: "legacy", ns: "inst1/u1", seq: 1, body: "newer body", attachments: [{ filename: "keep.txt", dataBase64: "YQ==" }] });
        tx.oncomplete = () => { db.close(); resolve(); };
      };
    });
    await prepareOfflineOwner({ installation_id: "inst1", user_id: "u1", email: "a@example.com" });
    expect(await loadDrafts()).toEqual([expect.objectContaining({ body: "newer body", attachments: [{ filename: "keep.txt", dataBase64: "YQ==" }] })]);
    expect(localStorage.getItem("es-draft-legacy")).toBeNull();
  });
});


describe("logout before legacy migration", () => {
  it("cannot reimport pre-logout localStorage drafts when the same owner returns", async () => {
    localStorage.setItem("es-offline-owner", "a@example.com");
    localStorage.setItem("es-drafts", JSON.stringify([{ id: "legacy" }]));
    localStorage.setItem("es-draft-legacy", JSON.stringify({ body: "must not survive logout" }));
    // First prepare an empty owner context so this is an admitted logout,
    // then introduce legacy remnants to model an interrupted old client.
    await prepareOfflineOwner({ installation_id: "inst1", user_id: "u1", email: "a@example.com" });
    localStorage.removeItem("lull-offline-v2");
    localStorage.setItem("es-offline-owner", "a@example.com");
    localStorage.setItem("es-drafts", JSON.stringify([{ id: "legacy" }]));
    localStorage.setItem("es-draft-legacy", JSON.stringify({ body: "must not survive logout" }));
    await clearOfflineData();
    await prepareOfflineOwner({ installation_id: "inst1", user_id: "u1", email: "a@example.com" });
    expect(await loadDrafts()).toEqual([]);
    expect(localStorage.getItem("es-draft-legacy")).toBeNull();
  });
});

describe("bounded replay attempts", () => {
  it("releases a stalled replay lock and retries the same queued key after backoff", async () => {
    await prepareOfflineOwner({ installation_id: "inst1", user_id: "u1", email: "a@example.com" });
    await queueMutation("/notes/n", "PUT", { text: "kept" }, "same-key");
    let held = false;
    vi.stubGlobal("navigator", { onLine: true, locks: {
      request: async (_name: string, _options: unknown, run: (lock: unknown) => Promise<unknown>) => {
        if (held) return run(null);
        held = true;
        try { return await run({}); }
        finally { held = false; }
      },
    } });
    let started!: () => void;
    const fetching = new Promise<void>((resolve) => { started = resolve; });
    const fetcher = vi.fn((_url: unknown, init?: RequestInit) => new Promise<Response>((_resolve, reject) => {
      init!.signal!.addEventListener("abort", () => reject(new DOMException("timed out", "AbortError")), { once: true });
      started();
    }));
    vi.stubGlobal("fetch", fetcher);
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
    const pending = replayMutations();
    await fetching;
    expect(held).toBe(true);
    await vi.advanceTimersByTimeAsync(30_000);
    const result = await pending;
    expect(held).toBe(false);
    expect(result.committed).toBe(0);
    expect(result.retryAt).toBeGreaterThan(Date.now());
    expect(await countStore("mutations")).toBe(1);
    fetcher.mockImplementation(async () => new Response(null, { status: 204 }));
    await vi.advanceTimersByTimeAsync(result.retryAt! - Date.now() + 1);
    expect(await replayMutations()).toEqual({ committed: 1, rejected: 0 });
    expect(await countStore("mutations")).toBe(0);
    expect(fetcher).toHaveBeenCalledTimes(2);
    for (const [, init] of fetcher.mock.calls) expect((init?.headers as Record<string, string>)["Idempotency-Key"]).toBe("same-key");
  });

  it("does not run an uncoordinated duplicate when the lock manager rejects", async () => {
    vi.stubGlobal("navigator", { locks: { request: () => Promise.reject(new Error("lock unavailable")) } });
    const run = vi.fn(async () => "done");
    await expect(withReplayLock(run)).rejects.toThrow("lock unavailable");
    expect(run).not.toHaveBeenCalled();
  });
});

describe("IndexedDB failure boundaries", () => {
  it("reports a blocked upgrade and closes its eventual connection so a retry succeeds", async () => {
    const held = await new Promise<IDBDatabase>((resolve, reject) => {
      const request = indexedDB.open(DB_NAME, 3);
      request.onupgradeneeded = () => {
        for (const name of ["responses", "mutations", "attachments", "drafts"]) {
          request.result.createObjectStore(name, { keyPath: name === "responses" ? "key" : "id" });
        }
      };
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
    try {
      await expect(prepareOfflineOwner({ installation_id: "inst1", user_id: "u1", email: "a@example.com" })).rejects.toThrow(/Close other Lullmail tabs/);
    } finally { held.close(); }
    await prepareOfflineOwner({ installation_id: "inst1", user_id: "u1", email: "a@example.com" });
    expect(await saveDraftFields("after-unblock", { body: "safe retry" })).toBe(true);
    await new Promise<void>((resolve, reject) => {
      const request = indexedDB.deleteDatabase(DB_NAME);
      request.onsuccess = () => resolve();
      request.onerror = request.onblocked = () => reject(request.error ?? new Error("leaked database connection"));
    });
  });

  it("does not acknowledge a draft revision or lose its attachment when a write aborts", async () => {
    await prepareOfflineOwner({ installation_id: "inst1", user_id: "u1", email: "a@example.com" });
    const attachments = [{ filename: "keep.txt", contentType: "text/plain", dataBase64: "YQ==" }];
    await saveDraftFields("kept", { body: "original", attachments });
    const [original] = await loadDrafts();
    const put = IDBObjectStore.prototype.put;
    const spy = vi.spyOn(IDBObjectStore.prototype, "put").mockImplementation(function (this: IDBObjectStore, value, key) {
      const request = key === undefined ? put.call(this, value) : put.call(this, value, key);
      if (this.name === "drafts") this.transaction.abort();
      return request;
    });
    await expect(saveDraftFields("kept", { body: "aborted edit", attachments: [] })).rejects.toThrow();
    spy.mockRestore();
    expect(await loadDrafts()).toEqual([original]);
    expect(await saveDraftFields("kept", { body: "successful retry" })).toBe(true);
    expect((await loadDrafts())[0]).toEqual(expect.objectContaining({ body: "successful retry", attachments, revision: original.revision! + 1 }));
  });
});
