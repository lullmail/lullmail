import { useEffect, useRef, useState } from "preact/hooks";
import { lazy, Suspense } from "preact/compat";
import { api, authed, authReady, authStatus, refreshAuth } from "./lib/api";
import { signal } from "@preact/signals";
import { path, routeFor, startRouter } from "./lib/router";
import { installKeys } from "./lib/keys";
import { refreshAccounts, refreshCounts } from "./lib/actions";
import { accountCount, accountFilter, attentionTotal, compose, layout, palette, query, reader, resolveLayout, shortcuts, theme } from "./lib/store";
import { Topline } from "./Topline";
import { Sidebar } from "./Sidebar";
import { Thread } from "./reader/Thread";
import { Compose } from "./ui/Compose";
import { Palette } from "./ui/Palette";
import { Shortcuts } from "./ui/Shortcuts";
import { Toast } from "./ui/Toast";
import { Gate } from "./ui/Gate";
import { ListSkeleton, RouteSkeleton } from "./ui/bits";
import { TodayView } from "./views/TodayView";
import { BucketView } from "./views/BucketView";
import { ScreenerView } from "./views/ScreenerView";
import { SearchView } from "./views/SearchView";
import { Welcome } from "./views/Welcome";
import { KeyHints } from "./ui/KeyHints";
import { offline, startPWA } from "./lib/pwa";
import { startOfflineData } from "./lib/offline";

// The mailbox loop stays in the first bundle. Larger secondary workspaces and
// settings load only when opened, keeping the gate and Inbox quick on modest
// phones without changing the app's one-screen-at-a-time character.
const BoardView = lazy(() => import("./views/BoardView").then((m) => ({ default: m.BoardView })));
const NotesView = lazy(() => import("./views/NotesView").then((m) => ({ default: m.NotesView })));
const CalendarView = lazy(() => import("./views/CalendarView").then((m) => ({ default: m.CalendarView })));
const PeopleView = lazy(() => import("./views/PeopleView").then((m) => ({ default: m.PeopleView })));
const AccountsView = lazy(() => import("./views/AccountsView").then((m) => ({ default: m.AccountsView })));
const SecurityView = lazy(() => import("./views/SecurityView").then((m) => ({ default: m.SecurityView })));

/** Classic needs three columns' worth of room; below that the preference is
    honoured by falling back rather than by cramming. */
function useWide(): boolean {
  const [wide, setWide] = useState(true);
  useEffect(() => {
    const mq = window.matchMedia("(min-width: 1080px)");
    const sync = () => setWide(mq.matches);
    sync();
    mq.addEventListener("change", sync);
    return () => mq.removeEventListener("change", sync);
  }, []);
  return wide;
}

function CurrentView() {
  // A live search takes over the column from whatever route drew it, so there
  // is exactly one place results appear.
  const q = query.value.trim();
  if (q) return <SearchView q={q} />;

  const route = routeFor(path.value);
  // Nothing connected yet: six empty buckets read as "no mail", which is the
  // wrong answer. Settings still opens so they can actually connect one.
  if (accountCount.value === 0 && route.kind !== "accounts" && route.kind !== "security") return <Welcome />;
  switch (route.kind) {
    case "today": return <TodayView />;
    case "board": return <BoardView />;
    case "notes": return <NotesView />;
    case "calendar": return <CalendarView />;
    case "screener": return <ScreenerView />;
    case "people": return <PeopleView />;
    case "accounts": return <AccountsView />;
    case "security": return <SecurityView />;
    default: return <BucketView bucket={route.bucket || "imbox"} />;
  }
}

function columnClass(): string {
  if (query.value.trim()) return "column";
  const kind = routeFor(path.value).kind;
  return kind === "board" || kind === "calendar" || kind === "notes"
    ? "column workspace-column"
    : "column";
}

/** A recovery-code or TOTP sign-in means this device has no passkey. One
    quiet line points at the Security page before the user forgets. Dismissal
    lasts the browser session — a new session can reasonably re-nudge. */
const passkeyNudgeDismissed = signal(
  typeof sessionStorage !== "undefined" && sessionStorage.getItem("es-nudge-off") === "1"
);

function PasskeyNudge() {
  const via = authStatus.value?.via;
  if (passkeyNudgeDismissed.value || (via !== "recovery" && via !== "totp")) return null;
  const method = via === "totp" ? "an authenticator code" : "a recovery code";
  return (
    <div class="nudge" role="status">
      <span>
        You signed in with {method}.{" "}
        <a href="/settings/security">Add a passkey on this device</a> so next time is one touch.
      </span>
      <button type="button" onClick={() => {
        passkeyNudgeDismissed.value = true;
        try { sessionStorage.setItem("es-nudge-off", "1"); } catch { /* private mode */ }
      }}>Dismiss</button>
    </div>
  );
}

/** Keeps the tab title and favicon in step with what is actually waiting. */
function TabBadge() {
  const count = attentionTotal.value;
  // Sepia is a light ground; the favicon inverts only for true dark.
  const dark = theme.value === "dark";
  useEffect(() => {
    let link = document.querySelector<HTMLLinkElement>("link[rel='icon']");
    if (!link) {
      link = document.createElement("link");
      link.rel = "icon";
      document.head.appendChild(link);
    }
    document.title = count > 0 ? "(" + count + ") email-soft" : "email-soft";
    const bg = dark ? "#ecedf1" : "#20242b";
    const fg = dark ? "#20242b" : "#f4efe6";
    const glyph =
      count > 0
        ? `<text x="32" y="43" font-family="system-ui,sans-serif" font-size="${count > 99 ? 24 : 34}" font-weight="700" text-anchor="middle" fill="${fg}">${count > 99 ? "99+" : count}</text>`
        : `<rect x="16" y="21" width="32" height="22" rx="3" fill="none" stroke="${fg}" stroke-width="3.5"/><path d="m16 21 16 12 16-12" fill="none" stroke="${fg}" stroke-width="3.5" stroke-linejoin="round"/>`;
    const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" rx="14" fill="${bg}"/>${glyph}</svg>`;
    link.href = "data:image/svg+xml," + encodeURIComponent(svg);
  }, [count, dark]);
  return null;
}

export default function App() {
  const openThreadId = reader.value.threadId;
  const wide = useWide();
  // The island is prerendered at build time, where there is no token and no URL.
  // Painting the chrome until mount keeps the prerendered markup and the first
  // client render identical, so hydration has nothing to reconcile.
  const [mounted, setMounted] = useState(false);

  useEffect(() => {
    setMounted(true);
    resolveLayout();
    startRouter();
    const offKeys = installKeys();
    const offPWA = startPWA();
    const offData = startOfflineData();
    refreshAuth().catch(() => {});
    const tick = setInterval(() => { if (authed.value) refreshCounts(); }, 45000);
    return () => { offKeys(); offPWA(); offData(); clearInterval(tick); };
  }, []);

  useEffect(() => {
    if (!mounted || !authed.value) return;
    refreshCounts();
    refreshAccounts();
  }, [mounted, authed.value]);

  // The lens is global: every badge follows it, not just the visible list.
  // The mount pass is covered by the auth effect above, so skip it.
  const firstLens = useRef(true);
  useEffect(() => {
    if (firstLens.current) { firstLens.current = false; return; }
    if (mounted && authed.value) refreshCounts();
  }, [accountFilter.value]);

  if (!mounted || !authReady.value) {
    return (
      <div class="page">
        <Topline />
        <div class="column"><ListSkeleton /></div>
      </div>
    );
  }

  if (!authed.value) {
    return (
      <div class="page">
        <Gate />
      </div>
    );
  }

  const classic = layout.value === "classic" && wide;

  if (classic) {
    return (
      <div class="page classic">
        <Topline />
        <PasskeyNudge />
        <Sidebar />
        <div class="list-pane"><div class="column"><Suspense fallback={<RouteSkeleton />}><CurrentView /></Suspense></div></div>
        <div class="reader-pane">
          {openThreadId ? (
            <Thread backTo={routeFor(path.value).title} variant="pane" />
          ) : (
            <div class="reader-empty">
              <div class="reader-empty-mark">✉</div>
              <div class="reader-empty-big">Nothing open</div>
              <div class="reader-empty-sub">
                Pick a thread — <span class="kbd">j</span> <span class="kbd">k</span> to move,{" "}
                <span class="kbd">↵</span> to open.
              </div>
            </div>
          )}
        </div>
        <Overlays />
      </div>
    );
  }

  return (
    <div class="page">
      <Topline />
      <PasskeyNudge />
      {/* A thread replaces the list rather than sitting beside it: the mail
          gets the whole window, and there is never an empty pane. */}
      {openThreadId ? (
        <Thread backTo={routeFor(path.value).title} />
      ) : (
        <div class={columnClass()}><Suspense fallback={<RouteSkeleton />}><CurrentView /></Suspense></div>
      )}

      <Overlays />
    </div>
  );
}

function Overlays() {
  return (
    <>
      {palette.value && <Palette />}
      {shortcuts.value && <Shortcuts />}
      {compose.value && <Compose />}
      {/* The thread has its own verb bar with key chips; two stacked bars is noise. */}
      {accountCount.value !== 0 && !reader.value.threadId && <KeyHints />}
      <Toast />
      <TabBadge />
      {offline.value && <div class="offline-note" role="status">Offline — cached mail is available; safe actions are queued for reconnect.</div>}
    </>
  );
}
