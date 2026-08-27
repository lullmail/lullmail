import { useEffect } from "preact/hooks";
import { resetSelection, setList } from "../lib/store";
import { PageHead } from "../ui/bits";
import { navigate } from "../lib/router";

// One front door for every knob: the ⋯ menu's single Settings entry lands
// here, and each card below is one hop from the others via the shared tabs.

const PAGES: { href: string; title: string; sub: string }[] = [
  { href: "/settings/accounts", title: "Mailboxes", sub: "Connect, sync, backfill, retention, and export." },
  { href: "/settings/appearance", title: "Appearance", sub: "Ten themes, seven accents, two subject voices." },
  { href: "/settings/security", title: "Security", sub: "Passkeys, recovery, sessions, agent tokens." },
];

export function SettingsHomeView() {
  useEffect(() => {
    resetSelection();
    setList({ kind: "none", key: "settings", loading: false, error: null, rows: [], senders: [], origin: null });
  }, []);

  return (
    <>
      <PageHead kicker="Settings" title="Settings" sub="Everything adjustable, three doors." />
      <div class="settings-tabs">
        <a class="active" href="/settings">Settings</a>
        <a href="/settings/accounts">Mailboxes</a>
        <a href="/settings/appearance">Appearance</a>
        <a href="/settings/security">Security</a>
      </div>
      {PAGES.map((p) => (
        <section class="settings-section" key={p.href}>
          <div class="settings-section-head">
            <div>
              <h2>{p.title}</h2>
              <p>{p.sub}</p>
            </div>
            <button class="btn btn-outline btn-sm" type="button" onClick={() => navigate(p.href)}>Open</button>
          </div>
        </section>
      ))}
    </>
  );
}
