// IndexedDB is the offline mailbox layer. The service worker deliberately
// caches only the shell; API data is namespaced here so owner changes and
// account deletion can provably evict it.
//
// offline-v2 storage (audit WEB-07/WEB-01/R08+F11+F12, F05 remainder):
// every store is namespaced by installation_id + user_id — identities the
// owner cannot reuse, unlike the old email-based owner marker — a
// generation counter fences every async read (a write belonging to a
// stale generation is discarded, never published), cache records are
// per-mailbox so disconnecting one account keeps the others' snapshots,
// and one draft is ONE record (fields and attachments in the same
// IndexedDB row, the ring being those rows in seq order — record and
// ring commit together by construction). Decided product semantics,
// founder-ratified 2026-09-18: logout clears that owner's local caches
// AND drafts; data belongs to the session owner, no survivorship; an
// owner or account switch initializes a new generation.

import { showError } from "./store";

const DB = "lullmail-offline-v1";
const VERSION = 3;
const CACHE = "responses";
const QUEUE = "mutations";
const ATTACHMENTS = "attachments"; // legacy v1 store; drained by the v2 migration
const DRAFTS = "drafts";
const NS_KEY = "lull-offline-ns";
const GEN_KEY = "lull-offline-gen";
const EMAIL_KEY = "lull-offline-email";
const V2_KEY = "lull-offline-v2";
const OWNER = "es-offline-owner"; // legacy v1 marker, consumed by the migration

interface Cached { key: string; owner: string; account: string; savedAt: number; value: unknown }
interface Queued {
  id: string;
  owner: string;
  path: string;
  method: string;
  body?: unknown;
  queuedAt: number;
  /** Server idempotency key (audit WEB-04): minted before the FIRST
   *  attempt so a request whose acknowledgment was lost replays the
   *  recorded answer instead of applying twice. */
  key?: string;
  /** Failed-attempt bookkeeping for bounded exponential retry (audit 3 WEB-05). */
  attempts?: number;
  nextAttemptAt?: number;
  /** Set when replay concluded this action can never succeed; kept for
   * visibility instead of being silently discarded (audit WEB-03). */
  failed?: string;
}
interface DraftAttachmentRow { id: string; owner: string; files: unknown[] }

/** One draft as a single IndexedDB record (audit F05 remainder): the
 *  fields and the attachment payloads live in the SAME row, and the ring
 *  is these rows ordered by seq — there is no second engine to fall out
 *  of sync with. */
export interface DraftRecord {
  id: string;
  ns: string;
  seq: number;
  savedAt: number;
  to: string;
  cc?: string;
  bcc?: string;
  subject: string;
  body: string;
  htmlMode?: boolean;
  accountId?: string;
  replyToId?: string;
  context?: string;
  attachments?: Array<{ filename: string; contentType: string; dataBase64: string }>;
}

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
      if (!db.objectStoreNames.contains(DRAFTS)) db.createObjectStore(DRAFTS, { keyPath: "id" });
      // v1 cache rows are keyed by the dead email namespace; they can
      // never be read again, so the upgrade drops them. The mutations
      // queue and attachment rows survive for the one-time v2 migration
      // (pending offline work and parked drafts are real user data).
      const upgrade = request as IDBOpenDBRequest & { oldVersion?: number };
      if (request.transaction && (upgrade.oldVersion ?? 0) > 0) {
        request.transaction.objectStore(CACHE).clear();
      }
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
 *  request's onsuccess reported saves that a later abort (quota, another
 *  operation's failure) silently rolled back (audit WEB-02). */
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

/* ---- namespace + generation (audit WEB-07/WEB-01/R08) ---- */

/** The offline namespace: installation_id + "/" + user_id. Empty when no
 *  owner has been prepared on this device. */
export function offlineOwner(): string { return localStorage.getItem(NS_KEY) || ""; }

/** The last owner's email, for display when the server is unreachable. */
export function offlineEmail(): string { return localStorage.getItem(EMAIL_KEY) || ""; }

/** The storage generation: bumped on every owner/account switch and
 *  every wipe. An async read captures it on the way in; if the value
 *  changed by the time the result is ready, the result is discarded. */
export function offlineGeneration(): number {
  return parseInt(localStorage.getItem(GEN_KEY) || "0", 10) || 0;
}

export function generationCurrent(gen: number): boolean {
  return offlineGeneration() === gen;
}

/** Fail-closed suspension (audit 4 F11): when preparing an owner's storage
 *  failed — most dangerously a wipe that did not commit during an owner
 *  change — the old localStorage marker is not authority to keep reading and
 *  writing that namespace. Every cache read, cache write, queue write and
 *  replay no-ops (or fails visibly) until a later successful prepare clears
 *  the flag. */
let storageSuspended = false;

export function suspendOfflineStorage(): void { storageSuspended = true; }

export function offlineStorageSuspended(): boolean { return storageSuspended; }

export interface OfflineOwnerIdentity {
  installation_id?: string;
  user_id?: string;
  email: string;
}

/** namespaceFor keeps the old email namespace for servers that predate
 *  the identity fields (a rolling deploy must not wipe a client). */
export function namespaceFor(identity: OfflineOwnerIdentity): string {
  if (identity.installation_id && identity.user_id) {
    return identity.installation_id + "/" + identity.user_id;
  }
  return identity.email;
}

export async function prepareOfflineOwner(identity: OfflineOwnerIdentity): Promise<void> {
  const ns = namespaceFor(identity);
  if (!ns || !identity.email) return;
  const previous = offlineOwner();
  if (previous && previous !== ns) {
    // Owner/account switch: caches, queued work, and drafts of the
    // previous owner all go (ratified semantics — data belongs to the
    // session owner), and the new generation fences every read still in
    // flight from the old one.
    await clearOfflineData();
    localStorage.setItem(NS_KEY, ns);
    localStorage.setItem(GEN_KEY, String(offlineGeneration() + 1));
    localStorage.setItem(EMAIL_KEY, identity.email);
    storageSuspended = false;
    return;
  }
  // First owner on this device: the one-time v1 migration may wipe a
  // different owner's v1 remnants, and that wipe also removes the v2
  // namespace markers — so it must run BEFORE this owner's markers are
  // written, or the just-prepared namespace is stripped and every
  // offline store silently no-ops.
  await migrateV1Storage(ns, identity.email);
  if (!previous) {
    localStorage.setItem(NS_KEY, ns);
    localStorage.setItem(GEN_KEY, String(offlineGeneration() + 1));
    localStorage.setItem(EMAIL_KEY, identity.email);
  }
  storageSuspended = false;
}

/* ---- one-time v1 -> v2 storage migration ----
   The v1 engine kept the draft ring + per-draft fields in localStorage
   and attachment payloads in the ATTACHMENTS store, all under an
   email owner marker. When the SAME owner returns, their parked drafts
   and pending offline queue carry into the namespaced v2 stores; a
   different owner's remnants are wiped, not inherited. */

async function migrateV1Storage(ns: string, email: string): Promise<void> {
  if (localStorage.getItem(V2_KEY) || typeof indexedDB === "undefined") return;
  localStorage.setItem(V2_KEY, "1");
  const v1Owner = localStorage.getItem(OWNER);
  const sameOwner = v1Owner !== null && v1Owner === email;
  try {
    if (sameOwner) {
      const db = await openDB();
      try {
        await new Promise<void>((resolve, reject) => {
          const tx = db.transaction([QUEUE, ATTACHMENTS, DRAFTS], "readwrite");
          tx.oncomplete = () => resolve();
          tx.onabort = () => reject(tx.error ?? new OfflineStorageError("v2 migration aborted"));
          // Pending offline mutations keep their ids (idempotency keys)
          // and their order; only the namespace field is rewritten.
          const queue = tx.objectStore(QUEUE);
          const queueRows = queue.getAll();
          queueRows.onsuccess = () => {
            for (const row of queueRows.result as Queued[]) {
              if (row.owner === v1Owner) queue.put({ ...row, owner: ns, key: row.key ?? row.id });
            }
            // Attachment payloads fold into their draft's single record.
            const atts = tx.objectStore(ATTACHMENTS).getAll();
            atts.onsuccess = () => {
              const byDraft = new Map<string, unknown[]>();
              for (const row of atts.result as DraftAttachmentRow[]) {
                if (row.owner === v1Owner && Array.isArray(row.files)) byDraft.set(row.id, row.files);
              }
              migrateDraftRing(tx.objectStore(DRAFTS), ns, byDraft);
              tx.objectStore(ATTACHMENTS).clear();
            };
          };
        });
      } finally {
        db.close();
      }
    } else {
      await clearOfflineData();
    }
  } catch (error) {
    // The migration is best-effort by design: failing it must not log the
    // owner out or suspend storage over data that was already local-only.
    console.warn("offline v2 storage migration did not complete", error);
  }
  for (const key of Object.keys(localStorage)) {
    if (key === "es-drafts" || key.startsWith("es-draft-")) localStorage.removeItem(key);
  }
  localStorage.removeItem(OWNER);
}

/** The v1 ring lived in localStorage ("es-drafts" metadata plus one
 *  "es-draft-<id>" field slot per draft). Worth-restoring drafts are
 *  written as v2 records with their attachments folded in. */
function migrateDraftRing(store: IDBObjectStore, ns: string, attachments: Map<string, unknown[]>): void {
  let ring: Array<Record<string, unknown>> = [];
  try {
    ring = JSON.parse(localStorage.getItem("es-drafts") || "[]");
  } catch { /* corrupt ring: nothing to migrate */ }
  if (!Array.isArray(ring)) return;
  let seq = Date.now();
  for (const entry of ring) {
    if (!entry || typeof entry.id !== "string") continue;
    let fields: Record<string, unknown> = {};
    try {
      fields = JSON.parse(localStorage.getItem("es-draft-" + entry.id) || "{}");
    } catch { /* unreadable slot: the ring entry alone is the draft */ }
    const merged = { ...entry, ...fields, attachments: attachments.get(entry.id) ?? [] } as unknown as DraftRecord;
    if (!worthRestoring(merged)) continue;
    store.put({
      id: merged.id ?? entry.id,
      ns,
      seq: seq++,
      savedAt: Date.now(),
      to: merged.to ?? "",
      cc: merged.cc,
      bcc: merged.bcc,
      subject: merged.subject ?? "",
      body: merged.body ?? "",
      htmlMode: merged.htmlMode,
      accountId: merged.accountId,
      replyToId: merged.replyToId,
      context: merged.context,
      attachments: merged.attachments?.length ? merged.attachments : undefined,
    });
  }
}

/** A draft is worth restoring when any field carries content OR it holds
 *  attachments (audit 4 F05). */
export function worthRestoring(d: { to?: string; cc?: string; bcc?: string; subject?: string; body?: string; attachments?: unknown[]; hasAttachments?: boolean }): boolean {
  if (d.hasAttachments || (d.attachments?.length ?? 0) > 0) return true;
  return [d.to, d.cc, d.bcc, d.subject, d.body].some((v) => typeof v === "string" && v.trim().length > 0);
}

/* ---- response snapshots, per-mailbox + generation-fenced ---- */

/** Which mailbox a request is lensed at ("": the unified view). */
export function accountOf(path: string): string {
  const at = path.indexOf("account=");
  if (at < 0) return "";
  const rest = path.slice(at + "account=".length);
  const end = rest.indexOf("&");
  return decodeURIComponent(end < 0 ? rest : rest.slice(0, end));
}

function cacheKey(ns: string, path: string): string {
  return ns + "\n" + accountOf(path) + "\n" + path;
}

export async function cacheResponse(path: string, value: unknown, gen = offlineGeneration()): Promise<void> {
  if (storageSuspended || !generationCurrent(gen)) return;
  const owner = offlineOwner(); if (!owner || typeof indexedDB === "undefined") return;
  await transaction(CACHE, "readwrite", (store) => store.put({
    key: cacheKey(owner, path), owner, account: accountOf(path), savedAt: Date.now(), value,
  } as Cached));
}

export async function cachedResponse<T>(path: string, gen = offlineGeneration()): Promise<T | undefined> {
  if (storageSuspended || !generationCurrent(gen)) return undefined;
  const owner = offlineOwner(); if (!owner || typeof indexedDB === "undefined") return undefined;
  const item = await transaction<Cached | undefined>(CACHE, "readonly", (store) => store.get(cacheKey(owner, path)));
  if (!generationCurrent(gen)) return undefined;
  return item?.value as T | undefined;
}

/** Disconnecting one mailbox removes its snapshots and the unified ones
 *  (they contain its mail); every other mailbox's lensed snapshots stay
 *  (audit 4 F12 + WEB-07's per-mailbox records). */
export async function purgeAccountSnapshots(accountId: string): Promise<void> {
  const owner = offlineOwner(); if (!owner || typeof indexedDB === "undefined") return;
  const db = await openDB();
  try {
    await new Promise<void>((resolve, reject) => {
      const tx = db.transaction(CACHE, "readwrite");
      tx.oncomplete = () => resolve();
      tx.onabort = () => reject(tx.error ?? new OfflineStorageError("Snapshot purge aborted"));
      const request = tx.objectStore(CACHE).openCursor();
      request.onsuccess = () => {
        const cursor = request.result;
        if (!cursor) return;
        const row = cursor.value as Cached;
        if (row.owner === owner && (row.account === accountId || row.account === "")) cursor.delete();
        cursor.continue();
      };
    });
  } finally {
    db.close();
  }
}

const QUEUEABLE = [
  /^\/messages\/[^/]+\/action$/, /^\/screener\/(decide|undecide)$/,
  /^\/board\/(pin|unpin)$/, /^\/board\/cards\/[^/]+\/done$/,
  /^\/notes\/[^/]+$/,
];

export function canQueue(path: string, method: string): boolean {
  return method !== "GET" && QUEUEABLE.some((pattern) => pattern.test(path.split("?")[0]));
}

export async function queueMutation(path: string, method: string, body?: unknown, key?: string): Promise<void> {
  if (storageSuspended) {
    // Failing visibly beats pretending the mutation was saved (audit 4 F11).
    throw new OfflineStorageError("Offline storage is suspended on this device — the change was NOT saved for replay");
  }
  const owner = offlineOwner(); if (!owner) throw new Error("Offline owner is not initialised");
  const id = newMutationKey();
  await transaction(QUEUE, "readwrite", (store) => store.put({ id, key: key ?? id, owner, path, method, body, queuedAt: Date.now() } as Queued));
}

/** A client-generated idempotency key: opaque, unique, safe as both the
 *  queue item id and the Idempotency-Key header (audit WEB-04). */
export function newMutationKey(): string {
  return typeof crypto.randomUUID === "function" ? crypto.randomUUID() : Date.now() + "-" + Math.random().toString(16).slice(2);
}

/** The exact fetch one replayed mutation issues — export shape so the
 *  contract (headers, body) is testable without a network. */
export function replayRequestInit(item: Pick<Queued, "method" | "body" | "key">): RequestInit {
  const headers: Record<string, string> = {};
  if (item.body !== undefined) headers["Content-Type"] = "application/json";
  if (item.key) headers["Idempotency-Key"] = item.key;
  return {
    method: item.method,
    credentials: "same-origin",
    headers: item.body === undefined && !item.key ? undefined : headers,
    body: item.body === undefined ? undefined : JSON.stringify(item.body),
  };
}

/** Web Locks coordinate replay across tabs (audit WEB-04): the lock is
 *  held for one whole replay pass, and a tab that cannot take it skips —
 *  the holder refreshes shared state when it finishes. Without the
 *  server contract this would only have narrowed the duplicate window;
 *  with api_mutations recorded server-side it is now safe coordination
 *  rather than an implied guarantee. */
const REPLAY_LOCK = "lullmail-offline-replay";

export async function withReplayLock<T>(run: () => Promise<T>): Promise<T | undefined> {
  const locks = (navigator as Navigator & {
    locks?: { request(name: string, options: { ifAvailable: true }, callback: () => Promise<T>): Promise<T | undefined> };
  }).locks;
  if (!locks) return run();
  try {
    return await locks.request(REPLAY_LOCK, { ifAvailable: true }, run);
  } catch {
    // A lock-manager failure must not strand the queue: an uncoordinated
    // pass can still only commit each item once (server-side keying).
    return run();
  }
}

export type ReplayDecision = "committed" | "reauth" | "retry" | "failed";

/** How one replayed mutation's response classifies. Only "committed"
 *  counts as replayed: a broad 4xx used to be deleted and counted as
 *  success, silently discarding the user's intended change and then
 *  triggering cache invalidation that concealed the loss (audit WEB-03). */
export function replayDecision(status: number): ReplayDecision {
  if (status >= 200 && status < 300) return "committed";
  if (status === 401) return "reauth";
  if ([408, 425, 429].includes(status) || status >= 500) return "retry";
  return "failed"; // 400/404/409/412/422...: this action can never apply
}

/** Retry-After in milliseconds; seconds form or HTTP-date form. 0 when
 *  absent or unparseable (audit 3 WEB-05). */
export function retryAfterMs(value: string | null, now = Date.now()): number {
  if (!value) return 0;
  const seconds = Number(value);
  if (Number.isFinite(seconds) && seconds >= 0) return seconds * 1000;
  const date = Date.parse(value);
  return Number.isFinite(date) ? Math.max(0, date - now) : 0;
}

/** Bounded exponential backoff with jitter, never shorter than the
 *  server's Retry-After (audit 3 WEB-05). */
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

/** The replay order: oldest first, and a backed-off head STOPS the pass —
 *  nothing behind it runs early. Letting newer mutations overtake an older
 *  backed-off one applied sequential edits in the wrong order and let the
 *  older write clobber the newer result (audit 4 F09). The resume time is
 *  exactly the head's deadline, because nothing may run before it anyway. */
export function replayPlan<T extends { owner: string; queuedAt: number; nextAttemptAt?: number; failed?: string }>(
  items: T[],
  owner: string,
  now: number,
): { due: T[]; retryAt?: number } {
  const due: T[] = [];
  let retryAt: number | undefined;
  for (const item of [...items]
    .filter((entry) => entry.owner === owner && !entry.failed)
    .sort((a, b) => a.queuedAt - b.queuedAt)) {
    if (item.nextAttemptAt && item.nextAttemptAt > now) {
      // Not due yet (persisted backoff from an earlier attempt): nothing
      // later may overtake it (audit 4 F09).
      retryAt = retryAt === undefined ? item.nextAttemptAt : Math.min(retryAt, item.nextAttemptAt);
      break;
    }
    due.push(item);
  }
  return retryAt === undefined ? { due } : { due, retryAt };
}

/** One ordered replay pass over this owner's queue (oldest first, a
 *  backed-off head stops the pass — audit 4 F09). Runs under the
 *  cross-tab replay lock when the browser offers one. */
async function replayDueMutations(): Promise<ReplaySummary> {
  const gen = offlineGeneration();
  const all = await transaction<Queued[]>(QUEUE, "readonly", (store) => store.getAll());
  if (!generationCurrent(gen)) return { committed: 0, rejected: 0 };
  const now = Date.now();
  let committed = 0;
  let rejected = 0;
  let retryAt: number | undefined;
  for (const item of replayPlan(all, offlineOwner(), now).due) {
    let response: Response;
    try {
      response = await fetch("/api" + item.path, replayRequestInit(item));
    } catch {
      // A network-level failure while navigator.onLine can still be true:
      // give the queue head a backoff slot and schedule the retry, or the
      // work strands until some later navigation or connectivity event
      // (audit 4 F10).
      const attempts = (item.attempts ?? 0) + 1;
      const delay = retryDelay(attempts, null);
      retryAt = retryAt === undefined ? now + delay : Math.min(retryAt, now + delay);
      await transaction(QUEUE, "readwrite", (store) => store.put({ ...item, attempts, nextAttemptAt: now + delay }));
      break; // keep the rest queued behind this one, in order
    }
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

/** The public entry: online, unsuspended, under an owner, and holding the
 *  cross-tab replay lock (WEB-04). A tab that loses the lock reports an
 *  empty pass — the holder's commits refresh the shared view anyway. */
export async function replayMutations(): Promise<ReplaySummary> {
  if (!navigator.onLine || storageSuspended || !offlineOwner()) return { committed: 0, rejected: 0 };
  return (await withReplayLock(replayDueMutations)) ?? { committed: 0, rejected: 0 };
}

export async function clearResponseCache(): Promise<void> {
  if (typeof indexedDB === "undefined") return;
  await transaction(CACHE, "readwrite", (store) => store.clear());
}

/** Wipes every private store in ONE transaction — caches, queued work,
 *  and drafts (ratified logout semantics: the data belongs to the
 *  session owner; no survivorship). The namespace marker is removed only
 *  if that transaction commits: swallowing a partial failure used to
 *  report erased data while something survived — dangerous exactly when
 *  the same namespace is reused later (audit 3 WEB-02). The generation
 *  counter is NEVER removed: it keeps counting up across wipes so a
 *  pre-wipe async write can never publish into a post-wipe namespace. */
export async function clearOfflineData(): Promise<void> {
  if (typeof indexedDB === "undefined") {
    localStorage.removeItem(NS_KEY);
    localStorage.removeItem(EMAIL_KEY);
    return;
  }
  const db = await openDB();
  try {
    await new Promise<void>((resolve, reject) => {
      const tx = db.transaction([CACHE, QUEUE, ATTACHMENTS, DRAFTS], "readwrite");
      tx.oncomplete = () => resolve();
      tx.onabort = () => reject(tx.error ?? new OfflineStorageError("Private-data reset aborted"));
      tx.onerror = () => { /* the abort handler owns rejection */ };
      tx.objectStore(CACHE).clear();
      tx.objectStore(QUEUE).clear();
      tx.objectStore(ATTACHMENTS).clear();
      tx.objectStore(DRAFTS).clear();
    });
  } finally {
    db.close();
  }
  localStorage.removeItem(NS_KEY);
  localStorage.removeItem(EMAIL_KEY);
}

/* ---- drafts: one record per draft, the ring in the same rows ---- */

/** Field edits merge into the draft's single record (get+put in one
 *  transaction, so a fields write can never clobber the attachments in
 *  the same row or vice versa). */
export async function saveDraftFields(id: string, fields: Partial<DraftRecord>): Promise<void> {
  const ns = offlineOwner(); if (!ns || typeof indexedDB === "undefined") return;
  const db = await openDB();
  try {
    await new Promise<void>((resolve, reject) => {
      const tx = db.transaction(DRAFTS, "readwrite");
      tx.oncomplete = () => resolve();
      tx.onabort = () => reject(tx.error ?? new OfflineStorageError("Draft save aborted"));
      const store = tx.objectStore(DRAFTS);
      const existing = store.get(id);
      existing.onsuccess = () => {
        const row = existing.result as DraftRecord | undefined;
        const seq = row?.seq ?? Date.now();
        store.put({ ...row, ...fields, id, ns, seq, savedAt: Date.now() } as DraftRecord);
      };
    });
  } finally {
    db.close();
  }
}

/** Attachment payloads ride in the draft record itself — there is no
 *  separate attachment store to fall out of sync with (audit F05). */
export async function saveDraftAttachments(id: string, files: Array<{ filename: string; contentType: string; dataBase64: string }>): Promise<void> {
  await saveDraftFields(id, { attachments: files });
}

/** Every parked draft for this owner, in ring (seq) order. */
export async function loadDrafts(): Promise<DraftRecord[]> {
  const ns = offlineOwner(); if (!ns || typeof indexedDB === "undefined") return [];
  const rows = await transaction<DraftRecord[]>(DRAFTS, "readonly", (store) => store.getAll());
  return rows.filter((row) => row && row.ns === ns).sort((a, b) => a.seq - b.seq);
}

export async function deleteDraft(id: string): Promise<void> {
  if (typeof indexedDB === "undefined") return;
  await transaction(DRAFTS, "readwrite", (store) => store.delete(id));
}

export function startOfflineData(): () => void {
  // Replayed state refreshes whatever is on screen; the persisted offline
  // snapshots STAY — a mutation is invalidation, not a reason to delete
  // the only offline copy of the mailbox (audit 3 WEB-06). Fresh GETs
  // overwrite the snapshots they replace.
  let timer: number | undefined;
  // An in-flight replay must not schedule a new timer after the cleanup
  // function has run: unmount would leave an orphaned retry loop (audit
  // 4 F10).
  let stopped = false;
  const replay = () => {
    if (stopped) return;
    replayMutations().then(async (summary) => {
      if (stopped) return;
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
    if (stopped) return;
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
    stopped = true;
    window.removeEventListener("online", replay);
    if (timer !== undefined) window.clearTimeout(timer);
  };
}
