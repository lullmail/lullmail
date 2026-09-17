// IndexedDB is the offline mailbox layer. The service worker deliberately
// caches only the shell; API data is account-namespaced here so owner changes
// and account deletion can provably evict it.

import { showError } from "./store";

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
  /** Failed-attempt bookkeeping for bounded exponential retry (audit 3 WEB-05). */
  attempts?: number;
  nextAttemptAt?: number;
  /** Set when replay concluded this action can never succeed; kept for
   * visibility instead of being silently discarded (audit WEB-03). */
  failed?: string;
}
interface DraftAttachmentRow { id: string; owner: string; files: unknown[] }

/** Storage-layer failure, distinct from a network/auth failure so callers
 *  can report it accurately instead of claiming the server is unreachable
 *  (audit 3 WEB-08). */
export class OfflineStorageError extends Error {}

function openDB(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(DB, VERSION);
    // A held connection in another tab must surface as an actionable
    // storage error, not a silent hang (audit 3 WEB-08).
    request.onblocked = () => reject(new OfflineStorageError("Close other Lullmail tabs to update offline storage"));
    request.onupgradeneeded = () => {
      const db = request.result;
      if (!db.objectStoreNames.contains(CACHE)) db.createObjectStore(CACHE, { keyPath: "key" });
      if (!db.objectStoreNames.contains(QUEUE)) db.createObjectStore(QUEUE, { keyPath: "id" });
      if (!db.objectStoreNames.contains(ATTACHMENTS)) db.createObjectStore(ATTACHMENTS, { keyPath: "id" });
    };
    request.onerror = () => reject(new OfflineStorageError(request.error?.message ?? "Storage unavailable"));
    request.onsuccess = () => {
      const db = request.result;
      // Another tab wants to upgrade: close so it can, rather than
      // blocking it forever (audit 3 WEB-08).
      db.onversionchange = () => db.close();
      resolve(db);
    };
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

/** Retry-After in milliseconds; seconds form or HTTP-date form. 0 when
 * absent or unparseable (audit 3 WEB-05). */
export function retryAfterMs(value: string | null, now = Date.now()): number {
  if (!value) return 0;
  const seconds = Number(value);
  if (Number.isFinite(seconds) && seconds >= 0) return seconds * 1000;
  const date = Date.parse(value);
  return Number.isFinite(date) ? Math.max(0, date - now) : 0;
}

/** Bounded exponential backoff with jitter, never shorter than the
 * server's Retry-After (audit 3 WEB-05). */
export function retryDelay(attempts: number, retryAfter: string | null): number {
  const base = Math.min(300_000, 1000 * 2 ** Math.min(attempts, 8));
  const jittered = base * (0.8 + Math.random() * 0.4);
  return Math.max(jittered, retryAfterMs(retryAfter));
}

export interface ReplaySummary {
  committed: number;
  /** Actions the server permanently rejected (audit 3 WEB-04). */
  rejected: number;
  /** Epoch ms when a retryable failure may resume; set while online so a
   * transient outage does not strand the queue until the next navigation
   * (audit 3 WEB-05). */
  retryAt?: number;
}

export async function replayMutations(): Promise<ReplaySummary> {
  if (!navigator.onLine || !offlineOwner()) return { committed: 0, rejected: 0 };
  const all = await transaction<Queued[]>(QUEUE, "readonly", (store) => store.getAll());
  const now = Date.now();
  let committed = 0;
  let rejected = 0;
  let retryAt: number | undefined;
  const due = all
    .filter((entry) => entry.owner === offlineOwner() && !entry.failed)
    .sort((a, b) => a.queuedAt - b.queuedAt);
  for (const item of due) {
    if (item.nextAttemptAt && item.nextAttemptAt > now) {
      // Not due yet (persisted backoff from an earlier attempt): remember
      // when it becomes due so a timer can resume replay (audit 3 WEB-05).
      retryAt = retryAt === undefined ? item.nextAttemptAt : Math.min(retryAt, item.nextAttemptAt);
      continue;
    }
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
    if (decision === "retry") {
      const attempts = (item.attempts ?? 0) + 1;
      const delay = retryDelay(attempts, response.headers.get("Retry-After"));
      retryAt = retryAt === undefined ? now + delay : Math.min(retryAt, now + delay);
      await transaction(QUEUE, "readwrite", (store) => store.put({ ...item, attempts, nextAttemptAt: now + delay }));
      break; // keep the rest queued behind this one, in order
    }
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
      rejected++;
      continue;
    }
    await transaction(QUEUE, "readwrite", (store) => store.delete(item.id));
    committed++;
  }
  return retryAt === undefined ? { committed, rejected } : { committed, rejected, retryAt };
}

export async function clearResponseCache(): Promise<void> {
  if (typeof indexedDB === "undefined") return;
  await transaction(CACHE, "readwrite", (store) => store.clear());
}

/** Wipes every private store in ONE transaction. The owner marker is
 * removed only if that transaction commits: swallowing a partial failure
 * used to report erased data while cached responses, queued commands, or
 * attachments survived — dangerous exactly when the same namespace is
 * reused later (audit 3 WEB-02). */
export async function clearOfflineData(): Promise<void> {
  if (typeof indexedDB === "undefined") {
    localStorage.removeItem(OWNER);
    return;
  }
  const db = await openDB();
  try {
    await new Promise<void>((resolve, reject) => {
      const tx = db.transaction([CACHE, QUEUE, ATTACHMENTS], "readwrite");
      tx.oncomplete = () => resolve();
      tx.onabort = () => reject(tx.error ?? new OfflineStorageError("Private-data reset aborted"));
      tx.onerror = () => { /* the abort handler owns rejection */ };
      tx.objectStore(CACHE).clear();
      tx.objectStore(QUEUE).clear();
      tx.objectStore(ATTACHMENTS).clear();
    });
  } finally {
    db.close();
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
  // Replayed state refreshes whatever is on screen; the persisted offline
  // snapshots STAY — a mutation is invalidation, not a reason to delete
  // the only offline copy of the mailbox (audit 3 WEB-06). Fresh GETs
  // overwrite the snapshots they replace.
  let timer: number | undefined;
  const replay = () => {
    replayMutations().then(async (summary) => {
      if (summary.committed > 0) {
        const { reload, refreshCounts } = await import("./actions");
        reload(); refreshCounts();
      }
      if (summary.rejected > 0) {
        showError(`${summary.rejected} offline action${summary.rejected === 1 ? "" : "s"} could not be applied — check the browser console for what the server rejected`);
      }
      if (summary.retryAt !== undefined) scheduleRetry(summary.retryAt);
    }).catch((error) => {
      // Queued work and cached mail stay intact; the next online event
      // retries.
      console.error("Offline replay failed", error);
    });
  };
  const scheduleRetry = (at: number) => {
    if (timer !== undefined) window.clearTimeout(timer);
    const delay = Math.max(0, at - Date.now());
    // A transient failure while still online must schedule its own next
    // attempt instead of waiting for a navigation or network transition
    // (audit 3 WEB-05).
    timer = window.setTimeout(() => {
      timer = undefined;
      if (navigator.onLine) replay();
    }, delay);
  };
  window.addEventListener("online", replay); replay();
  return () => {
    window.removeEventListener("online", replay);
    if (timer !== undefined) window.clearTimeout(timer);
  };
}
