// The keyboard layer.
//
// SPEC 16 asks for keyboard-first; the old build bound j/k/Enter and only on
// bucket pages, so Today, the Screener and the reader were mouse-only. This is
// one handler for the whole app: it reads the current list out of the store, so
// every surface that publishes rows gets the same keys for free.
import { closeCompose, compose, composeOpen, cursor, checked, list, noteKeyUse, openCompose, overlayOpen, palette, reader, resetSelection, shortcuts, toggleChecked, closeReader, targetRows, dismissToast, toast } from "./store";
import { decide, markDone, moveTo, openThread, pinThreads, snooze } from "./actions";
import { navigate } from "./router";
import { splitFrom } from "./fmt";

const GOTO: Record<string, string> = {
  t: "/today",
  b: "/board",
  d: "/calendar",
  n: "/notes",
  i: "/",
  s: "/screener",
  r: "/reading",
  c: "/receipts",
  z: "/snoozed",
  p: "/people",
};

function isTyping(target: EventTarget | null): boolean {
  const el = target as HTMLElement | null;
  if (!el || !el.closest) return false;
  return !!el.closest("input, textarea, select, [contenteditable='true']");
}

function moveCursor(delta: number) {
  const l = list.value;
  const len = l.kind === "rows" ? l.rows.length : l.kind === "senders" ? l.senders.length : 0;
  if (!len) return;
  const at = cursor.value;
  const next = at < 0 ? (delta > 0 ? 0 : len - 1) : Math.min(len - 1, Math.max(0, at + delta));
  cursor.value = next;
  const node = document.querySelector<HTMLElement>('[data-cursor-index="' + next + '"]');
  node?.scrollIntoView({ block: "nearest" });
}

function openAtCursor() {
  const l = list.value;
  if (l.kind !== "rows") return;
  const row = l.rows[cursor.value];
  if (row) openThread(row.thread_id, l.origin);
}

function replyToCursor() {
  const l = list.value;
  const row = l.kind === "rows" ? l.rows[cursor.value] : undefined;
  const source = reader.value.messages[reader.value.messages.length - 1];
  if (source) {
    const who = splitFrom(source.from);
    openCompose({
      to: who.email,
      subject: /^re:/i.test(source.subject) ? source.subject : "Re: " + source.subject,
      replyToId: source.id,
      context: "Replying to " + (who.name || who.email),
    });
    return;
  }
  if (!row) return;
  const who = splitFrom(row.from);
  openCompose({
    to: who.email,
    subject: /^re:/i.test(row.subject) ? row.subject : "Re: " + row.subject,
    replyToId: row.message_id,
    context: "Replying to " + (who.name || who.email),
  });
}

export function installKeys(): () => void {
  let gPending = false;
  let gTimer: ReturnType<typeof setTimeout> | undefined;

  const onKey = (ev: KeyboardEvent) => {
    // Anything the handler actually acts on counts as learning; the hint bar
    // uses this to decide it is no longer needed.
    const learn = () => noteKeyUse();
    // Palette is global and must win everywhere, including inside inputs.
    if ((ev.metaKey || ev.ctrlKey) && ev.key.toLowerCase() === "k") {
      ev.preventDefault();
      palette.value = !palette.value;
      return;
    }
    if (ev.key === "Escape") {
      if (palette.value) { palette.value = false; return; }
      if (shortcuts.value) { shortcuts.value = false; return; }
      if (composeOpen.value) { closeCompose(); return; }
      if (toast.value) { dismissToast(); return; }
      if (checked.value.size) { resetSelection(); return; }
      if (reader.value.threadId) { closeReader(); return; }
      return;
    }
    // While composing, `c` stacks another draft onto the carousel instead of
    // being swallowed by the overlay guard.
    if (composeOpen.value && !isTyping(ev.target) && ev.key.toLowerCase() === "c") {
      ev.preventDefault();
      openCompose();
      return;
    }
    if (overlayOpen.value) return;
    if (isTyping(ev.target)) return;
    if (ev.metaKey || ev.ctrlKey || ev.altKey) return;

    const k = ev.key;

    // `g` then a destination — the two-stroke jump, so single letters stay free.
    if (gPending) {
      gPending = false;
      clearTimeout(gTimer);
      const dest = GOTO[k.toLowerCase()];
      if (dest) {
        ev.preventDefault();
        navigate(dest);
      }
      return;
    }
    if (k === "g") {
      gPending = true;
      gTimer = setTimeout(() => { gPending = false; }, 1200);
      return;
    }

    switch (k) {
      case "j": case "ArrowDown": ev.preventDefault(); learn(); moveCursor(1); return;
      case "k": case "ArrowUp": ev.preventDefault(); learn(); moveCursor(-1); return;
      case "Enter": case "o": ev.preventDefault(); learn(); openAtCursor(); return;
      case "u": ev.preventDefault(); learn(); closeReader(); return;
      case "c": ev.preventDefault(); learn(); openCompose(); return;
      case "r": ev.preventDefault(); learn(); replyToCursor(); return;
      case "/": ev.preventDefault(); learn(); palette.value = true; return;
      case "?": ev.preventDefault(); shortcuts.value = true; return;
    }

    // Screener decisions by number, in the order the buttons appear.
    if (list.value.kind === "senders") {
      const sender = list.value.senders[cursor.value];
      if (!sender) return;
      const map: Record<string, [boolean, "imbox" | "feed" | "paper_trail" | "blocked"]> = {
        "1": [true, "imbox"],
        "2": [true, "feed"],
        "3": [true, "paper_trail"],
        "0": [false, "blocked"],
      };
      const choice = map[k];
      if (choice) {
        ev.preventDefault();
        learn();
        decide(sender.sender, choice[0], choice[1]);
      }
      return;
    }

    // Row verbs. `targetRows` resolves the checkbox selection or the cursor.
    const rows = targetRows();
    if (!rows.length) return;
    switch (k) {
      case "x":
        ev.preventDefault();
        learn();
        { const at = list.value.rows[cursor.value]; if (at) toggleChecked(at.message_id); }
        return;
      case "e": ev.preventDefault(); learn(); markDone(rows); return;
      // One deferral key. Filing a single message into Reading or Receipts is
      // rare — you change the sender's rule instead — so those lost their keys.
      case "s": ev.preventDefault(); learn(); snooze(rows, 3); return;
      case "i": ev.preventDefault(); learn(); moveTo(rows, "imbox"); return;
      case "p": ev.preventDefault(); learn(); pinThreads(rows); return;
    }
  };

  document.addEventListener("keydown", onKey);
  return () => document.removeEventListener("keydown", onKey);
}

export const SHORTCUTS: [string, string][] = [
  ["j / k", "Move down / up"],
  ["Enter", "Open the selected thread"],
  ["u", "Back to the list"],
  ["x", "Select — then any verb applies to all of them"],
  ["e", "Done"],
  ["s", "Snooze for 3 days"],
  ["i", "Move to the Inbox"],
  ["p", "Pin to the board"],
  ["r", "Reply"],
  ["c", "Compose"],
  ["1 2 3 0", "Screener: Inbox, Reading, Receipts, Block"],
  ["g then t b d n i r z s c p", "Go to Today, Board, Calendar, Notes, Inbox, Reading, Snoozed, Screener, Receipts, People"],
  ["/ or ⌘K", "Search, browse, jump — one palette"],
  ["Esc", "Dismiss"],
  ["?", "This list"],
];
