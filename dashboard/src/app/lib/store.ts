// Cross-cutting app state. Anything two surfaces both need lives here — most
// importantly the current list and its selection, because the keyboard layer
// has to drive whichever list is on screen without knowing which view drew it.
import { signal, computed } from "@preact/signals";
import type { Bucket, Counts, ListBucket, Message, Row, ScreenerSender } from "./types";

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

export function toggleTheme() {
  const next = THEMES[(THEMES.indexOf(theme.value) + 1) % THEMES.length];
  setTheme(next);
}

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
  accountFilter.value = initialAccountFilter();
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
  try {
    localStorage.setItem("es-layout", next);
  } catch {
    /* private mode: the choice just won't survive a reload */
  }
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

export function setList(next: Partial<ListState>) {
  list.value = { ...list.value, ...next };
}

export function resetSelection() {
  cursor.value = -1;
  checked.value = new Set();
}

export function toggleChecked(id: string) {
  const next = new Set(checked.value);
  if (next.has(id)) next.delete(id);
  else next.add(id);
  checked.value = next;
}

/** Rows the verbs apply to: the explicit checkbox selection, else the cursor
    row. In document mode an open thread owns the page, so the verbs target
    it — otherwise j/k silently moved a cursor nobody can see, and the next
    e/s/i/p acted on a different thread than the one on screen. */
export function targetRows(): Row[] {
  const l = list.value;
  if (l.kind !== "rows") return [];
  if (checked.value.size) return l.rows.filter((r) => checked.value.has(r.message_id));
  if (reader.value.threadId && layout.value !== "classic") {
    const messages = reader.value.messages;
    const last = messages[messages.length - 1];
    if (!last) return [];
    return [{
      thread_id: reader.value.threadId,
      message_id: last.id,
      subject: last.subject,
      from: last.from,
      received_at: last.received_at,
      read: true,
      preview: "",
      bucket: last.bucket as Row["bucket"],
    }];
  }
  const at = l.rows[cursor.value];
  return at ? [at] : [];
}

/* ---- reader ---- */

export interface ReaderState {
  threadId: string | null;
  bucket: ListBucket | null;
  loading: boolean;
  error: string | null;
  messages: Message[];
  /** Message ids the user has opted into loading remote images for. */
  imagesOk: Set<string>;
}

export const reader = signal<ReaderState>({
  threadId: null, bucket: null, loading: false, error: null, messages: [], imagesOk: new Set(),
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

export function rememberListScroll() {
  if (typeof window !== "undefined") listScroll = window.scrollY;
}

export function closeReader() {
  reader.value = { threadId: null, bucket: null, loading: false, error: null, messages: [], imagesOk: new Set() };
  if (typeof window !== "undefined") {
    requestAnimationFrame(() => window.scrollTo({ top: listScroll }));
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
  subject: string;
  body: string;
  replyToId?: string;
  /** Shown above the fields so a reply never looks like a fresh message. */
  context?: string;
}

let draftSeq = 0;
function newDraftId(): string {
  return "d" + Date.now().toString(36) + "-" + (draftSeq++).toString(36);
}

/** Every open draft. The active one is draftStack[draftIndex]. */
export const draftStack = signal<ComposeState[]>([]);
export const draftIndex = signal(0);
export const compose = computed<ComposeState | null>(() => draftStack.value[draftIndex.value] ?? null);

export function openCompose(seed: Partial<ComposeState> = {}) {
  const stack = [...draftStack.value];
  // Opening compose when a blank draft is already queued focuses it instead
  // of stacking empties behind each other.
  if (!seed.replyToId && !seed.to) {
    const blank = stack.findIndex((d) => !d.to && !d.subject && !d.body);
    if (blank >= 0) { draftIndex.value = blank; return; }
  }
  stack.push({ id: newDraftId(), to: "", subject: "", body: "", ...seed });
  draftStack.value = stack;
  draftIndex.value = stack.length - 1;
}

/** Closes the active draft; the next one in the ring takes its place. */
export function closeCompose() {
  const stack = [...draftStack.value];
  stack.splice(draftIndex.value, 1);
  draftStack.value = stack;
  draftIndex.value = Math.min(draftIndex.value, stack.length - 1);
}

/** The carousel: rotate through open drafts, wrapping around. */
export function cycleDraft(dir: 1 | -1) {
  const n = draftStack.value.length;
  if (n > 1) draftIndex.value = (draftIndex.value + dir + n) % n;
}

/* ---- overlays ---- */

export const palette = signal<boolean>(false);
export const shortcuts = signal<boolean>(false);

/** True when a modal surface owns the keyboard. */
export const overlayOpen = computed(
  () => palette.value || shortcuts.value || compose.value !== null
);

/* ---- list-column search ---- */

export const query = signal<string>("");

/* ---- sync banner ---- */

export const syncNote = signal<string>("");
