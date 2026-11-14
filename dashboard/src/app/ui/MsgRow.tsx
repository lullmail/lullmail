import { useRef, useState } from "preact/hooks";
import type { Row } from "../lib/types";
import { accountFilter, checked, clearChecked, cursor, focusRow, list, mailboxName, rowIdentity, selectRange, toggleChecked } from "../lib/store";
import { markDone, moveTo, openThread, snooze, trashThreads } from "../lib/actions";
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
      <button class="btn-icon" type="button" title="Done (e)" aria-label="Mark done" onClick={stop(() => markDone([row]))}>
        <Icon name="check" size={15} />
      </button>
      <button class="btn-icon" type="button" title="Trash (#)" aria-label="Move to Trash" onClick={stop(() => trashThreads([row]))}>
        <Icon name="trash" size={15} />
      </button>
      <div style={{ position: "relative" }}>
        <button class="btn-icon" type="button" title="Set aside (s)" aria-label="Set aside" aria-expanded={asideOpen} onClick={stop(() => setAsideOpen((v) => !v))}>
          <Icon name="aside" size={15} />
        </button>
        {asideOpen && (
          <SnoozeMenu
            onPick={(days) => { setAsideOpen(false); snooze([row], days); }}
            onClose={() => setAsideOpen(false)}
          />
        )}
      </div>
      <button class="btn-icon" type="button" title="Move to Snoozed" aria-label="Move to Snoozed" onClick={stop(() => moveTo([row], "later"))}>
        <Icon name="later" size={15} />
      </button>
    </div>
  );
}

/** Sender, subject, snippet. No avatar: the subject of a list row is a message,
    and a 40px colour disc on every line is what made this read like Gmail. */
export function MsgRow({ row, index, q }: { row: Row; index: number; q?: string }) {
  const who = splitFrom(row.from);
  const mailbox = mailboxName(row.account);
  const isChecked = checked.value.has(rowIdentity(row));
  const cls = [
    "msg-row",
    row.read ? "read" : "unread",
    cursor.value === index ? "cursor" : "",
    isChecked ? "picked" : "",
  ].filter(Boolean).join(" ");

  // Touch: drag a row left to mark it done. The hover cluster does not exist
  // on a phone, and four buttons under every row doubled the list's height.
  // Only a deliberate sideways drag arms it; a vertical one is a scroll and
  // is left to the browser (touch-action: pan-y in the stylesheet).
  const swipe = useRef<{ id: number; x: number; y: number; on: boolean } | null>(null);
  const swipedAt = useRef(0);
  const SWIPE_COMMIT = 96;
  const endSwipe = (el: HTMLElement, commit: boolean) => {
    const was = swipe.current;
    swipe.current = null;
    el.classList.remove("swiping", "swipe-armed");
    if (!was?.on) return;
    swipedAt.current = Date.now();
    if (!commit) { el.style.removeProperty("--swipe"); return; }
    el.style.setProperty("--swipe", "-100%");
    // Let the row slide out before the list closes the gap. If the action
    // fails and the row stays, it must not stay shoved off-screen.
    setTimeout(() => { markDone([row]); el.style.removeProperty("--swipe"); }, 150);
  };

  return (
    <div
      class={cls}
      role="listitem"
      onPointerDown={(ev) => {
        if (ev.pointerType !== "touch" || checked.value.size) return;
        if ((ev.target as HTMLElement).closest("a, button, input")) return;
        swipe.current = { id: ev.pointerId, x: ev.clientX, y: ev.clientY, on: false };
      }}
      onPointerMove={(ev) => {
        const s = swipe.current;
        if (!s || s.id !== ev.pointerId) return;
        const dx = ev.clientX - s.x, dy = ev.clientY - s.y;
        const el = ev.currentTarget as HTMLElement;
        if (!s.on) {
          if (Math.abs(dy) > 10 && Math.abs(dy) >= Math.abs(dx)) { swipe.current = null; return; }
          if (dx > -14 || Math.abs(dx) < Math.abs(dy) * 1.5) return;
          s.on = true;
          try { el.setPointerCapture(ev.pointerId); } catch { /* already released */ }
          el.classList.add("swiping");
        }
        el.style.setProperty("--swipe", Math.min(0, dx) + "px");
        el.classList.toggle("swipe-armed", dx <= -SWIPE_COMMIT);
      }}
      onPointerUp={(ev) => {
        const s = swipe.current;
        if (!s || s.id !== ev.pointerId) return;
        endSwipe(ev.currentTarget as HTMLElement, s.on && ev.clientX - s.x <= -SWIPE_COMMIT);
      }}
      onPointerCancel={(ev) => endSwipe(ev.currentTarget as HTMLElement, false)}
      tabIndex={cursor.value === index || (cursor.value < 0 && index === 0) ? 0 : -1}
      data-cursor-index={index}
      onFocus={(ev) => { if (ev.target === ev.currentTarget) focusRow(index, true); }}
      onMouseDown={(ev) => { if (ev.shiftKey && !(ev.target as HTMLElement).closest("a, button, input")) ev.preventDefault(); }}
      onClick={(ev) => {
        if (ev.defaultPrevented || ev.button !== 0 || ev.altKey) return;
        if (Date.now() - swipedAt.current < 400) return; // the tail of a swipe is not a tap
        if ((ev.target as HTMLElement).closest("a, button, input, select, textarea, [contenteditable]")) return;
        if (ev.shiftKey) selectRange(index, ev.ctrlKey || ev.metaKey);
        else if (ev.ctrlKey || ev.metaKey) toggleChecked(row);
        else {
          clearChecked();
          focusRow(index);
        }
        (ev.currentTarget as HTMLElement).focus({ preventScroll: true });
        if (ev.shiftKey || ev.ctrlKey || ev.metaKey) { ev.preventDefault(); return; }
        openThread(row.thread_id, row.account, list.value.origin);
      }}
    >
      <button
        class="row-check" type="button"
        aria-label={isChecked ? "Deselect" : "Select"} aria-pressed={isChecked}
        onClick={(ev) => {
          ev.stopPropagation();
          if (ev.shiftKey) selectRange(index, ev.ctrlKey || ev.metaKey);
          else toggleChecked(row);
        }}
      >
        <Icon name="check" size={11} />
      </button>

      <div class="row-top">
        <span class="row-sender">{who.name || who.email}</span>
        <span class="row-meta">
          {row.attachment_state === "present" && <span class="chip" title="Has attachments"><Icon name="clip" size={11} /></span>}
          {(row.thread_len || 0) > 1 && <span class="chip">{row.thread_len}</span>}
          {!accountFilter.value && mailbox && <span class="row-mailbox" title={"In " + mailbox}>{mailbox}</span>}
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
    out.push(<MsgRow row={row} index={i} q={q} key={rowIdentity(row)} />);
  });
  return <div role="list" aria-label="Messages" class={"msg-list" + (checked.value.size ? " has-selection" : "")}>{out}</div>;
}
