import { useState } from "preact/hooks";
import { download } from "../lib/api";
import { showError } from "../lib/store";
import { fmtBytes } from "../lib/fmt";
import type { Attachment } from "../lib/types";
import { Icon } from "../ui/Icon";

export function Attachments({ account, messageId, items }: { account: string; messageId: string; items: Attachment[] }) {
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
              await download(
                "/messages/" + encodeURIComponent(messageId) +
                  "/attachment/" + encodeURIComponent(att.part_id) +
                  "?account=" + encodeURIComponent(account),
                att.filename || "attachment"
              );
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
