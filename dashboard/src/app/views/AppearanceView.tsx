import { useEffect } from "preact/hooks";
import { resetSelection, setList, setTheme, theme, accent, setAccent, typeFlavor, setTypeFlavor, type Accent, type Theme } from "../lib/store";
import { ListSkeleton, PageHead } from "../ui/bits";
import { navigate } from "../lib/router";

// Appearance is applied the moment it is picked — no save button, no "Apply".
// The whole product reads CSS variables, so a choice here re-skins everything
// live: base theme, accent, and whether subjects speak in the editorial serif
// or the interface sans.

// Each card previews the theme with ITS OWN tokens — including its default
// accent — so the swatch is honest even before it is applied.

interface ThemeCard { id: Theme; name: string; blurb: string; bg: string; panel: string; ink: string; line: string; accent: string }

const FOUNDATIONS: ThemeCard[] = [
  { id: "light", name: "Light", blurb: "Paper white, quiet grey ink", bg: "#fbfbfd", panel: "#ffffff", ink: "#15161a", line: "#e9eaef", accent: "#d8402a" },
  { id: "sepia", name: "Sepia", blurb: "A warm room, aged paper", bg: "#e7dfd2", panel: "#efe8db", ink: "#3d3833", line: "#d8cfbe", accent: "#c25a24" },
  { id: "dark", name: "Dark", blurb: "Night, no theatrics", bg: "#0f1013", panel: "#15161a", ink: "#eceef2", line: "#23252b", accent: "#ff6a44" },
];

const CHARACTERS: ThemeCard[] = [
  { id: "terminal", name: "Terminal", blurb: "Green phosphor on a switched-off screen", bg: "#060906", panel: "#0a0f0a", ink: "#b8f5b4", line: "#142014", accent: "#3dff88" },
  { id: "amber", name: "Amber", blurb: "The warmer CRT — honey instead of green", bg: "#0f0a04", panel: "#171108", ink: "#f2c078", line: "#241b0f", accent: "#ffab40" },
  { id: "nord", name: "Nord", blurb: "Polar night and frost", bg: "#2e3440", panel: "#343c4b", ink: "#eceff4", line: "#3b4252", accent: "#88c0d0" },
  { id: "dracula", name: "Dracula", blurb: "Night market — purple ground, lavender accent", bg: "#282a36", panel: "#2e313f", ink: "#f8f8f2", line: "#3a3d4e", accent: "#bd93f9" },
  { id: "rose", name: "Rosé", blurb: "Muted rose and gold on plum", bg: "#191724", panel: "#1f1d2e", ink: "#e0def4", line: "#2a2740", accent: "#ebbcba" },
  { id: "solarized", name: "Solarized", blurb: "The classic warm cream", bg: "#fdf6e3", panel: "#fffdf4", ink: "#2c3f45", line: "#e9e0c8", accent: "#b58900" },
  { id: "blueprint", name: "Blueprint", blurb: "Drafting blue with a pencil-yellow accent", bg: "#0d2440", panel: "#112c4d", ink: "#dbe9ff", line: "#1c3a63", accent: "#ffd23f" },
];

function ThemeCards({ cards }: { cards: ThemeCard[] }) {
  return (
    <div class="theme-cards">
      {cards.map((t) => (
        <button
          key={t.id}
          type="button"
          class={"theme-card" + (theme.value === t.id ? " active" : "")}
          onClick={() => setTheme(t.id)}
          title={t.blurb}
        >
          <div class="theme-card-surface" style={{ background: t.bg }}>
            <span class="theme-chip" style={{ background: t.panel, border: `1px solid ${t.line}` }} />
            <span class="theme-chip" style={{ background: t.ink, opacity: 0.85 }} />
            <span class="theme-chip" style={{ background: t.accent }} />
          </div>
          <span class="theme-card-name" style={{ color: "var(--ink)" }}>{t.name}</span>
        </button>
      ))}
    </div>
  );
}

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
        <a href="/settings">Settings</a>
        <a href="/settings/accounts">Mailboxes</a>
        <a href="/settings/appearance" class="active">Appearance</a>
        <a href="/settings/security">Security</a>
      </div>

      <section class="settings-section">
        <div class="settings-section-head">
          <div>
            <h2>Foundations</h2>
            <p>The ground everything sits on — start here, then tune below.</p>
          </div>
        </div>
        <ThemeCards cards={FOUNDATIONS} />
      </section>

      <section class="settings-section">
        <div class="settings-section-head">
          <div>
            <h2>Characters</h2>
            <p>Palettes with opinions. Pairs with any accent and either subject voice.</p>
          </div>
        </div>
        <ThemeCards cards={CHARACTERS} />
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
