import { useEffect, useRef } from "preact/hooks";
import { counts, moreSheet, screeningEnabled } from "../lib/store";
import { path, routeFor } from "../lib/router";
import { Icon, type IconName } from "./Icon";

// The phone shell. A header row of text links cannot hold eight places at
// 390px — it scrolled sideways and hid half of them — so on a phone the
// places move to the thumb: four tabs that are always one tap away, and a
// sheet for the rest. CSS shows this only at phone widths; wider windows
// keep the topline nav and never see it.
type Tab = [href: string, nav: string, label: string, icon: IconName];

const TABS: Tab[] = [
  ["/today", "today", "Today", "sun"],
  ["/", "imbox", "Inbox", "inbox"],
  ["/screener", "screener", "Screener", "shield"],
  ["/reading", "feed", "Reading", "book"],
];

const MORE: { title: string; items: [href: string, nav: string, label: string, sub: string][] }[] = [
  { title: "Your day", items: [
    ["/board", "board", "Board", "What needs you, what you're waiting on"],
    ["/calendar", "calendar", "Calendar", "When snoozed mail comes back"],
    ["/notes", "notes", "Notes", "Thoughts, not tasks"],
  ] },
  { title: "Mail", items: [
    ["/receipts", "paper_trail", "Receipts", "Orders, confirmations, notifications"],
    ["/sent", "sent", "Sent", "Everything you've written"],
    ["/snoozed", "snoozed", "Snoozed", "Everything you put off"],
    ["/people", "people", "People", "Who you've decided about"],
    ["/outbox", "outbox", "Outbox", "Sends in progress"],
  ] },
  { title: "Lull Mail", items: [
    ["/settings", "settings", "Settings", "Mailboxes, appearance, security"],
  ] },
];

export function TabBar() {
  const here = path.value ? routeFor(path.value).nav : "";
  const open = moreSheet.value;
  const setOpen = (next: boolean | ((was: boolean) => boolean)) => {
    moreSheet.value = typeof next === "function" ? next(moreSheet.value) : next;
  };
  const sheetRef = useRef<HTMLDivElement>(null);
  const moreRef = useRef<HTMLButtonElement>(null);
  // The Screener tab stays put while screening is on, so the bar never
  // reshuffles under the thumb; with screening off it would be a dead end.
  const tabs = TABS.filter((t) => t[1] !== "screener" || screeningEnabled.value !== false || here === "screener");
  const inTabs = tabs.some((t) => t[1] === here);

  // Any navigation — a sheet link, the back gesture — puts the sheet away.
  useEffect(() => { setOpen(false); }, [path.value]);

  useEffect(() => {
    if (!open) return;
    sheetRef.current?.querySelector<HTMLAnchorElement>("a")?.focus({ preventScroll: true });
    const key = (ev: KeyboardEvent) => {
      if (ev.key === "Escape") {
        ev.preventDefault(); ev.stopPropagation();
        setOpen(false);
        moreRef.current?.focus();
        return;
      }
      // The sheet says aria-modal, so Tab stays inside it — plus the More
      // button, which sits outside the dialog and is the way to close it.
      if (ev.key !== "Tab") return;
      const stops = [...(sheetRef.current?.querySelectorAll<HTMLElement>("a") || []), moreRef.current!].filter(Boolean);
      const at = stops.indexOf(document.activeElement as HTMLElement);
      ev.preventDefault();
      stops[(at + (ev.shiftKey ? -1 : 1) + stops.length) % stops.length]?.focus();
    };
    document.addEventListener("keydown", key, true);
    return () => document.removeEventListener("keydown", key, true);
  }, [open]);

  // Unmounting with the sheet up (a thread opened by keyboard, say) must not
  // leave the app believing an overlay is still open.
  useEffect(() => () => { moreSheet.value = false; }, []);

  return (
    <>
      {open && (
        <div class="sheet-veil" onClick={() => setOpen(false)}>
          <div class="sheet" id="more-places" role="dialog" aria-modal="true" aria-label="More places" ref={sheetRef}
            onClick={(ev) => ev.stopPropagation()}>
            <div class="sheet-grip" aria-hidden="true" />
            {MORE.map((group) => (
              <div class="sheet-group" key={group.title}>
                <div class="sheet-title">{group.title}</div>
                {group.items.map(([href, nav, label, sub]) => {
                  const n = counts.value[nav as keyof typeof counts.value] || 0;
                  return (
                    <a class={"sheet-item" + (here === nav ? " active" : "")} href={href} key={nav}
                      aria-current={here === nav ? "page" : undefined} onClick={() => setOpen(false)}>
                      <span class="sheet-item-main">
                        <span class="sheet-item-label">{label}</span>
                        <span class="sheet-item-sub">{sub}</span>
                      </span>
                      {n > 0 && <span class="sheet-item-n">{n}</span>}
                      <Icon name="chevron" size={14} />
                    </a>
                  );
                })}
              </div>
            ))}
          </div>
        </div>
      )}
      <nav class="tabbar" aria-label="Places">
        {tabs.map(([href, nav, label, icon]) => {
          const n = counts.value[nav as keyof typeof counts.value] || 0;
          const active = here === nav && !open;
          return (
            <a class={"tab" + (active ? " active" : "")} href={href} key={nav} aria-current={active ? "page" : undefined}
              // The tabs sit above the sheet's veil. Tapping the place you are
              // already in changes no route, so nothing else would close it.
              onClick={() => setOpen(false)}>
              <span class="tab-glyph">
                <Icon name={icon} size={22} />
                {n > 0 && <span class={"tab-n" + (nav === "screener" ? " loud" : "")}>{n > 99 ? "99+" : n}</span>}
              </span>
              <span class="tab-label">{label}</span>
            </a>
          );
        })}
        <button class={"tab" + (open || (!inTabs && here !== "") ? " active" : "")} type="button" ref={moreRef}
          aria-haspopup="dialog" aria-expanded={open} aria-controls={open ? "more-places" : undefined}
          onClick={() => setOpen((v) => !v)}>
          <span class="tab-glyph"><Icon name="dots" size={22} /></span>
          <span class="tab-label">More</span>
        </button>
      </nav>
    </>
  );
}
