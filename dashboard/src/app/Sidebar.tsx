import { computed } from "@preact/signals";
import { counts, mailboxes, screeningEnabled } from "./lib/store";
import { folderLabel, folderPath, path, routeFor } from "./lib/router";

// Classic's nav. Text only and grouped, with counts — a column has room for the
// numbers the topline deliberately leaves out. Snoozed is off the nav here
// too: reachable from the board column and the palette, not browsed. The
// mailbox lens stays in the topline dropdown — a switcher, not a fixture.
//
// Two groups, and the order is the point. Folders are the real mailboxes on
// the mail server — the same ones every other client shows, so what is here is
// what is there. Lull's own sorting comes second: those are this app's
// categories, they exist nowhere else, and nothing is hidden behind them that
// a folder above will not also show.

// The standard four lead in the order every mail client uses them; anything
// else the provider reports follows alphabetically.
const FOLDER_ORDER = ["inbox", "drafts", "sent", "junk", "spam", "archive", "trash"];

const LULL: [string, string, string, keyof typeof COUNT_KEYS | null, string][] = [
  ["/", "imbox", "Focused", "imbox", "Inbox mail from senders you allowed, minus Reading and Receipts"],
  ["/screener", "screener", "Screener", "screener", "New senders waiting for your approval"],
  ["/reading", "feed", "Reading", "feed", "Allowed mail you never have to answer"],
  ["/receipts", "paper_trail", "Receipts", "paper_trail", "Confirmations and notifications"],
];

const COUNT_KEYS = {
  imbox: 1, screener: 1, feed: 1, paper_trail: 1,
} as const;

const sortedFolders = computed(() => {
  const rank = (n: string) => {
    const i = FOLDER_ORDER.indexOf(n);
    return i === -1 ? FOLDER_ORDER.length : i;
  };
  return [...mailboxes.value].sort((a, b) => rank(a.name) - rank(b.name) || a.name.localeCompare(b.name));
});

function Item({ href, nav, label, count, here, title, urgent }: {
  href: string; nav: string; label: string; count?: number; here: string; title?: string; urgent?: boolean;
}) {
  const active = here === nav;
  return (
    <a href={href} class={"side-item" + (active ? " active" : "")} title={title} aria-current={active ? "page" : undefined}>
      <span class="side-text">{label}</span>
      {!!count && <span class={"side-count" + (urgent ? " urgent" : "")}>{count}</span>}
    </a>
  );
}

export function Sidebar() {
  const here = path.value ? routeFor(path.value).nav : "";
  const c = counts.value;
  const folders = sortedFolders.value;
  // Undefined means /prefs has not answered yet; showing the Screener until it
  // does matches the server's own default, so the rail never flickers a
  // disappearing entry.
  const screening = screeningEnabled.value !== false;

  return (
    <aside class="sidebar">
      <nav>
        <div class="side-label">Mailbox</div>
        <Item href="/today" nav="today" label="Today" here={here} />
        {folders.map((f) => (
          <Item
            key={f.name}
            href={folderPath(f.name)}
            nav={"folder:" + f.name}
            label={folderLabel(f.name)}
            here={here}
          />
        ))}

        <div class="side-label">Lull</div>
        {LULL.filter(([, nav]) => screening || nav !== "screener").map(([href, nav, label, key, title]) => (
          <Item
            key={nav}
            href={href}
            nav={nav}
            label={label}
            title={title}
            here={here}
            urgent={nav === "screener"}
            count={key ? c[key] : undefined}
          />
        ))}

        <div class="side-label">People</div>
        <Item href="/people" nav="people" label="People" here={here} />
      </nav>
    </aside>
  );
}
