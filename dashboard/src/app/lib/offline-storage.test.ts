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
