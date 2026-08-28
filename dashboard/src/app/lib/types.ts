// Wire shapes, mirrored from the Go handlers. Kept in one place so a change
// on the server side breaks compilation here instead of a view at runtime.

/** Storage buckets, as written to hey_messages.bucket. */
export type Bucket = "imbox" | "screener" | "feed" | "paper_trail" | "set_aside" | "later";

/** What the UI lists. "snoozed" is the union of set_aside and later. */
export type ListBucket = Bucket | "snoozed";

/** Shared row shape: /buckets/{b}, /search, /recent, /folder. */
export interface Row {
  thread_id: string;
  message_id: string;
  subject: string;
  from: string;
  received_at: string;
  read: boolean;
  has_attachment?: boolean;
  preview: string;
  bucket?: string;
  thread_len?: number;
  /** Present on Snoozed rows: when a dated snooze returns. */
  snooze_until?: string;
}

/** A board card. Derived cards carry thread data only; pinned cards add
    card_id; manual notes have card_id + manual and nothing else. */
export interface BoardCard {
  card_id?: string;
  thread_id?: string;
  message_id?: string;
  subject: string;
  from?: string;
  received_at?: string;
  preview?: string;
  note?: string;
  manual?: boolean;
}

export interface Board {
  needs_you: BoardCard[];
  waiting_on: BoardCard[];
  done: BoardCard[];
}

/** A sticky on the canvas. Color indexes the client-side palette. */
export interface StickyNote {
  id: string;
  x: number;
  y: number;
  text: string;
  color: number;
}

export interface Attachment {
  part_id: string;
  filename: string;
  type: string;
  size: number;
}

/** One message inside a thread — /threads/{id}. */
export interface Message {
  id: string;
  account: string;
  subject: string;
  from: string;
  to: string;
  received_at: string;
  bucket: string;
  body: string;
  html?: string;
  attachments?: Attachment[];
}

export interface ScreenerSender {
  sender: string;
  waiting: number;
  newest: string;
  sample_subject: string;
}

export interface BriefThread {
  thread_id: string;
  message_id: string;
  subject: string;
  from: string;
  received_at: string;
  preview: string;
}

export interface Briefing {
  needs_you: BriefThread[];
  waiting_on: BriefThread[];
  feed_unread: number;
  paper_unread: number;
  screener: number;
}

export interface Person {
  sender: string;
  route: string;
  allowed: boolean;
  total: number;
  last_at: string | null;
  last_subject: string | null;
}

export interface Account {
  id: string;
  provider: string;
  address: string;
  label: string;
  backfill_days: number;
  retention_days: number;
  sync_enabled: boolean;
  last_sync_at: string | null;
  last_error: string | null;
  message_count: number;
  screener_count: number;
}

export interface Mailbox {
  name: string;
  role?: string;
}

export type Counts = Partial<Record<ListBucket, number>>;
