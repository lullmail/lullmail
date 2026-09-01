import { useEffect, useRef, useState } from "preact/hooks";
import { api } from "./lib/api";
import { refreshAccounts, refreshCounts, reload } from "./lib/actions";
import { accountFilter, accounts, counts, draftStack, openCompose, palette, setAccountFilter, showError, showToast } from "./lib/store";
import { path, routeFor } from "./lib/router";
import { Icon } from "./ui/Icon";
import { MoreMenu } from "./ui/MoreMenu";

// The topline in two groups with a hairline rule: the day's surfaces on the
// left of it (Today, Calendar, Board, Notes — one click, always), the mail's
// places on the right (Inbox, Reading, and the Screener only while senders
// wait — a queue, not a place, so its badge sets it apart). In Classic the
// nav moves into the sidebar and this line keeps only the wordmark.
type NavItem = [string, string, string, keyof typeof COUNTABLE | null];
const COUNTABLE = { imbox: 1, screener: 1, feed: 1 } as const;

const DAY: NavItem[] = [
  ["/today", "today", "Today", null],
  ["/calendar", "calendar", "Calendar", null],
  ["/board", "board", "Board", null],
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

export function Topline({ classic = false }: { classic?: boolean }) {
  const here = path.value ? routeFor(path.value).nav : "";
  const [stuck, setStuck] = useState(false);
  const [syncing, setSyncing] = useState(false);
  const screenerWaiting = counts.value.screener || 0;

  useEffect(() => {
    if (classic) return; // the classic shell doesn't scroll the window
    const onScroll = () => setStuck(window.scrollY > 4);
    onScroll();
    window.addEventListener("scroll", onScroll, { passive: true });
    return () => window.removeEventListener("scroll", onScroll);
  }, [classic]);

  const syncNow = async () => {
    if (syncing) return;
    const targets = accountFilter.value
      ? accounts.value.filter((account) => account.id === accountFilter.value)
      : accounts.value;
    if (!targets.length) return;
    setSyncing(true);
    try {
      await Promise.all(targets.map((account) =>
        api("/accounts/" + encodeURIComponent(account.id) + "?op=sync&wait=1", { method: "POST" })
      ));
      await Promise.all([refreshAccounts(), refreshCounts()]);
      reload();
      showToast(targets.length === 1 ? "Mailbox is up to date" : "Mailboxes are up to date");
    } catch (error) {
      showError(error instanceof Error ? error.message : "Could not sync mail");
    } finally {
      setSyncing(false);
    }
  };

  return (
    <header class={"topline" + (stuck || classic ? " stuck" : "")}>
      <div class="topline-in">
        <div class="topline-side left">
          <a href="/today" class="wordmark">email-soft</a>
          <AccountPicker />
        </div>

        {/* The day's surfaces sit in the center header in BOTH layouts — a
            clock on the wall, not a drawer. The mail places join them only
            in document mode; Classic keeps those in the sidebar. */}
        <nav class="nav">
          {DAY.map((item) => <NavLink key={item[1]} item={item} here={here} />)}
          {!classic && (
            <>
              <span class="nav-rule" />
              {MAIL.map((item) => <NavLink key={item[1]} item={item} here={here} />)}
              {(screenerWaiting > 0 || here === "screener") && (
                <NavLink item={["/screener", "screener", "Screener", "screener"]} here={here} />
              )}
            </>
          )}
        </nav>

        <div class="topline-side right topline-acts">
          <button class={"btn-icon sync-button" + (syncing ? " syncing" : "")} type="button"
            disabled={!accounts.value.length || syncing} aria-busy={syncing}
            title={syncing ? "Syncing mail…" : "Sync mail now"}
            aria-label={syncing ? "Syncing mail" : "Sync mail now"} onClick={syncNow}>
            <Icon name="refresh" size={16} />
          </button>
          {/* Replaces the permanent search field: one glyph, same palette. */}
          <button class="btn-icon" type="button" title="Search and jump (⌘K)" aria-label="Search and jump"
            onClick={() => { palette.value = true; }}>
            <Icon name="search" size={16} />
          </button>
          <MoreMenu />
          <button class="btn btn-accent" type="button" onClick={() => openCompose()}>
            <Icon name="compose" size={14} /><span class="label">Compose{draftStack.value.length > 0 ? " · " + draftStack.value.length : ""}</span>
          </button>
        </div>
      </div>
    </header>
  );
}
