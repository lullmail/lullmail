import type { ComponentChildren } from "preact";

export function head() {
  return {
    title: "email-soft",
    meta: [{ name: "viewport", content: "width=device-width, initial-scale=1" }],
  };
}

export default function Layout(props: { children?: ComponentChildren }) {
  return (
    <div class="shell">
      <aside class="sidebar">
        <a href="/" class="brand">email-soft</a>
        <nav>
          <a href="/" data-nav="imbox">Imbox</a>
          <a href="/screener" data-nav="screener">Screener</a>
          <a href="/feed" data-nav="feed">Feed</a>
          <a href="/paper-trail" data-nav="paper_trail">Paper Trail</a>
          <a href="/set-aside" data-nav="set_aside">Set Aside</a>
          <a href="/later" data-nav="later">Later</a>
        </nav>
        <nav class="sidebar-bottom">
          <a href="/settings/accounts" data-nav="accounts">Accounts</a>
        </nav>
      </aside>
      <div class="main">
        <header class="topbar">
          <button id="compose-btn" class="primary" type="button">Compose</button>
          <span id="sync-note" class="sync-note"></span>
        </header>
        {props.children}
      </div>
      <div id="overlay" class="overlay" hidden></div>
      <div id="toast" class="toast" hidden></div>
      <script src="/app.js" defer></script>
    </div>
  );
}
