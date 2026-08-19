import { shortcuts } from "../lib/store";
import { SHORTCUTS } from "../lib/keys";

export function Shortcuts() {
  if (!shortcuts.value) return null;
  const close = () => { shortcuts.value = false; };
  return (
    <div class="veil" onClick={(ev) => { if (ev.target === ev.currentTarget) close(); }}>
      <div class="panel panel-narrow" role="dialog" aria-modal="true" aria-label="Keyboard shortcuts">
        <div class="shortcuts-body">
          <h2 class="shortcuts-title">Keyboard</h2>
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
