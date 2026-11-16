import { useEffect, useRef } from "preact/hooks";
import { moreSheet, screeningEnabled } from "../lib/store";
import { path, routeFor } from "../lib/router";
import { PLACES, type PlaceGroup, placeCount, placeOffered } from "../lib/places";
import { Icon } from "./Icon";

// The phone shell. A header row of text links cannot hold eight places at
// 390px — it scrolled sideways and hid half of them — so on a phone the
// places move to the thumb: four tabs that are always one tap away, and a
// sheet for the rest. CSS shows this only at phone widths; wider windows
// keep the topline nav and never see it.
// The tabs are a fixed four so the bar never reshuffles under the thumb;
// the sheet is everything else, and there the queues come and go like they
// do in the sidebar. Which places exist is lib/places.
const TABS = PLACES.filter((p) => p.tab);

const SHEET: { title: string; groups: PlaceGroup[] }[] = [
  { title: "Your day", groups: ["day"] },
  { title: "Mail", groups: ["mail", "sent", "people"] },
  { title: "Lull Mail", groups: ["app"] },
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
  const tabs = TABS.filter((t) => t.nav !== "screener" || screeningEnabled.value !== false || here === "screener");
  const inTabs = tabs.some((t) => t.nav === here);

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
            {SHEET.map((group) => {
              const items = PLACES.filter((p) => group.groups.includes(p.group) && !p.tab && placeOffered(p, here));
              if (!items.length) return null;
              return (
                <div class="sheet-group" key={group.title}>
                  <div class="sheet-title">{group.title}</div>
                  {items.map((p) => (
                    <a class={"sheet-item" + (here === p.nav ? " active" : "")} href={p.href} key={p.nav}
                      aria-current={here === p.nav ? "page" : undefined} onClick={() => setOpen(false)}>
                      <span class="sheet-item-main">
                        <span class="sheet-item-label">{p.label}</span>
                        <span class="sheet-item-sub">{p.sub}</span>
                      </span>
                      <Icon name="chevron" size={14} />
                    </a>
                  ))}
                </div>
              );
            })}
          </div>
        </div>
      )}
      <nav class="tabbar" aria-label="Places">
        {tabs.map((p) => {
          const { href, nav, label, icon } = p;
          const n = placeCount(p);
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
