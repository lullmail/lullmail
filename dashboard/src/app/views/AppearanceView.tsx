import { useEffect } from "preact/hooks";
import { resetSelection, setList, setTheme, theme, accent, setAccent, typeFlavor, setTypeFlavor, type Accent, type Theme } from "../lib/store";
import { ListSkeleton, PageHead } from "../ui/bits";
import { navigate } from "../lib/router";

// Appearance is applied the moment it is picked — no save button, no "Apply".
// The whole product reads CSS variables, so a choice here re-skins everything
// live: base theme, accent, and whether subjects speak in the editorial serif
// or the interface sans.

const THEMES: { id: Theme; name: string; bg: string; panel: string; ink: string; line: string }[] = [
  { id: "light", name: "Light", bg: "#f6f5f2", panel: "#ffffff", ink: "#1c1d21", line: "#e4e2dc" },
  { id: "sepia", name: "Sepia", bg: "#e7dfd2", panel: "#f2ece1", ink: "#4a4238", line: "#d8cdba" },
  { id: "dark", name: "Dark", bg: "#15161a", panel: "#1e2026", ink: "#ecedf1", line: "#2d3038" },
];

const ACCENT_SWATCH: Record<Accent, string> = {
  ember: "#d8402a",
  ocean: "#2f6fed",
  forest: "#2e7d4f",
  violet: "#7a4fd8",
  rose: "#d84a7a",
  teal: "#12909a",
  graphite: "#4a5568",
};

export function AppearanceView() {
  useEffect(() => {
    resetSelection();
    setList({ kind: "none", key: "appearance", loading: false, error: null, rows: [], senders: [], origin: null });
  }, []);

  return (
    <>
      <PageHead kicker="Settings" title="Appearance" sub="Applied the moment you pick. Your mail, your colors." />
      <div class="settings-tabs">
        <a href="/settings/accounts">Mailboxes</a>
        <a href="/settings/appearance" class="active">Appearance</a>
        <a href="/settings/security">Security</a>
      </div>

      <section class="settings-section">
        <div class="settings-section-head">
          <div>
            <h2>Theme</h2>
            <p>The ground everything sits on — paper, warm paper, or night.</p>
          </div>
        </div>
        <div class="theme-cards">
          {THEMES.map((t) => (
            <button
              key={t.id}
              type="button"
              class={"theme-card" + (theme.value === t.id ? " active" : "")}
              onClick={() => setTheme(t.id)}
            >
              <div class="theme-card-surface" style={{ background: t.bg }}>
                <span class="theme-chip" style={{ background: t.panel, border: `1px solid ${t.line}` }} />
                <span class="theme-chip" style={{ background: t.ink, opacity: 0.85 }} />
                <span class="theme-chip" style={{ background: ACCENT_SWATCH[accent.value] }} />
              </div>
              <span class="theme-card-name" style={{ color: "var(--ink)" }}>{t.name}</span>
            </button>
          ))}
        </div>
      </section>

      <section class="settings-section">
        <div class="settings-section-head">
          <div>
            <h2>Accent</h2>
            <p>What "needs you" looks like — buttons, badges, and the waiting dot.</p>
          </div>
        </div>
        <div class="accent-dots">
          {(Object.keys(ACCENT_SWATCH) as Accent[]).map((id) => (
            <button
              key={id}
              type="button"
              class={"accent-dot" + (accent.value === id ? " active" : "")}
              style={{ background: ACCENT_SWATCH[id] }}
              aria-label={id}
              title={id.charAt(0).toUpperCase() + id.slice(1)}
              onClick={() => setAccent(id)}
            />
          ))}
        </div>
      </section>

      <section class="settings-section">
        <div class="settings-section-head">
          <div>
            <h2>Subjects</h2>
            <p>The voice of subject lines and titles across the app.</p>
          </div>
        </div>
        <div class="type-row">
          <button
            type="button"
            class={"type-card" + (typeFlavor.value === "editorial" ? " active" : "")}
            onClick={() => setTypeFlavor("editorial")}
          >
            <span class="type-card-sample" style={{ fontFamily: "var(--serif)" }}>Editorial</span>
            <span class="type-card-note">Serif subjects, like a front page</span>
          </button>
          <button
            type="button"
            class={"type-card" + (typeFlavor.value === "clean" ? " active" : "")}
            onClick={() => setTypeFlavor("clean")}
          >
            <span class="type-card-sample" style={{ fontFamily: "var(--sans)", fontWeight: 700 }}>Clean</span>
            <span class="type-card-note">Sans subjects, like an instrument</span>
          </button>
        </div>
      </section>

      <section class="settings-section">
        <div class="settings-section-head">
          <div>
            <h2>Layout</h2>
            <p>One column where mail is a document, or three panes like a workstation.</p>
          </div>
        </div>
        <div class="inline-form">
          <button class="btn btn-outline btn-sm" type="button" onClick={() => navigate("/settings/accounts")}>
            Layout and mailboxes live in the overflow (⋯) menu
          </button>
        </div>
      </section>
    </>
  );
}
