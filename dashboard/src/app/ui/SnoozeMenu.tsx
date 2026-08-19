import { useEffect, useRef } from "preact/hooks";

// Deferring is one idea; the date is an attribute of it. "Someday" stores no
// return date, which is exactly what the old separate "Later" bucket was.
const CHOICES: [string, number, string][] = [
  ["Tomorrow", 1, "1 day"],
  ["This weekend", 3, "3 days"],
  ["Next week", 7, "7 days"],
  ["In a month", 30, "30 days"],
  ["Someday", 0, "no date"],
];

export function SnoozeMenu(
  { onPick, onClose, placement = "down" }:
  { onPick: (days: number) => void; onClose: () => void; placement?: "up" | "down" }
) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const away = (ev: MouseEvent) => {
      if (!ref.current?.contains(ev.target as Node)) onClose();
    };
    // Deferred: the click that opened the menu is still propagating.
    const t = setTimeout(() => document.addEventListener("click", away), 0);
    return () => { clearTimeout(t); document.removeEventListener("click", away); };
  }, [onClose]);

  return (
    <div
      class="menu" ref={ref} role="menu"
      style={placement === "up" ? { bottom: "100%", left: 0, marginBottom: 6 } : { top: "100%", right: 0, marginTop: 6 }}
    >
      {CHOICES.map(([label, days, note]) => (
        <button class="menu-item" type="button" key={label} role="menuitem" onClick={() => onPick(days)}>
          {label}<span class="note">{note}</span>
        </button>
      ))}
    </div>
  );
}
