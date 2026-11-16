import { useEffect, useRef, useState } from "preact/hooks";
import { BUCKET_LABEL, routeSender, senderRoute } from "../lib/actions";
import type { Bucket, Row } from "../lib/types";
import { Icon } from "./Icon";

// Filing a thread and deciding about its sender are two different acts, and
// the second used to live only on the People page: read a newsletter in the
// Inbox, leave the message, find the sender in a list, change them there.
// This is that decision where the thought occurs — on the message.
const ROUTES: Bucket[] = ["imbox", "feed", "paper_trail"];

export function SenderMenu({ sender, name, rows }: { sender: string; name: string; rows: Row[] }) {
  const [open, setOpen] = useState(false);
  // undefined: not known (yet, or the lookup failed). null: no rule lets them in.
  const [current, setCurrent] = useState<Bucket | null | undefined>(undefined);
  const ref = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (!open) return;
    let alive = true;
    setCurrent(undefined);
    senderRoute(sender).then((route) => { if (alive) setCurrent(route); }).catch(() => { /* the menu still works unticked */ });
    ref.current?.querySelector<HTMLButtonElement>("[role='menuitemradio']")?.focus();
    const away = (ev: MouseEvent) => {
      if (!ref.current?.contains(ev.target as Node)) setOpen(false);
    };
    // Deferred: the click that opened the menu is still propagating.
    const t = setTimeout(() => document.addEventListener("click", away), 0);
    return () => { alive = false; clearTimeout(t); document.removeEventListener("click", away); };
  }, [open, sender]);

  const close = (refocus = true) => { setOpen(false); if (refocus) triggerRef.current?.focus(); };

  return (
    <div class="sender-menu" style={{ position: "relative" }} ref={ref}
      onKeyDown={(ev) => {
        if (!open || !["Escape", "ArrowDown", "ArrowUp"].includes(ev.key)) return;
        ev.preventDefault();
        ev.stopPropagation();
        if (ev.key === "Escape") { close(); return; }
        const items = [...(ref.current?.querySelectorAll<HTMLButtonElement>("[role='menuitemradio']") || [])];
        const at = items.indexOf(document.activeElement as HTMLButtonElement);
        items[(at + (ev.key === "ArrowDown" ? 1 : -1) + items.length) % items.length]?.focus();
      }}>
      <button class="btn btn-ghost btn-sm" type="button" ref={triggerRef}
        aria-haspopup="menu" aria-expanded={open}
        aria-label={"Always send mail from " + (name || sender) + " to…"}
        title={"Choose where mail from " + sender + " always goes"}
        onClick={() => setOpen((v) => !v)}>
        Always…
      </button>
      {open && (
        <div class="menu menu-up-right" role="menu" aria-label={"Where mail from " + (name || sender) + " goes"}>
          <div class="menu-head">
            Mail from <b>{name || sender}</b> always goes to
            {name && <span class="menu-head-sub">{sender}</span>}
          </div>
          {ROUTES.map((b) => (
            <button class={"menu-item" + (current === b ? " lens-on" : "")} type="button" key={b}
              role="menuitemradio" aria-checked={current === b}
              onClick={() => { close(false); if (current !== b) routeSender(sender, name, b, rows, current); }}>
              {BUCKET_LABEL[b]}
              {current === b && <span class="note"><Icon name="check" size={13} /></span>}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
