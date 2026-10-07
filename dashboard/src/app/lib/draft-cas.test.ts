// @vitest-environment jsdom
import "fake-indexeddb/auto";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { clearOfflineData, loadDrafts, OfflineStorageError, offlineGeneration, offlineStorageSuspended, prepareOfflineOwner, saveDraftFields, suspendOfflineStorage } from "./offline";
import { draftStack, openCompose, prepareDraftSend, updateDraft } from "./store";

// LUL-F01: a same-tab edit between a draft put and its transaction's
// completion used to make tx.oncomplete skip the local revision
// bookkeeping (its currency check included the snapshot identity). The
// write committed, but this tab forgot it owned the new revision — the
// next autosave and prepareDraftSend then failed the CAS comparison with
// a false "changed in another tab", and a reload restored the older body.

const identity = { installation_id: "inst", user_id: "owner-a", email: "a@example.test" };
const DB = "lullmail-offline-v1";

beforeEach(async () => {
  await new Promise<void>((resolve, reject) => {
    const request = indexedDB.deleteDatabase(DB);
    request.onsuccess = () => resolve();
    request.onerror = request.onblocked = () => reject(request.error ?? new Error("database blocked"));
  });
  localStorage.clear();
  await prepareOfflineOwner(identity);
});

afterEach(async () => {
  vi.restoreAllMocks();
  await clearOfflineData().catch(() => undefined);
});

/** Edits the live draft between the put and the transaction's completion:
 *  the exact same-tab interleaving LUL-F01 pins down. Fires once. */
function editBetweenPutAndComplete(id: string, edit: () => void): void {
  let armed = true;
  const realPut = IDBObjectStore.prototype.put;
  vi.spyOn(IDBObjectStore.prototype, "put").mockImplementation(function (this: IDBObjectStore, value: unknown) {
    const request = realPut.call(this, value);
    if (armed && (value as { id?: string })?.id === id) {
      armed = false;
      edit();
    }
    return request;
  });
}

describe("draft revision CAS bookkeeping", () => {
  it("advances local bookkeeping when the draft is edited between put and commit", async () => {
    let live = true;
    editBetweenPutAndComplete("cas-1", () => { live = false; });
    const first = await saveDraftFields("cas-1", { body: "first" }, offlineGeneration(), () => live);
    // The payload was superseded by the newer edit, so this save does not
    // claim success — but its committed revision is now KNOWN locally.
    expect(first).toBe(false);

    // The newer content saves instead of failing the CAS with a false
    // "changed in another tab".
    await expect(saveDraftFields("cas-1", { body: "second" })).resolves.toBe(true);
    const rows = await loadDrafts();
    expect(rows[0].body).toBe("second");
    expect(rows[0].revision).toBe(2);
  });

  it("lets a send prepare after the same interleaving instead of refusing it", async () => {
    openCompose({ to: "x@example.test", body: "first" });
    const id = draftStack.value[0].id;
    let live = true;
    editBetweenPutAndComplete(id, () => { live = false; });
    await saveDraftFields(id, { body: "first" }, offlineGeneration(), () => live);
    // The user keeps typing: the newer edit is the draft's content now.
    updateDraft({ body: "second" });
    const key = await prepareDraftSend(id);
    expect(key).toBeTruthy();
    expect((await loadDrafts())[0].body).toBe("second");
  });

  it("rebuilds revision authority from disk after a session transition", async () => {
    let live = true;
    editBetweenPutAndComplete("cas-2", () => {
      live = false;
      // A concurrent wipe suspends the session mid-transaction: the
      // committed revision must not be published into it.
      suspendOfflineStorage();
    });
    const stale = await saveDraftFields("cas-2", { body: "first" }, offlineGeneration(), () => live);
    expect(stale).toBe(false);
    expect(offlineStorageSuspended()).toBe(true);
    // The next session admits the same namespace; revision authority
    // rebuilds from what is on disk, never from the stale session's
    // bookkeeping.
    await prepareOfflineOwner(identity);
    const rows = await loadDrafts();
    expect(rows.map((row) => row.body)).toEqual(["first"]);
    await expect(saveDraftFields("cas-2", { body: "second" })).resolves.toBe(true);
  });

  it("still rejects a real cross-tab edit", async () => {
    await saveDraftFields("cas-3", { body: "mine" });
    // Another tab writes a newer revision directly.
    await new Promise<void>((resolve, reject) => {
      const open = indexedDB.open(DB);
      open.onerror = () => reject(open.error);
      open.onsuccess = () => {
        const db = open.result;
        const tx = db.transaction("drafts", "readwrite");
        const request = tx.objectStore("drafts").get("cas-3");
        request.onsuccess = () => {
          const row = request.result as { id: string; revision?: number };
          tx.objectStore("drafts").put({ ...row, revision: (row.revision ?? 0) + 1 });
        };
        tx.oncomplete = () => { db.close(); resolve(); };
        tx.onabort = () => { db.close(); reject(tx.error); };
      };
    });
    await expect(saveDraftFields("cas-3", { body: "stale tab" })).rejects.toThrow(OfflineStorageError);
    await expect(saveDraftFields("cas-3", { body: "stale tab" })).rejects.toThrow(/another tab/);
  });
});
