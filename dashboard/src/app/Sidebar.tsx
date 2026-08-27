import { accountFilter, accounts, counts, setAccountFilter } from "./lib/store";
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

/** The mailbox lens lives at the head of the rail — where an account
    switcher belongs in a three-pane client. "All" is always offered. */
function SideAccountPicker() {
  const list = accounts.value;
  if (list.length < 2) return null;
  const current = list.find((a) => a.id === accountFilter.value);
  return (
    <div class="side-acct">
      <div class="side-label">Mailboxes</div>
      <button
        class={"side-item side-acct-all" + (!current ? " active" : "")}
        type="button"
        onClick={() => setAccountFilter("")}
      >
        <span class="side-text">All</span>
      </button>
      {list.map((a) => {
        const [local, domain] = a.address.split("@");
        return (
          <button
            key={a.id}
            class={"side-item side-acct-one" + (current?.id === a.id ? " active" : "")}
            type="button"
            title={a.address}
            onClick={() => setAccountFilter(a.id)}
          >
            <span class="side-text">{local || a.address}</span>
            <span class="side-acct-domain">{domain}</span>
          </button>
        );
      })}
    </div>
  );
}

export function Sidebar() {
  const here = path.value ? routeFor(path.value).nav : "";
  const c = counts.value;
  return (
    <aside class="sidebar">
      <nav>
        {/* The day's surfaces lead, Calendar first; then the mail's places;
            then people. Settings stay in the overflow menu, not the rail. */}
        <SideAccountPicker />
        <div class="side-label">Day</div>
        <Item href="/today" nav="today" label="Today" here={here} />
        <Item href="/calendar" nav="calendar" label="Calendar" here={here} />
        <Item href="/board" nav="board" label="Board" here={here} />
        <Item href="/notes" nav="notes" label="Notes" here={here} />

        <div class="side-label">Mailbox</div>
        {MAILBOX.map(([href, nav, label, key]) => (
          <Item key={nav} href={href} nav={nav} label={label} here={here} count={key ? c[key] : undefined} />
        ))}

        <div class="side-label">People</div>
        <Item href="/people" nav="people" label="People" here={here} />
      </nav>
    </aside>
  );
}
