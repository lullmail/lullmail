// One fetching hook for every view: aborts superseded requests, exposes a
// reload, and registers that reload as the app-wide one so any action can
// refresh whatever view is on screen.
import { useCallback, useEffect, useMemo, useRef, useState } from "preact/hooks";
import { ApiError, api, authed, isAbort } from "./api";
import { setReloader } from "./actions";

export interface Load<T> {
  data: T | null;
  loading: boolean;
  error: string | null;
  reload: () => void;
}

interface LoadRequest {
  scope: object;
  controller: AbortController;
}

interface LoadResult<T> {
  request: LoadRequest;
  data: T;
}

interface LoadState<T> {
  request: LoadRequest;
  result: LoadResult<T> | null;
  loading: boolean;
  error: string | null;
}

/** Keep scope and request ownership separate: a refresh may retain the
 * current scope's answer, but neither a new scope nor an older request
 * may publish it. Pagination shares the first request's lifetime. */
function useLoader<T>(key: string, fn: (signal: AbortSignal) => Promise<T>) {
  const authenticated = authed.value;
  const scope = useMemo(() => ({}), [key, authenticated]);
  const [nonce, setNonce] = useState(0);
  const request = useMemo<LoadRequest>(() => ({ scope, controller: new AbortController() }), [scope, nonce]);
  const active = useRef<LoadRequest | null>(request);
  // Fence completions during the new render, before effect cleanup runs.
  active.current = request;
  const [state, setState] = useState<LoadState<T>>({ request, result: null, loading: true, error: null });
  const ownsRequest = useCallback(() => active.current === request && !request.controller.signal.aborted, [request]);

  const reload = useCallback(() => {
    const previous = active.current;
    if (!previous) return;
    // Invalidate synchronously: global reloads and repeated events must not
    // leave a window for an old continuation before the next render.
    active.current = null;
    previous.controller.abort();
    setNonce((n) => n + 1);
  }, []);

  useEffect(() => {
    setState((previous) => ({
      request, result: previous.result?.request.scope === scope ? previous.result : null,
      loading: true, error: null,
    }));
    const load = async () => {
      try {
        const data = await fn(request.controller.signal);
        if (!ownsRequest()) return;
        // A new result identity also distinguishes refreshes whose loader
        // returns the same cached object.
        setState({ request, result: { request, data }, loading: false, error: null });
      } catch (e) {
        if (!ownsRequest()) return;
        // 401 flips the app to the gate; don't flash a view-level error.
        const error = isAbort(e) || (e instanceof ApiError && e.status === 401)
          ? null : e instanceof Error ? e.message : "Something went wrong";
        setState((previous) => ({ ...previous, request, loading: false, error }));
      }
    };
    void load();
    return () => {
      if (active.current === request) active.current = null;
      request.controller.abort();
    };
    // `key` is the caller's declaration of what the request depends on.
  }, [request]);

  useEffect(() => { setReloader(reload); }, [reload]);

  // Effects run after rendering. Never label the previous scope's data or
  // error as the answer to a new key, even for that first render.
  const result = state.result?.request.scope === scope ? state.result : null;
  return {
    data: result?.data ?? null, result, request, ownsRequest, reload,
    loading: state.request !== request || state.loading,
    error: state.request === request ? state.error : null,
  };
}

export function useLoad<T>(key: string, fn: (signal: AbortSignal) => Promise<T>): Load<T> {
  const { data, loading, error, reload } = useLoader(key, fn);
  return { data, loading, error, reload };
}

/** The keyset-paginated envelope every list endpoint answers (audit
 *  DATA-06): rows, whether more exist, and the continuation for the
 *  next page when they do. */
export interface Page<T> {
  rows: T[];
  has_more: boolean;
  next_cursor?: string;
}

export interface Paged<T> {
  rows: T[];
  loading: boolean;
  error: string | null;
  hasMore: boolean;
  loadingMore: boolean;
  loadMore: () => void;
  reload: () => void;
}

interface Pages<T> {
  base: LoadResult<Page<T>> | null;
  rows: T[];
  cursor: string | undefined;
  pending: object | null;
  error: string | null;
}

/** A paged list surface. A refresh keeps the current rows while pending,
 * then collapses to its new first page. Every continuation belongs to
 * that exact first-page request, including its abort/unmount fence. */
export function usePaged<T>(key: string, pathFor: (cursor: string | undefined) => string): Paged<T> {
  const { result, request, ownsRequest, loading, error, reload } = useLoader<Page<T>>(key, (signal) =>
    api<Page<T>>(pathFor(undefined), { signal })
  );
  const firstPage = useMemo<Pages<T>>(() => ({
    base: result, rows: result?.data.rows ?? [],
    cursor: result?.data.has_more ? result.data.next_cursor : undefined,
    pending: null, error: null,
  }), [result]);
  const [pages, setPages] = useState(firstPage);
  const visible = pages.base === result ? pages : firstPage;
  // The ref is the immediate cursor/lock; state schedules rendering. Two
  // calls in the same event cannot request or append the same page twice.
  const current = useRef(visible);
  current.current = visible;
  const canPage = !loading && result?.request === request;

  const loadMore = useCallback(() => {
    const previous = current.current;
    if (!canPage || !ownsRequest() || previous.base !== result || !previous.cursor || previous.pending) return;
    const pending = {};
    const update = (next: Pages<T>) => { current.current = next; setPages(next); };
    update({ ...previous, pending, error: null });
    const ownsPage = () => ownsRequest() && current.current.base === result && current.current.pending === pending;
    const load = async () => {
      try {
        const page = await api<Page<T>>(pathFor(previous.cursor), { signal: request.controller.signal });
        if (!ownsPage()) return;
        update({
          base: result, rows: [...previous.rows, ...page.rows],
          cursor: page.has_more ? page.next_cursor : undefined, pending: null, error: null,
        });
      } catch (e) {
        if (!ownsPage()) return;
        // Retain the cursor and rows so the same button can retry.
        const error = isAbort(e) || (e instanceof ApiError && e.status === 401)
          ? null : e instanceof Error ? e.message : "Something went wrong";
        update({ ...previous, pending: null, error });
      }
    };
    void load();
  }, [canPage, result, request, ownsRequest, pathFor]);

  // Rows retain their identity until an actual page changes. Publishing
  // effects must not repeatedly overwrite optimistic row/selection state.
  return {
    rows: visible.rows, loading, error: error ?? (canPage ? visible.error : null),
    hasMore: canPage && !!visible.cursor,
    loadingMore: canPage && !!visible.pending,
    loadMore, reload,
  };
}
