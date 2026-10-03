// @vitest-environment jsdom
import "fake-indexeddb/auto";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import * as first from "./offline";
import { authed, authStatus, refreshAuth, StaleOwnerError } from "./api";
import { draftStack, openCompose, prepareDraftSend, resetPrivateState } from "./store";

const identity = { installation_id: "inst", user_id: "a", email: "a@example.test" };
const other = { installation_id: "inst", user_id: "b", email: "b@example.test" };
let second: typeof first;
const DB = "lullmail-offline-v1";

async function raw<T>(stores: string[], run: (tx: IDBTransaction) => IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(DB);
    request.onerror = () => reject(request.error);
    request.onsuccess = () => {
      const db = request.result;
      const tx = db.transaction(stores, "readwrite");
      const result = run(tx);
      tx.oncomplete = () => { db.close(); resolve(result.result); };
      tx.onabort = () => { db.close(); reject(tx.error); };
    };
  });
}

beforeAll(async () => {
  // Each module owns its own admitted identity, suspension, revision map,
  // and snapshot counter, while both use the same origin's durable stores.
  vi.resetModules();
  second = await import("./offline");
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
  await second.prepareOfflineOwner(identity);
});
afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  resetPrivateState();
});

describe("independent tab storage contexts", () => {
  it("prevents a stale tab from adopting another owner's current markers for its old draft", async () => {
    await first.saveDraftFields("old", { body: "private" });
    await second.prepareOfflineOwner(other);
    expect(await first.saveDraftFields("old", { body: "late autosave" })).toBe(false);
    await expect(first.queueMutation("/notes/n", "PUT", { text: "old work" })).rejects.toThrow();
    expect(await second.loadDrafts()).toEqual([]);
  });

  it("resets the old live draft before admitting a new owner even before its storage event", async () => {
    openCompose({ to: "x@example.test", body: "owner A private draft" });
    await second.prepareOfflineOwner(other);
    await second.saveDraftFields("b-draft", { body: "owner B draft" });
    await first.prepareOfflineOwner(other);
    expect(draftStack.value).toEqual([]);
    expect((await first.loadDrafts()).map((row) => row.id)).toEqual(["b-draft"]);
  });

  it("checks metadata even when cross-tab invalidation events and mirrors have not arrived", async () => {
    await first.saveDraftFields("old", { body: "private" });
    await first.cacheResponse("/buckets/imbox", "private snapshot");
    await first.queueMutation("/notes/n", "PUT", {});
    const meta = await raw<any>(["meta"], (tx) => tx.objectStore("meta").get("session"));
    await raw(["meta"], (tx) => tx.objectStore("meta").put({ ...meta, owner: "inst/new", generation: meta.generation + 1 }));
    expect(await first.saveDraftFields("late", { body: "stale" })).toBe(false);
    expect(await first.loadDrafts()).toEqual([]);
    expect(await first.cachedResponse("/buckets/imbox")).toBeUndefined();
    await expect(first.queueMutation("/notes/n", "PUT", {})).rejects.toThrow();
    const fetcher = vi.fn();
    vi.stubGlobal("fetch", fetcher);
    expect(await first.replayMutations()).toEqual({ committed: 0, rejected: 0 });
    expect(fetcher).not.toHaveBeenCalled();
  });

  it("persists draft retirement across independently loaded tabs", async () => {
    await first.saveDraftFields("shared", { body: "private" });
    await second.loadDrafts();
    await first.deleteDraft("shared");
    await expect(second.saveDraftFields("shared", { body: "late" })).rejects.toThrow(/sent or discarded/);
    expect(await first.loadDrafts()).toEqual([]);
  });

  it("rejects stale cross-tab revisions instead of overwriting newer draft fields", async () => {
    await first.saveDraftFields("shared", { body: "original" });
    await second.loadDrafts();
    await first.saveDraftFields("shared", { body: "newer first-tab edit" });
    await expect(second.saveDraftFields("shared", { body: "stale second-tab edit" })).rejects.toThrow(/changed in another tab/);
    expect((await first.loadDrafts())[0].body).toBe("newer first-tab edit");
    await second.loadDrafts();
    await expect(second.saveDraftFields("shared", { body: "still stale live editor after background read" })).rejects.toThrow(/changed in another tab/);
  });

  it("does not acknowledge an unseen revision via a stale partial no-op", async () => {
    await first.saveDraftFields("shared", { body: "old body", attachments: [] });
    await second.loadDrafts();
    await first.saveDraftFields("shared", { body: "new body" });
    await expect(second.saveDraftFields("shared", { attachments: [] })).rejects.toThrow(/changed in another tab/);
    await expect(second.saveDraftFields("shared", { body: "old body" })).rejects.toThrow(/changed in another tab/);
    expect((await first.loadDrafts())[0].body).toBe("new body");
  });

  it("does not let a stale discard erase a newer edit in another tab", async () => {
    await first.saveDraftFields("shared", { body: "old body" });
    await second.loadDrafts();
    await first.saveDraftFields("shared", { body: "new body" });
    await expect(second.deleteDraft("shared")).rejects.toThrow();
    expect((await first.loadDrafts())[0].body).toBe("new body");
  });

  it("does not create a revision conflict from another tab's unchanged hydration flush", async () => {
    await first.saveDraftFields("shared", { body: "original" });
    const [restored] = await second.loadDrafts();
    expect(await second.saveDraftFields("shared", { body: restored.body, revision: restored.revision })).toBe(true);
    expect(await first.saveDraftFields("shared", { body: "first-tab edit" })).toBe(true);
    expect((await second.loadDrafts())[0].body).toBe("first-tab edit");
  });

  it("rolls back both namespace and payload changes when owner preparation aborts", async () => {
    await first.saveDraftFields("kept", { body: "private" });
    const before = await raw<any>(["meta"], (tx) => tx.objectStore("meta").get("session"));
    const put = IDBObjectStore.prototype.put;
    const spy = vi.spyOn(IDBObjectStore.prototype, "put").mockImplementation(function (this: IDBObjectStore, value, key) {
      const request = key === undefined ? put.call(this, value) : put.call(this, value, key);
      if (this.name === "meta" && value.owner === "inst/b") this.transaction.abort();
      return request;
    });
    await expect(first.prepareOfflineOwner(other)).rejects.toThrow();
    spy.mockRestore();
    expect(await raw(["meta"], (tx) => tx.objectStore("meta").get("session"))).toEqual(before);
    expect(await raw(["drafts"], (tx) => tx.objectStore("drafts").count())).toBe(1);
    await first.prepareOfflineOwner(identity);
    expect((await first.loadDrafts())[0].body).toBe("private");
  });

  it("does not let a stale tab's mailbox purge disable the newer owner's cache", async () => {
    await second.prepareOfflineOwner(other);
    await second.cacheResponse("/buckets/imbox?account=mirror-b", "owner B mail");
    const snapshots = second.snapshotGeneration();
    await first.purgeAccountSnapshots("product-a", "mirror-a");
    expect(second.snapshotGeneration()).toBe(snapshots);
    expect(await second.cachedResponse("/buckets/imbox?account=mirror-b")).toBe("owner B mail");
  });

  it("rejects purge hints when newer owner metadata committed before its mirror", async () => {
    const metadata = await raw<any>(["meta"], (tx) => tx.objectStore("meta").get("session"));
    const newer = { ...metadata, owner: "inst/b", generation: metadata.generation + 1 };
    await raw(["meta"], (tx) => tx.objectStore("meta").put(newer));
    await raw(["responses"], (tx) => tx.objectStore("responses").put({ key: "inst/b\nmirror-b\n/buckets/imbox?account=mirror-b", owner: "inst/b", account: "mirror-b", value: "owner B mail" }));
    await raw(["drafts"], (tx) => tx.objectStore("drafts").put({ id: "b", ns: "inst/b", body: "owner B draft" }));
    const snapshotHint = localStorage.getItem("lull-offline-snapshots");
    await first.purgeAccountSnapshots("product-a", "mirror-a");
    expect(localStorage.getItem("lull-offline-snapshots")).toBe(snapshotHint);
    await second.prepareOfflineOwner(other, await second.captureOfflineContext());
    expect(await second.cachedResponse("/buckets/imbox?account=mirror-b")).toBe("owner B mail");
    expect((await second.loadDrafts()).map((row) => row.id)).toEqual(["b"]);
  });

  it("does not carry a failed old-owner wipe into a newer owner's preparation", async () => {
    vi.spyOn(indexedDB, "open").mockImplementationOnce(() => { throw new Error("temporarily blocked"); });
    await expect(first.clearOfflineData()).rejects.toThrow("temporarily blocked");
    await second.prepareOfflineOwner(other, await second.captureOfflineContext());
    await second.saveDraftFields("b", { body: "owner B draft" });
    await first.prepareOfflineOwner(other, await first.captureOfflineContext());
    expect((await first.loadDrafts()).map((row) => row.id)).toEqual(["b"]);
  });

  it("recovers a failed logout from another tab without reviving the old session's drafts", async () => {
    await first.saveDraftFields("old-a", { body: "must not survive logout" });
    vi.spyOn(indexedDB, "open").mockImplementationOnce(() => { throw new Error("temporarily blocked"); });
    await expect(first.clearOfflineData()).rejects.toThrow("temporarily blocked");
    await second.prepareOfflineOwner(identity, await second.captureOfflineContext());
    expect(await second.loadDrafts()).toEqual([]);
    await second.saveDraftFields("new-a", { body: "new session draft" });
    await first.prepareOfflineOwner(identity, await first.captureOfflineContext());
    expect((await first.loadDrafts()).map((row) => row.id)).toEqual(["new-a"]);
  });

  it("keeps a failed wipe retry from changing a newer owner's shared markers", async () => {
    vi.spyOn(indexedDB, "open").mockImplementationOnce(() => { throw new Error("temporarily blocked"); });
    await expect(first.clearOfflineData()).rejects.toThrow("temporarily blocked");
    await second.prepareOfflineOwner(other, await second.captureOfflineContext());
    await second.saveDraftFields("b", { body: "owner B draft" });
    const generation = second.offlineGeneration(), snapshots = second.snapshotGeneration();
    await first.clearOfflineData();
    expect(second.offlineGeneration()).toBe(generation);
    expect(second.snapshotGeneration()).toBe(snapshots);
    expect((await second.loadDrafts()).map((row) => row.id)).toEqual(["b"]);
  });

  it("does not let a stale tab's logout erase a newer owner's data", async () => {
    await second.prepareOfflineOwner(other);
    await second.saveDraftFields("b", { body: "owner B draft" });
    await first.clearOfflineData();
    expect(second.offlineOwner()).toBe("inst/b");
    expect((await second.loadDrafts())[0].body).toBe("owner B draft");
  });

  it("does not let a wipe delayed at database open erase a newer committed owner", async () => {
    const open = indexedDB.open.bind(indexedDB);
    const releases: Array<() => void> = [];
    const ready: Array<() => void> = [];
    const waits = [0, 1].map((i) => new Promise<void>((resolve) => { ready[i] = resolve; }));
    let calls = 0;
    const spy = vi.spyOn(indexedDB, "open").mockImplementation((...args) => {
      const request = open(...args);
      const at = calls++;
      if (at < 2) Object.defineProperty(request, "onsuccess", { set(callback) {
        request.addEventListener("success", (event) => {
          releases[at] = () => callback.call(request, event);
          ready[at]();
        });
      } });
      return request;
    });
    // Both operations were admitted under A; B's already-started prepare
    // commits first while A's wipe is still waiting to open its database.
    const prepareB = second.prepareOfflineOwner(other);
    await waits[0];
    const wipeA = first.clearOfflineData();
    await waits[1];
    releases[0]();
    await prepareB;
    await second.saveDraftFields("b", { body: "owner B draft" });
    releases[1]();
    await wipeA;
    spy.mockRestore();
    expect(second.offlineOwner()).toBe("inst/b");
    expect((await second.loadDrafts())[0].id).toBe("b");
  });

  it("does not roll shared owner markers backward when an old completion callback resumes late", async () => {
    const transaction = IDBDatabase.prototype.transaction;
    let release!: () => void;
    let ready!: () => void;
    const waiting = new Promise<void>((resolve) => { ready = resolve; });
    const spy = vi.spyOn(IDBDatabase.prototype, "transaction").mockImplementationOnce(function (this: IDBDatabase, ...args) {
      const tx = transaction.apply(this, args);
      Object.defineProperty(tx, "oncomplete", { set(callback) {
        tx.addEventListener("complete", (event) => {
          release = () => callback.call(tx, event);
          ready();
        });
      } });
      return tx;
    });
    const preparing = first.prepareOfflineOwner(identity);
    const rejected = expect(preparing).rejects.toBeInstanceOf(first.OfflineOwnerChangedError);
    await waiting;
    await second.prepareOfflineOwner(other);
    await second.saveDraftFields("b", { body: "owner B draft" });
    const generation = second.offlineGeneration();
    release();
    await rejected;
    spy.mockRestore();
    expect(second.offlineGeneration()).toBe(generation);
    expect(second.offlineOwner()).toBe("inst/b");
    expect((await second.loadDrafts())[0].id).toBe("b");
  });

  it("does not let an older prepare re-enable storage after explicit suspension", async () => {
    const transaction = IDBDatabase.prototype.transaction;
    let release!: () => void;
    let ready!: () => void;
    const waiting = new Promise<void>((resolve) => { ready = resolve; });
    vi.spyOn(IDBDatabase.prototype, "transaction").mockImplementationOnce(function (this: IDBDatabase, ...args) {
      const tx = transaction.apply(this, args);
      Object.defineProperty(tx, "oncomplete", { set(callback) {
        tx.addEventListener("complete", (event) => {
          release = () => callback.call(tx, event);
          ready();
        });
      } });
      return tx;
    });
    const preparing = first.prepareOfflineOwner(identity);
    const rejected = expect(preparing).rejects.toBeInstanceOf(first.OfflineOwnerChangedError);
    await waiting;
    first.suspendOfflineStorage();
    release();
    await rejected;
    expect(first.offlineStorageSuspended()).toBe(true);
  });

  it("does not roll the snapshot epoch backward after a delayed same-owner preparation", async () => {
    const transaction = IDBDatabase.prototype.transaction;
    let release!: () => void;
    let ready!: () => void;
    const waiting = new Promise<void>((resolve) => { ready = resolve; });
    vi.spyOn(IDBDatabase.prototype, "transaction").mockImplementationOnce(function (this: IDBDatabase, ...args) {
      const tx = transaction.apply(this, args);
      Object.defineProperty(tx, "oncomplete", { set(callback) {
        tx.addEventListener("complete", (event) => {
          release = () => callback.call(tx, event);
          ready();
        });
      } });
      return tx;
    });
    const staleSnapshot = first.snapshotGeneration();
    const preparing = first.prepareOfflineOwner(identity);
    await waiting;
    await second.purgeAccountSnapshots("product-a", "mirror-a");
    const currentSnapshot = second.snapshotGeneration();
    release();
    await preparing;
    expect(first.snapshotGeneration()).toBe(currentSnapshot);
    expect(first.snapshotGeneration()).toBeGreaterThan(staleSnapshot);
    await first.cacheResponse("/threads/t?account=mirror-a", "late", first.offlineGeneration(), staleSnapshot);
    expect(await second.cachedResponse("/threads/t?account=mirror-a")).toBeUndefined();
  });

  it("recovers an account purge that failed before opening IDB in another tab", async () => {
    await first.cacheResponse("/buckets/imbox?account=mirror-a", "removed private mail");
    await first.saveDraftFields("kept", { body: "unsent" });
    await first.queueMutation("/notes/n", "PUT", { text: "pending" });
    vi.spyOn(indexedDB, "open").mockImplementationOnce(() => { throw new Error("temporarily blocked"); });
    await expect(first.purgeAccountSnapshots("product-a", "mirror-a")).rejects.toThrow("temporarily blocked");
    expect(await second.cachedResponse("/buckets/imbox?account=mirror-a")).toBeUndefined();
    // Another account's successful purge must not acknowledge the failed
    // request or re-enable its still-present private rows.
    await second.purgeAccountSnapshots("product-b", "mirror-b");
    expect(await second.cachedResponse("/buckets/imbox?account=mirror-a")).toBeUndefined();
    await second.prepareOfflineOwner(identity, await second.captureOfflineContext());
    expect(await raw(["responses"], (tx) => tx.objectStore("responses").count())).toBe(0);
    expect((await second.loadDrafts()).map((row) => row.id)).toEqual(["kept"]);
    expect(await raw(["mutations"], (tx) => tx.objectStore("mutations").count())).toBe(1);
  });

  it("recovers a failed account purge with cache-only erasure before readmission", async () => {
    await first.cacheResponse("/buckets/imbox?account=mirror-a", "removed private mail");
    await first.cacheResponse("/buckets/imbox?account=mirror-b", "other snapshot");
    await first.saveDraftFields("kept", { body: "unsent" });
    await first.queueMutation("/notes/n", "PUT", { text: "pending" });
    const generation = second.offlineGeneration();
    const remove = IDBObjectStore.prototype.delete;
    const spy = vi.spyOn(IDBObjectStore.prototype, "delete").mockImplementation(function (this: IDBObjectStore, key) {
      const request = remove.call(this, key);
      if (this.name === "responses") this.transaction.abort();
      return request;
    });
    await expect(first.purgeAccountSnapshots("product-a", "mirror-a")).rejects.toThrow();
    spy.mockRestore();
    // The abort kept the rows physically on disk, but other tabs must
    // stop serving them and a fresh preparation must finish the cleanup.
    expect(await raw(["responses"], (tx) => tx.objectStore("responses").count())).toBe(2);
    expect(await second.cachedResponse("/buckets/imbox?account=mirror-a")).toBeUndefined();
    await second.prepareOfflineOwner(identity, await second.captureOfflineContext());
    expect(second.offlineGeneration()).toBe(generation);
    expect(await raw(["responses"], (tx) => tx.objectStore("responses").count())).toBe(0);
    expect((await second.loadDrafts()).map((row) => row.id)).toEqual(["kept"]);
    expect(await raw(["mutations"], (tx) => tx.objectStore("mutations").count())).toBe(1);
  });

  it("shares account-purge epochs without invalidating unrelated drafts", async () => {
    await first.saveDraftFields("kept", { body: "unsent" });
    await first.cacheResponse("/threads/t?account=mirror-a", "private");
    await first.cacheResponse("/threads/t?account=mirror-b", "other mailbox");
    const gen = first.offlineGeneration(), snapshots = first.snapshotGeneration();
    await second.purgeAccountSnapshots("product-a", "mirror-a");
    await first.cacheResponse("/threads/t?account=mirror-a", "late", gen, snapshots);
    expect(await first.cachedResponse("/threads/t?account=mirror-a")).toBeUndefined();
    expect(await first.cachedResponse("/threads/t?account=mirror-b")).toBe("other mailbox");
    expect(await first.saveDraftFields("kept", { body: "still live" }, gen)).toBe(true);
  });

  it("never starts send when another tab retired the draft or metadata refused its save", async () => {
    openCompose({ to: "x@example.test", body: "private" });
    const id = draftStack.value[0].id;
    expect(await prepareDraftSend(id)).toBeTruthy();
    await second.loadDrafts();
    await second.deleteDraft(id);
    expect(await prepareDraftSend(id)).toBeNull();
    const meta = await raw<any>(["meta"], (tx) => tx.objectStore("meta").get("session"));
    await raw(["meta"], (tx) => tx.objectStore("meta").put({ ...meta, generation: meta.generation + 1 }));
    expect(await prepareDraftSend(id)).toBeNull();
  });
});

describe("authentication publication", () => {
  it("recovers a lost mirror for a different freshly authenticated owner without manual database clearing", async () => {
    await second.prepareOfflineOwner(other);
    await second.saveDraftFields("b-private", { body: "owner B draft" });
    localStorage.clear(); // metadata remains authoritative on disk
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ ...identity, configured: true, authenticated: true }))));
    expect((await refreshAuth()).authenticated).toBe(true);
    expect(first.offlineOwner()).toBe("inst/a");
    expect(await first.loadDrafts()).toEqual([]);
    expect(await first.saveDraftFields("a-new", { body: "owner A draft" })).toBe(true);
  });

  it("rejects a delayed same-owner auth response after the captured disk generation changes with no mirror", async () => {
    localStorage.clear();
    let finish!: (response: Response) => void;
    const fetcher = vi.fn(() => new Promise<Response>((resolve) => { finish = resolve; }));
    vi.stubGlobal("fetch", fetcher);
    const pending = refreshAuth();
    const rejected = expect(pending).rejects.toBeInstanceOf(StaleOwnerError);
    await vi.waitFor(() => expect(fetcher).toHaveBeenCalledOnce());
    const meta = await raw<any>(["meta"], (tx) => tx.objectStore("meta").get("session"));
    await raw(["meta"], (tx) => tx.objectStore("meta").put({ ...meta, generation: meta.generation + 1 }));
    finish(new Response(JSON.stringify({ ...identity, configured: true, authenticated: true })));
    await rejected;
    expect(first.offlineOwner()).toBe("");
  });

  it("does not let a delayed old auth response switch back over a newer owner", async () => {
    let finish!: (response: Response) => void;
    vi.stubGlobal("fetch", vi.fn(() => new Promise<Response>((resolve) => { finish = resolve; })));
    const pending = refreshAuth();
    const rejected = expect(pending).rejects.toBeInstanceOf(StaleOwnerError);
    await second.prepareOfflineOwner(other);
    finish(new Response(JSON.stringify({ ...identity, configured: true, authenticated: true })));
    await rejected;
    expect(second.offlineOwner()).toBe("inst/b");
  });

  it("clears live private state and reauthenticates after another tab's storage notification", async () => {
    openCompose({ to: "x@example.test", body: "old private draft" });
    authed.value = true;
    authStatus.value = { ...identity, configured: true, authenticated: true, bootstrap_available: false, passkey_supported: true };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ configured: true, authenticated: false, email: "" }))));
    await second.clearOfflineData();
    window.dispatchEvent(new StorageEvent("storage", { key: "lull-offline-gen" }));
    expect(draftStack.value).toEqual([]);
    expect(authed.value).toBe(false);
    await vi.waitFor(() => expect(authStatus.value?.authenticated).toBe(false));
  });
});
