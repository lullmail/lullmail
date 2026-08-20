import { counts } from "./lib/store";
import { path, routeFor } from "./lib/router";

// Classic's nav. Text only and grouped, with counts — a column has room for the
// numbers the topline deliberately leaves out. Snoozed is off the nav here
// too: reachable from the board column and the palette, not browsed.
const MAILBOX: [string, string, string, keyof typeof COUNT_KEYS | null][] = [
  ["/", "imbox", "Inbox", "imbox"],
  ["/screener", "screener", "Screener", "screener"],
  ["/reading", "feed", "Reading", "feed"],
  ["/receipts", "paper_trail", "Receipts", "paper_trail"],
];

const COUNT_KEYS = {
  imbox: 1, screener: 1, feed: 1, paper_trail: 1,
} as const;

function Item({ href, nav, label, count, here }: {
  href: string; nav: string; label: string; count?: number; here: string;
}) {
  const active = here === nav;
  return (
    <a href={href} class={"side-item" + (active ? " active" : "")} aria-current={active ? "page" : undefined}>
      <span class="side-text">{label}</span>
      {!!count && <span class={"side-count" + (nav === "screener" ? " urgent" : "")}>{count}</span>}
    </a>
  );
}

export function Sidebar() {
  const here = path.value ? routeFor(path.value).nav : "";
  const c = counts.value;
  return (
    <aside class="sidebar">
      <nav>
        <Item href="/today" nav="today" label="Today" here={here} />
        <Item href="/board" nav="board" label="Board" here={here} />
        <Item href="/calendar" nav="calendar" label="Calendar" here={here} />
        <Item href="/notes" nav="notes" label="Notes" here={here} />

        <div class="side-label">Mailbox</div>
        {MAILBOX.map(([href, nav, label, key]) => (
          <Item key={nav} href={href} nav={nav} label={label} here={here} count={key ? c[key] : undefined} />
        ))}

        <div class="side-label">People</div>
        <Item href="/people" nav="people" label="People" here={here} />

        {/* Settings belongs at the foot, not filed under People. */}
        <div class="side-spacer" />
        <Item href="/settings/accounts" nav="accounts" label="Accounts" here={here} />
      </nav>
    </aside>
  );
}
