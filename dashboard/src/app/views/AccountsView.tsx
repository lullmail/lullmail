import { useEffect, useState } from "preact/hooks";
import { api, download } from "../lib/api";
import { useLoad } from "../lib/useLoad";
import { setList, showError, showToast, syncNote } from "../lib/store";
import { refreshAccounts, refreshCounts } from "../lib/actions";
import type { Account } from "../lib/types";
import { countOf, fmtDate } from "../lib/fmt";
import { Empty, ListSkeleton, PageHead } from "../ui/bits";
import { installApp, installKind } from "../lib/pwa";

function AccountCard({ account, onChange }: { account: Account; onChange: () => void }) {
  const [busy, setBusy] = useState<"sync" | "export" | "delete" | null>(null);
  const [exportProgress, setExportProgress] = useState("");
  const [confirming, setConfirming] = useState(false);

  const sync = async () => {
    setBusy("sync");
    syncNote.value = "Syncing " + account.address + "…";
    try {
      await api("/accounts/" + encodeURIComponent(account.id) + "?op=sync", { method: "POST" });
      showToast("Sync running");
      setTimeout(() => { syncNote.value = ""; onChange(); refreshCounts(); }, 4000);
    } catch (e) {
      syncNote.value = "";
      showError(e instanceof Error ? e.message : "Sync failed");
    } finally {
      setBusy(null);
    }
  };

  const exportMail = async () => {
    setBusy("export");
    setExportProgress("Preparing export…");
    let lastMB = -1;
    try {
      await download(
        "/accounts/" + encodeURIComponent(account.id) + "/export",
        account.address.replace(/[^a-z0-9._-]+/gi, "-") + "-mail-export.zip",
        {
          streamToDisk: true,
          onProgress: (received, total) => {
            const mb = Math.floor(received / 1048576);
            if (mb === lastMB) return;
            lastMB = mb;
            const receivedLabel = Math.max(1, mb) + " MB";
            const totalLabel = total > 0 ? " of " + Math.max(1, Math.ceil(total / 1048576)) + " MB" : "";
            setExportProgress("Saving " + receivedLabel + totalLabel + "…");
          },
        }
      );
      showToast("Mail export downloaded");
    } catch (e) {
      if (!(e instanceof DOMException && e.name === "AbortError")) {
        showError(e instanceof Error ? e.message : "Export failed");
      }
    } finally {
      setBusy(null);
      setExportProgress("");
    }
  };

  const disconnect = async () => {
    setBusy("delete");
    try {
      await api("/accounts/" + encodeURIComponent(account.id), { method: "DELETE" });
      showToast(account.address + " disconnected");
      onChange();
      refreshCounts();
    } catch (e) {
      showError(e instanceof Error ? e.message : "Could not disconnect");
    } finally {
      setBusy(null);
      setConfirming(false);
    }
  };

  const setRetention = async (days: number) => {
    setBusy("sync");
    try {
      await api("/accounts/" + encodeURIComponent(account.id) + "?op=retention", { body: { days } });
      showToast(days ? "Local mail older than " + days + " days removed" : "Local mail kept until you delete it");
      onChange(); refreshCounts();
    } catch (e) { showError(e instanceof Error ? e.message : "Could not change retention"); }
    finally { setBusy(null); }
  };

  const setSyncEnabled = async (enabled: boolean) => {
    setBusy("sync");
    try {
      await api("/accounts/" + encodeURIComponent(account.id) + "?op=sync_enabled", { body: { enabled } });
      showToast(enabled ? "Background sync on" : "Background sync paused — Sync now still works");
      onChange();
    } catch (e) { showError(e instanceof Error ? e.message : "Could not change sync setting"); }
    finally { setBusy(null); }
  };

  return (
    <div class="account">
      <div class="account-line">
        <strong>{account.address}</strong>
        {account.label && <span class="account-sub">{account.label}</span>}
        <span class="chip">{account.provider.toUpperCase()}</span>
      </div>

      <div class="account-meta">
        <span class={"dot " + (account.last_error ? "dot-bad" : "dot-ok")} />
        {account.last_error ? (
          <span>{account.last_error}</span>
        ) : (
          <span>
            {account.last_sync_at ? "Synced " + fmtDate(account.last_sync_at) : "Connecting…"}
            {" · " + countOf(account.message_count, "message") + " mirrored"}
            {account.screener_count > 0 && " · " + account.screener_count + " to screen"}
            {" · " + account.backfill_days + "-day backfill"}
            {" · " + (account.retention_days ? account.retention_days + "-day local retention" : "kept locally")}
          </span>
        )}
      </div>

      <div class="account-btns">
        <button class="btn btn-outline btn-sm" type="button" disabled={!!busy} onClick={sync}>
          {busy === "sync" ? "Starting sync…" : "Sync now"}
        </button>
        <button class="btn btn-outline btn-sm" type="button" disabled={!!busy} onClick={exportMail}>
          {busy === "export" ? exportProgress : "Export mail"}
        </button>
        <label class="retention-control">
          <span>Local retention</span>
          <select disabled={!!busy} value={account.retention_days} onChange={(e) => setRetention(Number((e.target as HTMLSelectElement).value))}>
            <option value={0}>Forever</option><option value={30}>30 days</option><option value={90}>90 days</option><option value={365}>1 year</option><option value={1095}>3 years</option>
          </select>
        </label>
        <label class="retention-control">
          <span>Background sync</span>
          <select disabled={!!busy} value={account.sync_enabled ? "on" : "off"} onChange={(e) => setSyncEnabled((e.target as HTMLSelectElement).value === "on")}>
            <option value="on">On</option><option value="off">Paused</option>
          </select>
        </label>
        {confirming ? (
          <>
            {/* Inline confirm: a native confirm() blocks the page and is unstyleable. */}
            <button class="btn btn-danger btn-sm" type="button" disabled={!!busy} onClick={disconnect}>
              Delete credentials and local mail
            </button>
            <button class="btn btn-ghost btn-sm" type="button" disabled={!!busy} onClick={() => setConfirming(false)}>Cancel</button>
          </>
        ) : (
          <button class="btn btn-ghost btn-sm" type="button" disabled={!!busy} onClick={() => setConfirming(true)}>Disconnect</button>
        )}
      </div>
      {confirming && (
        <p class="account-warning">
          This permanently removes the encrypted credential, mirrored messages, bodies, sync state,
          and filing state for this mailbox. It does not delete anything at your mail provider.
        </p>
      )}
    </div>
  );
}

const FIELDS: [string, string, string, string?][] = [
  ["address", "Address", "email", "you@example.com"],
  ["username", "Username", "text", "defaults to the address"],
  ["password", "App password", "password"],
  ["host", "IMAP host", "text", "imap.example.com"],
  ["port", "IMAP port", "number", "993"],
  ["smtp_host", "SMTP host", "text", "defaults to the IMAP host"],
  ["smtp_port", "SMTP port", "number", "587"],
  ["backfill_days", "Backfill days", "number", "90"],
];

function ConnectForm({ onDone }: { onDone: () => void }) {
  const [busy, setBusy] = useState(false);

  const submit = async (ev: Event) => {
    ev.preventDefault();
    const form = ev.target as HTMLFormElement;
    const data = new FormData(form);
    const body: Record<string, unknown> = {};
    data.forEach((v, k) => {
      const s = String(v).trim();
      if (!s) return;
      body[k] = ["port", "smtp_port", "backfill_days"].includes(k) ? parseInt(s, 10) : s;
    });
    setBusy(true);
    try {
      const res = await api<{ mailboxes: number }>("/accounts", { body });
      showToast("Connected — " + countOf(res.mailboxes, "mailbox", "mailboxes") + " found, first sync running");
      form.reset();
      onDone();
    } catch (e) {
      showError(e instanceof Error ? e.message : "Could not connect that mailbox");
    } finally {
      setBusy(false);
    }
  };

  return (
    <form class="form" onSubmit={submit}>
      <h3>Connect a mailbox</h3>
      <div class="form-grid">
        <label class="field">
          Provider
          <select name="provider">
            <option value="imap">IMAP</option>
            <option value="jmap">JMAP</option>
          </select>
        </label>
        {FIELDS.map(([name, label, type, placeholder]) => (
          <label class="field" key={name}>
            {label}
            <input
              name={name} type={type} placeholder={placeholder}
              required={name === "address" || name === "password" || name === "host"}
              defaultValue={name === "backfill_days" ? "90" : undefined}
            />
          </label>
        ))}
      </div>
      <div class="form-btns">
        <button class="btn btn-primary" type="submit" disabled={busy}>
          {busy ? "Checking the connection…" : "Connect"}
        </button>
      </div>
      <p class="form-note">
        The password is sealed with AES-256-GCM before it is stored, and the server is dialled
        once to verify the credentials before anything is written.
      </p>
    </form>
  );
}

export function AccountsView() {
  const [oauth, setOauth] = useState<{ google: boolean; microsoft: boolean } | null>(null);
  const [oauthBusy, setOauthBusy] = useState("");
  const { data, loading, error, reload } = useLoad<Account[]>("accounts", (signal) =>
    api<Account[]>("/accounts", { signal })
  );

  // Connects and disconnects change what the picker, the welcome gate and the
  // lens see — the app-wide stores must follow this view's reloads.
  useEffect(() => { refreshAccounts(); }, [data]);

  useEffect(() => {
    setList({ kind: "none", key: "", loading, error, rows: [], senders: [], origin: null });
  }, [loading, error]);

  useEffect(() => {
    api<{ google: boolean; microsoft: boolean }>("/oauth/status").then(setOauth).catch(() => {});
    const params = new URLSearchParams(window.location.search);
    if (params.get("connected")) { showToast("Mailbox connected"); window.history.replaceState({}, "", "/settings/accounts"); reload(); }
    if (params.get("oauth_error")) { showError("Provider connection was cancelled: " + params.get("oauth_error")); window.history.replaceState({}, "", "/settings/accounts"); }
  }, []);

  const startOAuth = async (provider: "google" | "microsoft") => {
    setOauthBusy(provider);
    try { const result = await api<{ url: string }>("/oauth/" + provider + "/start", { method: "POST" }); window.location.assign(result.url); }
    catch (e) { showError(e instanceof Error ? e.message : "Could not start provider connection"); setOauthBusy(""); }
  };

  return (
    <>
      <PageHead
        kicker="Settings"
        title="Accounts"
        sub="Mailboxes this app mirrors. Credentials are encrypted at rest."
      />
      <div class="settings-tabs"><a href="/settings">Settings</a><a class="active" href="/settings/accounts">Mailboxes</a><a href="/settings/appearance">Appearance</a><a href="/settings/security">Security</a></div>
      {loading && !data && <ListSkeleton rows={2} />}
      {error && <Empty title="That didn't load." sub={error} />}
      {data?.map((a) => <AccountCard account={a} onChange={reload} key={a.id} />)}
      {data && data.length === 0 && (
        <Empty title="No mailboxes yet." sub="Connect one below and the Screener starts filling up." />
      )}
      {data && oauth && (
        <section class="provider-connect">
          <div class="provider-connect-head"><div><h2>Connect in one step</h2><p>OAuth tokens are encrypted at rest and refreshed only by this server.</p></div></div>
          <div class="provider-buttons">
            <button class="btn btn-primary" type="button" disabled={!oauth.google || !!oauthBusy} onClick={() => startOAuth("google")}>{oauthBusy === "google" ? "Opening Google…" : "Continue with Google"}</button>
            <button class="btn btn-outline" type="button" disabled={!oauth.microsoft || !!oauthBusy} onClick={() => startOAuth("microsoft")}>{oauthBusy === "microsoft" ? "Opening Microsoft…" : "Continue with Microsoft"}</button>
          </div>
          {(!oauth.google || !oauth.microsoft) && <p class="form-note">Provider buttons stay disabled until their client ID and secret are configured. IMAP/JMAP below remains fully available.</p>}
        </section>
      )}
      {data && installKind.value && (
        <section class="device-install">
          <div>
            <strong>Keep mail close</strong>
            <p>
              {installKind.value === "ios"
                ? "In Safari, tap Share, then Add to Home Screen. Apple enables web notifications only after installation."
                : "Install the app for its own window. Recently viewed mail and drafts work offline; safe filing actions queue for reconnect."}
            </p>
          </div>
          {installKind.value === "native" && (
            <button class="btn btn-outline btn-sm" type="button" onClick={() => installApp()}>
              Install app
            </button>
          )}
        </section>
      )}
      {data && (
        <section class="device-install">
          <div><strong>Notes and board archive</strong><p>Portable Markdown plus JSON that preserves note positions, colours, card state, and thread references.</p></div>
          <button class="btn btn-outline btn-sm" type="button" onClick={async () => { try { await download("/personal/export", "email-soft-personal-data.zip"); showToast("Personal data downloaded"); } catch (e) { showError(e instanceof Error ? e.message : "Export failed"); } }}>Export personal data</button>
        </section>
      )}
      {data && <ConnectForm onDone={reload} />}
    </>
  );
}
