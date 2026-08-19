import { useState } from "preact/hooks";
import type { Row } from "../lib/types";
import { checked, cursor, list, toggleChecked } from "../lib/store";
import { markDone, moveTo, openThread, snooze } from "../lib/actions";
import { dayLabel, fmtDate, splitFrom } from "../lib/fmt";
import { Highlight } from "./bits";
import { Icon } from "./Icon";
import { SnoozeMenu } from "./SnoozeMenu";

function QuickActs({ row }: { row: Row }) {
  const [asideOpen, setAsideOpen] = useState(false);
  const stop = (fn: () => void) => (ev: MouseEvent) => {
    ev.stopPropagation();
    ev.preventDefault();
    fn();
  };
  return (
    <div class="row-acts" onClick={(e) => e.stopPropagation()}>
      <button class="btn-icon" type="button" title="Done (e)" onClick={stop(() => markDone([row]))}>
        <Icon name="check" size={15} />
      </button>
      <div style={{ position: "relative" }}>
        <button class="btn-icon" type="button" title="Set aside (s)" onClick={stop(() => setAsideOpen((v) => !v))}>
          <Icon name="aside" size={15} />
        </button>
        {asideOpen && (
          <SnoozeMenu
            onPick={(days) => { setAsideOpen(false); snooze([row], days); }}
            onClose={() => setAsideOpen(false)}
          />
        )}
      </div>
      <button class="btn-icon" type="button" title="Reply later (l)" onClick={stop(() => moveTo([row], "later"))}>
        <Icon name="later" size={15} />
      </button>
    </div>
  );
}

/** Sender, subject, snippet. No avatar: the subject of a list row is a message,
    and a 40px colour disc on every line is what made this read like Gmail. */
export function MsgRow({ row, index, q }: { row: Row; index: number; q?: string }) {
  const who = splitFrom(row.from);
  const isChecked = checked.value.has(row.message_id);
  const cls = [
    "msg-row",
    row.read ? "read" : "unread",
    cursor.value === index ? "cursor" : "",
    isChecked ? "picked" : "",
  ].filter(Boolean).join(" ");

  return (
    <div
      class={cls}
      data-cursor-index={index}
      onClick={() => {
        cursor.value = index;
        openThread(row.thread_id, list.value.origin);
      }}
    >
      <button
        class="row-check" type="button"
        aria-label={isChecked ? "Deselect" : "Select"} aria-pressed={isChecked}
        onClick={(ev) => { ev.stopPropagation(); toggleChecked(row.message_id); }}
      >
        <Icon name="check" size={11} />
      </button>

      <div class="row-top">
        <span class="row-sender">{who.name || who.email}</span>
        <span class="row-meta">
          {row.has_attachment && <span class="chip"><Icon name="clip" size={11} /></span>}
          {(row.thread_len || 0) > 1 && <span class="chip">{row.thread_len}</span>}
          <span class="row-date">{fmtDate(row.received_at)}</span>
        </span>
      </div>
      <div class="row-subject">
        <Highlight text={row.subject || "(no subject)"} q={q} />
      </div>
      {row.preview && <div class="row-preview">{row.preview}</div>}

      <QuickActs row={row} />
    </div>
  );
}

/** Rows grouped under Today / Yesterday / This week rules. */
export function MsgList({ rows, q }: { rows: Row[]; q?: string }) {
  let last = "";
  const out: preact.ComponentChild[] = [];
  rows.forEach((row, i) => {
    const label = dayLabel(row.received_at);
    if (label !== last) {
      last = label;
      out.push(<div class="date-rule" key={"rule-" + label}><span>{label}</span></div>);
    }
    out.push(<MsgRow row={row} index={i} q={q} key={row.message_id} />);
  });
  return <div class={"msg-list" + (checked.value.size ? " has-selection" : "")}>{out}</div>;
}
