// The one place that talks to the server. Product calls use the HttpOnly
// session cookie; JavaScript never sees a long-lived authentication secret.
import { signal } from "@preact/signals";
import { cacheResponse, cachedResponse, canQueue, clearResponseCache, prepareOfflineOwner, queueMutation } from "./offline";

export const authed = signal(false);
export const authReady = signal(false);

export interface AuthStatus {
  configured: boolean;
  authenticated: boolean;
  email: string;
  bootstrap_available: boolean;
  passkey_supported: boolean;
  /** First-run only: where the server believes the browser is, shown so a
   *  wrong proxy header is visible before a passkey is bound to it. */
  detected_origin?: string;
  /** How this session was created: "passkey" | "recovery" | "totp" |
   *  "bootstrap". Recovery/TOTP sessions get an add-a-passkey nudge. */
  via?: string;
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

interface Opts {
  method?: string;
  body?: unknown;
  signal?: AbortSignal;
  /** Skip the short in-memory route cache for counters and explicit refreshes. */
  fresh?: boolean;
}

const MEMORY_TTL = 15_000;
const memoryResponses = new Map<string, { savedAt: number; value: unknown }>();

function copyValue<T>(value: T): T {
  return typeof structuredClone === "function" ? structuredClone(value) : value;
}

async function request<T>(path: string, opts: Opts = {}, setupToken = "", protectedRoute = true): Promise<T> {
  const headers: Record<string, string> = {};
  if (setupToken) headers.Authorization = "Bearer " + setupToken;
  let body: string | undefined;
  if (opts.body !== undefined) {
    headers["Content-Type"] = "application/json";
    body = JSON.stringify(opts.body);
  }
  const method = opts.method || (body ? "POST" : "GET");
  if (protectedRoute && method === "GET" && !opts.fresh) {
    const cached = memoryResponses.get(path);
    if (cached && Date.now() - cached.savedAt < MEMORY_TTL) return copyValue(cached.value as T);
    if (cached) memoryResponses.delete(path);
  }
  let res: Response;
  try {
    res = await fetch("/api" + path, { method, headers, body, signal: opts.signal, credentials: "same-origin" });
  } catch (error) {
    if (opts.signal?.aborted) throw error;
    if (protectedRoute && method === "GET") {
      const cached = await cachedResponse<T>(path);
      if (cached !== undefined) return cached;
    }
    if (protectedRoute && canQueue(path, method)) {
      await queueMutation(path, method, opts.body);
      return { queued: true } as T;
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
  if (protectedRoute && method === "GET") {
    memoryResponses.set(path, { savedAt: Date.now(), value: copyValue(value) });
    cacheResponse(path, value).catch(() => {});
  } else if (protectedRoute && res.ok) {
    memoryResponses.clear();
    clearResponseCache().catch(() => {});
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
    authStatus.value = status;
    authed.value = status.authenticated;
    if (!status.authenticated) memoryResponses.clear();
    if (status.authenticated && status.email) await prepareOfflineOwner(status.email);
    return status;
  } finally {
    authReady.value = true;
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
