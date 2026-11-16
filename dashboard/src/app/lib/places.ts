// Every place in the app, said once.
//
// The top bar, the classic sidebar, the phone's tab bar and More sheet, the
// "…" menu, the palette and the g-keys each used to keep a list of their
// own. They drifted: Receipts could be reached from the sidebar and the
// palette but not from the document layout's menus, Outreach had to be added
// in five files, and a label changed in one list stayed stale in another.
// Each surface now picks from this table and says only which places it shows.
import { counts, hasOutreach, screeningEnabled } from "./store";
import type { IconName } from "../ui/Icon";

export type PlaceGroup = "day" | "mail" | "sent" | "people" | "app";

export interface Place {
  /** Highlight key; matches the route's `nav` and, for lists, the API's bucket name. */
  nav: string;
  href: string;
  label: string;
  /** One line on what is there, for tooltips and the phone sheet. */
  sub: string;
  group: PlaceGroup;
  icon: IconName;
  /** The /counts number shown beside it. Only places with something to clear
      have one: Receipts file themselves and are never "owed", so no numeral. */
  count?: "imbox" | "screener" | "feed";
  /** The letter after `g` that jumps here. */
  go?: string;
  /** Sits in the document layout's top bar. */
  primary?: boolean;
  /** One of the phone's fixed tabs. */
  tab?: boolean;
}

export const PLACES: Place[] = [
  { nav: "today", href: "/today", label: "Today", sub: "What needs you now", group: "day", icon: "sun", go: "t", primary: true, tab: true },
  { nav: "calendar", href: "/calendar", label: "Calendar", sub: "When snoozed mail comes back", group: "day", icon: "aside", go: "d", primary: true },
  { nav: "board", href: "/board", label: "Board", sub: "What needs you, what you're waiting on", group: "day", icon: "pin", go: "b", primary: true },
  { nav: "notes", href: "/notes", label: "Notes", sub: "Thoughts, not tasks", group: "day", icon: "document", go: "n", primary: true },

  { nav: "imbox", href: "/", label: "Inbox", sub: "Mail from senders you allowed", group: "mail", icon: "inbox", count: "imbox", go: "i", primary: true, tab: true },
  { nav: "screener", href: "/screener", label: "Screener", sub: "New senders waiting for your decision", group: "mail", icon: "shield", count: "screener", go: "s", primary: true, tab: true },
  { nav: "feed", href: "/reading", label: "Reading", sub: "Allowed mail you never have to answer", group: "mail", icon: "book", count: "feed", go: "r", primary: true, tab: true },
  { nav: "paper_trail", href: "/receipts", label: "Receipts", sub: "Orders, confirmations, notifications", group: "mail", icon: "later", go: "c" },
  { nav: "snoozed", href: "/snoozed", label: "Snoozed", sub: "Everything you put off", group: "mail", icon: "aside", go: "z" },

  { nav: "sent", href: "/sent", label: "Sent", sub: "Everything you've written", group: "sent", icon: "reply" },
  { nav: "outreach", href: "/outreach", label: "Outreach", sub: "Sent from your outreach mailboxes", group: "sent", icon: "arrow" },
  { nav: "outbox", href: "/outbox", label: "Outbox", sub: "Sends in progress", group: "sent", icon: "arrow" },

  { nav: "people", href: "/people", label: "People", sub: "Who you've decided about", group: "people", icon: "check", go: "p" },
  { nav: "settings", href: "/settings", label: "Settings", sub: "Mailboxes, appearance, security", group: "app", icon: "settings" },
];

const BY_NAV = new Map(PLACES.map((p) => [p.nav, p]));

export function place(nav: string): Place {
  const found = BY_NAV.get(nav);
  if (!found) throw new Error("no such place: " + nav);
  return found;
}

/** Whether a place has anything to offer right now.
 *
 *  A queue with nothing in it is not a place worth a row: the Screener with
 *  no senders waiting, Snoozed with nothing put off, the Outbox with no send
 *  in flight or stopped, Outreach with no outreach mailbox. They appear when
 *  they hold something and stay while you are standing in them, so a list
 *  never vanishes from under you as its last item leaves.
 *
 *  This is about navigation chrome only. The palette and the g-keys reach
 *  every place regardless, so nothing is ever unreachable. */
export function placeOffered(p: Place, here = ""): boolean {
  if (p.nav === here) return true;
  const c = counts.value;
  switch (p.nav) {
    // Undefined means /prefs has not answered; the server's default is on.
    case "screener": return screeningEnabled.value !== false && (c.screener || 0) > 0;
    case "snoozed": return (c.snoozed || 0) > 0;
    case "outbox": return (c.outbox || 0) > 0;
    case "outreach": return hasOutreach.value;
    default: return true;
  }
}

/** The numeral beside a place, or 0 for none. */
export function placeCount(p: Place): number {
  return p.count ? counts.value[p.count] || 0 : 0;
}

export function placesIn(group: PlaceGroup, here = ""): Place[] {
  return PLACES.filter((p) => p.group === group && placeOffered(p, here));
}

/** `g` then a letter. */
export const GOTO: Record<string, string> = Object.fromEntries(
  PLACES.filter((p) => p.go).map((p) => [p.go!, p.href]),
);
