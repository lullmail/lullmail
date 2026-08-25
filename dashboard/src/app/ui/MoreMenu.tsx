import { useEffect, useRef, useState } from "preact/hooks";
import { layout, shortcuts, theme, toggleLayout, toggleTheme } from "../lib/store";
import { navigate } from "../lib/router";
import { Icon } from "./Icon";

/** Two unlabelled glyphs sitting side by side is a guessing game. One button,
    and everything behind it says what it does in words. */
export function MoreMenu() {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const away = (ev: MouseEvent) => {
      if (!ref.current?.contains(ev.target as Node)) setOpen(false);
    };
    const t = setTimeout(() => document.addEventListener("click", away), 0);
    return () => { clearTimeout(t); document.removeEventListener("click", away); };
  }, [open]);

  const pick = (fn: () => void) => () => { setOpen(false); fn(); };
  const themeLabel = theme.value === "dark" ? "Dark" : theme.value === "sepia" ? "Sepia" : "Light";

  return (
    <div style={{ position: "relative" }} ref={ref}>
      <button
        class="btn-icon" type="button" aria-label="Settings and shortcuts"
        aria-expanded={open} title="Settings and shortcuts"
        onClick={() => setOpen((v) => !v)}
      >
        <Icon name="more" size={16} />
      </button>
      {open && (
        <div class="menu menu-right" role="menu">
          <button class="menu-item" type="button" role="menuitem" onClick={pick(toggleTheme)}>
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
          <button class="menu-item" type="button" role="menuitem"
            onClick={pick(() => navigate("/settings/accounts"))}>
            <Icon name="settings" size={14} />
            Accounts
          </button>
          <button class="menu-item" type="button" role="menuitem"
            onClick={pick(() => navigate("/settings/security"))}>
            <Icon name="settings" size={14} />
            Security
          </button>
        </div>
      )}
    </div>
  );
}
