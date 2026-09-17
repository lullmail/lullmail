// IndexedDB is the offline mailbox layer. The service worker deliberately
// caches only the shell; API data is account-namespaced here so owner changes
// and account deletion can provably evict it.

const DB = "lullmail-offline-v1";
const VERSION = 2;
const CACHE = "responses";
const QUEUE = "mutations";
const ATTACHMENTS = "attachments";
const OWNER = "es-offline-owner";

interface Cached { key: string; owner: string; savedAt: number; value: unknown }
interface Queued {
  id: string;
  owner: string;
  path: string;
  method: string;
  body?: unknown;
  queuedAt: number;
  /** Set when replay concluded this action can never succeed; kept for
   * visibility instead of being silently discarded (audit WEB-03). */
  failed?: string;
}
interface DraftAttachmentRow { id: string; owner: string; files: unknown[] }

function openDB(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(DB, VERSION);
    request.onupgradeneeded = () => {
      const db = request.result;
      if (!db.objectStoreNames.contains(CACHE)) db.createObjectStore(CACHE, { keyPath: "key" });
      if (!db.objectStoreNames.contains(QUEUE)) db.createObjectStore(QUEUE, { keyPath: "id" });
      if (!db.objectStoreNames.contains(ATTACHMENTS)) db.createObjectStore(ATTACHMENTS, { keyPath: "id" });
    };
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
}

/** One transaction, resolved only when it COMMITS. Resolving on the
 * request's onsuccess reported saves that a later abort (quota, another
 * operation's failure) silently rolled back (audit WEB-02). */
function transaction<T>(store: string, mode: IDBTransactionMode, run: (s: IDBObjectStore) => IDBRequest<T>): Promise<T> {
  return openDB().then((db) => new Promise<T>((resolve, reject) => {
    const tx = db.transaction(store, mode);
    let value!: T;
    let requestError: DOMException | null = null;
    tx.oncomplete = () => { db.close(); resolve(value); };
    tx.onabort = () => {
      db.close();
      reject(tx.error ?? requestError ?? new Error("Storage transaction aborted"));
    };
    tx.onerror = () => { requestError = tx.error; };
    try {
      const request = run(tx.objectStore(store));
      request.onsuccess = () => { value = request.result; };
      request.onerror = () => { requestError = request.error; };
    } catch (error) {
      try { tx.abort(); } catch { /* already inactive */ }
      db.close();
      reject(error);
    }
  }));
}

export function offlineOwner(): string { return localStorage.getItem(OWNER) || ""; }

export async function prepareOfflineOwner(owner: string): Promise<void> {
  if (!owner) return;
  const previous = offlineOwner();
  if (previous && previous !== owner) await clearOfflineData();
  localStorage.setItem(OWNER, owner);
}

export async function cacheResponse(path: string, value: unknown): Promise<void> {
  const owner = offlineOwner(); if (!owner || typeof indexedDB === "undefined") return;
  await transaction(CACHE, "readwrite", (store) => store.put({ key: owner + "\n" + path, owner, savedAt: Date.now(), value } as Cached));
}

export async function cachedResponse<T>(path: string): Promise<T | undefined> {
  const owner = offlineOwner(); if (!owner || typeof indexedDB === "undefined") return undefined;
  const item = await transaction<Cached | undefined>(CACHE, "readonly", (store) => store.get(owner + "\n" + path));
  return item?.value as T | undefined;
}

const QUEUEABLE = [
  /^\/messages\/[^/]+\/action$/, /^\/screener\/(decide|undecide)$/,
  /^\/board\/(pin|unpin)$/, /^\/board\/cards\/[^/]+\/done$/,
  /^\/notes\/[^/]+$/,
];

export function canQueue(path: string, method: string): boolean {
  return method !== "GET" && QUEUEABLE.some((pattern) => pattern.test(path.split("?")[0]));
}

export async function queueMutation(path: string, method: string, body?: unknown): Promise<void> {
  const owner = offlineOwner(); if (!owner) throw new Error("Offline owner is not initialised");
  const id = typeof crypto.randomUUID === "function" ? crypto.randomUUID() : Date.now() + "-" + Math.random();
  await transaction(QUEUE, "readwrite", (store) => store.put({ id, owner, path, method, body, queuedAt: Date.now() } as Queued));
}

export type ReplayDecision = "committed" | "reauth" | "retry" | "failed";

/** How one replayed mutation's response classifies. Only "committed"
 * counts as replayed: a broad 4xx used to be deleted and counted as
 * success, silently discarding the user's intended change and then
 * triggering cache invalidation that concealed the loss (audit WEB-03). */
export function replayDecision(status: number): ReplayDecision {
  if (status >= 200 && status < 300) return "committed";
  if (status === 401) return "reauth";
  if ([408, 425, 429].includes(status) || status >= 500) return "retry";
  return "failed"; // 400/404/409/412/422...: this action can never apply
}

export async function replayMutations(): Promise<number> {
  if (!navigator.onLine || !offlineOwner()) return 0;
  const all = await transaction<Queued[]>(QUEUE, "readonly", (store) => store.getAll());
  let replayed = 0;
  for (const item of all
    .filter((entry) => entry.owner === offlineOwner() && !entry.failed)
    .sort((a, b) => a.queuedAt - b.queuedAt)) {
    let response: Response;
    try {
      response = await fetch("/api" + item.path, {
        method: item.method, credentials: "same-origin",
        headers: item.body === undefined ? undefined : { "Content-Type": "application/json" },
        body: item.body === undefined ? undefined : JSON.stringify(item.body),
      });
    } catch { break; } // network failed mid-replay: keep the rest queued
    const decision = replayDecision(response.status);
    if (decision === "reauth") break;
    if (decision === "retry") break;
    if (decision === "failed") {
      // Permanently invalid: keep a marked record for visibility instead
      // of counting it as replayed, and let detail show what rejected it.
      let detail = String(response.status);
      try {
        const problem = await response.json();
        detail = problem.detail || problem.title || detail;
      } catch { /* non-JSON error body */ }
      console.warn("Offline action rejected by the server and dropped from retry:", item.path, detail);
      await transaction(QUEUE, "readwrite", (store) => store.put({ ...item, failed: detail }));
      continue;
    }
    await transaction(QUEUE, "readwrite", (store) => store.delete(item.id));
    replayed++;
  }
  return replayed;
}

export async function clearResponseCache(): Promise<void> {
  if (typeof indexedDB === "undefined") return;
  await transaction(CACHE, "readwrite", (store) => store.clear());
}

export async function clearOfflineData(): Promise<void> {
  if (typeof indexedDB !== "undefined") {
    await Promise.all([
      transaction(CACHE, "readwrite", (store) => store.clear()),
      transaction(QUEUE, "readwrite", (store) => store.clear()),
      transaction(ATTACHMENTS, "readwrite", (store) => store.clear()),
    ]).catch(() => {});
  }
  localStorage.removeItem(OWNER);
}

/* ---- draft attachments: too big for localStorage, durable in IndexedDB ----
   Attachments must survive parking a draft (the README promises resilient
   local drafts), so they persist here keyed by draft id. Owner-checked on
   load so a different account never inherits another's files. */

export async function saveDraftAttachments(id: string, files: unknown[]): Promise<void> {
  const owner = offlineOwner(); if (!owner || typeof indexedDB === "undefined") return;
  await transaction(ATTACHMENTS, "readwrite", (store) => store.put({ id, owner, files } as DraftAttachmentRow));
}

export async function loadDraftAttachments<T>(id: string): Promise<T[] | undefined> {
  if (typeof indexedDB === "undefined") return undefined;
  const row = await transaction<DraftAttachmentRow | undefined>(ATTACHMENTS, "readonly", (store) => store.get(id));
  if (!row || row.owner !== offlineOwner()) return undefined;
  return row.files as T[];
}

export async function clearDraftAttachments(id: string): Promise<void> {
  if (typeof indexedDB === "undefined") return;
  await transaction(ATTACHMENTS, "readwrite", (store) => store.delete(id));
}

export function startOfflineData(): () => void {
  // A replay changed real state; whatever is on screen should catch up now,
  // not on the next navigation or the 45s counts tick. Zero committed
  // mutations means NOTHING changed: clearing the response cache then
  // would wipe the offline mailbox the next offline launch needs to read
  // (audit WEB-01). Dynamic import: actions imports this module's cache
  // helpers, and a static back-edge would cycle.
  const replay = () => {
    replayMutations().then(async (committed) => {
      if (committed === 0) return;
      await clearResponseCache();
      const { reload, refreshCounts } = await import("./actions");
      reload(); refreshCounts();
    }).catch((error) => {
      // Queued work and cached mail stay intact; the next online event
      // retries.
      console.error("Offline replay failed", error);
    });
  };
  window.addEventListener("online", replay); replay();
  return () => window.removeEventListener("online", replay);
}
