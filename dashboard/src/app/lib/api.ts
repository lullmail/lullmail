// The one place that talks to the server. Product calls use the HttpOnly
// session cookie; JavaScript never sees a long-lived authentication secret.
import { signal } from "@preact/signals";
import {
  cacheResponse, cachedResponse, canQueue, generationCurrent, newMutationKey, offlineEmail,
  offlineGeneration, offlineOwner, prepareOfflineOwner, queueMutation, suspendOfflineStorage,
} from "./offline";

export const authed = signal(false);
export const authReady = signal(false);
/** The server could not be reached (network failure or a 5xx from the proxy).
 *  Distinct from signed-out: an unreachable server must never show the gate. */
export const unreachable = signal(false);

export interface AuthStatus {
  configured: boolean;
  authenticated: boolean;
  email: string;
  bootstrap_available: boolean;
  passkey_supported: boolean;
  /** Durable offline namespace pair (audit WEB-07): the deployment's
   *  installation id and the user's id. All offline storage is keyed by
   *  the pair, never the (mutable, reusable) email. */
  installation_id?: string;
  user_id?: string;
  /** First-run only: where the server believes the browser is, shown so a
   *  wrong proxy header is visible before a passkey is bound to it. */
  detected_origin?: string;
  /** How this session was created: "passkey" | "password" | "recovery" |
   *  "totp" | "bootstrap". Only recovery sessions get the add-a-passkey
   *  nudge — password and TOTP are chosen sign-in methods, not fallbacks. */
  via?: string;
  /** True when the pinned origin is reachable from the public internet
   *  (not loopback, LAN, or a Tailnet). Warn-only; nothing is enforced. */
  exposed?: boolean;
}
export const authStatus = signal<AuthStatus | null>(null);

/** Thrown for any non-2xx. `unauthorized` is handled by the shell, not views. */
export class ApiError extends Error {
  status: number;
  constructor(message: string, status: number) {
    super(message);
    this.status = status;
  }
}

/** Thrown when a mutation could not reach the server and was queued for
 *  offline replay instead. It has NOT committed: callers must not treat a
 *  fulfilled promise as a server-side change or build success/undo
 *  feedback on top of it (audit 3 WEB-07). */
export class QueuedOffline extends Error {
  constructor() {
    super("saved offline — will apply when the connection returns");
  }
}

interface Opts {
  method?: string;
  body?: unknown;
  signal?: AbortSignal;
  /** Skip the short in-memory route cache for counters and explicit refreshes. */
  fresh?: boolean;
}

const MEMORY_TTL = 15_000;
const memoryResponses = new Map<string, { savedAt: number; value: unknown }>();

export function clearMemoryCache() {
  memoryResponses.clear();
}

/** The memory cache key is owner+generation-scoped (audit 5 OFF-02): a
 *  route-only key let owner B's first read hit owner A's cached private
 *  response after an in-browser owner switch, and nothing tied entries
 *  to the generation that fetched them. */
function memoryKey(owner: string, gen: number, path: string): string {
  return owner + "\n" + gen + "\n" + path;
}

function copyValue<T>(value: T): T {
  return typeof structuredClone === "function" ? structuredClone(value) : value;
}

/** A stale owner snapshot — the fetch began under one owner/generation
 *  and the device is now under another. The private response must not be
 *  published to the new owner's UI, cached, or queued (audit 5 OFF-02). */
export class StaleOwnerError extends Error {
  constructor() {
    super("the account changed during the operation");
  }
}

function assertOwner(owner: string, gen: number): void {
  // No prepared offline namespace (fresh page before auth, or a test
  // environment): there is no owner identity to fence on.
  if (!owner) return;
  if (offlineOwner() !== owner || !generationCurrent(gen)) {
    throw new StaleOwnerError();
  }
}

async function request<T>(path: string, opts: Opts = {}, setupToken = "", protectedRoute = true): Promise<T> {
  const headers: Record<string, string> = {};
  if (setupToken) headers.Authorization = "Bearer " + setupToken;
  // The idempotency key is minted BEFORE the first attempt (audit
  // WEB-04): a request whose response never arrived may still have been
  // applied server-side, and replaying under the SAME key returns the
  // recorded answer instead of applying the change twice.
  const queueable = protectedRoute && canQueue(path, opts.method || (opts.body !== undefined ? "POST" : "GET"));
  const idempotencyKey = queueable ? newMutationKey() : undefined;
  if (idempotencyKey) headers["Idempotency-Key"] = idempotencyKey;
  // The owner snapshot fences this whole operation against an owner
  // switch: if the owner or generation changes while the request is in
  // flight, the result is discarded — never cached, never queued, never
  // returned to the caller as the new owner's data (audit WEB-07/R08,
  // 5 OFF-02).
  const owner = offlineOwner();
  const gen = offlineGeneration();
  let body: string | undefined;
  if (opts.body !== undefined) {
    headers["Content-Type"] = "application/json";
    body = JSON.stringify(opts.body);
  }
  const method = opts.method || (body ? "POST" : "GET");
  if (protectedRoute && method === "GET" && !opts.fresh) {
    const cached = memoryResponses.get(memoryKey(owner, gen, path));
    if (cached && Date.now() - cached.savedAt < MEMORY_TTL) return copyValue(cached.value as T);
    if (cached) memoryResponses.delete(memoryKey(owner, gen, path));
  }
  let res: Response;
  try {
    res = await fetch("/api" + path, { method, headers, body, signal: opts.signal, credentials: "same-origin" });
  } catch (error) {
    if (opts.signal?.aborted) throw error;
    if (protectedRoute && method === "GET") {
      const cached = await cachedResponse<T>(path, gen);
      if (cached !== undefined) return cached;
    }
    if (queueable) {
      // The failed mutation is queued under the owner that INTENDED it:
      // an owner switch during the flight used to queue it under the new
      // owner (audit 5 OFF-02).
      assertOwner(owner, gen);
      await queueMutation(path, method, opts.body, idempotencyKey);
      throw new QueuedOffline();
    }
    throw error;
  }
  if (res.status === 401 && protectedRoute) {
    authed.value = false;
    throw new ApiError("unauthorized", 401);
  }
  if (res.status === 204) return null as T;
  if (!res.ok) {
    let detail = String(res.status);
    try {
      const p = await res.json();
      detail = p.detail || p.title || detail;
    } catch {
      /* non-JSON error body: the status is all we have */
    }
    throw new ApiError(detail, res.status);
  }
  const value = (await res.json()) as T;
  // A late private response is rejected before publication, not merely
  // before caching: returning it handed the previous owner's data to
  // whoever is on screen now (audit 5 OFF-02).
  if (protectedRoute) assertOwner(owner, gen);
  if (protectedRoute && method === "GET") {
    memoryResponses.set(memoryKey(owner, gen, path), { savedAt: Date.now(), value: copyValue(value) });
    cacheResponse(path, value, gen).catch(() => {});
  } else {
    // A mutation invalidates the in-memory cache only. The persisted
    // offline snapshots are the ONLY offline copy of the mailbox —
    // deleting them on every note or read-state change made the next
    // offline visit start empty. Fresh GETs overwrite the snapshots they
    // replace (audit 3 WEB-06).
    memoryResponses.clear();
  }
  return value;
}

export function api<T>(path: string, opts: Opts = {}): Promise<T> {
  return request<T>(path, opts, "", true);
}

export function authApi<T>(path: string, opts: Opts = {}, setupToken = ""): Promise<T> {
  return request<T>(path, opts, setupToken, false);
}

export async function refreshAuth(): Promise<AuthStatus> {
  try {
    const status = await authApi<AuthStatus>("/auth/status");
    unreachable.value = false;
    authStatus.value = status;
    authed.value = status.authenticated;
    if (!status.authenticated) memoryResponses.clear();
    if (status.authenticated && status.email) {
      const previousOwner = offlineOwner();
      try {
        await prepareOfflineOwner(status);
      } catch (storageError) {
        // A storage failure is not an unreachable server: the session is
        // known-good, only offline persistence is disabled (audit 3 WEB-08).
        // It is also not permission to keep using whatever namespace the
        // old localStorage marker names — most dangerously after an owner
        // change whose wipe did not commit. Suspend fail-closed until a
        // later successful prepare (audit 4 F11).
        suspendOfflineStorage();
        console.warn("Offline storage unavailable; offline mailbox disabled", storageError);
      }
      // An owner switch starts a fresh memory namespace; the old owner's
      // entries must not linger behind their TTL (audit 5 OFF-02).
      if (offlineOwner() !== previousOwner) memoryResponses.clear();
    }
    return status;
  } catch (error) {
    if (error instanceof ApiError && error.status < 500) throw error;
    // Network failure or proxy 5xx: nothing is known about the session, so
    // nothing changes. A previous owner on this device keeps the offline
    // mailbox open; a fresh device waits for the server rather than being
    // told to sign in to something it cannot reach.
    unreachable.value = true;
    const owner = offlineEmail();
    if (owner && !authStatus.value) {
      authStatus.value = { configured: true, authenticated: true, email: owner, bootstrap_available: false, passkey_supported: true };
      authed.value = true;
    }
    throw error;
  } finally {
    authReady.value = true;
    // The offline replay driver waits for this confirmation before its
    // first pass (audit 5 OFF-02).
    try { window.dispatchEvent(new Event("lullmail-auth-refreshed")); } catch { /* non-browser */ }
  }
}

/** Download an authenticated response without putting the bearer token in a
    URL. Used for attachments and trust exports alike. */
interface DownloadOpts {
  /** Chromium's File System Access API lets large exports reach disk without
      first occupying the tab's memory. Other browsers keep the Blob fallback. */
  streamToDisk?: boolean;
  onProgress?: (received: number, total: number) => void;
}

interface WritableFileLike {
  write(chunk: Uint8Array): Promise<void>;
  close(): Promise<void>;
  abort(reason?: unknown): Promise<void>;
}

interface FileHandleLike {
  createWritable(): Promise<WritableFileLike>;
}

export async function download(path: string, fallbackName: string, opts: DownloadOpts = {}): Promise<void> {
  let fileHandle: FileHandleLike | null = null;
  const picker = (window as typeof window & {
    showSaveFilePicker?: (options: { suggestedName: string }) => Promise<FileHandleLike>;
  }).showSaveFilePicker;
  if (opts.streamToDisk && picker) {
    // Ask while the click still carries user activation; browsers refuse this
    // prompt after the network request has awaited.
    fileHandle = await picker({ suggestedName: fallbackName });
  }

  const res = await fetch("/api" + path, {
    credentials: "same-origin",
  });
  if (res.status === 401) {
    authed.value = false;
    throw new ApiError("unauthorized", 401);
  }
  if (!res.ok) {
    let detail = "HTTP " + res.status;
    try {
      const p = await res.json();
      detail = p.detail || p.title || detail;
    } catch {
      /* a download may fail with a provider's plain-text response */
    }
    throw new ApiError(detail, res.status);
  }

  let filename = fallbackName;
  const disposition = res.headers.get("Content-Disposition") || "";
  const encoded = disposition.match(/filename\*=UTF-8''([^;]+)/i)?.[1];
  const plain = disposition.match(/filename="?([^";]+)"?/i)?.[1];
  try {
    filename = encoded ? decodeURIComponent(encoded) : plain || fallbackName;
  } catch {
    filename = plain || fallbackName;
  }

  const total = Number(res.headers.get("Content-Length")) || 0;
  if (fileHandle && res.body) {
    const writable = await fileHandle.createWritable();
    const reader = res.body.getReader();
    let received = 0;
    try {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        await writable.write(value);
        received += value.byteLength;
        opts.onProgress?.(received, total);
      }
      await writable.close();
      return;
    } catch (error) {
      await writable.abort(error);
      throw error;
    }
  }

  const blob = await res.blob();
  opts.onProgress?.(blob.size, total || blob.size);
  const objectURL = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = objectURL;
  anchor.download = filename;
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  setTimeout(() => URL.revokeObjectURL(objectURL), 5000);
}

/** True for the aborts we cause ourselves by superseding a request. */
export function isAbort(e: unknown): boolean {
  return e instanceof DOMException && e.name === "AbortError";
}
