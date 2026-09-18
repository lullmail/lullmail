import { useEffect } from "preact/hooks";
import { usePaged } from "../lib/useLoad";
import { accountFilter, accountQS, list, resetSelection, setList } from "../lib/store";
import type { Row } from "../lib/types";
import { countOf } from "../lib/fmt";
import { Empty, ListSkeleton, PageHead } from "../ui/bits";
import { MsgList } from "../ui/MsgRow";
import { BulkBar } from "../ui/BulkBar";

/** One result surface for search, wherever the query was typed. Results
 *  page through the same keyset continuation as buckets (audit DATA-06). */
export function SearchView({ q }: { q: string }) {
  const lens = accountFilter.value;
  const pathFor = (cursor: string | undefined) => {
    let p = accountQS("/search?q=" + encodeURIComponent(q));
    p += p.includes("?") ? "&" : "?";
    p += "limit=50";
    if (cursor) p += "&cursor=" + encodeURIComponent(cursor);
    return p;
  };
  const { rows, loading, error, hasMore, loadingMore, loadMore } = usePaged<Row>("search:" + q + ":" + lens, pathFor);

  useEffect(() => { resetSelection(); }, [q]);

  useEffect(() => {
    setList({ kind: "rows", key: "search:" + q + ":" + lens, loading, error, rows, senders: [], origin: null });
  }, [rows, loading, error, q, lens]);

  return (
    <>
      <BulkBar />
      <PageHead
        kicker="Search"
        title={"“" + q + "”"}
        sub={loading && rows.length === 0 ? "Searching…" : countOf(rows.length, "result")}
      />
      {loading && rows.length === 0 && <ListSkeleton rows={4} />}
      {error && <Empty title="Search failed." sub={error} />}
      {rows.length === 0 && !loading && !error && (
        <Empty title="Nothing matched." sub="Try a sender, a subject, or a word from a preview." />
      )}
      {rows.length > 0 && <MsgList rows={list.value.key === "search:" + q + ":" + lens ? list.value.rows : rows} q={q} />}
      {hasMore && (
        <div class="load-more">
          <button class="btn btn-outline" type="button" disabled={loadingMore} onClick={loadMore}>
            {loadingMore ? "Loading…" : "Load more"}
          </button>
        </div>
      )}
    </>
  );
}
