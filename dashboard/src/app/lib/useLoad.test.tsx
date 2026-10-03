// @vitest-environment jsdom
import { render } from "preact";
import { useEffect } from "preact/hooks";
import { act } from "preact/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("./actions", () => ({ setReloader: vi.fn() }));
vi.mock("./api", async () => {
  const { signal } = await import("@preact/signals");
  return {
    api: vi.fn(), authed: signal(true),
    ApiError: class extends Error { constructor(message: string, public status: number) { super(message); } },
    isAbort: (error: unknown) => error instanceof Error && error.name === "AbortError",
  };
});

import { api, authed } from "./api";
import { setReloader } from "./actions";
import { useLoad, usePaged, type Load, type Page, type Paged } from "./useLoad";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

type Request = ReturnType<typeof deferred<unknown>> & { path: string; signal: AbortSignal };
let requests: Request[];
let host: HTMLDivElement;
let loaded: Load<string>;
let paged: Paged<string>;
let loadRenders: { scope: string; data: string | null; loading: boolean; error: string | null }[];
let pageRenders: { scope: string; rows: string[]; loading: boolean }[];
let published: { scope: string; rows: string[] }[];

function Loader({ scope }: { scope: string }) {
  loaded = useLoad(scope, (signal) => api<string>(scope, { signal }));
  loadRenders.push({ scope, data: loaded.data, loading: loaded.loading, error: loaded.error });
  return <div>{loaded.data}</div>;
}

function Pager({ scope, tick = 0 }: { scope: string; tick?: number }) {
  paged = usePaged<string>(scope, (cursor) => scope + (cursor ? ":" + cursor : ""));
  pageRenders.push({ scope, rows: paged.rows, loading: paged.loading });
  const { rows } = paged;
  useEffect(() => { published.push({ scope, rows }); }, [scope, rows]);
  return <div data-tick={tick}>{rows.join(",")}<button onClick={paged.loadMore}>More</button></div>;
}

function page(rows: string[], cursor?: string): Page<string> {
  return { rows, has_more: !!cursor, next_cursor: cursor };
}

async function resolve(request: Request, value: unknown) {
  await act(async () => { request.resolve(value); await request.promise; });
}

async function reject(request: Request, error = new Error("Try again")) {
  await act(async () => { request.reject(error); await request.promise.catch(() => {}); });
}

function mountPager(scope: string, tick = 0) {
  act(() => render(<Pager scope={scope} tick={tick} />, host));
}

beforeEach(() => {
  vi.useFakeTimers();
  requests = [];
  host = document.createElement("div");
  loadRenders = [];
  pageRenders = [];
  published = [];
  authed.value = true;
  vi.clearAllMocks();
  vi.mocked(api).mockImplementation((path, options) => {
    const response = deferred<unknown>();
    // Deliberately ignore abort: a transport/cache that resolves late still
    // must not publish outside its request's lifetime.
    requests.push({ ...response, path, signal: options!.signal! });
    return response.promise;
  });
});

afterEach(() => {
  act(() => render(null, host));
  vi.runOnlyPendingTimers();
  vi.useRealTimers();
});

describe("useLoad scope ownership", () => {
  it("hides the previous answer on the very first render of a new key", async () => {
    act(() => render(<Loader scope="account:A" />, host));
    await resolve(requests[0], "A answer");
    act(() => render(<Loader scope="account:B" />, host));
    const newRenders = loadRenders.filter((entry) => entry.scope === "account:B");
    expect(newRenders.length).toBeGreaterThan(0);
    expect(newRenders.every((entry) => entry.data === null && entry.loading && entry.error === null)).toBe(true);
    expect(requests[0].signal.aborted).toBe(true);
    await resolve(requests[1], "B answer");
    expect(loaded.data).toBe("B answer");
  });

  it("ignores late first-page success and errors after a scope change", async () => {
    act(() => render(<Loader scope="A" />, host));
    act(() => render(<Loader scope="B" />, host));
    await resolve(requests[1], "B answer");
    await resolve(requests[0], "late A answer");
    expect(loaded.data).toBe("B answer");
    act(() => render(<Loader scope="C" />, host));
    const failed = requests[2];
    act(() => render(<Loader scope="D" />, host));
    await reject(failed);
    expect(loaded).toMatchObject({ data: null, loading: true, error: null });
  });

  it("retains same-scope data on refresh and supports failure then retry", async () => {
    act(() => render(<Loader scope="A" />, host));
    await resolve(requests[0], "original");
    act(() => loaded.reload());
    expect(loaded).toMatchObject({ data: "original", loading: true, error: null });
    await reject(requests[1]);
    expect(loaded).toMatchObject({ data: "original", loading: false, error: "Try again" });
    act(() => loaded.reload());
    expect(loaded.error).toBeNull();
    await resolve(requests[2], "fresh");
    expect(loaded).toMatchObject({ data: "fresh", loading: false, error: null });
  });

  it("does not expose old data or errors when authentication changes", async () => {
    act(() => render(<Loader scope="A" />, host));
    await resolve(requests[0], "private");
    act(() => { authed.value = false; render(<Loader scope="A" />, host); });
    expect(loaded).toMatchObject({ data: null, loading: true, error: null });
    expect(requests[0].signal.aborted).toBe(true);
  });

  it("aborts on unmount and ignores a late completion and stale reload", async () => {
    act(() => render(<Loader scope="A" />, host));
    const reload = loaded.reload;
    const renders = loadRenders.length;
    act(() => render(null, host));
    expect(requests[0].signal.aborted).toBe(true);
    await resolve(requests[0], "late");
    act(() => reload());
    expect(loadRenders).toHaveLength(renders);
    expect(requests).toHaveLength(1);
  });
});

describe("usePaged continuation ownership", () => {
  it.each(["account:B", "search:new query"])("never publishes A rows under %s, even when its continuation finishes late", async (nextScope) => {
    mountPager("account:A");
    await resolve(requests[0], page(["A1"], "a2"));
    act(() => paged.loadMore());
    const oldPage = requests[1];
    mountPager(nextScope);
    expect(paged).toMatchObject({ rows: [], loading: true, hasMore: false, loadingMore: false });
    expect(oldPage.signal.aborted).toBe(true);
    await resolve(requests[2], page(["B1"], "b2"));
    await resolve(oldPage, page(["A2"], "a3"));
    expect(paged.rows).toEqual(["B1"]);
    expect(pageRenders.filter((entry) => entry.scope === nextScope).every((entry) => entry.rows.every((row) => !row.startsWith("A")))).toBe(true);
    expect(published.filter((entry) => entry.scope === nextScope).every((entry) => entry.rows.every((row) => !row.startsWith("A")))).toBe(true);
    act(() => paged.loadMore());
    expect(requests[3].path).toBe(nextScope + ":b2");
  });

  it("fences an old page before the new key's effects run", async () => {
    mountPager("A");
    await resolve(requests[0], page(["A1"], "a2"));
    act(() => paged.loadMore());
    const oldPage = requests[1];
    // render is synchronous, but passive effects have not flushed yet.
    render(<Pager scope="B" />, host);
    expect(paged.rows).toEqual([]);
    await resolve(oldPage, page(["A2"]));
    expect(paged.rows).toEqual([]);
  });

  it("ignores A's page after A → B → A rather than matching only a string key", async () => {
    mountPager("A");
    await resolve(requests[0], page(["A old"], "old"));
    act(() => paged.loadMore());
    const oldPage = requests[1];
    mountPager("B");
    mountPager("A");
    await resolve(requests[3], page(["A new"], "new"));
    await resolve(oldPage, page(["A obsolete tail"]));
    expect(paged.rows).toEqual(["A new"]);
  });

  it("locks immediately against repeated load-more calls and advances the cursor once", async () => {
    mountPager("A");
    await resolve(requests[0], page(["A1"], "a2"));
    const more = paged.loadMore;
    act(() => { more(); more(); more(); });
    expect(requests.map((request) => request.path)).toEqual(["A", "A:a2"]);
    expect(paged.loadingMore).toBe(true);
    await resolve(requests[1], page(["A2"], "a3"));
    expect(paged.rows).toEqual(["A1", "A2"]);
    act(() => { more(); more(); });
    expect(requests[2].path).toBe("A:a3");
    expect(requests).toHaveLength(3);
    await resolve(requests[2], page(["A3"]));
    expect(paged).toMatchObject({ rows: ["A1", "A2", "A3"], hasMore: false, loadingMore: false });
  });

  it("preserves the current cursor after failure and retries without duplicate rows", async () => {
    mountPager("A");
    await resolve(requests[0], page(["A1"], "a2"));
    act(() => paged.loadMore());
    await reject(requests[1]);
    expect(paged).toMatchObject({ rows: ["A1"], error: "Try again", hasMore: true, loadingMore: false });
    act(() => { paged.loadMore(); paged.loadMore(); });
    expect(paged.error).toBeNull();
    expect(requests).toHaveLength(3);
    expect(requests[2].path).toBe("A:a2");
    await resolve(requests[2], page(["A2"]));
    expect(paged).toMatchObject({ rows: ["A1", "A2"], error: null, hasMore: false, loadingMore: false });
  });

  it.each(["local", "global"])("invalidates pagination synchronously on %s refresh and keeps rows while loading", async (mode) => {
    mountPager("A");
    await resolve(requests[0], page(["A1"], "a2"));
    act(() => paged.loadMore());
    await resolve(requests[1], page(["A2"], "a3"));
    act(() => paged.loadMore());
    const oldPage = requests[2];
    const more = paged.loadMore;
    const reload = mode === "local" ? paged.reload : vi.mocked(setReloader).mock.calls.at(-1)![0];
    act(() => { reload(); more(); });
    expect(oldPage.signal.aborted).toBe(true);
    expect(requests).toHaveLength(4);
    expect(paged).toMatchObject({ rows: ["A1", "A2"], loading: true, hasMore: false, loadingMore: false });
    act(() => paged.loadMore());
    expect(requests).toHaveLength(4);
    await resolve(requests[3], page(["fresh A1"], "fresh2"));
    expect(paged.rows).toEqual(["fresh A1"]);
    act(() => paged.loadMore());
    expect(requests[4].path).toBe("A:fresh2");
    await resolve(oldPage, page(["stale A3"]));
    // An obsolete completion must not clear the newer page's loading lock.
    expect(paged).toMatchObject({ rows: ["fresh A1"], loadingMore: true });
    act(() => paged.loadMore());
    expect(requests).toHaveLength(5);
    await resolve(requests[4], page(["fresh A2"]));
    expect(paged.rows).toEqual(["fresh A1", "fresh A2"]);
  });

  it("discards a late old-page rejection without clearing a newer request", async () => {
    mountPager("A");
    await resolve(requests[0], page(["A1"], "a2"));
    act(() => paged.loadMore());
    const oldPage = requests[1];
    mountPager("B");
    await resolve(requests[2], page(["B1"], "b2"));
    act(() => paged.loadMore());
    await reject(oldPage);
    expect(paged).toMatchObject({ rows: ["B1"], error: null, loadingMore: true });
    act(() => paged.loadMore());
    expect(requests).toHaveLength(4);
  });

  it("collapses a refreshed tail even if the first-page response reuses the same object", async () => {
    mountPager("A");
    const cachedPage = page(["A1"], "a2");
    await resolve(requests[0], cachedPage);
    act(() => paged.loadMore());
    await resolve(requests[1], page(["A2"]));
    act(() => paged.reload());
    await resolve(requests[2], cachedPage);
    expect(paged).toMatchObject({ rows: ["A1"], hasMore: true });
  });

  it("keeps rows referentially stable for unrelated renders and loading-more flags", async () => {
    mountPager("A");
    await resolve(requests[0], page(["A1"], "a2"));
    const rows = paged.rows;
    const publications = published.length;
    mountPager("A", 1);
    expect(paged.rows).toBe(rows);
    act(() => paged.loadMore());
    expect(paged.rows).toBe(rows);
    await reject(requests[1]);
    expect(paged.rows).toBe(rows);
    expect(published).toHaveLength(publications);
    act(() => paged.loadMore());
    await resolve(requests[2], page(["A2"]));
    const extended = paged.rows;
    mountPager("A", 2);
    expect(paged.rows).toBe(extended);
    expect(published).toHaveLength(publications + 1);
  });

  it("honors has_more=false even if a server returns a leftover cursor", async () => {
    mountPager("A");
    await resolve(requests[0], { rows: ["A1"], has_more: false, next_cursor: "unused" });
    expect(paged.hasMore).toBe(false);
    act(() => paged.loadMore());
    expect(requests).toHaveLength(1);
  });

  it("aborts an in-flight page on unmount and makes its callback inert", async () => {
    mountPager("A");
    await resolve(requests[0], page(["A1"], "a2"));
    act(() => paged.loadMore());
    const more = paged.loadMore;
    const renders = pageRenders.length;
    act(() => render(null, host));
    expect(requests[1].signal.aborted).toBe(true);
    await resolve(requests[1], page(["late"]));
    act(() => more());
    expect(requests).toHaveLength(2);
    expect(pageRenders).toHaveLength(renders);
  });
});
