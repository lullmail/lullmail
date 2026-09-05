// Every mutation the user can make, in one place, each one undoable.
//
// The rule this file exists to enforce: no action that moves or hides mail is
// final. Previously only "send" had an undo, so a mis-click in a bucket row
// silently relocated a thread with no way back. Each verb here captures the
// state it replaced and hands it to the toast.
import { api, ApiError, clearMemoryCache } from "./api";
import type { BoardCard, Bucket, Counts, ListBucket, Message, Row, StickyNote } from "./types";
import {
  accountCount, accountFilter, accountQS, accounts, closeReader, counts, list, type Mailbox, mailboxes, openCompose, reader, rememberListScroll, resetSelection, screeningEnabled, setAccountFilter, showError, showToast,
} from "./store";

/** The one place bucket names are written. Storage values are unchanged. */
export const BUCKET_LABEL: Record<ListBucket, string> = {
  imbox: "Inbox",
  screener: "Screener",
  feed: "Reading",
  paper_trail: "Receipts",
  snoozed: "Snoozed",
  set_aside: "Snoozed",
  later: "Snoozed",
};

/* ---- the current view's reloader, so an action can refresh whatever drew it ---- */

let reloader: () => void = () => {};

export function setReloader(fn: () => void) {
  reloader = fn;
}

export function reload() {
  clearMemoryCache();
  reloader();
}

/** The Screener switch and the server's folder list — both drive the sidebar. */
export async function refreshPrefs() {
  try {
    const p = await api<{ screening_enabled: boolean }>("/prefs", { fresh: true });
    screeningEnabled.value = p.screening_enabled;
  } catch {
    /* the nav falls back to showing the Screener, which is the safe default */
  }
}

export async function refreshFolders() {
  try {
    mailboxes.value = await api<Mailbox[]>(accountQS("/mailboxes"), { fresh: true });
  } catch {
    /* a folder list that will not load leaves the rail on buckets alone */
  }
}

export async function refreshCounts() {
  try {
    counts.value = await api<Counts>(accountQS("/counts"), { fresh: true });
  } catch {
    /* counts are decoration; a failure here must not disturb the view */
  }
}

/** Reload the mailbox list everywhere it is consumed (picker, welcome gate,
    lens validation) — one source of truth for connect/disconnect events. */
export async function refreshAccounts() {
  try {
    const rows = await api<{ id: string; address: string }[]>("/accounts", { fresh: true });
    accounts.value = rows;
    accountCount.value = rows.length;
    // A lens pointing at a mailbox that no longer exists (disconnected while
    // lensed, or another owner's id in this browser) is a silent dead-end:
    // every list would fetch an empty account forever. Fall back to All.
    if (accountFilter.value && !rows.some((a) => a.id === accountFilter.value)) {
      setAccountFilter("");
    }
  } catch {
    /* the welcome gate keeps its last known count */
  }
}

/* ---- primitives ---- */

type ActionName = Bucket | "read" | "unread";

async function actOn(account: string, messageId: string, action: ActionName, untilDays?: number) {
  await api("/messages/" + encodeURIComponent(messageId) + "/action?account=" + encodeURIComponent(account), {
    body: untilDays ? { action, until_days: untilDays } : { action },
  });
}

async function actMany(rows: Row[], action: ActionName, untilDays?: number) {
  await Promise.all(rows.map((r) => actOn(r.account, r.message_id, action, untilDays)));
}

/** Where a row should go back to if the user undoes.
    The Snoozed list mixes two storage buckets, so the row's own value wins —
    it is the only thing that knows whether the snooze had a date. */
function originOf(row: Row): Bucket {
  const own = row.bucket as Bucket | undefined;
  if (own && own !== ("snoozed" as unknown as Bucket)) return own;
  const origin = list.value.origin;
  if (!origin || origin === "snoozed") return "set_aside";
  return origin;
}

function afterMutation() {
  resetSelection();
  reload();
  refreshCounts();
}

function describe(rows: Row[], verbPhrase: string): string {
  return rows.length === 1
    ? verbPhrase
    : rows.length + " threads " + verbPhrase.toLowerCase();
}

/* ---- verbs ---- */

/** Done = read and out of the way. The inverse is exact, so the undo is honest. */
export async function markDone(rows: Row[]) {
  if (!rows.length) return;
  const previouslyUnread = rows.filter((r) => !r.read);
  try {
    await actMany(rows, "read");
    if (rows.some((r) => r.thread_id === reader.value.threadId && r.account === reader.value.account)) closeReader();
    afterMutation();
    showToast(describe(rows, "Done"), () => undoRead(previouslyUnread));
  } catch (e) {
    fail(e, "Could not mark done");
  }
}

async function undoRead(rows: Row[]) {
  if (!rows.length) return;
  try {
    await actMany(rows, "unread");
    afterMutation();
  } catch (e) {
    fail(e, "Could not undo");
  }
}

export async function markRead(rows: Row[], read: boolean) {
  if (!rows.length) return;
  try {
    await actMany(rows, read ? "read" : "unread");
    afterMutation();
    showToast(describe(rows, read ? "Marked read" : "Marked unread"), () =>
      markRead(rows, !read)
    );
  } catch (e) {
    fail(e, "Could not update");
  }
}

/** Rows the verbs apply to. Captured with their original snooze so an undo
    restores the exact deferral, not a generic three days. */
interface Before { row: Row; from: Bucket; days?: number }

function snoozeUndoState(r: Row): { from: Bucket; days?: number } {
  const from = originOf(r);
  if (from === "later") return { from };
  if (from === "set_aside" && r.snooze_until) {
    const until = new Date(r.snooze_until).getTime();
    if (!isNaN(until)) {
      const days = Math.max(1, Math.ceil((until - Date.now()) / 86400000));
      return { from, days };
    }
  }
  return { from };
}

export async function moveTo(rows: Row[], to: Bucket) {
  if (!rows.length) return;
  const before: Before[] = rows.map((r) => ({ row: r, ...snoozeUndoState(r) }));
  try {
    await actMany(rows, to);
    if (rows.some((r) => r.thread_id === reader.value.threadId && r.account === reader.value.account)) closeReader();
    afterMutation();
    showToast(describe(rows, "Moved to " + BUCKET_LABEL[to]), () => restore(before));
  } catch (e) {
    fail(e, "Could not move");
  }
}

/** days = 0 means someday: snoozed with no return date. */
export async function snooze(rows: Row[], days: number) {
  if (!rows.length) return;
  const before: Before[] = rows.map((r) => ({ row: r, ...snoozeUndoState(r) }));
  try {
    await actMany(rows, days > 0 ? "set_aside" : "later", days > 0 ? days : undefined);
    if (rows.some((r) => r.thread_id === reader.value.threadId && r.account === reader.value.account)) closeReader();
    afterMutation();
    const when = days === 0 ? "for someday" : days === 1 ? "until tomorrow" : "for " + days + " days";
    showToast(describe(rows, "Snoozed " + when), () => restore(before));
  } catch (e) {
    fail(e, "Could not snooze that");
  }
}

async function restore(before: Before[]) {
  try {
    await Promise.all(before.map(({ row, from, days }) =>
      actOn(row.account, row.message_id, from, from === "set_aside" ? days : undefined)));
    afterMutation();
  } catch (e) {
    fail(e, "Could not undo");
  }
}

/* ---- board ---- */

/** Pin = a marker, not a move: the mail stays in whatever bucket it is in. */
export async function pinThreads(rows: Row[]) {
  const threads = [...new Map(rows.map((r) => [r.account + "\u0000" + r.thread_id, r])).values()];
  if (!threads.length) return;
  try {
    const pinned: string[] = [];
    for (const row of threads) {
      const res = await api<BoardCard>("/board/pin", { body: { account: row.account, thread_id: row.thread_id } });
      if (res.card_id) pinned.push(res.card_id);
    }
    afterMutation();
    showToast(describe(rows, "Pinned to the board"), () => removeCards(pinned, true));
  } catch (e) {
    fail(e, "Could not pin that");
  }
}

export async function removeCard(card: BoardCard) {
  if (!card.card_id) return;
  try {
    await api("/board/unpin", { body: { card_id: card.card_id } });
    afterMutation();
    showToast(card.manual ? "Note deleted" : "Unpinned", () => restoreCard(card));
  } catch (e) {
    fail(e, "Could not remove that");
  }
}

async function removeCards(ids: string[], quiet = false) {
  try {
    for (const id of ids) await api("/board/unpin", { body: { card_id: id } });
    afterMutation();
    if (!quiet) showToast("Removed from the board");
  } catch (e) {
    fail(e, "Could not remove that");
  }
}

async function restoreCard(card: BoardCard) {
  try {
    if (card.account && card.thread_id && !card.manual) {
      await api("/board/pin", { body: { account: card.account, thread_id: card.thread_id } });
    } else {
      await api("/board/cards", { body: { title: card.subject, note: card.note || "" } });
    }
    afterMutation();
  } catch (e) {
    fail(e, "Could not restore that");
  }
}

/** Done on a pinned card or note checks the card off; derived cards resolve
    by reading (markDone) instead, because they are made of mail. */
export async function setCardDone(card: BoardCard, done: boolean) {
  if (!card.card_id) return;
  try {
    await api("/board/cards/" + encodeURIComponent(card.card_id) + "/done", {
      body: { done },
    });
    afterMutation();
    showToast(done ? "Done" : "Back on the board", () => setCardDone(card, !done));
  } catch (e) {
    fail(e, "Could not update that card");
  }
}

export async function addCard(title: string, note: string): Promise<boolean> {
  try {
    await api("/board/cards", { body: { title, note } });
    afterMutation();
    return true;
  } catch (e) {
    fail(e, "Could not add that");
    return false;
  }
}

/* ---- stickies ---- */

export async function createNote(x: number, y: number, text: string, color = 0): Promise<StickyNote | null> {
  try {
    return await api<StickyNote>("/notes", { body: { x, y, text, color } });
  } catch (e) {
    fail(e, "Could not stick that");
    return null;
  }
}

/** Silent by design: saving a position or a keystroke must not refetch the
    whole wall (that would flicker and drop scroll). */
export async function saveNote(id: string, patch: Partial<Pick<StickyNote, "x" | "y" | "text" | "color">>) {
  try {
    await api("/notes/" + encodeURIComponent(id), { body: patch });
  } catch (e) {
    fail(e, "Could not save that note");
  }
}

export async function throwAwayNote(note: StickyNote, after: () => void) {
  try {
    await api("/notes/" + encodeURIComponent(note.id), { method: "DELETE" });
    after();
    showToast("Thrown away", async () => {
      const again = await createNote(note.x, note.y, note.text, note.color);
      if (again) after();
    }, 8000);
  } catch (e) {
    fail(e, "Could not throw that away");
  }
}

/* ---- screener ---- */

export async function decide(sender: string, allow: boolean, route: Bucket | "blocked") {
  try {
    await api("/screener/decide", { body: { sender, allow, route } });
    afterMutation();
    const label = allow ? "→ " + BUCKET_LABEL[route as Bucket] : "blocked";
    showToast(sender + " " + label, () => undecide(sender, true));
  } catch (e) {
    fail(e, "Could not save that decision");
  }
}

/** Returns a sender to the Screener — the undo for `decide`, and the unblock on People. */
export async function undecide(sender: string, quiet = false) {
  try {
    await api("/screener/undecide", { body: { sender } });
    afterMutation();
    if (!quiet) showToast(sender + " is back in the Screener");
  } catch (e) {
    fail(e, "Could not undo that decision");
  }
}

/* ---- reader ---- */

export async function openThread(threadId: string, account: string, bucket: ListBucket | null) {
  rememberListScroll();
  window.scrollTo({ top: 0 });
  reader.value = {
    threadId, account, bucket, loading: true, error: null, messages: [], imagesOk: new Set(),
  };
  try {
    const messages = await api<Message[]>("/threads/" + encodeURIComponent(threadId) + "?account=" + encodeURIComponent(account));
    if (reader.value.threadId !== threadId || reader.value.account !== account) return;
    reader.value = { ...reader.value, loading: false, messages };
    const last = messages[messages.length - 1];
    if (last) {
      await actOn(last.account, last.id, "read");
      refreshCounts();
      markRowRead(threadId, account);
    }
  } catch (e) {
    if (reader.value.threadId !== threadId || reader.value.account !== account) return;
    reader.value = {
      ...reader.value, loading: false,
      error: e instanceof Error ? e.message : "Could not open that thread",
    };
  }
}

/** Greys the row immediately instead of waiting for a whole-list refetch. */
function markRowRead(threadId: string, account: string) {
  const l = list.value;
  if (l.kind !== "rows") return;
  let touched = false;
  const rows = l.rows.map((r) => {
    if (r.thread_id === threadId && r.account === account && !r.read) {
      touched = true;
      return { ...r, read: true };
    }
    return r;
  });
  if (touched) list.value = { ...l, rows };
}

/* ---- send, with the existing server-side undo window ---- */

export interface SendAttachment {
  filename: string;
  contentType: string;
  /** Raw base64 of the file bytes, no data-url prefix. */
  dataBase64: string;
}

export interface SendInput {
  to: string;
  cc?: string;
  bcc?: string;
  subject: string;
  text: string;
  /** Optional rich body; the server derives the plain-text alternative when absent. */
  html?: string;
  accountId?: string;
  replyToId?: string;
  attachments?: SendAttachment[];
}

export async function sendMail(input: SendInput): Promise<boolean> {
  try {
    const res = await api<{ queued: string }>("/send", {
      body: {
        to: input.to,
        cc: input.cc || "",
        bcc: input.bcc || "",
        subject: input.subject,
        text: input.text,
        html: input.html || "",
        account_id: input.accountId || "",
        reply_to_message_id: input.replyToId || "",
        attachments: (input.attachments || []).map((a) => ({
          filename: a.filename,
          content_type: a.contentType,
          data_base64: a.dataBase64,
        })),
      },
    });
    showToast(
      "Sending in 5s",
      async () => {
        try {
          await api("/outbox/" + encodeURIComponent(res.queued), { method: "DELETE" });
          // The toast promised the draft comes back — so it has to actually
          // come back, seeded exactly as it was sent.
          openCompose({
            to: input.to, subject: input.subject,
            body: input.html || input.text,
            htmlMode: !!input.html,
            accountId: input.accountId,
            replyToId: input.replyToId,
          });
          showToast("Send cancelled — your draft is back");
        } catch (e) {
          fail(e, "Too late to cancel");
        }
      },
      5500
    );
    return true;
  } catch (e) {
    fail(e, "Could not send");
    return false;
  }
}

/* ---- errors ---- */

function fail(e: unknown, fallback: string) {
  if (e instanceof ApiError && e.status === 401) return; // the gate takes over
  showError(e instanceof Error && e.message ? fallback + ": " + e.message : fallback);
}
