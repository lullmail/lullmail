import { useState } from "preact/hooks";
import { checked, list, resetSelection, rowIdentity } from "../lib/store";
import { markDone, markRead, moveTo, snooze } from "../lib/actions";
import { countOf } from "../lib/fmt";
import { Icon } from "./Icon";
import { SnoozeMenu } from "./SnoozeMenu";

/** Bulk actions: previously there were none, so triaging a full Feed was
    one thread at a time. Appears only once something is selected. */
export function BulkBar() {
  const [asideOpen, setAsideOpen] = useState(false);
  const ids = checked.value;
  if (!ids.size) return null;

  const rows = list.value.rows.filter((r) => ids.has(rowIdentity(r)));
  if (!rows.length) return null;
  const allRead = rows.every((r) => r.read);

  return (
    <div class="bulkbar">
      <span class="bulkbar-count">{countOf(rows.length, "thread")}</span>

      <button class="btn btn-ghost btn-sm" type="button" onClick={() => markDone(rows)}>
        <Icon name="check" size={14} /> Done
      </button>

      <div style={{ position: "relative" }}>
        <button class="btn btn-ghost btn-sm" type="button" onClick={() => setAsideOpen((v) => !v)}>
          <Icon name="aside" size={14} /> Snooze
        </button>
        {asideOpen && (
          <SnoozeMenu
            onPick={(days) => { setAsideOpen(false); snooze(rows, days); }}
            onClose={() => setAsideOpen(false)}
          />
        )}
      </div>

      <button class="btn btn-ghost btn-sm" type="button" onClick={() => moveTo(rows, "imbox")}>Inbox</button>
      <button class="btn btn-ghost btn-sm" type="button" onClick={() => moveTo(rows, "feed")}>Reading</button>
      <button class="btn btn-ghost btn-sm" type="button" onClick={() => moveTo(rows, "paper_trail")}>Receipts</button>
      <button class="btn btn-ghost btn-sm" type="button" onClick={() => markRead(rows, !allRead)}>
        {allRead ? "Unread" : "Read"}
      </button>

      <span class="bulkbar-spacer" />
      <button class="btn btn-ghost btn-sm" type="button" onClick={resetSelection}>
        Clear <span class="kbd">Esc</span>
      </button>
    </div>
  );
}
