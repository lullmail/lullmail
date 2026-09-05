import { useEffect } from "preact/hooks";
import {
  accent, accentCustom, density, measure, resetSelection, setAccent, setAccentCustom, setDensity, setList, setMeasure,
  layout, setTextSize, setTheme, theme, textSize, toggleLayout, typeFlavor, setTypeFlavor, type Accent, type Theme,
} from "../lib/store";
import { PageHead, SettingsTabs } from "../ui/bits";

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
  { id: "y2k", name: "Y2K", blurb: "Luna blue, Aqua gloss — the optimistic internet", bg: "linear-gradient(180deg,#9cc3ee,#eaf3fc)", panel: "#ffffff", ink: "#16344f", line: "#c9dcef", accent: "#2f7dc9" },
  { id: "1999", name: "1999", blurb: "The beige box — silver chrome, white wells, navy selection, Tahoma", bg: "#d4d0c8", panel: "#c0c0c0", ink: "#101010", line: "#8f8b84", accent: "#000080" },
  { id: "vapor", name: "Vapor", blurb: "Pink-and-cyan sunset over a purple grid", bg: "linear-gradient(180deg,#140a24,#7c2a86)", panel: "#251342", ink: "#f4e9ff", line: "#3a1f63", accent: "#38f0ff" },
];

function ThemeCards({ cards }: { cards: ThemeCard[] }) {
  return (
    <div class="theme-cards" role="group" aria-label="Theme">
      {cards.map((t) => (
        <button
          key={t.id}
          type="button"
          class={"theme-card" + (theme.value === t.id ? " active" : "")}
          onClick={() => setTheme(t.id)}
          title={t.blurb}
          aria-pressed={theme.value === t.id}
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

const ACCENT_SWATCH: Record<Exclude<Accent, "custom">, string> = {
  ember: "#d8402a",
  ocean: "#2f6fed",
  forest: "#2e7d4f",
  violet: "#7a4fd8",
  rose: "#d84a7a",
  teal: "#12909a",
  graphite: "#4a5568",
};

const TEXT_SIZES: { id: import("../lib/store").TextSize; label: string; px: number }[] = [
  { id: "s", label: "S", px: 12 },
  { id: "m", label: "M", px: 14 },
  { id: "l", label: "L", px: 17 },
  { id: "xl", label: "XL", px: 20 },
];

const DENSITIES: { id: import("../lib/store").Density; label: string; sub: string }[] = [
  { id: "compact", label: "Compact", sub: "No previews, tight rows" },
  { id: "comfortable", label: "Comfortable", sub: "Previews, room to read" },
  { id: "roomy", label: "Roomy", sub: "Everything gets air" },
];

const MEASURES: { id: import("../lib/store").Measure; label: string; sub: string; bar: string }[] = [
  { id: "narrow", label: "Narrow", sub: "A book column", bar: "34%" },
  { id: "standard", label: "Standard", sub: "The default measure", bar: "56%" },
  { id: "wide", label: "Wide", sub: "Use the screen", bar: "82%" },
];

export function AppearanceView() {
  useEffect(() => {
    resetSelection();
    setList({ kind: "none", key: "appearance", loading: false, error: null, rows: [], senders: [], origin: null });
  }, []);

  return (
    <>
      <PageHead kicker="Settings" title="Appearance" sub="Applied the moment you pick. Your mail, your colors." />
      <SettingsTabs here="/settings/appearance" />

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
        <div class="accent-dots" role="group" aria-label="Accent color">
          {(Object.keys(ACCENT_SWATCH) as Exclude<Accent, "custom">[]).map((id) => (
            <button
              key={id}
              type="button"
              class={"accent-dot" + (accent.value === id ? " active" : "")}
              style={{ background: ACCENT_SWATCH[id] }}
              aria-label={id}
              aria-pressed={accent.value === id}
              title={id.charAt(0).toUpperCase() + id.slice(1)}
              onClick={() => setAccent(id)}
            />
          ))}
          {/* The wheel: any color at all. Ink contrast is chosen by luminance. */}
          <label
            class={"accent-dot custom" + (accent.value === "custom" ? " active" : "")}
            title="Custom color"
            aria-label="Custom color"
          >
            <input
              type="color"
              value={accentCustom.value || "#d8402a"}
              onInput={(e) => setAccentCustom((e.target as HTMLInputElement).value)}
            />
          </label>
        </div>
      </section>

      <section class="settings-section">
        <div class="settings-section-head">
          <div>
            <h2>Text size</h2>
            <p>Everything scales — subjects, previews, buttons, this page.</p>
          </div>
        </div>
        <div class="type-row" role="group" aria-label="Text size">
          {TEXT_SIZES.map((s) => (
            <button
              key={s.id}
              type="button"
              class={"type-card" + (textSize.value === s.id ? " active" : "")}
              onClick={() => setTextSize(s.id)}
              aria-pressed={textSize.value === s.id} aria-label={s.label + " text size"}
            >
              <span class="type-card-sample" style={{ fontSize: s.px + "px", fontWeight: 660 }}>Aa</span>
              <span class="type-card-note">{s.label}</span>
            </button>
          ))}
        </div>
      </section>

      <section class="settings-section">
        <div class="settings-section-head">
          <div>
            <h2>Density</h2>
            <p>How much mail fits on a screen.</p>
          </div>
        </div>
        <div class="type-row" role="group" aria-label="Density">
          {DENSITIES.map((d) => (
            <button
              key={d.id}
              type="button"
              class={"type-card" + (density.value === d.id ? " active" : "")}
              onClick={() => setDensity(d.id)}
              aria-pressed={density.value === d.id}
            >
              <span class="type-card-sample">{d.label}</span>
              <span class="type-card-note">{d.sub}</span>
            </button>
          ))}
        </div>
      </section>

      <section class="settings-section">
        <div class="settings-section-head">
          <div>
            <h2>Reading measure</h2>
            <p>How wide a document the mail gets to be.</p>
          </div>
        </div>
        <div class="type-row" role="group" aria-label="Reading measure">
          {MEASURES.map((m) => (
            <button
              key={m.id}
              type="button"
              class={"type-card" + (measure.value === m.id ? " active" : "")}
              onClick={() => setMeasure(m.id)}
              aria-pressed={measure.value === m.id}
            >
              <span class="measure-bar" style={{ width: m.bar }} />
              <span class="type-card-sample" style={{ fontSize: "16px" }}>{m.label}</span>
              <span class="type-card-note">{m.sub}</span>
            </button>
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
        <div class="type-row" role="group" aria-label="Subject typeface">
          <button
            type="button"
            class={"type-card" + (typeFlavor.value === "editorial" ? " active" : "")}
            onClick={() => setTypeFlavor("editorial")}
            aria-pressed={typeFlavor.value === "editorial"}
          >
            <span class="type-card-sample" style={{ fontFamily: "var(--serif)" }}>Editorial</span>
            <span class="type-card-note">Serif subjects, like a front page</span>
          </button>
          <button
            type="button"
            class={"type-card" + (typeFlavor.value === "clean" ? " active" : "")}
            onClick={() => setTypeFlavor("clean")}
            aria-pressed={typeFlavor.value === "clean"}
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
            <p>Use one reading column, or a mailbox rail with separate list and reader panes.</p>
          </div>
        </div>
        <div class="inline-form" role="group" aria-label="Layout">
          <button class="btn btn-outline btn-sm" type="button" aria-pressed={layout.value === "document"} onClick={() => { if (layout.value !== "document") toggleLayout(); }}>
            Document
          </button>
          <button class="btn btn-outline btn-sm" type="button" aria-pressed={layout.value === "classic"} onClick={() => { if (layout.value !== "classic") toggleLayout(); }}>
            Mailbox workstation
          </button>
        </div>
      </section>
    </>
  );
}
