import { useEffect } from "preact/hooks";
import { api } from "../lib/api";
import { useLoad } from "../lib/useLoad";
import { accountFilter, accountQS, list, resetSelection, setList } from "../lib/store";
import type { Row } from "../lib/types";
import { countOf } from "../lib/fmt";
import { Empty, ListSkeleton, PageHead } from "../ui/bits";
import { MsgList } from "../ui/MsgRow";
import { BulkBar } from "../ui/BulkBar";

/** One result surface for search, wherever the query was typed. */
export function SearchView({ q }: { q: string }) {
  const lens = accountFilter.value;
  const { data, loading, error } = useLoad<Row[]>("search:" + q + ":" + lens, (signal) =>
    api<Row[]>(accountQS("/search?q=" + encodeURIComponent(q)), { signal })
  );

  useEffect(() => { resetSelection(); }, [q]);

  useEffect(() => {
    setList({ kind: "rows", key: "search:" + q + ":" + lens, loading, error, rows: data || [], senders: [], origin: null });
  }, [data, loading, error, q, lens]);

  return (
    <>
      <BulkBar />
      <PageHead
        kicker="Search"
        title={"“" + q + "”"}
        sub={data ? countOf(data.length, "result") : "Searching…"}
      />
      {loading && !data && <ListSkeleton rows={4} />}
      {error && <Empty title="Search failed." sub={error} />}
      {data && data.length === 0 && !loading && (
        <Empty title="Nothing matched." sub="Try a sender, a subject, or a word from a preview." />
      )}
      {data && data.length > 0 && <MsgList rows={list.value.key === "search:" + q + ":" + lens ? list.value.rows : data} q={q} />}
    </>
  );
}
