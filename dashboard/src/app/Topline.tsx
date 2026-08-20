import { useEffect, useState } from "preact/hooks";
import { counts, layout, openCompose, palette } from "./lib/store";
import { path, routeFor } from "./lib/router";
import { Icon } from "./ui/Icon";
import { MoreMenu } from "./ui/MoreMenu";

// Seven words. No icons, no section labels, and one badge in the whole app —
// the Screener, because it is the only destination that is a to-do rather than
// a place. In Classic the nav moves into the sidebar and this line keeps only
// the wordmark and the actions.
// [href, key, label, countKey]. The rule splits the daily loop from the places
// mail files itself into — seven unfamiliar words in an even row give a
// newcomer nothing to hold on to.
type NavItem = [string, string, string, keyof typeof COUNTABLE | null];
const COUNTABLE = { imbox: 1, screener: 1, feed: 1 } as const;

// Four words, plus the Board experiment while it is under test. Receipts is
// deliberately absent: nobody browses receipts, they search them — it lives
// in the palette and on Today. The Screener joins only while senders are
// waiting, because it is a queue you empty, not a place. Snoozed is off the
// nav too: it is Inbox mail on a timer, not a destination — the board's
// third column and the palette (g z) reach it.
const NAV: NavItem[] = [
  ["/today", "today", "Today", null],
  ["/board", "board", "Board", null],
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
            {NAV.map((item) => <NavLink key={item[1]} item={item} here={here} />)}
            {(screenerWaiting > 0 || here === "screener") && (
              <>
                <span class="nav-rule" />
                <NavLink item={["/screener", "screener", "Screener", "screener"]} here={here} />
              </>
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
