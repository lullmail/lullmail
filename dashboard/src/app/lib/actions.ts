// Every mutation the user can make, in one place, each one undoable.
//
// The rule this file exists to enforce: no action that moves or hides mail is
// final. Previously only "send" had an undo, so a mis-click in a bucket row
// silently relocated a thread with no way back. Each verb here captures the
// state it replaced and hands it to the toast.
import { api, ApiError } from "./api";
import type { Bucket, Counts, ListBucket, Message, Row } from "./types";
import {
  closeReader, counts, list, reader, rememberListScroll, resetSelection, showError, showToast,
} from "./store";

/** The one place bucket names are written. Storage values are unchanged. */
export const BUCKET_LABEL: Record<ListBucket, string> = {
  imbox: "Imbox",
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
  reloader();
}

export async function refreshCounts() {
  try {
    counts.value = await api<Counts>("/counts");
  } catch {
    /* counts are decoration; a failure here must not disturb the view */
  }
}

/* ---- primitives ---- */

type ActionName = Bucket | "read" | "unread";

async function actOn(messageId: string, action: ActionName, untilDays?: number) {
  await api("/messages/" + encodeURIComponent(messageId) + "/action", {
    body: untilDays ? { action, until_days: untilDays } : { action },
  });
}

async function actMany(rows: Row[], action: ActionName, untilDays?: number) {
  await Promise.all(rows.map((r) => actOn(r.message_id, action, untilDays)));
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
    if (rows.some((r) => r.thread_id === reader.value.threadId)) closeReader();
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

export async function moveTo(rows: Row[], to: Bucket) {
  if (!rows.length) return;
  const before = rows.map((r) => ({ row: r, from: originOf(r) }));
  try {
    await actMany(rows, to);
    if (rows.some((r) => r.thread_id === reader.value.threadId)) closeReader();
    afterMutation();
    showToast(describe(rows, "Moved to " + BUCKET_LABEL[to]), () => restore(before));
  } catch (e) {
    fail(e, "Could not move");
  }
}

/** days = 0 means someday: snoozed with no return date. */
export async function snooze(rows: Row[], days: number) {
  if (!rows.length) return;
  const before = rows.map((r) => ({ row: r, from: originOf(r) }));
  try {
    await actMany(rows, days > 0 ? "set_aside" : "later", days > 0 ? days : undefined);
    if (rows.some((r) => r.thread_id === reader.value.threadId)) closeReader();
    afterMutation();
    const when = days === 0 ? "for someday" : days === 1 ? "until tomorrow" : "for " + days + " days";
    showToast(describe(rows, "Snoozed " + when), () => restore(before));
  } catch (e) {
    fail(e, "Could not snooze that");
  }
}

async function restore(before: { row: Row; from: Bucket }[]) {
  try {
    await Promise.all(before.map(({ row, from }) => actOn(row.message_id, from)));
    afterMutation();
  } catch (e) {
    fail(e, "Could not undo");
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

export async function openThread(threadId: string, bucket: ListBucket | null) {
  rememberListScroll();
  window.scrollTo({ top: 0 });
  reader.value = {
    threadId, bucket, loading: true, error: null, messages: [], imagesOk: new Set(),
  };
  try {
    const messages = await api<Message[]>("/threads/" + encodeURIComponent(threadId));
    if (reader.value.threadId !== threadId) return; // superseded by a faster click
    reader.value = { ...reader.value, loading: false, messages };
    const last = messages[messages.length - 1];
    if (last) {
      await actOn(last.id, "read");
      refreshCounts();
      markRowRead(threadId);
    }
  } catch (e) {
    if (reader.value.threadId !== threadId) return;
    reader.value = {
      ...reader.value, loading: false,
      error: e instanceof Error ? e.message : "Could not open that thread",
    };
  }
}

/** Greys the row immediately instead of waiting for a whole-list refetch. */
function markRowRead(threadId: string) {
  const l = list.value;
  if (l.kind !== "rows") return;
  let touched = false;
  const rows = l.rows.map((r) => {
    if (r.thread_id === threadId && !r.read) {
      touched = true;
      return { ...r, read: true };
    }
    return r;
  });
  if (touched) list.value = { ...l, rows };
}

/* ---- send, with the existing server-side undo window ---- */

export interface SendInput {
  to: string;
  subject: string;
  text: string;
  replyToId?: string;
}

export async function sendMail(input: SendInput): Promise<boolean> {
  try {
    const res = await api<{ queued: string }>("/send", {
      body: {
        to: input.to,
        subject: input.subject,
        text: input.text,
        reply_to_message_id: input.replyToId || "",
      },
    });
    showToast(
      "Sending in 5s",
      async () => {
        try {
          await api("/outbox/" + encodeURIComponent(res.queued), { method: "DELETE" });
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
