import { useEffect, useRef } from "preact/hooks";
import { snooze } from "../lib/actions";
import { snoozePickerRows } from "../lib/store";

// Deferring is one idea; the date is an attribute of it. "Someday" stores no
// return date, which is exactly what the old separate "Later" bucket was.
export function daysUntilWeekend(now = new Date()): number {
  const days = (6 - now.getDay() + 7) % 7;
  return days || 7;
}

function choices(now = new Date()): [string, number, string][] {
  const weekend = daysUntilWeekend(now);
  return [
    ["Tomorrow", 1, "1 day"],
    ["This weekend", weekend, weekend === 1 ? "tomorrow" : "in " + weekend + " days"],
    ["Next week", 7, "7 days"],
    ["In a month", 30, "30 days"],
    ["Someday", 0, "no date"],
  ];
}

export function SnoozeMenu(
  { onPick, onClose, placement = "down", standalone = false }:
  { onPick: (days: number) => void; onClose: () => void; placement?: "up" | "down"; standalone?: boolean }
) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    ref.current?.querySelector<HTMLButtonElement>("button")?.focus();
    const away = (ev: MouseEvent) => {
      if (!ref.current?.contains(ev.target as Node)) onClose();
    };
    // Deferred: the click that opened the menu is still propagating.
    const t = setTimeout(() => document.addEventListener("click", away), 0);
    const key = (ev: KeyboardEvent) => {
      const items = [...(ref.current?.querySelectorAll<HTMLButtonElement>("button") || [])];
      const at = items.indexOf(document.activeElement as HTMLButtonElement);
      if (ev.key === "Escape") { ev.preventDefault(); onClose(); }
      else if (ev.key === "ArrowDown") { ev.preventDefault(); items[(at + 1 + items.length) % items.length]?.focus(); }
      else if (ev.key === "ArrowUp") { ev.preventDefault(); items[(at - 1 + items.length) % items.length]?.focus(); }
    };
    document.addEventListener("keydown", key);
    return () => {
      clearTimeout(t);
      document.removeEventListener("click", away);
      document.removeEventListener("keydown", key);
      previous?.focus();
    };
  }, [onClose]);

  return (
    <div
      class="menu" ref={ref} role="menu"
      style={standalone ? { position: "relative" } : placement === "up" ? { bottom: "100%", left: 0, marginBottom: 6 } : { top: "100%", right: 0, marginTop: 6 }}
    >
      {choices().map(([label, days, note]) => (
        <button class="menu-item" type="button" key={label} role="menuitem" onClick={() => onPick(days)}>
          {label}<span class="note">{note}</span>
        </button>
      ))}
    </div>
  );
}

export function KeyboardSnoozePicker() {
  const rows = snoozePickerRows.value;
  if (!rows.length) return null;
  const close = () => { snoozePickerRows.value = []; };
  return (
    <div class="veil" onClick={(ev) => { if (ev.target === ev.currentTarget) close(); }}>
      <div class="panel panel-narrow" role="dialog" aria-modal="true" aria-label="Choose when to snooze">
        <h2>When should it return?</h2>
        <SnoozeMenu standalone onClose={close} onPick={(days) => { close(); snooze(rows, days); }} />
      </div>
    </div>
  );
}
