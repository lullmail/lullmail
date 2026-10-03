import { render } from "preact";
import { MsgList } from "../src/app/ui/MsgRow";
import { BulkBar } from "../src/app/ui/BulkBar";
import { installKeys } from "../src/app/lib/keys";
import { checked, cursor, dismissReader, layout, list, reader, rowIdentity, setList } from "../src/app/lib/store";
import type { Row } from "../src/app/lib/types";

const rows: Row[] = Array.from({ length: 8 }, (_, n) => ({
  account: n === 7 ? "second-account" : "test-account",
  message_id: n === 7 ? "0" : String(n), thread_id: "thread-" + n,
  from: "Test Sender <sender@example.test>", subject: "Selection test message " + n,
  received_at: "2026-10-01T12:00:00Z", read: false, preview: "Synthetic test mail. No real mailbox is connected.",
}));
layout.value = "classic";
setList({ key: "test-inbox", kind: "rows", loading: false, error: null, rows, senders: [], origin: "imbox" });
installKeys();

function Fixture() {
  return <main class="column" style={{ maxWidth: 820, margin: "32px auto" }}>
    <h1>Message selection test</h1>
    <input aria-label="Typing field" placeholder="Native editing stays native" />
    <BulkBar />
    <MsgList rows={list.value.rows} />
    <output data-testid="selected">{list.value.rows.filter((row) => checked.value.has(rowIdentity(row))).map((row) => row.thread_id).join(",")}</output>
    <output data-testid="cursor">{cursor.value}</output>
    <output data-testid="reader">{reader.value.threadId || "closed"}</output>
  </main>;
}
Object.assign(window, {
  selectionFixture: {
    reorder: () => setList({ rows: [...rows].reverse() }),
    close: dismissReader,
  },
});
render(<Fixture />, document.getElementById("app")!);
