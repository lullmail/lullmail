import { useEffect, useState } from "preact/hooks";
import { api } from "../lib/api";
import { resetSelection, setList, showError, showToast } from "../lib/store";
import { refreshCounts, refreshPrefs, reload } from "../lib/actions";
import { PageHead, SettingsTabs } from "../ui/bits";

// How mail is sorted, as opposed to how it looks. The Screener is the only
// switch here: it decides whether an unknown sender's first mail waits for
// approval or simply arrives.

interface Prefs { screening_enabled: boolean }

export function MailView() {
  const [screening, setScreening] = useState<boolean | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    resetSelection();
    setList({ kind: "none", key: "settings-mail", loading: false, error: null, rows: [], senders: [], origin: null });
    api<Prefs>("/prefs", { fresh: true })
      .then((p) => setScreening(p.screening_enabled))
      .catch((e) => showError(e instanceof Error ? e.message : "Could not load settings"));
  }, []);

  async function set(enabled: boolean) {
    setBusy(true);
    try {
      const res = await api<Prefs & { released: number }>("/prefs", { body: { screening_enabled: enabled } });
      setScreening(res.screening_enabled);
      showToast(
        enabled
          ? "Screening on — unknown senders wait for approval"
          : res.released > 0
            ? `Screening off — ${res.released} held ${res.released === 1 ? "message" : "messages"} moved to your Inbox`
            : "Screening off — new mail goes straight to your Inbox",
      );
      refreshPrefs(); // the Screener nav entry appears or leaves with the switch
      refreshCounts();
      reload();
    } catch (e) {
      showError(e instanceof Error ? e.message : "Could not save");
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <PageHead kicker="Settings" title="Mail" sub="How mail is sorted on its way in." />
      <SettingsTabs here="/settings/mail" />
      <section class="settings-section">
        <div class="settings-section-head">
          <div>
            <h2>Screener</h2>
            <p>
              On, mail from a sender you have never heard from waits in the Screener until you
              approve them — it does not reach your Inbox first. Off, it arrives like any other
              mail and anything already waiting is released.
            </p>
            <p>
              Senders you have already allowed or blocked keep their decision either way, so
              turning this off does not un-block anyone.
            </p>
          </div>
          <label class="retention-control">
            <span>Screen new senders</span>
            <select
              disabled={busy || screening === null}
              value={screening === false ? "off" : "on"}
              onChange={(e) => set((e.target as HTMLSelectElement).value === "on")}
            >
              <option value="on">On</option>
              <option value="off">Off</option>
            </select>
          </label>
        </div>
      </section>
    </>
  );
}
