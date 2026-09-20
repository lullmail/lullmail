// @vitest-environment jsdom
import "fake-indexeddb/auto";
import { beforeEach, describe, expect, it } from "vitest";

import {
  cacheResponse, cachedResponse, clearOfflineData, loadDrafts, offlineGeneration,
  offlineOwner, prepareOfflineOwner, queueMutation, saveDraftFields,
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
    expect(offlineGeneration()).toBe(genAtSetup);
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
    (globalThis as { indexedDB: typeof indexedDB }).indexedDB = failingOpen as unknown as typeof indexedDB;

    try {
      await prepareOfflineOwner({ installation_id: "inst1", user_id: "u1", email: "a@example.com" });
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
