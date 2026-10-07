// Cross-cutting app state. Anything two surfaces both need lives here — most
// importantly the current list and its selection, because the keyboard layer
// has to drive whichever list is on screen without knowing which view drew it.
import { signal, computed, batch } from "@preact/signals";
import type { Bucket, Counts, ListBucket, Message, Row, ScreenerSender } from "./types";
import { generationCurrent, loadDrafts, newMutationKey, OfflineStorageError, offlineGeneration, offlineOwner, offlineStorageSuspended, saveDraftFields, worthRestoring } from "./offline";

/* ---- theme ---- */

export type Theme =
  | "light" | "sepia" | "dark"
  | "terminal" | "amber" | "nord" | "dracula" | "rose" | "solarized" | "blueprint"
  | "y2k" | "1999" | "vapor";

export const THEMES: Theme[] = ["light", "sepia", "dark"];

/** Everything with a dark ground: badge/favicon inversions, sticky-note
    washes, and any code that assumes light-on-dark rendering. */
const DARK_THEMES = new Set<Theme>(["dark", "terminal", "amber", "nord", "dracula", "rose", "blueprint", "vapor"]);

export function isDarkTheme(t: Theme): boolean {
  return DARK_THEMES.has(t);
}

const ALL_THEMES = new Set<Theme>([...THEMES, "terminal", "amber", "nord", "dracula", "rose", "solarized", "blueprint", "y2k", "1999", "vapor"]);

function initialTheme(): Theme {
  if (typeof document === "undefined") return "light";
  const attr = document.documentElement.getAttribute("data-theme") as Theme | null;
  if (attr && ALL_THEMES.has(attr)) return attr;
  return "light";
}

export const theme = signal<Theme>(
  typeof document === "undefined"
    ? "light"
    : ((document.documentElement.getAttribute("data-theme") as Theme) || "light")
);

export function setTheme(next: Theme) {
  theme.value = next;
  document.documentElement.setAttribute("data-theme", next);
  try {
    localStorage.setItem("es-theme", next);
  } catch {
    /* private mode: the choice just won't survive a reload */
  }
}

/* ---- appearance: accent + type flavor ---- */

export type Accent = "ember" | "ocean" | "forest" | "violet" | "rose" | "teal" | "graphite" | "custom";
const ACCENTS: Accent[] = ["ember", "ocean", "forest", "violet", "rose", "teal", "graphite", "custom"];

function initialAccent(): Accent {
  if (typeof document === "undefined") return "ember";
  const attr = document.documentElement.getAttribute("data-accent");
  return ACCENTS.includes(attr as Accent) ? (attr as Accent) : "ember";
}

export const accent = signal<Accent>("ember");

/** The custom accent's hex, applied as inline root variables — the one
    appearance choice that cannot be a static CSS block. */
export const accentCustom = signal<string>("");

function applyCustomAccentVars(hex: string) {
  const n = parseInt(hex.slice(1), 16);
  const r = (n >> 16) & 255, g = (n >> 8) & 255, b = n & 255;
  const lum = 0.2126 * r + 0.7152 * g + 0.0722 * b;
  const s = document.documentElement.style;
  s.setProperty("--accent", hex);
  s.setProperty("--accent-ink", lum > 150 ? "#111111" : "#ffffff");
  s.setProperty("--accent-soft", "color-mix(in srgb, " + hex + " 12%, transparent)");
}

export function setAccentCustom(hex: string) {
  accentCustom.value = hex;
  accent.value = "custom";
  document.documentElement.setAttribute("data-accent", "custom");
  applyCustomAccentVars(hex);
  try {
    localStorage.setItem("es-accent", "custom");
    localStorage.setItem("es-accent-custom", hex);
  } catch { /* private mode */ }
}

export function setAccent(next: Accent) {
  accent.value = next;
  document.documentElement.setAttribute("data-accent", next);
  try {
    localStorage.setItem("es-accent", next);
  } catch { /* private mode */ }
}

export type TypeFlavor = "editorial" | "clean";

function initialType(): TypeFlavor {
  if (typeof document === "undefined") return "editorial";
  return document.documentElement.getAttribute("data-type") === "sans" ? "clean" : "editorial";
}

export const typeFlavor = signal<TypeFlavor>("editorial");

export function setTypeFlavor(next: TypeFlavor) {
  typeFlavor.value = next;
  document.documentElement.setAttribute("data-type", next === "clean" ? "sans" : "serif");
  try {
    localStorage.setItem("es-type", next);
  } catch { /* private mode */ }
}

/* ---- text size, density, reading measure ---- */

export type TextSize = "s" | "m" | "l" | "xl";
export type Density = "compact" | "comfortable" | "roomy";
export type Measure = "narrow" | "standard" | "wide";

function attrDefault(name: string, fallback: string): string {
  if (typeof document === "undefined") return fallback;
  return document.documentElement.getAttribute(name) || fallback;
}

export const textSize = signal<TextSize>("m");
export const density = signal<Density>("comfortable");
export const measure = signal<Measure>("standard");

function setAttr(key: string, value: string) {
  document.documentElement.setAttribute(key, value);
  try {
    localStorage.setItem("es-" + key.replace("data-", ""), value);
  } catch { /* private mode */ }
}

export function setTextSize(next: TextSize) { textSize.value = next; setAttr("data-textsize", next); }
export function setDensity(next: Density) { density.value = next; setAttr("data-density", next); }
export function setMeasure(next: Measure) { measure.value = next; setAttr("data-measure", next); }

/* ---- layout ---- */

export type Layout = "document" | "classic";

function initialLayout(): Layout {
  if (typeof localStorage === "undefined") return "document";
  try {
    return localStorage.getItem("es-layout") === "classic" ? "classic" : "document";
  } catch {
    return "document";
  }
}

/** Starts as "document" on the server and on the first client render alike, then
    resolves after mount — the prerendered markup has no localStorage to read,
    and Preact hydration will not repaint a mismatched shell. */
export const layout = signal<Layout>("document");

export function resolveLayout() {
  layout.value = initialLayout();
  try {
    document.documentElement.setAttribute("data-layout", layout.value);
  } catch { /* no document during prerender */ }
  accountFilter.value = initialAccountFilter();
  void hydrateDrafts();
  accent.value = initialAccent();
  typeFlavor.value = initialType();
  textSize.value = attrDefault("data-textsize", "m") as TextSize;
  density.value = attrDefault("data-density", "comfortable") as Density;
  measure.value = attrDefault("data-measure", "standard") as Measure;
  if (accent.value === "custom") {
    let hex = "";
    try { hex = localStorage.getItem("es-accent-custom") || ""; } catch { /* private mode */ }
    if (/^#[0-9a-f]{6}$/i.test(hex)) {
      accentCustom.value = hex;
      applyCustomAccentVars(hex);
    } else {
      accent.value = "ember";
    }
  }
  try {
    const split = parseInt(localStorage.getItem("es-classic-split") || "0", 10);
    if (split >= 320) splitWidth.value = split;
  } catch { /* private mode */ }
  keyUses.value = readNum("es-keyuses");
  hintsOff.value = readNum("es-hints-off") === 1;
}

export function setLayout(next: Layout) {
  layout.value = next;
  // The attribute is what the pre-paint script sets and what the loading
  // shell's CSS reads, so it has to track the live choice — a stale one would
  // frame the document layout as if it were classic.
  setAttr("data-layout", next);
}

export function toggleLayout() {
  setLayout(layout.value === "classic" ? "document" : "classic");
}

/* ---- onboarding ----
   A first-time user is the hardest case: every label here is invented
   vocabulary and every interaction is invisible. These two signals let the UI
   teach itself and then get out of the way. */

/** null = not checked yet. 0 means show the setup screen, not six empty buckets. */
export const accountCount = signal<number | null>(null);
/** The mailbox list could not be fetched, so the count stays null. Gated
    routes must stop waiting for it instead of showing a skeleton forever. */
export const accountsFailed = signal(false);

/* ---- the per-mailbox lens ----
   The unified view is the product; the lens is a scope, not a mode switch.
   Empty string = every mailbox together (the default). */

export interface AccountLite { id: string; address: string }
export const accounts = signal<AccountLite[]>([]);

function initialAccountFilter(): string {
  if (typeof localStorage === "undefined") return "";
  try {
    return localStorage.getItem("es-account") || "";
  } catch {
    return "";
  }
}

export const accountFilter = signal<string>("");

export function setAccountFilter(id: string) {
  if (id !== accountFilter.value) {
    resetSelection();
    dismissReader();
    setList({ kind: "none", key: "", rows: [], senders: [] });
  }
  accountFilter.value = id;
  try {
    localStorage.setItem("es-account", id);
  } catch {
    /* private mode: the lens just won't survive a reload */
  }
}

/** Appends the lens to any list path. Read at call time so views refetch. */
export function accountQS(path: string): string {
  const id = accountFilter.value;
  if (!id) return path;
  return path + (path.includes("?") ? "&" : "?") + "account=" + encodeURIComponent(id);
}

/* ---- classic pane split ----
   Zero means "default from the grid" — the user has never dragged. */

export const splitWidth = signal<number>(0);

export function setSplitWidth(px: number) {
  splitWidth.value = px;
  try {
    localStorage.setItem("es-classic-split", String(px));
  } catch {
    /* private mode */
  }
}

/** How many keyboard actions the user has actually used. The hint bar retires
    itself once they clearly know — teaching chrome should be temporary. */
export const keyUses = signal<number>(readNum("es-keyuses"));
export const hintsOff = signal<boolean>(readNum("es-hints-off") === 1);

function readNum(key: string): number {
  if (typeof localStorage === "undefined") return 0;
  try {
    return parseInt(localStorage.getItem(key) || "0", 10) || 0;
  } catch {
    return 0;
  }
}

function writeNum(key: string, n: number) {
  try {
    localStorage.setItem(key, String(n));
  } catch {
    /* private mode */
  }
}

export function noteKeyUse() {
  keyUses.value += 1;
  writeNum("es-keyuses", keyUses.value);
}

export function dismissHints() {
  hintsOff.value = true;
  writeNum("es-hints-off", 1);
}

/** Shown until dismissed, or until the shortcuts are visibly second nature. */
export const showHints = computed(() => !hintsOff.value && keyUses.value < 8);

/* ---- counts ---- */

export const counts = signal<Counts>({});

/** The owner's Screener preference. Off hides the Screener nav entry, which
 *  would otherwise sit there permanently empty. Undefined until /prefs lands. */
export const screeningEnabled = signal<boolean | undefined>(undefined);

/** Real provider mailboxes, lowercase as the API reports them. */
export interface Mailbox { name: string; role?: string | null }
export const mailboxes = signal<Mailbox[]>([]);

/** Unread that actually competes for attention — snoozed mail is chosen, not owed. */
export const attentionTotal = computed(() => {
  const c = counts.value;
  return (c.imbox || 0) + (c.feed || 0) + (c.paper_trail || 0);
});

/* ---- toast, with an optional single undo ---- */

export interface Toast {
  id: number;
  message: string;
  undo?: () => void;
  tone?: "normal" | "error";
}

export const toast = signal<Toast | null>(null);
let toastSeq = 0;
let toastTimer: ReturnType<typeof setTimeout> | undefined;

export function showToast(message: string, undo?: () => void, ms = 6000) {
  clearTimeout(toastTimer);
  const id = ++toastSeq;
  toast.value = { id, message, undo };
  toastTimer = setTimeout(() => {
    if (toast.value?.id === id) toast.value = null;
  }, ms);
}

export function showError(message: string, ms = 8000) {
  clearTimeout(toastTimer);
  const id = ++toastSeq;
  toast.value = { id, message, tone: "error" };
  toastTimer = setTimeout(() => {
    if (toast.value?.id === id) toast.value = null;
  }, ms);
}

export function dismissToast() {
  clearTimeout(toastTimer);
  toast.value = null;
}

/* ---- the list currently on screen ----
   Views publish their rows here so the keyboard layer, the bulk bar and the
   reader can all act on the same collection without prop-drilling. */

export type ListKind = "rows" | "senders" | "none";

export interface ListState {
  kind: ListKind;
  /** Identifies who published these rows, so a view never paints another view's
      list for a frame while its own fetch is still in flight. */
  key: string;
  loading: boolean;
  error: string | null;
  rows: Row[];
  senders: ScreenerSender[];
  /** Which list these rows came from, so an undo knows where to put them back. */
  origin: ListBucket | null;
}

export const list = signal<ListState>({
  kind: "none", key: "", loading: false, error: null, rows: [], senders: [], origin: null,
});

export const cursor = signal<number>(-1);
export const checked = signal<Set<string>>(new Set());
let selectionAnchor: string | null = null;

export function rowIdentity(row: Pick<Row, "account" | "message_id">): string {
  return row.account + "\u0000" + row.message_id;
}

export function setList(next: Partial<ListState>) {
  const previous = list.value;
  const updated = { ...previous, ...next };
  batch(() => {
    if (previous.key !== updated.key || previous.kind !== updated.kind) {
      resetSelection();
    } else if (updated.kind === "rows") {
      const focused = previous.rows[cursor.value];
      const ids = new Set(updated.rows.map(rowIdentity));
      cursor.value = focused ? updated.rows.findIndex((row) => rowIdentity(row) === rowIdentity(focused)) : -1;
      checked.value = new Set([...checked.value].filter((id) => ids.has(id)));
      if (selectionAnchor && !ids.has(selectionAnchor)) selectionAnchor = null;
    } else if (updated.kind === "senders") {
      const focused = previous.senders[cursor.value];
      cursor.value = focused ? updated.senders.findIndex((sender) => sender.sender === focused.sender) : -1;
    }
    list.value = updated;
  });
}

export function clearChecked() {
  checked.value = new Set();
  selectionAnchor = null;
}

export function resetSelection() {
  cursor.value = -1;
  clearChecked();
}

export function focusRow(index: number, keepAnchor = false) {
  const row = list.value.kind === "rows" ? list.value.rows[index] : undefined;
  cursor.value = index;
  if (!keepAnchor) selectionAnchor = row ? rowIdentity(row) : null;
}

export function toggleChecked(row: Pick<Row, "account" | "message_id">) {
  const id = rowIdentity(row);
  const index = list.value.kind === "rows" ? list.value.rows.findIndex((item) => rowIdentity(item) === id) : -1;
  if (index < 0) return;
  focusRow(index);
  const next = new Set(checked.value);
  if (next.has(id)) next.delete(id);
  else next.add(id);
  checked.value = next;
}

export function selectRange(index: number, additive = false) {
  const rows = list.value.kind === "rows" ? list.value.rows : [];
  if (!rows[index]) return;
  let start = rows.findIndex((row) => rowIdentity(row) === selectionAnchor);
  if (start < 0) start = rows[cursor.value] ? cursor.value : index;
  selectionAnchor = rowIdentity(rows[start]);
  const next = additive ? new Set(checked.value) : new Set<string>();
  for (let at = Math.min(start, index); at <= Math.max(start, index); at++) next.add(rowIdentity(rows[at]));
  checked.value = next;
  focusRow(index, true);
}

export function selectAllRows() {
  if (list.value.kind !== "rows" || !list.value.rows.length) return;
  if (!list.value.rows[cursor.value]) focusRow(0);
  checked.value = new Set(list.value.rows.map(rowIdentity));
}

export function readerOwnsPage(): boolean {
  return !!reader.value.threadId && (layout.value !== "classic" ||
    (typeof window !== "undefined" && typeof window.matchMedia === "function" && !window.matchMedia("(min-width: 1080px)").matches));
}

/** The reader's row projection of a thread's newest message: the verbs,
 *  the thread bar, and undo snapshots all address it. ONE shared
 *  conversion (LUL-F04): separate constructors dropped snooze_until, so a
 *  reader Undo of a dated snooze sent {action:"set_aside"} with no
 *  deadline and the server answered with its three-day default. The
 *  timestamp is carried verbatim — never parsed or re-rounded. */
export function readerRow(messages: Message[], threadId: string | null, bucket: ListBucket | null): Row | null {
  const last = messages[messages.length - 1];
  if (!last) return null;
  return {
    account: last.account,
    thread_id: threadId || "",
    message_id: last.id,
    subject: last.subject,
    from: last.from,
    received_at: last.received_at,
    read: true,
    preview: "",
    bucket: (last.bucket as Bucket) || (bucket === "snoozed" ? "set_aside" : bucket) || undefined,
    snooze_until: last.snooze_until,
  };
}

/** Rows the verbs apply to: the explicit checkbox selection, else the cursor
    row. In document mode an open thread owns the page, so the verbs target
    it — otherwise j/k silently moved a cursor nobody can see, and the next
    e/s/i/p acted on a different thread than the one on screen. */
export function targetRows(): Row[] {
  if (readerOwnsPage()) {
    const row = readerRow(reader.value.messages, reader.value.threadId, reader.value.bucket);
    return row ? [row] : [];
  }
  const l = list.value;
  if (l.kind !== "rows") return [];
  if (checked.value.size) return l.rows.filter((r) => checked.value.has(rowIdentity(r)));
  const at = l.rows[cursor.value];
  return at ? [at] : [];
}

/* ---- reader ---- */

export interface ReaderState {
  threadId: string | null;
  account: string | null;
  bucket: ListBucket | null;
  loading: boolean;
  error: string | null;
  messages: Message[];
  /** Message ids the user has opted into loading remote images for. */
  imagesOk: Set<string>;
}

export const reader = signal<ReaderState>({
  threadId: null, account: null, bucket: null, loading: false, error: null, messages: [], imagesOk: new Set(),
});

const imageSenderKey = "es-image-senders";
function storedImageSenders(): Set<string> {
  if (typeof window === "undefined") return new Set();
  try { return new Set(JSON.parse(localStorage.getItem(imageSenderKey) || "[]")); }
  catch { return new Set(); }
}
export const imageSenders = signal<Set<string>>(storedImageSenders());

/** Where the list was scrolled to when a thread was opened, so closing it does
    not dump you back at the top of a long bucket. */
let listScroll = 0;
let readerDismissal = 0;

export function rememberListScroll() {
  if (typeof window !== "undefined") listScroll = window.scrollY;
}

export function dismissReader() {
  readerDismissal++;
  reader.value = { threadId: null, account: null, bucket: null, loading: false, error: null, messages: [], imagesOk: new Set() };
}

export function closeReader() {
  const wasOpen = !!reader.value.threadId;
  dismissReader();
  const dismissal = readerDismissal;
  if (wasOpen && typeof window !== "undefined") {
    requestAnimationFrame(() => {
      if (readerDismissal === dismissal && !reader.value.threadId) window.scrollTo({ top: listScroll });
    });
  }
}

export function allowImages(messageId: string) {
  const next = new Set(reader.value.imagesOk);
  next.add(messageId);
  reader.value = { ...reader.value, imagesOk: next };
}

export function allowSenderImages(sender: string) {
  const key = sender.trim().toLowerCase();
  if (!key) return;
  const next = new Set(imageSenders.value); next.add(key); imageSenders.value = next;
  try { localStorage.setItem(imageSenderKey, JSON.stringify([...next])); } catch { /* privacy mode */ }
}

/* ---- compose ---- */

export interface ComposeState {
  /** Stable identity: one autosave slot per draft, carousel-safe. */
  id: string;
  to: string;
  cc?: string;
  bcc?: string;
  subject: string;
  body: string;
  /** When true the body holds HTML source, sent as a rich message. */
  htmlMode?: boolean;
  /** Product account id for new mail; mirror account id for replies. */
  accountId?: string;
  replyToId?: string;
  /** Shown above the fields so a reply never looks like a fresh message. */
  context?: string;
  /** Reused only while this draft's send content is unchanged. */
  sendKey?: string;
  /** A key whose send got no acknowledgment and could not be confirmed. It
   *  survives edits (unlike sendKey), so a changed draft is checked against
   *  it before a second copy can go out. */
  unconfirmedKey?: string;
  /** Full attachment set, carried by undo-send so a restored draft is
   * complete rather than a hand-picked subset of fields (audit SEND-05).
   * Structural twin of actions.SendAttachment; typed inline to keep this
   * module free of an actions import cycle. */
  attachments?: Array<{ filename: string; contentType: string; dataBase64: string }>;
}

let draftSeq = 0;
function newDraftId(): string {
  // crypto.randomUUID is the cross-tab uniqueness guarantee (audit 5
  // DRAFT-02): time-plus-per-tab-counter could collide between two tabs
  // creating a draft in the same millisecond, and one IndexedDB keyPath
  // means a collision silently merges two different drafts.
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return "d" + crypto.randomUUID();
  }
  return "d" + Date.now().toString(36) + "-" + (draftSeq++).toString(36) + "-" + Math.random().toString(16).slice(2, 8);
}

/** Every open draft. The active one is draftStack[draftIndex]. */
export const draftStack = signal<ComposeState[]>([]);
export const draftIndex = signal(0);
export const compose = computed<ComposeState | null>(() => draftStack.value[draftIndex.value] ?? null);

/** True when the last ring save failed (storage unavailable): surfaced
 *  as a persistent warning instead of a swallowed exception (audit 4
 *  F05). */
export const draftsUnsaved = signal(false);

/** offline-v2 drafts (audit F05 remainder / WEB-07): one draft is ONE
 *  IndexedDB record — fields and attachment payloads in the same row,
 *  the ordered ring being those rows in seq order, so the ring and the
 *  record commit together by construction. localStorage is no longer
 *  involved: no quota ceiling can drop a large draft's save. */
let draftSaveTimer: ReturnType<typeof setTimeout> | undefined;
let draftSaveQueued = false;

function fieldsOf(d: ComposeState): Omit<ComposeState, "id"> {
  const { id: _id, ...fields } = d;
  return fields;
}

async function flushDrafts(): Promise<void> {
  const gen = offlineGeneration(), owner = offlineOwner();
  const rows = [...draftStack.value];
  const current = () => generationCurrent(gen) && offlineOwner() === owner && !offlineStorageSuspended();
  draftSaveQueued = false;
  try {
    for (const { id } of rows) {
      if (!current()) return;
      const d = draftStack.value.find((live) => live.id === id);
      if (d && !await saveDraftFields(id, fieldsOf(d), gen, () => draftStack.value.find((live) => live.id === id) === d)) {
        if (current()) draftsUnsaved.value = true;
        return;
      }
    }
    if (current()) draftsUnsaved.value = false;
  } catch {
    if (current()) draftsUnsaved.value = true;
  }
}

function scheduleDraftFlush() {
  // SSR prerendering has no storage to flush to, and a timer left running
  // past the build's module lifetime is a crash, not a save.
  if (typeof window === "undefined" || !draftStack.value.length || offlineStorageSuspended()) return;
  draftSaveQueued = true;
  clearTimeout(draftSaveTimer);
  draftSaveTimer = setTimeout(() => {
    draftSaveTimer = undefined;
    void flushDrafts();
  }, 250);
}
draftStack.subscribe(() => scheduleDraftFlush());
// A reload can land inside the debounce window; the last state must win.
if (typeof window !== "undefined") {
  window.addEventListener("pagehide", () => {
    if (draftSaveQueued || draftSaveTimer !== undefined) void flushDrafts();
  });
}

/** Hydrates parked drafts from the single-record store. Runs after mount
 *  (IndexedDB is async); it never clobbers a draft the user has already
 *  started in this page. Blank drafts are not worth restoring; content
 *  and attachment-only drafts are (audit 4 F05). */
export async function hydrateDrafts(): Promise<void> {
  const gen = offlineGeneration(), owner = offlineOwner();
  try {
    const rows = await loadDrafts();
    if (!generationCurrent(gen) || offlineOwner() !== owner || offlineStorageSuspended() || draftStack.value.length || !rows.length) return;
    const live = rows
      .filter((row) => worthRestoring(row))
      .map(({ ns: _ns, seq: _seq, savedAt: _savedAt, ...rest }) => rest as ComposeState);
    if (live.length && !draftStack.value.length) {
      draftStack.value = live;
      draftIndex.value = 0;
      // Window stays closed: a reload should not slap a modal in your face.
      // The Compose button's count is the reminder.
    }
  } catch {
    // Unreadable drafts stay parked on disk; the ring starts empty rather
    // than half-restored, and the unsaved marker warns nothing persisted.
    if (generationCurrent(gen) && offlineOwner() === owner && !offlineStorageSuspended()) draftsUnsaved.value = true;
  }
}

/** The window. Closing it parks the drafts — they are only gone when sent
    or discarded, and the Compose button keeps counting them. */
export const composeOpen = signal(false);

/** Send undo window in seconds, as the server last reported it. */
export const undoSeconds = signal(5);

export function openCompose(seed: Partial<ComposeState> = {}) {
  if (seed.replyToId || seed.to) {
    pushDraft(seed);
    return;
  }
  // Plain compose is "back to my drafts": reuse a blank if one is parked,
  // else reopen the ring focused on its newest draft — never stack a fresh
  // empty behind drafts that already have content.
  const stack = draftStack.value;
  const blank = stack.findIndex((d) => !d.to && !d.subject && !d.body);
  if (blank >= 0) draftIndex.value = blank;
  else if (stack.length) draftIndex.value = stack.length - 1;
  else pushDraft(seed);
  composeOpen.value = true;
}

/** Always stacks — the explicit "+ New draft" control and c-while-composing. */
export function newDraft() {
  pushDraft({});
}

function pushDraft(seed: Partial<ComposeState>) {
  const stack = [...draftStack.value, {
    id: newDraftId(), to: "", subject: "", body: "",
    accountId: seed.accountId ?? accountFilter.value,
    ...seed,
  }];
  draftStack.value = stack;
  draftIndex.value = stack.length - 1;
  composeOpen.value = true;
}

/** Closes the window; drafts stay parked in the carousel. */
export function closeCompose() {
  composeOpen.value = false;
}

/** Removes the active draft from the ring (send or discard). The window
    follows the last one out. */
export function retireDraft(id: string) {
  releaseDraftAttachmentReads(id);
  const stack = draftStack.value.filter((d) => d.id !== id);
  draftStack.value = stack;
  draftIndex.value = Math.min(draftIndex.value, stack.length - 1);
  if (!stack.length) composeOpen.value = false;
}

/** Field edits flow back to the stack so the carousel knows what a draft
    actually contains (blank-draft reuse, park-vs-discard). */
export function updateDraft(patch: Partial<ComposeState>) {
  const stack = [...draftStack.value];
  const at = draftIndex.value;
  if (stack[at]) {
    stack[at] = patchDraft(stack[at], patch);
    draftStack.value = stack;
  }
}

/** Patch one draft BY ID (audit 5 DRAFT-01): attachment changes must
    update the exact draft they belong to — patching by index would land
    on whichever draft the carousel shows after a mid-save cycle. The
    stack is the single authoritative document; every writer (text,
    attachments, account) patches it and ONE persistence path flushes
    the whole record. */
export function updateDraftById(id: string, patch: Partial<ComposeState>) {
  const stack = [...draftStack.value];
  const at = stack.findIndex((d) => d.id === id);
  if (at < 0) return;
  stack[at] = patchDraft(stack[at], patch);
  draftStack.value = stack;
}

// Only content changes start a different submission. Persist the key with
// the draft, so parking, switching drafts, and reloading after a lost
// acknowledgment retain the retry identity. A new/undo draft starts fresh.
const sendFields: Array<keyof ComposeState> = ["to", "cc", "bcc", "subject", "body", "htmlMode", "accountId", "replyToId", "attachments"];
function patchDraft(draft: ComposeState, patch: Partial<ComposeState>): ComposeState {
  const changed = sendFields.some((field) => field in patch && JSON.stringify(patch[field]) !== JSON.stringify(draft[field]));
  return { ...draft, ...patch, sendKey: changed ? undefined : (patch.sendKey ?? draft.sendKey) };
}

/** Records (key) or clears (undefined) the draft's unconfirmed send. */
export function setUnconfirmedSend(id: string, key: string | undefined): void {
  const draft = draftStack.value.find((d) => d.id === id);
  if (draft && draft.unconfirmedKey !== key) updateDraftById(id, { unconfirmedKey: key });
}

/** The unconfirmed key that a send of this draft would NOT reuse: the draft
 *  was edited since, so sending now mints a new key and could deliver a
 *  second copy. Undefined when there is nothing to check. */
export function editedSinceUnconfirmed(id: string): string | undefined {
  const draft = draftStack.value.find((d) => d.id === id);
  return draft?.unconfirmedKey && draft.sendKey !== draft.unconfirmedKey ? draft.unconfirmedKey : undefined;
}

export const sendingDrafts = signal<Set<string>>(new Set());
export const pendingDraftReads = signal<Map<string, number>>(new Map());

export const MAX_DRAFT_ATTACHMENT_BYTES = 25 << 20;
export const MAX_DRAFT_ATTACHMENT_COUNT = 20;
export const MAX_DRAFT_MEMORY_BYTES = 128 << 20;
const MAX_ATTACHMENT_BYTES = 15 << 20;
type DraftAttachment = NonNullable<ComposeState["attachments"]>[number];
interface AttachmentReservation { draftId: string; bytes: number; reading: boolean; generation: number; owner: string }
const attachmentReservations = new Map<symbol, AttachmentReservation>();

export function attachmentBytes(attachment: DraftAttachment): number {
  const encoded = attachment.dataBase64;
  return Math.max(0, Math.floor(encoded.length * 3 / 4) - (encoded.endsWith("==") ? 2 : encoded.endsWith("=") ? 1 : 0));
}

function attachmentMemoryBytes(): number {
  // Conservative UTF-16 payload accounting plus the ArrayBuffer and
  // intermediate binary string during conversion. This is a bounded
  // application payload budget, not a promise about browser heap overhead.
  let bytes = 0;
  for (const draft of draftStack.value) {
    for (const value of [draft.to, draft.cc, draft.bcc, draft.subject, draft.body]) bytes += (value?.length ?? 0) * 2;
    for (const attachment of draft.attachments ?? []) bytes += attachment.dataBase64.length * 2;
  }
  for (const entry of attachmentReservations.values()) {
    bytes += Math.ceil(entry.bytes / 3) * 4 * 2;
    if (entry.reading) bytes += entry.bytes * 3;
  }
  return bytes;
}

function publishPendingAttachmentReads(): void {
  const counts = new Map<string, number>();
  for (const entry of attachmentReservations.values()) counts.set(entry.draftId, (counts.get(entry.draftId) ?? 0) + 1);
  pendingDraftReads.value = counts;
}

/** Reserve the entire selection synchronously before any arrayBuffer
 * call. Concurrent choosers and parked drafts share these reservations. */
export function reserveDraftAttachment(draftId: string, bytes: number): symbol | string {
  const draft = draftStack.value.find((entry) => entry.id === draftId);
  if (!draft || offlineStorageSuspended()) return "The draft is no longer active";
  if (!Number.isSafeInteger(bytes) || bytes < 0 || bytes > MAX_ATTACHMENT_BYTES) return "is over 15 MiB and was skipped";
  const pending = [...attachmentReservations.values()].filter((entry) => entry.draftId === draftId);
  const attachments = draft.attachments ?? [];
  if (attachments.length + pending.length >= MAX_DRAFT_ATTACHMENT_COUNT) return "would exceed the 20 attachments per message limit";
  const used = attachments.reduce((sum, attachment) => sum + attachmentBytes(attachment), 0) + pending.reduce((sum, entry) => sum + entry.bytes, 0);
  if (used + bytes > MAX_DRAFT_ATTACHMENT_BYTES) return "would exceed the 25 MiB per message limit";
  if (attachmentMemoryBytes() + Math.ceil(bytes / 3) * 4 * 2 > MAX_DRAFT_MEMORY_BYTES) return "would exceed the 128 MiB draft memory budget; remove attachments or discard another draft first";
  const token = Symbol(draftId);
  attachmentReservations.set(token, { draftId, bytes, reading: false, generation: offlineGeneration(), owner: offlineOwner() });
  publishPendingAttachmentReads();
  return token;
}

/** Admit transient conversion memory immediately before starting a read. */
export function beginDraftAttachmentRead(token: symbol): boolean {
  const entry = attachmentReservations.get(token);
  if (!entry || entry.reading || !generationCurrent(entry.generation) || offlineOwner() !== entry.owner || offlineStorageSuspended() || !draftStack.value.some((draft) => draft.id === entry.draftId)) return false;
  if (attachmentMemoryBytes() + entry.bytes * 3 > MAX_DRAFT_MEMORY_BYTES) return false;
  entry.reading = true;
  return true;
}

/** Release and publish a decoded file synchronously, so another selection
 * never observes a gap between its pending and completed byte accounting. */
export function finishDraftAttachmentRead(token: symbol, attachment?: DraftAttachment): void {
  const entry = attachmentReservations.get(token);
  if (!entry) return; // reset/retirement already released it
  attachmentReservations.delete(token);
  if (attachment && generationCurrent(entry.generation) && offlineOwner() === entry.owner && !offlineStorageSuspended()) {
    const draft = draftStack.value.find((item) => item.id === entry.draftId);
    if (draft) updateDraftById(entry.draftId, { attachments: [...(draft.attachments ?? []), attachment] });
  }
  publishPendingAttachmentReads();
}

function releaseDraftAttachmentReads(draftId?: string): void {
  for (const [token, entry] of attachmentReservations) {
    if (draftId === undefined || entry.draftId === draftId) attachmentReservations.delete(token);
  }
  publishPendingAttachmentReads();
}

/** Mint and persist before the first fetch; no key rotates on a failure. */
export async function prepareDraftSend(id: string): Promise<string | null> {
  const gen = offlineGeneration(), owner = offlineOwner();
  const draft = draftStack.value.find((d) => d.id === id);
  if (!draft || offlineStorageSuspended()) return null;
  const key = draft.sendKey || newMutationKey();
  updateDraftById(id, { sendKey: key });
  const saved = draftStack.value.find((d) => d.id === id)!;
  try {
    if (!await saveDraftFields(id, fieldsOf(saved), gen, () => draftStack.value.find((live) => live.id === id)?.sendKey === key)) {
      if (generationCurrent(gen)) draftsUnsaved.value = true;
      return null;
    }
  }
  catch (error) {
    if (!generationCurrent(gen)) return null;
    // Another tab sent, discarded or edited this draft: say so. The generic
    // "drafts are NOT saved" banner hid why a press of Send did nothing.
    if (error instanceof OfflineStorageError && /another tab/.test(error.message)) showError(error.message + ". Nothing was sent from this tab.");
    else draftsUnsaved.value = true;
    return null;
  }
  if (!generationCurrent(gen) || offlineOwner() !== owner || offlineStorageSuspended()) return null;
  return draftStack.value.find((d) => d.id === id)?.sendKey === key ? key : null;
}

/** Forces component-local private state (including inline replies) to unmount. */
export const privateStateVersion = signal(0);

/** Called before an offline wipe, even if storage later refuses that wipe. */
export function resetPrivateState(): void {
  clearTimeout(draftSaveTimer);
  draftSaveTimer = undefined;
  draftSaveQueued = false;
  draftStack.value = [];
  draftIndex.value = 0;
  composeOpen.value = false;
  draftsUnsaved.value = false;
  sendingDrafts.value = new Set();
  releaseDraftAttachmentReads();
  dismissToast(); // undo closures can hold a complete private sent draft
  dismissReader();
  list.value = { kind: "none", key: "", loading: false, error: null, rows: [], senders: [], origin: null };
  resetSelection();
  query.value = "";
  snoozePickerRows.value = [];
  accounts.value = [];
  accountCount.value = null;
  accountsFailed.value = false;
  accountFilter.value = "";
  counts.value = {};
  mailboxes.value = [];
  privateStateVersion.value++;
}

/** The carousel: rotate through open drafts, wrapping around. */
export function cycleDraft(dir: 1 | -1) {
  const n = draftStack.value.length;
  if (n > 1) draftIndex.value = (draftIndex.value + dir + n) % n;
}

/* ---- overlays ---- */

export const palette = signal<boolean>(false);
export const shortcuts = signal<boolean>(false);

/** Rows awaiting a date choice from the global keyboard snooze command. */
export const snoozePickerRows = signal<Row[]>([]);

/** True when a modal surface owns the keyboard. */
export const overlayOpen = computed(
  () => palette.value || shortcuts.value || composeOpen.value || snoozePickerRows.value.length > 0
);

/* ---- list-column search ---- */

export const query = signal<string>("");

export function searchMail(value: string) {
  const next = value.trim();
  dismissReader();
  if (next !== query.value) {
    setList({ kind: "none", key: "", rows: [], senders: [] });
  }
  query.value = next;
}
