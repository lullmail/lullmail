import { useEffect, useState } from "preact/hooks";
import { api, authed } from "./lib/api";
import { path, routeFor, startRouter } from "./lib/router";
import { installKeys } from "./lib/keys";
import { refreshCounts } from "./lib/actions";
import { accountCount, attentionTotal, compose, layout, palette, query, reader, resolveLayout, shortcuts, theme } from "./lib/store";
import { Topline } from "./Topline";
import { Sidebar } from "./Sidebar";
import { Thread } from "./reader/Thread";
import { Compose } from "./ui/Compose";
import { Palette } from "./ui/Palette";
import { Shortcuts } from "./ui/Shortcuts";
import { Toast } from "./ui/Toast";
import { Gate } from "./ui/Gate";
import { ListSkeleton } from "./ui/bits";
import { TodayView } from "./views/TodayView";
import { BucketView } from "./views/BucketView";
import { ScreenerView } from "./views/ScreenerView";
import { PeopleView } from "./views/PeopleView";
import { AccountsView } from "./views/AccountsView";
import { SearchView } from "./views/SearchView";
import { Welcome } from "./views/Welcome";
import { KeyHints } from "./ui/KeyHints";

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
  if (accountCount.value === 0 && route.kind !== "accounts") return <Welcome />;
  switch (route.kind) {
    case "today": return <TodayView />;
    case "screener": return <ScreenerView />;
    case "people": return <PeopleView />;
    case "accounts": return <AccountsView />;
    default: return <BucketView bucket={route.bucket || "imbox"} />;
  }
}

/** Keeps the tab title and favicon in step with what is actually waiting. */
function TabBadge() {
  const count = attentionTotal.value;
  const dark = theme.value === "dark";
  useEffect(() => {
    let link = document.querySelector<HTMLLinkElement>("link[rel='icon']");
    if (!link) {
      link = document.createElement("link");
      link.rel = "icon";
      document.head.appendChild(link);
    }
    document.title = count > 0 ? "(" + count + ") email-soft" : "email-soft";
    const bg = dark ? "#ecedf1" : "#15161a";
    const fg = dark ? "#0f1013" : "#ffffff";
    const glyph =
      count > 0
        ? `<text x="32" y="43" font-family="system-ui,sans-serif" font-size="${count > 99 ? 24 : 34}" font-weight="700" text-anchor="middle" fill="${fg}">${count > 99 ? "99+" : count}</text>`
        : `<text x="32" y="43" font-family="Georgia,serif" font-size="34" font-weight="700" text-anchor="middle" fill="${fg}">&#9993;</text>`;
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
    refreshCounts();
    // Classification is idempotent; running it on boot means a freshly synced
    // message is bucketed before the user notices it missing.
    api("/classify", { method: "POST" }).then(refreshCounts).catch(() => {});
    // Drives the first-run screen. Only a successful empty list means "no
    // mailboxes"; an error must not be mistaken for one.
    api<unknown[]>("/accounts")
      .then((rows) => { accountCount.value = rows.length; })
      .catch(() => {});
    const tick = setInterval(refreshCounts, 45000);
    return () => { offKeys(); clearInterval(tick); };
  }, []);

  if (!mounted) {
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
        <Sidebar />
        <div class="list-pane"><div class="column"><CurrentView /></div></div>
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
      {/* A thread replaces the list rather than sitting beside it: the mail
          gets the whole window, and there is never an empty pane. */}
      {openThreadId ? (
        <Thread backTo={routeFor(path.value).title} />
      ) : (
        <div class="column"><CurrentView /></div>
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
    </>
  );
}
