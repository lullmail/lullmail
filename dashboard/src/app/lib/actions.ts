// Every mutation the user can make, in one place, each one undoable.
//
// The rule this file exists to enforce: no action that moves or hides mail is
// final. Previously only "send" had an undo, so a mis-click in a bucket row
// silently relocated a thread with no way back. Each verb here captures the
// state it replaced and hands it to the toast.
import { api, ApiError, clearMemoryCache, QueuedOffline, StaleOwnerError } from "./api";
import { generationCurrent, offlineGeneration, offlineOwner, offlineStorageSuspended } from "./offline";
import type { BoardCard, Bucket, Counts, ListBucket, Message, Row, StickyNote } from "./types";
import {
  accountCount, accountsFailed, accountFilter, accountQS, accounts, closeReader, counts, list, type Mailbox, mailboxes, openCompose, reader, rememberListScroll, resetSelection, screeningEnabled, setAccountFilter, showError, showToast, undoSeconds,
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
    accountsFailed.value = false;
    // A lens pointing at a mailbox that no longer exists (disconnected while
    // lensed, or another owner's id in this browser) is a silent dead-end:
    // every list would fetch an empty account forever. Fall back to All.
    if (accountFilter.value && !rows.some((a) => a.id === accountFilter.value)) {
      setAccountFilter("");
    }
  } catch (e) {
    /* the welcome gate keeps its last known count; with none, release the
       routes that were waiting for it (a stale owner's rejection is not a failure) */
    if (accountCount.value === null && !(e instanceof StaleOwnerError)) accountsFailed.value = true;
  }
}

/* ---- primitives ---- */

type ActionName = Bucket | "read" | "unread";

/** The snooze wire contract (audit DATA-07): `until` is an absolute UTC
 *  RFC3339 instant captured at user-intent time, so offline replay
 *  applies the exact intended deadline whenever it runs, and undo sends
 *  the row's exact prior instant. */
async function actOn(account: string, messageId: string, action: ActionName, until?: string) {
  await api("/messages/" + encodeURIComponent(messageId) + "/action?account=" + encodeURIComponent(account), {
    body: until ? { action, until } : { action },
  });
}

async function actMany(rows: Row[], action: ActionName, until?: string) {
  await Promise.all(rows.map((r) => actOn(r.account, r.message_id, action, until)));
}

/** Per-row outcome of a bulk mutation: which rows actually changed, which
 *  requests failed, and which were parked for offline replay. A queued
 *  mutation is neither — it has NOT committed, so it must not be reported
 *  as done (no false undo) nor as failed (it will apply on reconnect)
 *  (audit 3 WEB-07). */
async function actManySettled(rows: Row[], action: ActionName, until?: string) {
  const results = await Promise.allSettled(rows.map((r) => actOn(r.account, r.message_id, action, until)));
  const changed: Row[] = [];
  const failed: Row[] = [];
  const queued: Row[] = [];
  results.forEach((res, i) => {
    if (res.status === "fulfilled") changed.push(rows[i]);
    else if (res.reason instanceof QueuedOffline) queued.push(rows[i]);
    else failed.push(rows[i]);
  });
  return { changed, failed, queued };
}

/** Reconcile the view after a (possibly partial or fully offline) bulk
 * mutation and say which subset failed. Returns the rows the user can
 * still undo, plus whether the whole action is merely parked offline. */
function settleMutation(changed: Row[], failed: Row[], queued: Row[], verb: string): { done: Row[]; allQueued: boolean } {
  afterMutation();
  if (failed.length) {
    showError(`Could not ${verb} ${failed.length} of ${failed.length + changed.length + queued.length} threads`);
  } else if (queued.length > 0 && changed.length === 0) {
    showToast(`Offline — ${queued.length === 1 ? "saved" : queued.length + " actions saved"} for when you're back online`);
  }
  return { done: changed, allQueued: queued.length > 0 && changed.length === 0 && failed.length === 0 };
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
  const { changed, failed, queued } = await actManySettled(rows, "read");
  const { done } = settleMutation(changed, failed, queued, "mark done");
  if (!done.length) return;
  if (rows.some((r) => r.thread_id === reader.value.threadId && r.account === reader.value.account)) closeReader();
  // Only rows that were unread before the action flip back on undo; rows
  // already read must stay read, and rows whose request failed were never
  // marked and must not be touched at all.
  const undoRows = done.filter((r) => !r.read);
  showToast(describe(done, "Done"), () => undoRead(undoRows));
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
  const { changed, failed, queued } = await actManySettled(rows, read ? "read" : "unread");
  const { done } = settleMutation(changed, failed, queued, "update");
  if (!done.length) return;
  // Snapshot the flipped rows' original state: undo restores what each row
  // was, never a blanket inverse that would unread rows the user had
  // already read before the action. Rows already in the target state, or
  // whose request failed, are not captured and stay untouched by undo.
  const before = done
    .filter((row) => !!row.read !== read)
    .map((row) => ({ row, was: !!row.read }));
  showToast(describe(done, read ? "Marked read" : "Marked unread"), () => undoMarkRead(before));
}

async function undoMarkRead(before: { row: Row; was: boolean }[]) {
  if (!before.length) return;
  try {
    await Promise.all(before.map(({ row, was }) => actOn(row.account, row.message_id, was ? "read" : "unread")));
    afterMutation();
  } catch (e) {
    fail(e, "Could not undo");
  }
}

/** Rows the verbs apply to. Captured with their original snooze so an undo
    restores the exact deferral — the prior bucket AND the prior instant,
    verbatim (audit DATA-07). */
interface Before { row: Row; from: Bucket; until?: string }

function snoozeUndoState(r: Row): { from: Bucket; until?: string } {
  const from = originOf(r);
  if (from === "set_aside" && r.snooze_until) return { from, until: r.snooze_until };
  return { from };
}

export async function moveTo(rows: Row[], to: Bucket) {
  if (!rows.length) return;
  const before = new Map(rows.map((r) => [r, snoozeUndoState(r)] as const));
  const { changed, failed, queued } = await actManySettled(rows, to);
  const { done } = settleMutation(changed, failed, queued, "move");
  if (!done.length) return;
  if (rows.some((r) => r.thread_id === reader.value.threadId && r.account === reader.value.account)) closeReader();
  showToast(describe(done, "Moved to " + BUCKET_LABEL[to]), () =>
    restore(done.map((row) => ({ row, ...before.get(row)! })))
  );
}

/** days = 0 means someday: snoozed with no return date. The deadline is
 *  computed HERE, at user-intent time, and travels as an absolute instant
 *  — offline replay applies exactly this deadline (audit DATA-07). */
export async function snooze(rows: Row[], days: number) {
  if (!rows.length) return;
  const until = days > 0 ? new Date(Date.now() + days * 86400000).toISOString() : undefined;
  const before = new Map(rows.map((r) => [r, snoozeUndoState(r)] as const));
  const { changed, failed, queued } = await actManySettled(rows, days > 0 ? "set_aside" : "later", until);
  const { done } = settleMutation(changed, failed, queued, "snooze");
  if (!done.length) return;
  if (rows.some((r) => r.thread_id === reader.value.threadId && r.account === reader.value.account)) closeReader();
  const when = days === 0 ? "for someday" : days === 1 ? "until tomorrow" : "for " + days + " days";
  showToast(describe(done, "Snoozed " + when), () =>
    restore(done.map((row) => ({ row, ...before.get(row)! })))
  );
}

async function restore(before: Before[]) {
  try {
    await Promise.all(before.map(({ row, from, until }) =>
      actOn(row.account, row.message_id, from, from === "set_aside" ? until : undefined)));
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
    // Only cards this operation CREATED are undoable: the server reports
    // created=false for a thread that was already pinned, and deleting that
    // card on undo would erase a pin (and its note/state) which existed
    // before the pin was clicked (audit 4 F20).
    const pinned: string[] = [];
    for (const row of threads) {
      const res = await api<BoardCard>("/board/pin", { body: { account: row.account, thread_id: row.thread_id } });
      if (res.card_id && res.created) pinned.push(res.card_id);
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
      try {
        await actOn(last.account, last.id, "read");
      } catch (e) {
        // Queued offline is a parked intent, not a failed thread open; the
        // mark-read replays with everything else (audit 3 WEB-07).
        if (!(e instanceof QueuedOffline)) console.warn("could not mark thread read", e);
      }
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

/** How a send ended for the caller. "accepted": the server holds it (queued
 *  or already submitted). "kept": a definite answer that it is not going out
 *  from this draft (refused, failed, ambiguous, cancelled); the draft stays.
 *  "unconfirmed": no acknowledgment arrived and the lookup could not confirm
 *  a row; the draft keeps its key, and an unchanged re-send reuses it. */
export type SendOutcome = "accepted" | "kept" | "unconfirmed";

type OutboxStatus = "pending" | "submitting" | "submitted" | "failed" | "ambiguous" | "cancelled";
interface SendReceipt { queued: string; undo_seconds: number; status?: OutboxStatus; durable?: boolean }
/** GET /outbox?key= — the list entry for one submission key. */
export interface SubmissionRecord { id: string; status: OutboxStatus; undo_until: string }

interface SendFlight { body: string; result: Promise<SendOutcome>; accepted: Promise<boolean> }
const sendFlights = new Map<string, SendFlight>();

/** Callers retain the key for one unchanged submission. This is only the
 * server's immutable durable submission identity. */
export function sendMail(input: SendInput, idempotencyKey: string): Promise<boolean> {
  return startSend(input, idempotencyKey).accepted;
}

export function sendMailOutcome(input: SendInput, idempotencyKey: string): Promise<SendOutcome> {
  return startSend(input, idempotencyKey).result;
}

function startSend(input: SendInput, idempotencyKey: string): SendFlight {
  const snapshot: SendInput = { ...input, attachments: input.attachments?.map((a) => ({ ...a })) };
  const scope = offlineOwner() + "\n" + offlineGeneration() + "\n" + idempotencyKey;
  const body = JSON.stringify(snapshot);
  const active = sendFlights.get(scope);
  if (active?.body === body) return active;
  const result = submitMail(snapshot, idempotencyKey);
  const flight = { body, result, accepted: result.then((outcome) => outcome === "accepted") };
  sendFlights.set(scope, flight);
  void result.finally(() => { if (sendFlights.get(scope) === flight) sendFlights.delete(scope); });
  return flight;
}

/** Pauses between submission lookups after an unacknowledged send. Mutable
 *  only so tests need not wait. */
export const submissionLookup = { backoffMs: [500, 1500, 3000] };

/** A send with no acknowledgment may still have been committed: a network
 *  failure, a timeout, or a 5xx from the server or a proxy. A 4xx is the
 *  server's definite answer. */
function outcomeUnknown(e: unknown): boolean {
  if (e instanceof StaleOwnerError || e instanceof QueuedOffline) return false;
  if (e instanceof ApiError) return e.status >= 500 || e.status === 408;
  return true;
}

/** Resolves a submission key against the server, retrying with backoff.
 *  "missing": the server answered every time that it holds no such
 *  submission. "unknown": the last lookup itself failed. */
export async function lookupSubmission(key: string, backoffMs = submissionLookup.backoffMs): Promise<SubmissionRecord | "missing" | "unknown"> {
  let verdict: "missing" | "unknown" = "unknown";
  for (let attempt = 0; ; attempt++) {
    try {
      return await api<SubmissionRecord>("/outbox?key=" + encodeURIComponent(key), { fresh: true });
    } catch (e) {
      if (e instanceof StaleOwnerError) throw e;
      verdict = e instanceof ApiError && e.status === 404 ? "missing" : "unknown";
    }
    if (attempt >= backoffMs.length) return verdict;
    await new Promise((resolve) => setTimeout(resolve, backoffMs[attempt]));
  }
}

async function submitMail(input: SendInput, idempotencyKey: string): Promise<SendOutcome> {
  const owner = offlineOwner(), gen = offlineGeneration();
  const current = () => !offlineStorageSuspended() && offlineOwner() === owner && generationCurrent(gen);
  let res: SendReceipt;
  try {
    res = await api<SendReceipt>("/send", {
      idempotencyKey,
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
  } catch (e) {
    if (!current()) return "kept";
    if (!outcomeUnknown(e)) {
      fail(e, "Could not send");
      return "kept";
    }
    // The request may have been committed before its answer was lost:
    // resolve it instead of reporting a failure the user would "fix" by
    // sending a second copy.
    showToast("Checking whether the message was queued…", undefined, 15_000);
    let found: SubmissionRecord | "missing" | "unknown";
    try {
      found = await lookupSubmission(idempotencyKey);
    } catch {
      return "kept";
    }
    if (!current()) return "kept";
    if (found === "missing" || found === "unknown") {
      showError("The send could not be confirmed. Your draft is kept: sending it again unchanged is safe, or check Outbox first.", 12_000);
      return "unconfirmed";
    }
    const remaining = found.status === "pending" ? Math.max(0, Math.floor((Date.parse(found.undo_until) - Date.now()) / 1000)) : 0;
    res = { queued: found.id, undo_seconds: remaining, status: found.status, durable: true };
  }
  if (!current()) return "kept";
  if (res.status === "ambiguous" || res.status === "failed" || res.status === "cancelled") {
    showError(res.status === "ambiguous" ? "This send may already have reached the recipient. Check Outbox and Sent before sending again." : "This submission was not sent. Its saved copy is in Outbox; this draft has been kept.");
    return "kept";
  }
  const window = Math.max(0, res.undo_seconds ?? 5);
  if (window === 0) {
    showToast(res.status === "submitted" ? "Message submitted" : "Send already queued — the undo window has ended");
    return "accepted";
  }
  undoSeconds.value = window;
  const queued = res.queued;
  showToast(
    `Sending in ${window}s`,
    async () => {
      try {
        if (!current()) return;
        await api("/outbox/" + encodeURIComponent(queued), { method: "DELETE" });
        if (!current()) return;
        // The toast promised the draft comes back — so it has to actually
        // come back complete: recipients, Cc/Bcc, body mode, sending
        // account, reply parent, and every attachment (audit SEND-05).
        openCompose({
          to: input.to, cc: input.cc || "", bcc: input.bcc || "",
          subject: input.subject,
          body: input.html || input.text,
          htmlMode: !!input.html,
          accountId: input.accountId,
          replyToId: input.replyToId,
          attachments: input.attachments || [],
        });
        showToast("Send cancelled — your draft is back");
      } catch (e) {
        fail(e, "Too late to cancel");
      }
    },
    window * 1000 + 500
  );
  return "accepted";
}

/** The blocking question before a possible second copy goes out. */
export const CONFIRM_RESEND = "This message may already have been sent. Check the Outbox before sending it again, or send anyway.\n\nOK sends it anyway. Cancel keeps the draft.";

/** Before a draft edited after an unconfirmed send gets a new key, looks the
 *  old key up again (once: that request is long over). "clear": the server
 *  holds no such send, or one that provably did not go out, so a new
 *  submission cannot duplicate it. "exists": it was queued or may have been
 *  sent. "unknown": the lookup failed. */
export async function checkEarlierSubmission(key: string): Promise<"clear" | "exists" | "unknown"> {
  const found = await lookupSubmission(key, []);
  if (found === "missing") return "clear";
  if (found === "unknown") return "unknown";
  return found.status === "cancelled" || found.status === "failed" ? "clear" : "exists";
}

/* ---- errors ---- */

function fail(e: unknown, fallback: string) {
  if (e instanceof StaleOwnerError) return;
  if (e instanceof ApiError && e.status === 401) return; // the gate takes over
  if (e instanceof QueuedOffline) {
    showToast("Offline — saved for when you're back online");
    return;
  }
  showError(e instanceof Error && e.message ? fallback + ": " + e.message : fallback);
}
