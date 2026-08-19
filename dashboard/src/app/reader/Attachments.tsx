import { useState } from "preact/hooks";
import { token } from "../lib/api";
import { showError } from "../lib/store";
import { fmtBytes } from "../lib/fmt";
import type { Attachment } from "../lib/types";
import { Icon } from "../ui/Icon";

/** The API is bearer-authenticated, so a plain download link would 401.
    Fetch with the header, then hand the blob to a synthetic anchor. */
async function download(messageId: string, att: Attachment) {
  const url =
    "/api/messages/" + encodeURIComponent(messageId) +
    "/attachment/" + encodeURIComponent(att.part_id);
  const res = await fetch(url, { headers: { Authorization: "Bearer " + token() } });
  if (!res.ok) throw new Error("HTTP " + res.status);
  const blob = await res.blob();
  const objectUrl = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = objectUrl;
  a.download = att.filename || "attachment";
  a.click();
  setTimeout(() => URL.revokeObjectURL(objectUrl), 5000);
}

export function Attachments({ messageId, items }: { messageId: string; items: Attachment[] }) {
  const [busy, setBusy] = useState<string | null>(null);
  if (!items.length) return null;

  return (
    <div class="attach-row">
      {items.map((att) => (
        <button
          class="attach-chip" type="button" key={att.part_id}
          disabled={busy === att.part_id}
          onClick={async () => {
            setBusy(att.part_id);
            try {
              await download(messageId, att);
            } catch (e) {
              showError("Download failed: " + (e instanceof Error ? e.message : "unknown"));
            } finally {
              setBusy(null);
            }
          }}
        >
          <Icon name="clip" size={14} />
          <span>{att.filename || "attachment"}</span>
          {att.size > 0 && <span class="attach-size">{fmtBytes(att.size)}</span>}
        </button>
      ))}
    </div>
  );
}
