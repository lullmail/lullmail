import type { ComponentChildren } from "preact";

export function head() {
  return {
    title: "email-soft",
    meta: [{ name: "viewport", content: "width=device-width, initial-scale=1" }],
  };
}

// Pre-paint theme resolve (akiroo pattern): stored preference wins, else
// system. Runs before any styled element paints, so no dark flash.
const themeInit =
  "(function(){try{var t=localStorage.getItem('es-theme');" +
  "if(t!=='light'&&t!=='dark'){t=(window.matchMedia&&window.matchMedia('(prefers-color-scheme: dark)').matches)?'dark':'light';}" +
  "document.documentElement.setAttribute('data-theme',t);}catch(e){}})();";

export default function Layout(props: { children?: ComponentChildren }) {
  return (
    <div class="shell">
      <script dangerouslySetInnerHTML={{ __html: themeInit }} />
      <aside class="sidebar">
        <a href="/today" class="brand">email-soft</a>
        <nav>
          <div class="nav-label">Today</div>
          <a href="/today" data-nav="today" class="nav-today">Today</a>
          <div class="nav-label">Mailbox</div>
          <a href="/" data-nav="imbox">Imbox</a>
          <a href="/screener" data-nav="screener">Screener</a>
          <a href="/feed" data-nav="feed">Feed</a>
          <a href="/paper-trail" data-nav="paper_trail">Paper Trail</a>
          <a href="/set-aside" data-nav="set_aside">Set Aside</a>
          <a href="/later" data-nav="later">Later</a>
          <div class="nav-label">People</div>
          <a href="/people" data-nav="people">People</a>
        </nav>
        <nav class="sidebar-bottom">
          <a href="/settings/accounts" data-nav="accounts">Accounts</a>
          <button id="theme-btn" class="sidebar-theme" type="button" title="Toggle theme" aria-label="Toggle theme">
            <svg class="icon-moon" viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/></svg>
            <svg class="icon-sun" viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M6.34 17.66l-1.41 1.41M19.07 4.93l-1.41 1.41"/></svg>
          </button>
        </nav>
      </aside>
      <div class="list-col">
        <header class="topbar">
          <span class="topbar-brand">email-soft</span>
          <span id="sync-note" class="sync-note"></span>
          <div class="searchwrap">
            <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><circle cx="11" cy="11" r="7"/><path d="m20 20-3.8-3.8"/></svg>
            <input id="search" type="search" placeholder="Search the mail" autocomplete="off" />
            <span class="kbd">/</span>
          </div>
          <div class="topbar-right">
            <button id="browse-btn" class="btn-ghost btn-sm browse-btn" type="button" title="Browse everything (Cmd+K)">
              Browse <span class="kbd">⌘K</span>
            </button>
            <a href="/" class="to-mailbox" id="to-mailbox">Mailbox →</a>
            <button id="shortcuts-btn" class="btn-ghost btn-sm kbd-hint" type="button" title="Keyboard shortcuts">
              <span class="kbd">?</span>
            </button>
            <button id="compose-btn" class="btn-primary" type="button">Compose</button>
          </div>
        </header>
        {props.children}
      </div>
      <aside class="reader" id="reader">
        <div class="reader-empty">
          <div class="reader-empty-mark">✉</div>
          <div class="reader-empty-big">Nothing open</div>
          <div class="reader-empty-sub">Pick a thread — j / k to move, Enter to open.</div>
        </div>
      </aside>
      <div id="overlay" class="overlay" hidden></div>
      <div id="toast" class="toast" hidden></div>
      <script src="/app.js" defer></script>
    </div>
  );
}
