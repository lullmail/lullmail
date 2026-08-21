import { useEffect, useState } from "preact/hooks";
import { counts, layout, openCompose, palette } from "./lib/store";
import { path, routeFor } from "./lib/router";
import { Icon } from "./ui/Icon";
import { MoreMenu } from "./ui/MoreMenu";

// Seven words. No icons, no section labels, and one badge in the whole app —
// the Screener, because it is the only destination that is a to-do rather than
// a place. In Classic the nav moves into the sidebar and this line keeps only
// the wordmark and the actions.
// [href, key, label, countKey].
type NavItem = [string, string, string, keyof typeof COUNTABLE | null];
const COUNTABLE = { imbox: 1, screener: 1, feed: 1 } as const;

// Two groups, one hairline between — different kinds of thing must not sit
// in one undifferentiated row. The day's surfaces (your time and work), then
// the mail's places (where filing happens). Receipts stays palette/Today-only
// (nobody browses receipts), Snoozed is mail-on-a-timer not a destination
// (board column + g z), and the Screener — a queue, not a place — joins the
// mail group only while senders wait, its badge setting it apart.
const DAY: NavItem[] = [
  ["/today", "today", "Today", null],
  ["/board", "board", "Board", null],
  ["/calendar", "calendar", "Calendar", null],
  ["/notes", "notes", "Notes", null],
];

const MAIL: NavItem[] = [
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
        </div>

        {!classic && (
          <nav class="nav">
            {DAY.map((item) => <NavLink key={item[1]} item={item} here={here} />)}
            <span class="nav-rule" />
            {MAIL.map((item) => <NavLink key={item[1]} item={item} here={here} />)}
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
