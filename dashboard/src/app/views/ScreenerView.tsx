import { useEffect } from "preact/hooks";
import { api } from "../lib/api";
import { useLoad } from "../lib/useLoad";
import { resetSelection, setList } from "../lib/store";
import type { ScreenerSender } from "../lib/types";
import { Empty, ListSkeleton, PageHead } from "../ui/bits";
import { ScreenerCard } from "../ui/ScreenerCard";

export function ScreenerView() {
  const { data, loading, error } = useLoad<ScreenerSender[]>("screener", (signal) =>
    api<ScreenerSender[]>("/screener", { signal })
  );

  useEffect(() => { resetSelection(); }, []);

  useEffect(() => {
    setList({ kind: "senders", key: "screener", loading, error, rows: [], senders: data || [], origin: "screener" });
  }, [data, loading, error]);

  return (
    <>
      <PageHead
        title="The Screener"
        sub="New senders wait here. Decide once — everything they ever send goes where you say, and you can take it back."
      />
      {!!data?.length && (
        <div class="keys-hint">
          <span class="kbd">j</span><span class="kbd">k</span> to move ·{" "}
          <span class="kbd">1</span> Imbox · <span class="kbd">2</span> Reading ·{" "}
          <span class="kbd">3</span> Receipts · <span class="kbd">0</span> Block
        </div>
      )}
      {loading && !data && <ListSkeleton rows={3} />}
      {error && <Empty title="That didn't load." sub={error} />}
      {data && data.length === 0 && !loading && (
        <Empty
          title="Nobody's waiting."
          sub="New senders land here first. Screen them once and they're sorted forever."
        />
      )}
      {data?.map((sender, i) => (
        <ScreenerCard sender={sender} index={i} key={sender.sender} />
      ))}
    </>
  );
}
