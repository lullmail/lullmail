import { useEffect, useRef, useState } from "preact/hooks";
import { layout, shortcuts, theme, toggleLayout, toggleTheme } from "../lib/store";
import { navigate } from "../lib/router";
import { Icon } from "./Icon";

/** Two unlabelled glyphs sitting side by side is a guessing game. One button,
    and everything behind it says what it does in words. */
export function MoreMenu() {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (!open) return;
    ref.current?.querySelector<HTMLButtonElement>("[role='menuitem']")?.focus();
    const away = (ev: MouseEvent) => {
      if (!ref.current?.contains(ev.target as Node)) setOpen(false);
    };
    const t = setTimeout(() => document.addEventListener("click", away), 0);
    const key = (ev: KeyboardEvent) => {
      const items = [...(ref.current?.querySelectorAll<HTMLButtonElement>("[role='menuitem']") || [])];
      const at = items.indexOf(document.activeElement as HTMLButtonElement);
      if (ev.key === "Escape") { ev.preventDefault(); setOpen(false); triggerRef.current?.focus(); }
      else if (ev.key === "ArrowDown") { ev.preventDefault(); items[(at + 1 + items.length) % items.length]?.focus(); }
      else if (ev.key === "ArrowUp") { ev.preventDefault(); items[(at - 1 + items.length) % items.length]?.focus(); }
    };
    document.addEventListener("keydown", key);
    return () => { clearTimeout(t); document.removeEventListener("click", away); document.removeEventListener("keydown", key); };
  }, [open]);

  const pick = (fn: () => void) => () => { setOpen(false); fn(); };
  const themeLabel = theme.value.charAt(0).toUpperCase() + theme.value.slice(1);

  return (
    <div style={{ position: "relative" }} ref={ref}>
      <button
        ref={triggerRef}
        class="btn-icon" type="button" aria-label="Settings and shortcuts"
        aria-haspopup="menu" aria-expanded={open} title="Settings and shortcuts"
        onClick={() => setOpen((v) => !v)}
      >
        <Icon name="more" size={16} />
      </button>
      {open && (
        <div class="menu menu-right" role="menu">
          <button class="menu-item" type="button" role="menuitem" onClick={pick(() => navigate("/settings/appearance"))}>
            <Icon name={theme.value === "dark" ? "sun" : "moon"} size={14} />
            Appearance<span class="note">{themeLabel}</span>
          </button>
          <button class="menu-item" type="button" role="menuitem" onClick={pick(toggleLayout)}>
            <Icon name={layout.value === "classic" ? "classic" : "document"} size={14} />
            Layout<span class="note">{layout.value === "classic" ? "Three columns" : "One column"}</span>
          </button>
          <div class="menu-rule" />
          <button class="menu-item" type="button" role="menuitem"
            onClick={pick(() => { shortcuts.value = true; })}>
            <Icon name="keyboard" size={14} />
            Keyboard shortcuts<span class="note">?</span>
          </button>
          <button class="menu-item" type="button" role="menuitem" onClick={pick(() => navigate("/settings"))}>
            <Icon name="settings" size={14} />
            Settings
          </button>
        </div>
      )}
    </div>
  );
}
