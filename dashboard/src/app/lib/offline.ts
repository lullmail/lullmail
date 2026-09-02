// IndexedDB is the offline mailbox layer. The service worker deliberately
// caches only the shell; API data is account-namespaced here so owner changes
// and account deletion can provably evict it.

const DB = "lullmail-offline-v1";
const VERSION = 1;
const CACHE = "responses";
const QUEUE = "mutations";
const OWNER = "es-offline-owner";

interface Cached { key: string; owner: string; savedAt: number; value: unknown }
interface Queued { id: string; owner: string; path: string; method: string; body?: unknown; queuedAt: number }

function openDB(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(DB, VERSION);
    request.onupgradeneeded = () => {
      const db = request.result;
      if (!db.objectStoreNames.contains(CACHE)) db.createObjectStore(CACHE, { keyPath: "key" });
      if (!db.objectStoreNames.contains(QUEUE)) db.createObjectStore(QUEUE, { keyPath: "id" });
    };
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
}

function transaction<T>(store: string, mode: IDBTransactionMode, run: (s: IDBObjectStore) => IDBRequest<T>): Promise<T> {
  return openDB().then((db) => new Promise<T>((resolve, reject) => {
    const tx = db.transaction(store, mode);
    const request = run(tx.objectStore(store));
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
    tx.oncomplete = () => db.close();
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

export async function replayMutations(): Promise<number> {
  if (!navigator.onLine || !offlineOwner()) return 0;
  const all = await transaction<Queued[]>(QUEUE, "readonly", (store) => store.getAll());
  let replayed = 0;
  for (const item of all.filter((entry) => entry.owner === offlineOwner()).sort((a, b) => a.queuedAt - b.queuedAt)) {
    let response: Response;
    try {
      response = await fetch("/api" + item.path, {
        method: item.method, credentials: "same-origin",
        headers: item.body === undefined ? undefined : { "Content-Type": "application/json" },
        body: item.body === undefined ? undefined : JSON.stringify(item.body),
      });
    } catch { break; }
    if (response.status === 401) break;
    // A stale action should not poison the whole queue. 2xx succeeds; 4xx is
    // permanently invalid after reconnect; 5xx stays for the next attempt.
    if (response.ok || (response.status >= 400 && response.status < 500)) {
      await transaction(QUEUE, "readwrite", (store) => store.delete(item.id)); replayed++;
    } else break;
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
    ]).catch(() => {});
  }
  localStorage.removeItem(OWNER);
}

export function startOfflineData(): () => void {
  // A replay changed real state; whatever is on screen should catch up now,
  // not on the next navigation or the 45s counts tick. Dynamic import:
  // actions imports this module's cache helpers, and a static back-edge
  // would cycle.
  const replay = () => {
    replayMutations().then(async (n) => {
      if (n > 0) {
        const { reload, refreshCounts } = await import("./actions");
        reload(); refreshCounts();
      }
      return clearResponseCache();
    }).catch(() => {});
  };
  window.addEventListener("online", replay); replay();
  return () => window.removeEventListener("online", replay);
}
