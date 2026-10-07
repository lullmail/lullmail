// The keyboard layer.
//
// SPEC 16 asks for keyboard-first; the old build bound j/k/Enter and only on
// bucket pages, so Today, the Screener and the reader were mouse-only. This is
// one handler for the whole app: it reads the current list out of the store, so
// every surface that publishes rows gets the same keys for free.
import { closeCompose, compose, composeOpen, cursor, checked, clearChecked, focusRow, list, newDraft, noteKeyUse, openCompose, overlayOpen, palette, reader, readerOwnsPage, selectAllRows, selectRange, shortcuts, snoozePickerRows, toggleChecked, closeReader, targetRows, dismissToast, toast } from "./store";
import { decide, loadReplySeed, markDone, moveTo, openThread, pinThreads } from "./actions";
import { offlineOwner } from "./offline";
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

export function isTyping(target: EventTarget | null): boolean {
  const el = target as HTMLElement | null;
  if (!el || !el.closest) return false;
  return !!el.closest("input, textarea, select, [contenteditable]:not([contenteditable='false']), [role='textbox']");
}

function moveCursor(delta: number, range = false, additive = false, edge?: "first" | "last") {
  if (readerOwnsPage()) return;
  const l = list.value;
  const len = l.kind === "rows" ? l.rows.length : l.kind === "senders" ? l.senders.length : 0;
  if (!len) return;
  const at = cursor.value;
  const next = edge ? (edge === "first" ? 0 : len - 1) : at < 0 ? (delta > 0 ? 0 : len - 1) : Math.min(len - 1, Math.max(0, at + delta));
  if (range && l.kind === "rows") selectRange(next, additive);
  else focusRow(next, additive);
  const node = document.querySelector<HTMLElement>('[data-cursor-index="' + next + '"]');
  node?.scrollIntoView({ block: "nearest" });
  if (node?.hasAttribute("tabindex")) node.focus({ preventScroll: true });
}

function openAtCursor() {
  if (readerOwnsPage()) return;
  const l = list.value;
  if (l.kind !== "rows") return;
  const row = l.rows[cursor.value];
  if (row) { clearChecked(); focusRow(cursor.value); openThread(row.thread_id, row.account, l.origin); }
}

function replyToCursor() {
  const l = list.value;
  const row = l.kind === "rows" ? l.rows[cursor.value] : undefined;
  // Every verb acts on the highlighted row (see targetRows). The open thread's
  // latest message is used only when it belongs to THAT row's account and
  // thread, or when the reader owns the page; provider-local thread ids are
  // not globally unique, so matching on thread_id alone could reply to
  // another account's identically-named thread (LUL-F03). After the cursor
  // moves elsewhere, reply to the row.
  const open = reader.value.messages[reader.value.messages.length - 1];
  const matchesOpen = !!row && !!reader.value.account &&
    row.account === reader.value.account && row.thread_id === reader.value.threadId;
  const source = open && (readerOwnsPage() || !row || matchesOpen) ? open : undefined;
  if (source) {
    // Loaded reader: the server computed the default recipients from the
    // stored envelope (audit 4 F06) and they are already authoritative
    // here. Empty means "ask": the composer opens with a blank To, never
    // a From substitution (LUL-F02).
    openCompose({
      to: source.reply_to || "",
      subject: /^re:/i.test(source.subject) ? source.subject : "Re: " + (source.subject || ""),
      accountId: source.account,
      replyToId: source.id,
      context: "Replying to " + (splitFrom(source.from).name || splitFrom(source.from).email),
    });
    return;
  }
  if (!row) return;
  // Row-only path (LUL-F02): From is not the reply default. Resolve the
  // server-computed recipients for the exact account/message parent, then
  // open the composer. Guard the late completion against navigation and
  // owner changes so a stale seed cannot publish.
  const listKey = l.key;
  const owner = offlineOwner();
  const fallback = () => {
    if (list.value.key !== listKey || offlineOwner() !== owner) return;
    // The parent could not be read (never opened offline, server
    // unreachable): open with a blank To — "ask" — never substitute From.
    openCompose({
      to: "",
      subject: /^re:/i.test(row.subject) ? row.subject : "Re: " + (row.subject || ""),
      accountId: row.account,
      replyToId: row.message_id,
      context: "Replying to " + (splitFrom(row.from).name || splitFrom(row.from).email),
    });
  };
  loadReplySeed(row.account, row.thread_id, row.message_id).then((seed) => {
    if (list.value.key !== listKey || offlineOwner() !== owner) return;
    openCompose(seed);
  }, fallback);
}

export function installKeys(): () => void {
  let gPending = false;
  let gTimer: ReturnType<typeof setTimeout> | undefined;

  const cancelJump = () => { gPending = false; clearTimeout(gTimer); };
  const onKey = (ev: KeyboardEvent) => {
    if (ev.defaultPrevented || ev.isComposing || ev.keyCode === 229) { cancelJump(); return; }
    // Anything the handler actually acts on counts as learning; the hint bar
    // uses this to decide it is no longer needed.
    const learn = () => noteKeyUse();
    // Palette is global and must win everywhere, including inside inputs.
    if ((ev.metaKey || ev.ctrlKey) && !ev.altKey && !ev.shiftKey && ev.key.toLowerCase() === "k") {
      cancelJump();
      ev.preventDefault();
      palette.value = !palette.value;
      return;
    }
    if (ev.key === "Escape") {
      cancelJump();
      // An open menu owns its own Escape; it closes itself, and nothing behind it unwinds.
      if ((ev.target as HTMLElement | null)?.closest?.("[role='menu']")) return;
      if (palette.value) { palette.value = false; return; }
      if (shortcuts.value) { shortcuts.value = false; return; }
      if (snoozePickerRows.value.length) { snoozePickerRows.value = []; return; }
      if (composeOpen.value) { closeCompose(); return; }
      if (toast.value) { dismissToast(); return; }
      if (checked.value.size && !readerOwnsPage()) { clearChecked(); return; }
      if (reader.value.threadId) { closeReader(); return; }
      return;
    }
    // While composing, `c` stacks another draft onto the carousel instead of
    // being swallowed by the overlay guard.
    if (composeOpen.value && !isTyping(ev.target) && !ev.metaKey && !ev.ctrlKey && !ev.altKey && !ev.shiftKey && !ev.repeat && ev.key === "c") {
      ev.preventDefault();
      newDraft();
      return;
    }
    if (overlayOpen.value || isTyping(ev.target) || (ev.target as HTMLElement | null)?.closest?.("[role='menu']")) { cancelJump(); return; }
    const interactive = (ev.target as HTMLElement | null)?.closest?.("button, a, summary, [role='button'], [role='menuitem'], [role='separator']");
    const rowCheck = interactive?.classList.contains("row-check");
    if (interactive && (["Enter", " "].includes(ev.key) || (!rowCheck && ["ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "Home", "End"].includes(ev.key)))) { cancelJump(); return; }
    const target = ev.target as HTMLElement | null;
    const inList = !target?.closest || target === document.body || !!target.closest(".msg-list, .bulkbar");
    const multi = inList && !readerOwnsPage() && list.value.kind === "rows" && !!document.querySelector(".msg-list");
    if (multi && (ev.metaKey || ev.ctrlKey) && !ev.altKey && !ev.shiftKey && ev.key.toLowerCase() === "a") {
      cancelJump(); ev.preventDefault(); learn(); selectAllRows(); return;
    }
    if (multi && !ev.altKey && ["ArrowDown", "ArrowUp", "Home", "End"].includes(ev.key)) {
      cancelJump(); ev.preventDefault(); learn();
      moveCursor(ev.key === "ArrowUp" ? -1 : 1, ev.shiftKey, ev.ctrlKey || ev.metaKey,
        ev.key === "Home" ? "first" : ev.key === "End" ? "last" : undefined);
      return;
    }
    if (multi && ev.key === " " && !ev.altKey && !ev.shiftKey && list.value.rows[cursor.value]) {
      cancelJump(); ev.preventDefault();
      if (!ev.repeat) { learn(); toggleChecked(list.value.rows[cursor.value]); }
      return;
    }
    if (ev.metaKey || ev.ctrlKey || ev.altKey) { cancelJump(); return; }
    if (ev.repeat && !["j", "k", "ArrowDown", "ArrowUp"].includes(ev.key)) return;

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
      case "j": case "ArrowDown": if (readerOwnsPage()) return; ev.preventDefault(); learn(); moveCursor(1); return;
      case "k": case "ArrowUp": if (readerOwnsPage()) return; ev.preventDefault(); learn(); moveCursor(-1); return;
      case "Enter": case "o": if (readerOwnsPage()) return; ev.preventDefault(); learn(); openAtCursor(); return;
      case "u": ev.preventDefault(); learn(); closeReader(); return;
      case "c": ev.preventDefault(); learn(); openCompose(); return;
      case "r": ev.preventDefault(); learn(); replyToCursor(); return;
      case "/": ev.preventDefault(); learn(); palette.value = true; return;
      case "?": ev.preventDefault(); shortcuts.value = true; return;
    }

    // Screener decisions by number, in the order the buttons appear.
    if (list.value.kind === "senders" && !readerOwnsPage()) {
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
        if (readerOwnsPage()) return;
        ev.preventDefault();
        learn();
        { const at = list.value.rows[cursor.value]; if (at) toggleChecked(at); }
        return;
      case "e": ev.preventDefault(); learn(); markDone(rows); return;
      // One deferral key. Filing a single message into Reading or Receipts is
      // rare — you change the sender's rule instead — so those lost their keys.
      case "s": ev.preventDefault(); learn(); snoozePickerRows.value = rows; return;
      case "i": ev.preventDefault(); learn(); moveTo(rows, "imbox"); return;
      case "p": ev.preventDefault(); learn(); pinThreads(rows); return;
    }
  };

  document.addEventListener("keydown", onKey);
  return () => { cancelJump(); document.removeEventListener("keydown", onKey); };
}

export const SHORTCUTS: [string, string][] = [
  ["j / k or ↓ / ↑", "Move focus down / up without changing selection"],
  ["Shift + click / ↑ / ↓", "Select a range from the anchor"],
  ["Ctrl/⌘ + click", "Toggle one message"],
  ["Ctrl/⌘ + Shift + click", "Add a range to the selection"],
  ["Home / End", "Focus first / last loaded message (Shift selects range)"],
  ["Ctrl/⌘ + A", "Select all loaded messages in this list"],
  ["Enter / o", "Open the focused thread and clear selection"],
  ["u", "Back to the list"],
  ["x / Space", "Toggle selection — then any verb applies to all selected messages"],
  ["e", "Done"],
  ["s", "Choose when to snooze"],
  ["i", "Move to the Inbox"],
  ["p", "Pin to the board"],
  ["r", "Reply"],
  ["c", "Compose"],
  ["1 2 3 0", "Screener: Inbox, Reading, Receipts, Block"],
  ["y / m / w", "Calendar: year / month / week view"],
  ["t", "Calendar: jump to today"],
  ["← / →", "Calendar: previous / next period"],
  ["g then t b d n i r z s c p", "Go to Today, Board, Calendar, Notes, Inbox, Reading, Snoozed, Screener, Receipts, People"],
  ["/ or Ctrl/⌘K", "Search, browse, jump — one palette"],
  ["Esc", "Dismiss"],
  ["?", "This list"],
];
