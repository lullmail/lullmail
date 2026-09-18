// One fetching hook for every view: aborts superseded requests, exposes a
// reload, and registers that reload as the app-wide one so any action can
// refresh whatever view is on screen.
import { useCallback, useEffect, useRef, useState } from "preact/hooks";
import { ApiError, api, isAbort } from "./api";
import { setReloader } from "./actions";
import { authed } from "./api";

export interface Load<T> {
  data: T | null;
  loading: boolean;
  error: string | null;
  reload: () => void;
}

export function useLoad<T>(key: string, fn: (signal: AbortSignal) => Promise<T>): Load<T> {
  const [data, setData] = useState<T | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [nonce, setNonce] = useState(0);
  const prevKey = useRef(key);

  const reload = useCallback(() => setNonce((n) => n + 1), []);

  useEffect(() => {
    const ac = new AbortController();
    let live = true;
    // A new key is a new question (lens switch, route change): the previous
    // answer must not sit on screen — actionable but wrong — while the
    // replacement loads.
    if (prevKey.current !== key) {
      prevKey.current = key;
      setData(null);
    }
    setLoading(true);
    setError(null);
    fn(ac.signal)
      .then((res) => {
        if (!live) return;
        setData(res);
        setLoading(false);
      })
      .catch((e) => {
        if (!live || isAbort(e)) return;
        // 401 flips the whole app to the gate; a view-level error would just
        // flash behind it.
        if (e instanceof ApiError && e.status === 401) return;
        setError(e instanceof Error ? e.message : "Something went wrong");
        setLoading(false);
      });
    return () => { live = false; ac.abort(); };
    // `key` is the caller's declaration of what the request depends on.
  }, [key, nonce, authed.value]);

  useEffect(() => { setReloader(reload); }, [reload]);

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

/** A paged list surface: page one flows through useLoad (so mutations
 *  refresh it and superseded fetches abort), appended pages ride local
 *  state, and "Load more" advances the server's keyset cursor. A reload
 *  collapses back to page one — fresh truth beats a stale accumulation,
 *  and the cursor keeps pages stable under new arrivals. */
export function usePaged<T>(key: string, pathFor: (cursor: string | undefined) => string): Paged<T> {
  const { data, loading, error, reload } = useLoad<Page<T>>(key, (signal) =>
    api<Page<T>>(pathFor(undefined), { signal })
  );
  const [tail, setTail] = useState<T[]>([]);
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  const [loadingMore, setLoadingMore] = useState(false);

  useEffect(() => {
    setTail([]);
    setCursor(data?.next_cursor);
  }, [data]);

  const loadMore = useCallback(() => {
    if (!cursor || loadingMore) return;
    setLoadingMore(true);
    api<Page<T>>(pathFor(cursor))
      .then((page) => {
        setTail((current) => [...current, ...page.rows]);
        setCursor(page.next_cursor);
      })
      .catch(() => { /* the button stays; pressing it retries */ })
      .finally(() => setLoadingMore(false));
    // pathFor is stable per view render scope by construction (it closes
    // over the view's key inputs, which also change `key`).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cursor, loadingMore, key]);

  const rows = [...(data?.rows ?? []), ...tail];
  return { rows, loading, error, hasMore: cursor !== undefined, loadingMore, loadMore, reload };
}
