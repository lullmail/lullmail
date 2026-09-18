import { useEffect } from "preact/hooks";
import { usePaged } from "../lib/useLoad";
import { accountFilter, accountQS, list, resetSelection, setList } from "../lib/store";
import type { ListBucket, Row } from "../lib/types";
import { Empty, ListSkeleton, PageHead } from "../ui/bits";
import { MsgList } from "../ui/MsgRow";
import { BulkBar } from "../ui/BulkBar";

const COPY: Record<ListBucket, { title: string; sub: string; emptyTitle: string; emptySub: string }> = {
  imbox: {
    title: "Inbox",
    sub: "The people you chose to hear from.",
    emptyTitle: "All quiet.",
    emptySub: "Nothing needs you right now. Enjoy it.",
  },
  screener: {
    title: "The Screener", sub: "New senders wait here.",
    emptyTitle: "Nobody's waiting.", emptySub: "New senders land here first.",
  },
  feed: {
    title: "Reading",
    sub: "Mail you allowed but never have to answer. Skim it when you feel like it.",
    emptyTitle: "Nothing to read.",
    emptySub: "Newsletters and periodic mail gather here, so they never touch your Inbox.",
  },
  paper_trail: {
    title: "Receipts",
    sub: "Confirmations, orders and notifications — kept so you can find them, not read them.",
    emptyTitle: "No receipts yet.",
    emptySub: "Order confirmations and notifications file themselves here. Search finds them when you need one.",
  },
  snoozed: {
    title: "Snoozed",
    sub: "Everything you put off. Dated ones come back on their day; the rest wait for someday.",
    emptyTitle: "Nothing snoozed.",
    emptySub: "Press s on any thread to deal with it later — tomorrow, next week, or someday.",
  },
  set_aside: {
    title: "Snoozed", sub: "Everything you put off.",
    emptyTitle: "Nothing snoozed.", emptySub: "Press s on any thread to deal with it later.",
  },
  later: {
    title: "Snoozed", sub: "Everything you put off.",
    emptyTitle: "Nothing snoozed.", emptySub: "Press s on any thread to deal with it later.",
  },
};

export function BucketView({ bucket }: { bucket: ListBucket }) {
  const copy = COPY[bucket];
  const lens = accountFilter.value;
  // Keyset pages (audit DATA-06): the first page loads with the view and
  // "Load more" advances the server's cursor — new arrivals never shift
  // what earlier pages already delivered.
  const pathFor = (cursor: string | undefined) => {
    let p = accountQS("/buckets/" + bucket);
    p += p.includes("?") ? "&" : "?";
    p += "limit=50";
    if (cursor) p += "&cursor=" + encodeURIComponent(cursor);
    return p;
  };
  const { rows, loading, error, hasMore, loadingMore, loadMore } = usePaged<Row>("bucket:" + bucket + ":" + lens, pathFor);

  useEffect(() => { resetSelection(); }, [bucket]);

  // Rows come from the store once this view has published them, so optimistic
  // updates (opening a thread marks it read) are visible immediately.
  const published = list.value.key;

  useEffect(() => {
    setList({
      kind: "rows", key: "bucket:" + bucket + ":" + lens, loading, error,
      rows, senders: [], origin: bucket,
    });
  }, [rows, loading, error, bucket, lens]);

  return (
    <>
      <BulkBar />
      {/* The subtitle explains the bucket, so it belongs most on an empty one. */}
      <PageHead title={copy.title} sub={copy.sub} />
      {loading && rows.length === 0 && <ListSkeleton />}
      {error && <Empty title="That didn't load." sub={error} />}
      {rows.length === 0 && !loading && !error && (
        <Empty title={copy.emptyTitle} sub={copy.emptySub} />
      )}
      {rows.length > 0 && <MsgList rows={published === "bucket:" + bucket + ":" + lens ? list.value.rows : rows} />}
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
