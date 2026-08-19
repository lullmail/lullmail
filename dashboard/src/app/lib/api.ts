// The one place that talks to the server. Every call carries the dev bearer
// token; a 401 flips the app into the gate rather than throwing at a view.
import { signal } from "@preact/signals";

const KEY = "es_token";
const canStore = typeof window !== "undefined";

export const authed = signal<boolean>(canStore ? !!localStorage.getItem(KEY) : false);

export function token(): string {
  return canStore ? localStorage.getItem(KEY) || "" : "";
}

export function setToken(t: string) {
  if (!canStore) return;
  localStorage.setItem(KEY, t);
  authed.value = !!t;
}

export function clearToken() {
  if (!canStore) return;
  localStorage.removeItem(KEY);
  authed.value = false;
}

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
}

export async function api<T>(path: string, opts: Opts = {}): Promise<T> {
  const headers: Record<string, string> = { Authorization: "Bearer " + token() };
  let body: string | undefined;
  if (opts.body !== undefined) {
    headers["Content-Type"] = "application/json";
    body = JSON.stringify(opts.body);
  }
  const res = await fetch("/api" + path, {
    method: opts.method || (body ? "POST" : "GET"),
    headers,
    body,
    signal: opts.signal,
  });
  if (res.status === 401) {
    clearToken();
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
  return (await res.json()) as T;
}

/** True for the aborts we cause ourselves by superseding a request. */
export function isAbort(e: unknown): boolean {
  return e instanceof DOMException && e.name === "AbortError";
}
