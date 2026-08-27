// One fetching hook for every view: aborts superseded requests, exposes a
// reload, and registers that reload as the app-wide one so any action can
// refresh whatever view is on screen.
import { useCallback, useEffect, useRef, useState } from "preact/hooks";
import { ApiError, isAbort } from "./api";
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
