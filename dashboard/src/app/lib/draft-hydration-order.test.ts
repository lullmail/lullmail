// @vitest-environment jsdom
import "fake-indexeddb/auto";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { refreshAuth } from "./api";
import { clearOfflineData, loadDrafts, offlineOwner } from "./offline";
import { draftStack, hydrateDrafts, newDraft } from "./store";

// LUL-D03: the only production hydrateDrafts() call rode the mount-time
// layout resolution, which runs BEFORE refreshAuth prepares storage. On a
// v3→v4/v5 upgrade (or a legacy migration, or a recovered missing-owner
// mirror) the draft rows survive on disk but the meta store has no
// session until preparation commits, so the mount hydration's loadDrafts
// transaction matches no metadata and returns nothing — and nothing ever
// hydrated again. The user saw an empty draft ring until a manual reload.
// These tests mount, hydrate, and only then authenticate: parked drafts
// must appear without a reload, and a live ring must never be clobbered.

const DB_NAME = "lullmail-offline-v1";

function dropOfflineDB(): Promise<void> {
  return new Promise((resolve) => {
    const req = indexedDB.deleteDatabase(DB_NAME);
    req.onsuccess = req.onerror = req.onblocked = () => resolve();
  });
}

/** Seeds one store's rows inside an existing transaction. */
function putRows(tx: IDBTransaction, store: string, rows: unknown[]) {
  for (const row of rows) tx.objectStore(store).put(row);
}

function seedV3(draft: Record<string, unknown>): Promise<void> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(DB_NAME, 3);
    request.onupgradeneeded = () => {
      for (const name of ["responses", "mutations", "attachments", "drafts"]) {
        request.result.createObjectStore(name, { keyPath: name === "responses" ? "key" : "id" });
      }
    };
    request.onsuccess = () => {
      const db = request.result;
      const tx = db.transaction("drafts", "readwrite");
      putRows(tx, "drafts", [draft]);
      tx.oncomplete = () => { db.close(); resolve(); };
      tx.onabort = () => { db.close(); reject(tx.error); };
    };
    request.onerror = () => reject(request.error);
  });
}

function seedLegacyV1(attachmentRow: Record<string, unknown>): Promise<void> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(DB_NAME, 1);
    request.onupgradeneeded = () => {
      for (const name of ["responses", "mutations", "attachments"]) {
        request.result.createObjectStore(name, { keyPath: name === "responses" ? "key" : "id" });
      }
    };
    request.onsuccess = () => {
      const db = request.result;
      const tx = db.transaction("attachments", "readwrite");
      putRows(tx, "attachments", [attachmentRow]);
      tx.oncomplete = () => { db.close(); resolve(); };
      tx.onabort = () => { db.close(); reject(tx.error); };
    };
    request.onerror = () => reject(request.error);
  });
}

function authenticated(installation: string, user: string, email: string) {
  const status = () => new Response(JSON.stringify({
    configured: true, authenticated: true, email,
    installation_id: installation, user_id: user,
    bootstrap_available: false, passkey_supported: true,
  }));
  vi.stubGlobal("fetch", vi.fn().mockImplementation(async () => status()));
}

beforeEach(async () => {
  vi.unstubAllGlobals();
  await clearOfflineData().catch(() => undefined);
  localStorage.clear();
  await dropOfflineDB();
});

describe("migration admission hydrates parked drafts (LUL-D03)", () => {
  it("hydrates surviving v3 draft rows after authentication prepares the meta session", async () => {
    localStorage.setItem("lull-offline-v2", "1");
    localStorage.setItem("lull-offline-ns", "inst1/u1");
    localStorage.setItem("lull-offline-gen", "17");
    await seedV3({ id: "parked-v3", ns: "inst1/u1", seq: 1, body: "unsent v3 draft", to: "peer@example.test", subject: "parked", attachments: [{ filename: "park.txt", dataBase64: "cGFyaw==" }], sendKey: "parked-key-1" });

    // Mount order: the layout's hydration runs before refreshAuth, on a
    // meta store with no session — it cannot see the rows yet.
    await hydrateDrafts();
    expect(draftStack.value).toEqual([]);

    // Authentication prepares storage (adopting the v3 rows) — the parked
    // draft must now appear WITHOUT a reload.
    authenticated("inst1", "u1", "a@example.com");
    await refreshAuth();
    expect(offlineOwner()).toBe("inst1/u1");
    if (draftStack.value.length !== 1) {
      throw new Error(`draft ring = ${draftStack.value.length}, want the parked v3 draft without a reload`);
    }
    const [draft] = draftStack.value;
    expect(draft.id).toBe("parked-v3");
    expect(draft.body).toBe("unsent v3 draft");
    expect(draft.sendKey).toBe("parked-key-1");
    expect(draft.attachments).toEqual([{ filename: "park.txt", dataBase64: "cGFyaw==" }]);

    // A live ring is never clobbered by a later refresh: hydration's
    // no-clobber guard keeps the user's open editor exactly as it was.
    newDraft();
    const ring = draftStack.value.length;
    await refreshAuth();
    expect(draftStack.value.length).toBe(ring);
    expect(draftStack.value[0].id).toBe("parked-v3");
  });

  it("hydrates a genuine legacy migration once the owner is prepared", async () => {
    localStorage.setItem("es-offline-owner", "legacy@example.com");
    localStorage.setItem("es-drafts", JSON.stringify([{ id: "legacy" }]));
    localStorage.setItem("es-draft-legacy", JSON.stringify({ body: "unsent legacy" }));
    await seedLegacyV1({ id: "legacy", owner: "legacy@example.com", files: [{ filename: "a.txt", dataBase64: "YQ==" }] });

    await hydrateDrafts();
    expect(draftStack.value).toEqual([]);

    authenticated("inst2", "u2", "legacy@example.com");
    await refreshAuth();
    if (draftStack.value.length !== 1 || draftStack.value[0].body !== "unsent legacy") {
      throw new Error(`legacy migration did not hydrate: ${JSON.stringify(draftStack.value.map((d) => d.body))}`);
    }
    expect(draftStack.value[0].attachments).toEqual([{ filename: "a.txt", dataBase64: "YQ==" }]);
    // The migration's source copies are gone, the durable record remains.
    expect(await loadDrafts()).toEqual([expect.objectContaining({ id: "legacy", body: "unsent legacy" })]);
  });

  it("keeps hydration fenced when preparation fails: unreadable storage is not an empty success", async () => {
    localStorage.setItem("lull-offline-v2", "1");
    localStorage.setItem("lull-offline-ns", "inst3/u3");
    localStorage.setItem("lull-offline-gen", "5");
    await seedV3({ id: "parked-fail", ns: "inst3/u3", seq: 1, body: "must stay parked" });

    await hydrateDrafts();
    authenticated("inst3", "u3", "fail@example.com");
    // Preparation fails (the mirror write throws): the re-hydration must
    // not run against storage that was never admitted, and the parked
    // row must survive on disk untouched.
    const set = Storage.prototype.setItem;
    const spy = vi.spyOn(Storage.prototype, "setItem").mockImplementation(function (this: Storage, key, value) {
      if (key === "lull-offline-gen") throw new Error("mirror quota");
      return set.call(this, key, value);
    });
    await expect(refreshAuth()).resolves.toBeTruthy();
    spy.mockRestore();
    expect(draftStack.value).toEqual([]);
    expect((await loadDrafts()).length).toBe(0);
    const rows = await new Promise<unknown[]>((resolve, reject) => {
      const open = indexedDB.open(DB_NAME);
      open.onsuccess = () => {
        const db = open.result;
        const tx = db.transaction("drafts", "readonly");
        const all = tx.objectStore("drafts").getAll();
        all.onsuccess = () => { db.close(); resolve(all.result); };
        all.onerror = () => { db.close(); reject(all.error); };
      };
    });
    expect(rows).toEqual([expect.objectContaining({ id: "parked-fail", body: "must stay parked" })]);
  });
});
