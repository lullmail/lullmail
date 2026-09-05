import { useEffect } from "preact/hooks";
import { api } from "../lib/api";
import { useLoad } from "../lib/useLoad";
import { accountFilter, accountQS, list, resetSelection, setList } from "../lib/store";
import { folderLabel } from "../lib/router";
import type { Row } from "../lib/types";
import { Empty, ListSkeleton, PageHead } from "../ui/bits";
import { MsgList } from "../ui/MsgRow";
import { BulkBar } from "../ui/BulkBar";

// A real mailbox on the mail server, listed as the server has it. Unlike a
// bucket, what shows here is what every other mail client sees in that folder.

export function FolderView({ folder }: { folder: string }) {
  const lens = accountFilter.value;
  const key = "folder:" + folder + ":" + lens;
  const { data, loading, error } = useLoad<Row[]>(key, (signal) =>
    api<Row[]>(accountQS("/folder?name=" + encodeURIComponent(folder)), { signal })
  );

  useEffect(() => { resetSelection(); }, [folder]);

  const published = list.value.key;

  useEffect(() => {
    setList({ kind: "rows", key, loading, error, rows: data || [], senders: [], origin: null });
  }, [data, loading, error, key]);

  return (
    <>
      <BulkBar />
      <PageHead title={folderLabel(folder)} sub="A folder on your mail server, exactly as it holds it." />
      {loading && !data && <ListSkeleton />}
      {error && <Empty title="That didn't load." sub={error} />}
      {data && data.length === 0 && !loading && (
        <Empty title="This folder is empty." sub="Nothing your mail server has synced yet." />
      )}
      {data && data.length > 0 && <MsgList rows={published === key ? list.value.rows : data} />}
    </>
  );
}
