// @vitest-environment jsdom
import "fake-indexeddb/auto";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import * as first from "./offline";
import { draftStack, openCompose, resetPrivateState } from "./store";

// LUL-D01: the localStorage generation/snapshot writes are invalidation
// mirrors, not prerequisites for the authoritative IndexedDB erase. A
// quota failure on the first setItem used to reject clearOfflineData
// before IndexedDB was even opened — the erase intent died in memory, and
// a reload re-admitted every private record even though the server logout
// had succeeded.

const identity = { installation_id: "inst", user_id: "owner-a", email: "a@example.test" };
const DB = "lullmail-offline-v1";
let reloaded: typeof first;

async function rawRows<T>(store: string): Promise<T[]> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(DB);
    request.onerror = () => reject(request.error);
    request.onsuccess = () => {
      const db = request.result;
      if (!db.objectStoreNames.contains(store)) {
        db.close();
        resolve([]);
        return;
      }
      const tx = db.transaction(store, "readonly");
      const rows = tx.objectStore(store).getAll();
      tx.oncomplete = () => {
        db.close();
        resolve(rows.result);
      };
      tx.onabort = () => {
        db.close();
        reject(tx.error);
      };
    };
  });
}

async function sessionMeta(): Promise<{ owner: string } | undefined> {
  const rows = await rawRows<{ key: string; owner: string }>("meta");
  return rows.find((row) => row.key === "session");
}

beforeAll(async () => {
  // The reloaded page: a fresh module over the same durable stores.
  vi.resetModules();
  reloaded = await import("./offline");
});

beforeEach(async () => {
  resetPrivateState();
  await new Promise<void>((resolve, reject) => {
    const request = indexedDB.deleteDatabase(DB);
    request.onsuccess = () => resolve();
    request.onerror = request.onblocked = () => reject(request.error ?? new Error("database blocked"));
  });
  localStorage.clear();
  await first.prepareOfflineOwner(identity);
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("durable logout erasure", () => {
  it("erases every private store despite a failed generation mirror write", async () => {
    await first.saveDraftFields("d01-draft", { body: "private" });
    await first.cacheResponse("/buckets/imbox", "private snapshot");
    await first.queueMutation("/notes/n", "PUT", { text: "queued work" });
    openCompose({ to: "x@example.test", body: "still live" });
    vi.spyOn(Storage.prototype, "setItem").mockImplementationOnce(() => {
      throw new Error("quota exceeded");
    });

    // The mirror failure is reported — never silently swallowed — but it
    // no longer prevents or undoes the authoritative erase.
    await expect(first.clearOfflineData()).rejects.toThrow(/mirror could not be updated/);
    expect(draftStack.value).toEqual([]);
    expect(first.offlineStorageSuspended()).toBe(true);

    expect(await rawRows("drafts")).toEqual([]);
    expect(await rawRows("responses")).toEqual([]);
    expect(await rawRows("mutations")).toEqual([]);
    expect(await sessionMeta()).toMatchObject({ owner: "" });
  });

  it("restores nothing after a reload when every mirror write fails", async () => {
    await first.saveDraftFields("keep", { body: "private" });
    await first.cacheResponse("/threads/t?account=mirror-a", "private body");
    await first.queueMutation("/notes/n", "PUT", { text: "queued work" });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("quota exceeded");
    });
    await expect(first.clearOfflineData()).rejects.toThrow(/mirror could not be updated/);

    // The mirrors still describe the erased owner — every write failed —
    // but the authoritative wipe committed, so the reloaded page admits
    // none of the old records through those stale mirrors.
    expect(localStorage.getItem("lull-offline-ns")).toBe("inst/owner-a");
    expect(await reloaded.loadDrafts()).toEqual([]);
    expect(await reloaded.cachedResponse("/threads/t?account=mirror-a")).toBeUndefined();
    expect(await reloaded.replayMutations()).toEqual({ committed: 0, rejected: 0 });
    expect(await rawRows("drafts")).toEqual([]);
    expect(await rawRows("mutations")).toEqual([]);
  });

  it("keeps the explicit failure when both storage layers fail", async () => {
    await first.saveDraftFields("keep", { body: "private" });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("quota exceeded");
    });
    vi.spyOn(indexedDB, "open").mockImplementation(() => {
      throw new Error("storage blocked");
    });
    // The authoritative layer's failure is the one reported; the erase
    // stays pending for the next successful owner preparation.
    await expect(first.clearOfflineData()).rejects.toThrow("storage blocked");
    expect(first.offlineStorageSuspended()).toBe(true);
  });

  it("treats a mirror failure as a failed erase when IndexedDB does not exist", async () => {
    vi.stubGlobal("indexedDB", undefined);
    try {
      vi.spyOn(Storage.prototype, "setItem").mockImplementationOnce(() => {
        throw new Error("quota exceeded");
      });
      await expect(first.clearOfflineData()).rejects.toThrow(/could not be erased.*quota exceeded/);
      expect(first.offlineStorageSuspended()).toBe(true);
    } finally {
      vi.unstubAllGlobals();
    }
  });
});
