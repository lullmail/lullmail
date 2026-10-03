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

import { resetPrivateState, showError } from "./store";

const DB = "lullmail-offline-v1";
const VERSION = 4;
const CACHE = "responses";
const QUEUE = "mutations";
const ATTACHMENTS = "attachments"; // legacy v1 store; drained by the v2 migration
const DRAFTS = "drafts";
const META = "meta";
const TOMBSTONES = "draft-tombstones";
const SNAPSHOTS_KEY = "lull-offline-snapshots";
const PURGE_PREFIX = "lull-offline-purge:";
interface StorageMeta { key: "session"; owner: string; generation: number; snapshots: number; legacyMigrated?: boolean }
interface StorageContext { owner: string; generation: number; snapshots?: number }
let admitted: StorageContext | undefined;
const draftRevisions = new Map<string, number>();
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
  revision?: number;
  to: string;
  cc?: string;
  bcc?: string;
  subject: string;
  body: string;
  htmlMode?: boolean;
  accountId?: string;
  replyToId?: string;
  context?: string;
  /** Retry identity for this unchanged submission, never a delivery receipt. */
  sendKey?: string;
  attachments?: Array<{ filename: string; contentType: string; dataBase64: string }>;
}

/** Storage-layer failure, distinct from a network/auth failure so callers
 *  can report it accurately instead of claiming the server is unreachable
 *  (audit 3 WEB-08). */
export class OfflineStorageError extends Error {}
export class OfflineOwnerChangedError extends OfflineStorageError {}

function openDB(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(DB, VERSION);
    // A held connection in another tab must surface as an actionable
    // storage error, not a silent hang (audit 3 WEB-08).
    let blocked = false;
    request.onblocked = () => {
      blocked = true;
      reject(new OfflineStorageError("Close other Lullmail tabs to update offline storage"));
    };
    request.onupgradeneeded = (event: IDBVersionChangeEvent) => {
      const db = request.result;
      if (!db.objectStoreNames.contains(CACHE)) db.createObjectStore(CACHE, { keyPath: "key" });
      if (!db.objectStoreNames.contains(QUEUE)) db.createObjectStore(QUEUE, { keyPath: "id" });
      if (!db.objectStoreNames.contains(ATTACHMENTS)) db.createObjectStore(ATTACHMENTS, { keyPath: "id" });
      if (!db.objectStoreNames.contains(DRAFTS)) db.createObjectStore(DRAFTS, { keyPath: "id" });
      if (!db.objectStoreNames.contains(META)) db.createObjectStore(META, { keyPath: "key" });
      if (!db.objectStoreNames.contains(TOMBSTONES)) db.createObjectStore(TOMBSTONES, { keyPath: "id" });
      // v1 cache rows are keyed by the dead email namespace; they can
      // never be read again, so the upgrade drops them. The mutations
      // queue and attachment rows survive for the one-time v2 migration
      // (pending offline work and parked drafts are real user data).
      // The PREVIOUS version comes from the version-change event, not
      // the request object (audit 5 OFF-01).
      if (request.transaction && event.oldVersion > 0 && event.oldVersion < 3) {
        request.transaction.objectStore(CACHE).clear();
      }
    };
    request.onerror = () => reject(new OfflineStorageError(request.error?.message ?? "Storage unavailable"));
    request.onsuccess = () => {
      const db = request.result;
      if (blocked) { db.close(); return; }
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
function context(gen = offlineGeneration(), snapshots?: number): StorageContext {
  return { owner: offlineOwner(), generation: gen, snapshots };
}

function purgeHintPrefix(ctx: StorageContext): string {
  return PURGE_PREFIX + JSON.stringify([ctx.owner, ctx.generation]) + ":";
}

function purgeHints(ctx: StorageContext): string[] {
  const prefix = purgeHintPrefix(ctx);
  return Object.keys(localStorage).filter((key) => key.startsWith(prefix));
}

function matches(meta: StorageMeta | undefined, ctx: StorageContext): boolean {
  return !!meta && meta.owner === ctx.owner && meta.generation === ctx.generation &&
    (ctx.snapshots === undefined || meta.snapshots === ctx.snapshots);
}

function currentContext(ctx: StorageContext): boolean {
  return !storageSuspended && offlineOwner() === ctx.owner && generationCurrent(ctx.generation) &&
    (ctx.snapshots === undefined || (snapshotGeneration() === ctx.snapshots && purgeHints(ctx).length === 0));
}

/** Metadata and payload share the transaction scope. A wipe that wins the
 * open/transaction race cannot be followed by an old-generation write,
 * even while another tab has not received its storage event yet. */
function transaction<T>(store: string, mode: IDBTransactionMode, run: (s: IDBObjectStore, tx: IDBTransaction) => IDBRequest<T>,
  current?: () => boolean, ctx = context()): Promise<T | undefined> {
  return openDB().then((db) => new Promise<T | undefined>((resolve, reject) => {
    if (!currentContext(ctx) || (current && !current())) { db.close(); resolve(undefined); return; }
    let tx: IDBTransaction;
    try { tx = db.transaction(store === DRAFTS ? [META, DRAFTS, TOMBSTONES] : [META, store], mode); }
    catch (error) { db.close(); reject(error); return; }
    let value: T | undefined;
    let requestError: DOMException | null = null;
    tx.oncomplete = () => { db.close(); resolve(currentContext(ctx) ? value : undefined); };
    tx.onabort = () => { db.close(); reject(tx.error ?? requestError ?? new OfflineStorageError("Storage transaction aborted")); };
    tx.onerror = () => { requestError = tx.error; };
    const meta = tx.objectStore(META).get("session");
    meta.onsuccess = () => {
      if (!matches(meta.result, ctx) || !currentContext(ctx) || (current && !current())) return;
      try {
        const request = run(tx.objectStore(store), tx);
        request.onsuccess = () => { value = request.result; };
        request.onerror = () => { requestError = request.error; };
      } catch (error) { tx.abort(); reject(error); }
    };
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
  return offlineGeneration() === gen && (!admitted ||
    (admitted.owner === offlineOwner() && admitted.generation === gen));
}

/** Fail-closed suspension (audit 4 F11): when preparing an owner's storage
 *  failed — most dangerously a wipe that did not commit during an owner
 *  change — the old localStorage marker is not authority to keep reading and
 *  writing that namespace. Every cache read, cache write, queue write and
 *  replay no-ops (or fails visibly) until a later successful prepare clears
 *  the flag. */
let storageSuspended = false;
// A failed logout may be retried only against the session it was erasing.
// A boolean would carry that erase intent into a newer tab's session.
let wipePending: StorageContext | undefined;
let snapshotPurgePending: StorageContext | undefined;
let transitionVersion = 0;

// Separate from the owner generation: a mailbox purge must fence late
// response publication without invalidating unrelated live drafts.
let responseGeneration = 0;
export function snapshotGeneration(): number {
  return Math.max(responseGeneration, Number(localStorage.getItem(SNAPSHOTS_KEY)) || 0,
    ...purgeHints(context()).map((key) => Number(localStorage.getItem(key)) || 0));
}

export function suspendOfflineStorage(): void {
  transitionVersion++;
  storageSuspended = true;
}

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

/** localStorage is a synchronous display/invalidation mirror only. The
 * IndexedDB row is the authority, including on tabs suspended between a
 * transaction commit and publishing their mirror. */
function publishMeta(meta: StorageMeta, email?: string): void {
  if (meta.generation < offlineGeneration()) {
    throw new OfflineOwnerChangedError("A newer offline owner transition already completed");
  }
  admitted = { owner: meta.owner, generation: meta.generation };
  responseGeneration = Math.max(meta.snapshots, snapshotGeneration());
  localStorage.setItem(GEN_KEY, String(meta.generation));
  localStorage.setItem(SNAPSHOTS_KEY, String(responseGeneration));
  if (meta.owner) localStorage.setItem(NS_KEY, meta.owner);
  else localStorage.removeItem(NS_KEY);
  if (email) localStorage.setItem(EMAIL_KEY, email);
  else if (!meta.owner) localStorage.removeItem(EMAIL_KEY);
}

/** Capture storage authority before starting authentication. A missing or
 * stale mirror can then recover to the server-confirmed owner without
 * authorizing an auth response over a transition that happened mid-flight. */
export async function captureOfflineContext(): Promise<StorageContext> {
  const fallback = context();
  if (typeof indexedDB === "undefined") return fallback;
  const db = await openDB();
  try {
    return await new Promise<StorageContext>((resolve, reject) => {
      const tx = db.transaction(META, "readonly");
      const request = tx.objectStore(META).get("session");
      tx.oncomplete = () => {
        const meta = request.result as StorageMeta | undefined;
        resolve(meta ? { owner: meta.owner, generation: meta.generation } : fallback);
      };
      tx.onabort = () => reject(tx.error ?? new OfflineStorageError("Owner metadata read aborted"));
    });
  } finally { db.close(); }
}

export async function prepareOfflineOwner(identity: OfflineOwnerIdentity, captured?: StorageContext): Promise<void> {
  const ns = namespaceFor(identity);
  if (!ns || !identity.email) return;
  const version = ++transitionVersion;
  const pendingWipe = wipePending;
  const pendingPurge = snapshotPurgePending;
  const expected = captured ?? context();
  const mustWipe = pendingWipe || (!!expected.owner && expected.owner !== ns);
  if (mustWipe || (admitted && (admitted.owner !== ns || admitted.generation !== expected.generation))) {
    storageSuspended = true;
    resetPrivateState();
  }
  if (typeof indexedDB === "undefined") {
    publishMeta({ key: "session", owner: ns, generation: expected.generation + (expected.owner === ns ? 0 : 1), snapshots: snapshotGeneration() }, identity.email);
    wipePending = undefined;
    storageSuspended = false;
    return;
  }
  const db = await openDB().catch((error) => {
    if (version === transitionVersion) storageSuspended = true;
    throw error;
  });
  try {
    const migrate = !localStorage.getItem(V2_KEY);
    const v1Owner = localStorage.getItem(OWNER);
    let recoveredPurgeHints: string[] = [];
    const meta = await new Promise<StorageMeta>((resolve, reject) => {
      const tx = db.transaction([META, CACHE, QUEUE, ATTACHMENTS, DRAFTS, TOMBSTONES], "readwrite");
      let result!: StorageMeta;
      let failure: Error | undefined;
      tx.oncomplete = () => resolve(result);
      tx.onabort = () => reject(failure ?? tx.error ?? new OfflineStorageError("Owner preparation aborted"));
      const request = tx.objectStore(META).get("session");
      request.onsuccess = () => {
        if (version !== transitionVersion) {
          failure = new OfflineOwnerChangedError("A newer offline storage transition already started");
          tx.abort(); return;
        }
        const existing = request.result as StorageMeta | undefined;
        // Do not let an auth response that began before another tab's
        // switch erase or reclaim that newer owner's state.
        if (existing && !matches(existing, expected) && (captured !== undefined || existing.owner !== ns) && !(captured === undefined && pendingWipe && matches(existing, pendingWipe))) {
          failure = new OfflineOwnerChangedError("The offline owner changed in another tab; refresh authentication");
          tx.abort(); return;
        }
        const previous = existing?.owner ?? expected.owner;
        const needsMigration = migrate && !existing?.legacyMigrated;
        const pendingHints = purgeHints(existing ?? expected);
        const pendingSnapshots = Math.max(0, ...pendingHints.map((key) => Number(localStorage.getItem(key)) || 0));
        // Logout invalidates the synchronous mirror before opening IDB.
        // If it failed/crashed there, another freshly authenticated tab
        // must finish that erase rather than revive the previous session.
        const interruptedWipe = existing && offlineOwner() === existing.owner && offlineGeneration() > existing.generation;
        const wipe = (!!pendingWipe && (!existing || matches(existing, pendingWipe))) ||
          interruptedWipe || (!!previous && previous !== ns) || (needsMigration && v1Owner !== identity.email);
        const recoverSnapshots = pendingHints.length > 0 || (pendingPurge && matches(existing, pendingPurge)) ||
          (existing && offlineOwner() === existing.owner && offlineGeneration() === existing.generation &&
            (Number(localStorage.getItem(SNAPSHOTS_KEY)) || 0) > existing.snapshots);
        if (wipe) for (const name of [CACHE, QUEUE, ATTACHMENTS, DRAFTS, TOMBSTONES]) tx.objectStore(name).clear();
        else if (recoverSnapshots) {
          // Scoped hints survive failure before opening IDB; the shared epoch
          // is written only under matching transactional authority. Recover
          // either interruption with a cache-only erase before trusting mail.
          // Drafts and pending mutations remain in the current session.
          tx.objectStore(CACHE).clear();
        }
        recoveredPurgeHints = pendingHints;
        const generation = Math.max(existing?.generation ?? 0, expected.generation, interruptedWipe ? offlineGeneration() : 0) + (wipe || previous !== ns ? 1 : 0);
        result = { key: "session", owner: ns, generation, snapshots: Math.max(existing?.snapshots ?? 0, snapshotGeneration(), pendingSnapshots) + (wipe ? 1 : 0), legacyMigrated: true };
        tx.objectStore(META).put(result);
        if (needsMigration && v1Owner === identity.email && !wipe) {
          const queue = tx.objectStore(QUEUE);
          const rows = queue.getAll();
          rows.onsuccess = () => {
            for (const row of rows.result as Queued[]) {
              if (row.owner === v1Owner) queue.put({ ...row, owner: ns, key: row.key ?? row.id });
            }
          };
          const attachments = tx.objectStore(ATTACHMENTS).getAll();
          attachments.onsuccess = () => {
            const byDraft = new Map<string, unknown[]>();
            for (const row of attachments.result as DraftAttachmentRow[]) {
              if (row.owner === v1Owner && Array.isArray(row.files)) byDraft.set(row.id, row.files);
            }
            migrateDraftRing(tx.objectStore(DRAFTS), ns, byDraft);
            tx.objectStore(ATTACHMENTS).clear();
          };
        }
      };
    });
    if (version !== transitionVersion) throw new OfflineOwnerChangedError("A newer offline storage transition already started");
    if (!admitted || admitted.owner !== meta.owner || admitted.generation !== meta.generation) {
      draftRevisions.clear();
      if (admitted) resetPrivateState();
    }
    publishMeta(meta, identity.email);
    if (migrate) {
      localStorage.setItem(V2_KEY, "1");
      for (const key of Object.keys(localStorage)) {
        if (key === "es-drafts" || key.startsWith("es-draft-")) localStorage.removeItem(key);
      }
      localStorage.removeItem(OWNER);
    }
    for (const key of recoveredPurgeHints) localStorage.removeItem(key);
    if (wipePending === pendingWipe) wipePending = undefined;
    if (snapshotPurgePending === pendingPurge) snapshotPurgePending = undefined;
    storageSuspended = false;
  } catch (error) {
    if (version === transitionVersion) storageSuspended = true;
    throw error; // A failed migration retains every legacy source for retry.
  } finally { db.close(); }
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

export async function cacheResponse(path: string, value: unknown, gen = offlineGeneration(), snapshots = snapshotGeneration()): Promise<void> {
  if (storageSuspended || !generationCurrent(gen)) return;
  const owner = offlineOwner(); if (!owner || typeof indexedDB === "undefined") return;
  await transaction(CACHE, "readwrite", (store) => store.put({
    key: cacheKey(owner, path), owner, account: accountOf(path), savedAt: Date.now(), value,
  } as Cached), () => !storageSuspended && offlineOwner() === owner && generationCurrent(gen) && snapshotGeneration() === snapshots, context(gen, snapshots));
}

export async function cachedResponse<T>(path: string, gen = offlineGeneration(), snapshots = snapshotGeneration()): Promise<T | undefined> {
  if (storageSuspended || !generationCurrent(gen)) return undefined;
  const owner = offlineOwner(); if (!owner || typeof indexedDB === "undefined") return undefined;
  const item = await transaction<Cached | undefined>(CACHE, "readonly", (store) => store.get(cacheKey(owner, path)), undefined, context(gen, snapshots));
  if (storageSuspended || offlineOwner() !== owner || !generationCurrent(gen) || snapshotGeneration() !== snapshots) return undefined;
  return item?.value as T | undefined;
}

/** Disconnecting one mailbox removes its snapshots and the unified ones
 *  (they contain its mail); every other mailbox's lensed snapshots stay
 *  (audit 4 F12 + WEB-07's per-mailbox records). */
export async function purgeAccountSnapshots(accountId: string, mirrorAccountId?: string): Promise<void> {
  const ctx = context();
  if (!currentContext(ctx)) return;
  responseGeneration = snapshotGeneration() + 1;
  if (!ctx.owner || typeof indexedDB === "undefined") return;
  snapshotPurgePending = ctx;
  // Persist cleanup intent before IDB can fail to open. Separate scoped
  // keys avoid stale owners poisoning a newer session, and concurrent
  // purges cannot acknowledge/remove one another's still-pending intent.
  const hint = purgeHintPrefix(ctx) + newMutationKey();
  try { localStorage.setItem(hint, String(responseGeneration)); }
  catch { /* still attempt the authoritative disk erase */ }
  const ids = new Set([accountId, mirrorAccountId]);
  let committedSnapshots = responseGeneration;
  const purged = await transaction(CACHE, "readwrite", (store, tx) => {
    const meta = tx.objectStore(META).get("session");
    meta.onsuccess = () => {
      const row = meta.result as StorageMeta;
      committedSnapshots = Math.max(row.snapshots + 1, responseGeneration);
      snapshotPurgePending = ctx;
      try {
        // Other tabs fail closed even if the ensuing delete/commit fails.
        // This hint is written only after the authoritative owner check,
        // never by a stale tab before its transaction has been admitted.
        localStorage.setItem(SNAPSHOTS_KEY, String(committedSnapshots));
      } catch { /* the IDB commit can still erase the private rows */ }
      tx.objectStore(META).put({ ...row, snapshots: committedSnapshots });
    };
    const request = store.getAll();
    request.addEventListener("success", () => {
      for (const row of request.result as Cached[]) {
        if (row.owner === ctx.owner && (!mirrorAccountId || ids.has(row.account) || row.account === "")) store.delete(row.key);
      }
    });
    return request;
  }, undefined, ctx);
  // A stale/failed purge cannot acknowledge recovery or rewrite the hint.
  if (purged === undefined || !currentContext(ctx)) {
    localStorage.removeItem(hint);
    return;
  }
  responseGeneration = Math.max(snapshotGeneration(), committedSnapshots);
  localStorage.setItem(SNAPSHOTS_KEY, String(responseGeneration));
  localStorage.removeItem(hint);
  if (snapshotPurgePending === ctx) snapshotPurgePending = undefined;
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
  const saved = await transaction(QUEUE, "readwrite", (store) => store.put({ id, key: key ?? id, owner, path, method, body, queuedAt: Date.now() } as Queued));
  if (saved === undefined) throw new OfflineStorageError("The offline owner changed; the change was NOT saved for replay");
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
 * held for one whole replay pass, and a tab that cannot take it SKIPS —
 * the holder refreshes shared state when it finishes. Without the
 * server contract this would only have narrowed the duplicate window;
 * with api_mutations recorded server-side it is now safe coordination
 * rather than an implied guarantee.
 *
 * The callback's argument is load-bearing (audit 5 OFF-03): with
 * ifAvailable, a CONTENDED lock invokes it with null — passing `run`
 * through directly executed a second replay pass next to the holder's.
 * A lock-manager or worker failure propagates without running another
 * uncoordinated pass. Only an absent API uses the server-only fallback. */
const REPLAY_LOCK = "lullmail-offline-replay";
const REPLAY_ATTEMPT_TIMEOUT_MS = 30_000;

export async function withReplayLock<T>(run: () => Promise<T>): Promise<T | undefined> {
  const locks = (navigator as Navigator & {
    locks?: { request(name: string, options: { ifAvailable: true }, callback: (lock: unknown) => Promise<T>): Promise<T | undefined> };
  }).locks;
  if (!locks) {
    // No lock manager: the server's idempotency contract is the whole
    // coordination story; the pass runs uncoordinated.
    return run();
  }
  return locks.request(REPLAY_LOCK, { ifAvailable: true }, async (lock) => {
    if (lock === null) return undefined; // another tab holds the pass
    return run();
  });
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

/** A stalled network/body read must not hold every tab's replay lock
 * forever. Abort is an uncertain attempt, so the caller keeps the same
 * idempotency key and persists backoff instead of dropping the action. */
async function fetchReplay(item: Queued): Promise<{ response: Response; failureDetail?: string }> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), REPLAY_ATTEMPT_TIMEOUT_MS);
  try {
    const response = await fetch("/api" + item.path, { ...replayRequestInit(item), signal: controller.signal });
    let failureDetail: string | undefined;
    if (replayDecision(response.status) === "failed") {
      failureDetail = String(response.status);
      try {
        const problem = await response.json();
        failureDetail = problem.detail || problem.title || failureDetail;
      } catch { /* the definitive status survives an unreadable/stalled body */ }
    }
    return { response, failureDetail };
  } finally { clearTimeout(timer); }
}

/** One ordered replay pass over this owner's queue (oldest first, a
 *  backed-off head stops the pass — audit 4 F09). Runs under the
 *  cross-tab replay lock when the browser offers one. The generation is
 *  re-checked before EVERY send and every queue write: an owner switch
 *  mid-pass must stop the pass, never replay the previous owner's
 *  mutations under the new session (audit 5 OFF-02). */
async function replayDueMutations(): Promise<ReplaySummary> {
  const ctx = context();
  const all = await transaction<Queued[]>(QUEUE, "readonly", (store) => store.getAll(), undefined, ctx);
  if (!all || !currentContext(ctx)) return { committed: 0, rejected: 0 };
  const now = Date.now();
  let committed = 0;
  let rejected = 0;
  const plan = replayPlan(all, ctx.owner, now);
  // A persisted backoff from an earlier attempt must reach the driver's
  // scheduler, or a reload with nothing due schedules no retry until some
  // unrelated event fires (audit 5 OFF-04).
  let retryAt = plan.retryAt;
  for (const item of plan.due) {
    if (!currentContext(ctx)) break;
    const live = await transaction<Queued>(QUEUE, "readonly", (store) => store.get(item.id), undefined, ctx);
    if (!live || !currentContext(ctx)) break; // check authoritative session before each send
    let response: Response;
    let failureDetail: string | undefined;
    try {
      ({ response, failureDetail } = await fetchReplay(item));
    } catch {
      if (!currentContext(ctx)) break; // owner changed: no writeback
      // A network-level failure while navigator.onLine can still be true:
      // give the queue head a backoff slot and schedule the retry, or the
      // work strands until some later navigation or connectivity event
      // (audit 4 F10).
      const attempts = (item.attempts ?? 0) + 1;
      const delay = retryDelay(attempts, null);
      retryAt = Date.now() + delay;
      await transaction(QUEUE, "readwrite", (store) => store.put({ ...item, attempts, nextAttemptAt: retryAt }), undefined, ctx);
      break; // keep the rest queued behind this one, in order
    }
    const decision = replayDecision(response.status);
    if (decision === "reauth") break;
    if (decision === "retry") {
      if (!currentContext(ctx)) break;
      const attempts = (item.attempts ?? 0) + 1;
      const delay = retryDelay(attempts, response.headers.get("Retry-After"));
      retryAt = Date.now() + delay;
      await transaction(QUEUE, "readwrite", (store) => store.put({ ...item, attempts, nextAttemptAt: retryAt }), undefined, ctx);
      break; // keep the rest queued behind this one, in order
    }
    if (decision === "failed") {
      if (!currentContext(ctx)) break;
      // Permanently invalid: keep a marked record for visibility instead
      // of counting it as replayed, and let detail show what rejected it.
      const detail = failureDetail ?? String(response.status);
      console.warn("Offline action rejected by the server and dropped from retry:", item.path, detail);
      await transaction(QUEUE, "readwrite", (store) => store.put({ ...item, failed: detail }), undefined, ctx);
      rejected++;
      continue;
    }
    if (!currentContext(ctx)) break;
    await transaction(QUEUE, "readwrite", (store) => store.delete(item.id), undefined, ctx);
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
  const version = ++transitionVersion;
  const expected = wipePending ?? admitted ?? context();
  const stale = admitted && (admitted.owner !== offlineOwner() || admitted.generation !== offlineGeneration());
  // Tear down this tab before awaiting storage, but an old tab has no
  // authority to erase a newer admitted owner's durable state.
  storageSuspended = true;
  resetPrivateState();
  draftRevisions.clear();
  if (stale && !wipePending) return;
  const pendingWipe = expected;
  wipePending = pendingWipe;
  if (!stale) {
    responseGeneration = snapshotGeneration() + 1;
    localStorage.setItem(GEN_KEY, String(offlineGeneration() + 1));
    localStorage.setItem(SNAPSHOTS_KEY, String(responseGeneration));
  }
  if (typeof indexedDB === "undefined") {
    localStorage.removeItem(NS_KEY);
    localStorage.removeItem(EMAIL_KEY);
    wipePending = undefined;
    return;
  }
  const db = await openDB();
  try {
    const meta = await new Promise<StorageMeta | undefined>((resolve, reject) => {
      const tx = db.transaction([META, CACHE, QUEUE, ATTACHMENTS, DRAFTS, TOMBSTONES], "readwrite");
      let result: StorageMeta | undefined;
      tx.oncomplete = () => resolve(result);
      tx.onabort = () => reject(tx.error ?? new OfflineStorageError("Private-data reset aborted"));
      tx.onerror = () => { /* the abort handler owns rejection */ };
      const request = tx.objectStore(META).get("session");
      request.onsuccess = () => {
        const previous = request.result as StorageMeta | undefined;
        if (previous && !matches(previous, expected)) return;
        for (const name of [CACHE, QUEUE, ATTACHMENTS, DRAFTS, TOMBSTONES]) tx.objectStore(name).clear();
        result = { key: "session", owner: "", generation: Math.max((previous?.generation ?? 0) + 1, offlineGeneration()), snapshots: Math.max((previous?.snapshots ?? 0) + 1, responseGeneration), legacyMigrated: true };
        tx.objectStore(META).put(result);
      };
    });
    if (meta && version === transitionVersion) {
      publishMeta(meta);
      // A logout before first migration must not reimport these old drafts.
      localStorage.setItem(V2_KEY, "1");
      for (const key of Object.keys(localStorage)) {
        if (key === OWNER || key === "es-drafts" || key.startsWith("es-draft-")) localStorage.removeItem(key);
      }
    }
    if (wipePending === pendingWipe) wipePending = undefined;
  } finally { db.close(); }
}

/* ---- drafts: one record per draft, the ring in the same rows ---- */

/** Field edits merge into the draft's single record (get+put in one
 *  transaction, so a fields write can never clobber the attachments in
 *  the same row or vice versa). */
export async function saveDraftFields(id: string, fields: Partial<DraftRecord>, gen = offlineGeneration(), stillLive: () => boolean = () => true): Promise<boolean> {
  const ns = offlineOwner(); if (storageSuspended || !generationCurrent(gen) || !ns || typeof indexedDB === "undefined") return false;
  const current = () => !storageSuspended && offlineOwner() === ns && generationCurrent(gen) && stillLive();
  const db = await openDB();
  try {
    if (!current()) return false;
    return await new Promise<boolean>((resolve, reject) => {
      const tx = db.transaction([META, DRAFTS, TOMBSTONES], "readwrite");
      let savedRevision: number | undefined;
      let failure: Error | undefined;
      tx.oncomplete = () => {
        if (savedRevision !== undefined && current()) draftRevisions.set(ns + "\n" + id, savedRevision);
        resolve(savedRevision !== undefined && current());
      };
      tx.onabort = () => reject(failure ?? tx.error ?? new OfflineStorageError("Draft save aborted"));
      const store = tx.objectStore(DRAFTS);
      const meta = tx.objectStore(META).get("session");
      meta.onsuccess = () => {
        if (!current() || !matches(meta.result, { owner: ns, generation: gen })) return;
        const tombstone = tx.objectStore(TOMBSTONES).get(id);
        tombstone.onsuccess = () => {
          if (tombstone.result) {
            failure = new OfflineStorageError("This draft was sent or discarded in another tab");
            tx.abort(); return;
          }
          const existing = store.get(id);
          existing.onsuccess = () => {
            if (!current()) return;
            const row = existing.result as DraftRecord | undefined;
            if (row && row.ns !== ns) return;
            const previousRevision = row?.revision ?? 0;
            const expectedRevision = draftRevisions.get(ns + "\n" + id);
            if (row && expectedRevision !== previousRevision) {
              failure = new OfflineStorageError("This draft changed in another tab; reload before editing it");
              tx.abort(); return;
            }
            // Hydration and pagehide may flush an unchanged draft. They
            // must not manufacture a conflict for a real edit in another tab.
            // Check CAS first: a stale partial no-op must not acknowledge an
            // unseen newer revision and authorize a later stale full save.
            if (row && Object.entries(fields).every(([key, value]) => key === "revision" || JSON.stringify(value) === JSON.stringify(row[key as keyof DraftRecord]))) {
              savedRevision = previousRevision;
              return;
            }
            const revision = previousRevision + 1;
            savedRevision = revision;
            const seq = row?.seq ?? Date.now();
            store.put({ ...row, ...fields, id, ns, seq, revision, savedAt: Date.now() } as DraftRecord);
          };
        };
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
  const ns = offlineOwner(), gen = offlineGeneration();
  if (storageSuspended || !ns || typeof indexedDB === "undefined") return [];
  const rows = await transaction<DraftRecord[]>(DRAFTS, "readonly", (store) => store.getAll());
  if (storageSuspended || offlineOwner() !== ns || !generationCurrent(gen)) return [];
  const owned = (rows ?? []).filter((row) => row && row.ns === ns).sort((a, b) => a.seq - b.seq);
  for (const row of owned) {
    const key = ns + "\n" + row.id;
    // A background read is not permission to overwrite a newer revision
    // from an existing live editor. Reloading the page starts a fresh map.
    if (!draftRevisions.has(key)) draftRevisions.set(key, row.revision ?? 0);
  }
  return owned;
}

export async function deleteDraft(id: string, gen = offlineGeneration()): Promise<void> {
  const ns = offlineOwner();
  if (storageSuspended || !ns || typeof indexedDB === "undefined") return;
  await transaction(DRAFTS, "readwrite", (store, tx) => {
    const request = store.get(id);
    request.addEventListener("success", () => {
      if (storageSuspended || offlineOwner() !== ns || !generationCurrent(gen)) return;
      const row = request.result as DraftRecord | undefined;
      if (row && (row.ns !== ns || draftRevisions.get(ns + "\n" + id) !== (row.revision ?? 0))) {
        tx.abort(); return; // a stale discard/send must preserve the newer edit
      }
      tx.objectStore(TOMBSTONES).put({ id, ns, deletedAt: Date.now() });
      store.delete(id);
    });
    return request;
  },
    () => !storageSuspended && offlineOwner() === ns && generationCurrent(gen));
}

export function startOfflineData(authenticated: () => boolean = () => true): () => void {
  // Replayed state refreshes whatever is on screen; the persisted offline
  // snapshots STAY — a mutation is invalidation, not a reason to delete
  // the only offline copy of the mailbox (audit 3 WEB-06). Fresh GETs
  // overwrite the snapshots they replace.
  //
  // Replay waits for a CONFIRMED authenticated session (audit 5 OFF-02):
  // the pass used to start before refreshAuth resolved, so a previous
  // owner's queued mutations could replay under whatever session cookie
  // the browser holds before the server confirmed the namespace matches.
  let timer: number | undefined;
  // An in-flight replay must not schedule a new timer after the cleanup
  // function has run: unmount would leave an orphaned retry loop (audit
  // 4 F10).
  let stopped = false;
  const replay = () => {
    if (stopped) return;
    if (!authenticated()) {
      // Not yet confirmed: the auth refresh re-triggers replay via its
      // own refresh path once the session is known.
      return;
    }
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
    const delay = Math.max(0, Math.min(at - Date.now(), 2_147_000_000)); // timer-range cap
    // A transient failure while still online must schedule its own next
    // attempt instead of waiting for a navigation or network transition
    // (audit 3 WEB-05).
    timer = window.setTimeout(() => {
      timer = undefined;
      if (navigator.onLine) replay();
    }, delay);
  };
  window.addEventListener("online", replay); replay();
  const retryWhenAuthenticated = () => {
    if (!stopped && authenticated()) replay();
  };
  window.addEventListener("lullmail-auth-refreshed", retryWhenAuthenticated);
  return () => {
    stopped = true;
    window.removeEventListener("online", replay);
    window.removeEventListener("lullmail-auth-refreshed", retryWhenAuthenticated);
    if (timer !== undefined) window.clearTimeout(timer);
  };
}


// Storage events are a UI invalidation hint, never the transaction fence.
// Pinning the admitted owner also blocks an old tab that observes the new
// markers synchronously before this event has been delivered.
if (typeof window !== "undefined") {
  window.addEventListener("storage", (event) => {
    if (![NS_KEY, GEN_KEY, SNAPSHOTS_KEY, null].includes(event.key)) return;
    if (admitted && (admitted.owner !== offlineOwner() || admitted.generation !== offlineGeneration())) {
      suspendOfflineStorage();
      resetPrivateState();
      window.dispatchEvent(new Event("lullmail-offline-invalidated"));
    }
  });
}
