import { useEffect } from "preact/hooks";
import { resetSelection, setList } from "../lib/store";
import { PageHead, SettingsTabs } from "../ui/bits";
import { navigate } from "../lib/router";

// One front door for every knob: the ⋯ menu's single Settings entry lands
// here, and each card below is one hop from the others via the shared tabs.

const PAGES: { href: string; title: string; sub: string }[] = [
  { href: "/settings/accounts", title: "Mailboxes", sub: "Connect, sync, backfill, retention, and export." },
  { href: "/settings/mail", title: "Mail", sub: "Whether unknown senders are screened before they reach you." },
  { href: "/settings/appearance", title: "Appearance", sub: "Thirteen themes, seven accents, two subject voices." },
  { href: "/settings/security", title: "Security", sub: "Passkeys, recovery, sessions, agent tokens." },
];

export function SettingsHomeView() {
  useEffect(() => {
    resetSelection();
    setList({ kind: "none", key: "settings", loading: false, error: null, rows: [], senders: [], origin: null });
  }, []);

  return (
    <>
      <PageHead kicker="Settings" title="Settings" sub="Everything adjustable, four doors." />
      <SettingsTabs here="/settings" />
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
