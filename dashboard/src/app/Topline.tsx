import { useEffect, useRef, useState } from "preact/hooks";
import { accountFilter, accounts, counts, layout, openCompose, palette, setAccountFilter } from "./lib/store";
import { path, routeFor } from "./lib/router";
import { Icon } from "./ui/Icon";
import { MoreMenu } from "./ui/MoreMenu";

// Seven words. No icons, no section labels, and one badge in the whole app —
// the Screener, because it is the only destination that is a to-do rather than
// a place. In Classic the nav moves into the sidebar and this line keeps only
// the wordmark and the actions.
// The topline is mail's territory, and Today is the front door to everything
// else. Three words most days — Today, then the mail places; the Screener
// (a queue, not a place) joins only while senders wait, its badge setting it
// apart. The other day surfaces (Board, Calendar, Notes) live one hop away:
// Today's day row, the palette, g-keys, and Classic's labeled sidebar —
// six-plus words in one row was an app-switcher pretending to be a nav.
type NavItem = [string, string, string, keyof typeof COUNTABLE | null];
const COUNTABLE = { imbox: 1, screener: 1, feed: 1 } as const;

const NAV: NavItem[] = [
  ["/today", "today", "Today", null],
  ["/", "imbox", "Inbox", "imbox"],
  ["/reading", "feed", "Reading", "feed"],
];

function NavLink({ item, here }: { item: NavItem; here: string }) {
  const [href, key, label, countKey] = item;
  const n = countKey ? counts.value[countKey] || 0 : 0;
  const active = here === key;
  return (
    <a href={href} class={"nav-item" + (active ? " active" : "")} aria-current={active ? "page" : undefined}>
      {label}
      {/* The Screener is the only to-do, so it is the only badge. The rest carry
          a plain numeral, because "where did my mail go" needs an answer. */}
      {n > 0 && (
        key === "screener"
          ? <span class="nav-count">{n}</span>
          : <span class="nav-n">{n}</span>
      )}
    </a>
  );
}

/** The per-mailbox lens. One mailbox is the product's normal state — this
    only appears once a second mailbox exists, and "All" is always offered. */
function AccountPicker() {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const list = accounts.value;

  useEffect(() => {
    if (!open) return;
    const away = (ev: MouseEvent) => {
      if (!ref.current?.contains(ev.target as Node)) setOpen(false);
    };
    const t = setTimeout(() => document.addEventListener("click", away), 0);
    return () => { clearTimeout(t); document.removeEventListener("click", away); };
  }, [open]);

  // Hooks above must run unconditionally; hiding is a render concern.
  if (list.length < 2) return null;

  const current = list.find((a) => a.id === accountFilter.value);
  const label = current ? current.address.split("@")[0] : "All";
  const pick = (id: string) => () => { setOpen(false); setAccountFilter(id); };

  return (
    <div style={{ position: "relative" }} ref={ref}>
      <button class="acct-toggle" type="button" aria-expanded={open} aria-label="Choose mailbox"
        title={current ? current.address : "All mailboxes"}
        onClick={() => setOpen((v) => !v)}>
        {label}<Icon name="chevron" size={12} />
      </button>
      {open && (
        <div class="menu menu-left" role="menu">
          <button class={"menu-item" + (!current ? " lens-on" : "")} type="button" role="menuitem" onClick={pick("")}>
            All mailboxes
          </button>
          <div class="menu-rule" />
          {list.map((a) => {
            const [local, domain] = a.address.split("@");
            return (
              <button key={a.id} class={"menu-item" + (current?.id === a.id ? " lens-on" : "")} type="button"
                role="menuitem" onClick={pick(a.id)}>
                <span class="lens-label">{local || a.address}</span>
                <span class="note">{domain}</span>
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}

export function Topline() {
  const here = path.value ? routeFor(path.value).nav : "";
  const [stuck, setStuck] = useState(false);
  const classic = layout.value === "classic";
  const screenerWaiting = counts.value.screener || 0;

  useEffect(() => {
    if (classic) return; // the classic shell doesn't scroll the window
    const onScroll = () => setStuck(window.scrollY > 4);
    onScroll();
    window.addEventListener("scroll", onScroll, { passive: true });
    return () => window.removeEventListener("scroll", onScroll);
  }, [classic]);

  return (
    <header class={"topline" + (stuck || classic ? " stuck" : "")}>
      <div class="topline-in">
        <div class="topline-side left">
          <a href="/today" class="wordmark">email-soft</a>
          <AccountPicker />
        </div>

        {!classic && (
          <nav class="nav">
            {NAV.slice(0, 1).map((item) => <NavLink key={item[1]} item={item} here={here} />)}
            <span class="nav-rule" />
            {NAV.slice(1).map((item) => <NavLink key={item[1]} item={item} here={here} />)}
            {(screenerWaiting > 0 || here === "screener") && (
              <NavLink item={["/screener", "screener", "Screener", "screener"]} here={here} />
            )}
          </nav>
        )}

        <div class="topline-side right topline-acts">
          {/* Replaces the permanent search field: one glyph, same palette. */}
          <button class="btn-icon" type="button" title="Search and jump (⌘K)" aria-label="Search and jump"
            onClick={() => { palette.value = true; }}>
            <Icon name="search" size={16} />
          </button>
          <MoreMenu />
          <button class="btn btn-accent" type="button" onClick={() => openCompose()}>
            <Icon name="compose" size={14} /><span class="label">Compose</span>
          </button>
        </div>
      </div>
    </header>
  );
}
