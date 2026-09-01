import { useEffect, useRef } from "preact/hooks";
import { shortcuts } from "../lib/store";
import { SHORTCUTS } from "../lib/keys";

export function Shortcuts() {
  const closeRef = useRef<HTMLButtonElement>(null);
  const close = () => { shortcuts.value = false; };
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    closeRef.current?.focus();
    return () => previous?.focus();
  }, []);
  if (!shortcuts.value) return null;
  return (
    <div class="veil" onClick={(ev) => { if (ev.target === ev.currentTarget) close(); }}>
      <div class="panel panel-narrow" role="dialog" aria-modal="true" aria-label="Keyboard shortcuts">
        <div class="shortcuts-body">
          <h2 class="shortcuts-title">Keyboard</h2>
          <button ref={closeRef} class="btn btn-ghost btn-sm" type="button" onClick={close} aria-label="Close keyboard shortcuts">Close</button>
          <div class="shortcut-grid">
            {SHORTCUTS.map(([keys, desc]) => (
              <>
                <span class="kbd" key={keys}>{keys}</span>
                <span class="shortcut-desc">{desc}</span>
              </>
            ))}
          </div>
        </div>
      </div>
    </div>
  );
}
