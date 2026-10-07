// @vitest-environment jsdom
import "fake-indexeddb/auto";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import * as first from "./offline";
import { api } from "./api";

// LUL-F06: the offline queue's primary keys are random UUIDs and its
// order was a stable sort on queuedAt alone, so entries admitted in the
// same millisecond replayed in random-ID order — two same-tick changes to
// one note could restore the older state last. Admission now allocates a
// durable sequence from a shared META counter inside the enqueue
// transaction itself.
//
// LUL-F05: a network-level API failure (connection reset, transient
// proxy failure) with navigator.onLine still true fired no online event
// and set no unreachable state, so the first queued item had no replay
// wakeup at all. A committed enqueue now dispatches one, and the driver
// coalesces it against an in-flight pass.

const identity = { installation_id: "inst", user_id: "owner-a", email: "a@example.test" };
const DB = "lullmail-offline-v1";

async function dropDatabase(): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    const request = indexedDB.deleteDatabase(DB);
    request.onsuccess = () => resolve();
    request.onerror = request.onblocked = () => reject(request.error ?? new Error("database blocked"));
  });
}

async function queueRows(): Promise<Array<{ id: string; sequence?: number; queuedAt?: number }>> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(DB);
    request.onerror = () => reject(request.error);
    request.onsuccess = () => {
      const db = request.result;
      const tx = db.transaction("mutations", "readonly");
      const rows = tx.objectStore("mutations").getAll();
      tx.oncomplete = () => { db.close(); resolve(rows.result); };
      tx.onabort = () => { db.close(); reject(tx.error); };
    };
  });
}

async function metaRow(): Promise<{ queueSequence?: number } | undefined> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(DB);
    request.onerror = () => reject(request.error);
    request.onsuccess = () => {
      const db = request.result;
      const tx = db.transaction("meta", "readonly");
      const row = tx.objectStore("meta").get("session");
      tx.oncomplete = () => { db.close(); resolve(row.result); };
      tx.onabort = () => { db.close(); reject(tx.error); };
    };
  });
}

beforeEach(async () => {
  await dropDatabase();
  localStorage.clear();
  await first.prepareOfflineOwner(identity);
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("durable queue admission order (LUL-F06)", () => {
  it("replays equal-millisecond entries in admission order, not random-ID order", async () => {
    vi.spyOn(Date, "now").mockReturnValue(1728200000000);
    const uuids = ["zzzz-first-queued", "aaaa-second-queued"];
    let next = 0;
    vi.stubGlobal("crypto", { ...crypto, randomUUID: () => uuids[next++] });
    // getAll() returns primary-key order: "aaaa…" before "zzzz…". The
    // older entry (queued first) has the larger key.
    await first.queueMutation("/notes/n1", "PUT", { text: "older write" }, "key-old");
    await first.queueMutation("/notes/n1", "PUT", { text: "newer write" }, "key-new");
    const sent: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (_input: unknown, init?: RequestInit) => {
      // Only the replayed mutations: the driver's post-commit refresh
      // also issues GETs.
      if (init?.method && init.method !== "GET") sent.push(String(init.body));
      return new Response(null, { status: 204 });
    }));
    expect(await first.replayMutations()).toEqual({ committed: 2, rejected: 0 });
    // The server saw the older write FIRST; the newer state is the one
    // that survives.
    expect(sent).toEqual([JSON.stringify({ text: "older write" }), JSON.stringify({ text: "newer write" })]);
    const rows = await queueRows();
    expect(rows).toEqual([]);
  });

  it("allocates the sequence from the shared counter across two enqueuing tabs", async () => {
    vi.resetModules();
    const second = await import("./offline");
    await second.prepareOfflineOwner(identity);
    await first.queueMutation("/notes/n1", "PUT", {}, "k1");
    await second.queueMutation("/notes/n2", "PUT", {}, "k2");
    const rows = await queueRows();
    expect(rows.map((row) => row.sequence).sort((a, b) => a! - b!)).toEqual([1, 2]);
    expect(await metaRow()).toMatchObject({ queueSequence: 2 });
  });

  it("preserves sequences and order through retry backoff", async () => {
    await first.queueMutation("/notes/n1", "PUT", { text: "older" }, "k1");
    await first.queueMutation("/notes/n2", "PUT", { text: "newer" }, "k2");
    const fetcher = vi.fn(async () => new Response("busy", { status: 503, headers: { "Retry-After": "60" } }));
    vi.stubGlobal("fetch", fetcher);
    const summary = await first.replayMutations();
    expect(summary.committed).toBe(0);
    expect(summary.retryAt).toBeGreaterThan(Date.now());
    const rows = (await queueRows()).sort((a, b) => (a.sequence ?? 0) - (b.sequence ?? 0));
    expect(rows.map((row) => row.sequence)).toEqual([1, 2]);
    // The head keeps its sequence through the backoff write.
    expect(rows[0].sequence).toBe(1);
  });

  it("keeps a same-owner re-preparation from resetting the counter", async () => {
    await first.queueMutation("/notes/n1", "PUT", {}, "k1");
    await first.prepareOfflineOwner(identity);
    await first.queueMutation("/notes/n2", "PUT", {}, "k2");
    const rows = await queueRows();
    expect(rows.find((row) => row.id !== undefined && row.sequence === 2)).toBeDefined();
    expect(await metaRow()).toMatchObject({ queueSequence: 2 });
  });

  it("sequences legacy rows in the v5 upgrade and lets fresh admissions follow them", async () => {
    await dropDatabase();
    // Build a VERSION 4 database with two unsequenced rows whose key
    // order is the reverse of their enqueue order.
    await new Promise<void>((resolve, reject) => {
      const open = indexedDB.open(DB, 4);
      open.onupgradeneeded = () => {
        const db = open.result;
        for (const [store, path] of [["responses", "key"], ["mutations", "id"], ["attachments", "id"], ["drafts", "id"], ["meta", "key"], ["draft-tombstones", "id"]] as const) {
          if (!db.objectStoreNames.contains(store)) db.createObjectStore(store, { keyPath: path });
        }
      };
      open.onerror = () => reject(open.error);
      open.onsuccess = () => {
        const db = open.result;
        const tx = db.transaction(["mutations", "meta"], "readwrite");
        const owner = "inst/owner-a";
        tx.objectStore("mutations").put({ id: "zzz-legacy-old", key: "l1", owner, path: "/notes/a", method: "PUT", body: {}, queuedAt: 1000 });
        tx.objectStore("mutations").put({ id: "aaa-legacy-new", key: "l2", owner, path: "/notes/b", method: "PUT", body: {}, queuedAt: 2000 });
        tx.objectStore("meta").put({ key: "session", owner, generation: 1, snapshots: 0 });
        tx.oncomplete = () => { db.close(); resolve(); };
        tx.onabort = () => { db.close(); reject(tx.error); };
      };
    });
    // Any storage operation opens (and upgrades) the database to v5. The
    // V2 marker keeps this a same-owner re-prepare rather than a legacy
    // migration (which would legitimately wipe).
    localStorage.setItem("lull-offline-v2", "1");
    await first.prepareOfflineOwner(identity);
    let rows = await queueRows();
    expect(rows.sort((a, b) => a.sequence! - b.sequence!).map((row) => row.id)).toEqual(["zzz-legacy-old", "aaa-legacy-new"]);
    expect(await metaRow()).toMatchObject({ queueSequence: 2 });
    // A fresh admission follows the legacy entries.
    await first.queueMutation("/notes/c", "PUT", {}, "k-fresh");
    rows = await queueRows();
    expect(rows.find((row) => row.id === "k-fresh" || row.sequence === 3)).toBeDefined();
    // Mixed-version fencing: a tab still holding VERSION 4 can no longer
    // open the database to enqueue unsequenced rows.
    const stale = indexedDB.open(DB, 4);
    const staleOutcome = await new Promise<string>((resolve) => {
      stale.onerror = () => resolve(stale.error?.name ?? "error");
      stale.onsuccess = () => { stale.result.close(); resolve("opened"); };
    });
    expect(staleOutcome).toBe("VersionError");
  });
});

describe("replay wakeups (LUL-F05)", () => {
  it("replays a first queue item that arrived while the browser stayed online", async () => {
    const cleanup = first.startOfflineData(() => true);
    try {
      let failuresLeft = 1;
      const applied: string[] = [];
      vi.stubGlobal("fetch", vi.fn(async (input: unknown, init?: RequestInit) => {
        if (failuresLeft > 0 && String(input).includes("/notes/wake")) {
          failuresLeft--;
          throw new TypeError("connection reset");
        }
        if (init?.method && init.method !== "GET") applied.push(String(input));
        return new Response(null, { status: 204 });
      }));
      // A queueable mutation fails at the network level: no online event,
      // no unreachable state, navigator.onLine still true.
      await expect(api("/notes/wake", { method: "PUT", body: { text: "v" } })).rejects.toThrow();
      expect(applied).toEqual([]);
      // The committed enqueue's wake drives the replay without any
      // connectivity or auth event.
      const deadline = Date.now() + 5000;
      while (applied.length === 0 && Date.now() < deadline) {
        await new Promise((resolve) => setTimeout(resolve, 25));
      }
      expect(applied).toEqual(["/api/notes/wake"]);
      expect(await queueRows()).toEqual([]);
    } finally {
      cleanup();
    }
  });

  it("requests one further pass when the enqueue lands mid-pass", async () => {
    let releaseFirst!: (response: Response) => void;
    const firstFetch = new Promise<Response>((resolve) => { releaseFirst = resolve; });
    let firstSent = false;
    const applied: string[] = [];
    const cleanup = first.startOfflineData(() => true);
    try {
      vi.stubGlobal("fetch", vi.fn(async (input: unknown, init?: RequestInit) => {
        if (!firstSent) {
          firstSent = true;
          if (init?.method && init.method !== "GET") applied.push(String(input));
          return firstFetch;
        }
        if (init?.method && init.method !== "GET") applied.push(String(input));
        return new Response(null, { status: 204 });
      }));
      // The driver's mount pass hangs on its own replay of a pre-seeded
      // item, and the new enqueue arrives after that pass read the queue.
      await first.queueMutation("/notes/head", "PUT", { text: "head" }, "k-head");
      await new Promise((resolve) => setTimeout(resolve, 50));
      await first.queueMutation("/notes/tail", "PUT", { text: "tail" }, "k-tail");
      await new Promise((resolve) => setTimeout(resolve, 50));
      releaseFirst(new Response(null, { status: 204 }));
      const deadline = Date.now() + 5000;
      while (applied.length < 2 && Date.now() < deadline) {
        await new Promise((resolve) => setTimeout(resolve, 25));
      }
      expect(applied.sort()).toEqual(["/api/notes/head", "/api/notes/tail"]);
      expect(await queueRows()).toEqual([]);
    } finally {
      cleanup();
    }
  });

  it("does not let a wake bypass the head's backoff deadline", async () => {
    const head = { id: "q1", key: "q1", owner: "inst/owner-a", path: "/notes/n", method: "PUT", body: {}, queuedAt: 1000, attempts: 2, nextAttemptAt: Date.now() + 60_000, sequence: 1 };
    const tail = { id: "q2", key: "q2", owner: "inst/owner-a", path: "/notes/m", method: "PUT", body: {}, queuedAt: 2000, sequence: 2 };
    const plan = first.replayPlan([tail, head], "inst/owner-a", Date.now());
    expect(plan.due).toEqual([]);
    expect(plan.retryAt).toBe(head.nextAttemptAt);
  });

  it("keeps the wake pending when another tab holds the replay lock", async () => {
    let cleanup: (() => void) | undefined;
    try {
      // Another tab holds the cross-tab lock for this pass.
      vi.stubGlobal("navigator", { onLine: true, locks: { request: async (_n: string, _o: unknown, run: (lock: unknown) => Promise<unknown>) => run(null) } });
      cleanup = first.startOfflineData(() => true);
      await first.queueMutation("/notes/held", "PUT", {}, "k-held");
      await new Promise((resolve) => setTimeout(resolve, 100));
      // The queued row survives: this tab's wake did not run a pass and
      // did not consume the item.
      expect((await queueRows()).map((row) => row.id).length).toBe(1);
      const status = await first.replayPassStatus();
      expect(status.ran).toBe(false);
    } finally {
      cleanup?.();
    }
  });
});
