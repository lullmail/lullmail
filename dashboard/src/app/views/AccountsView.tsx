import { useEffect, useState } from "preact/hooks";
import { api } from "../lib/api";
import { useLoad } from "../lib/useLoad";
import { setList, showError, showToast, syncNote } from "../lib/store";
import { refreshCounts } from "../lib/actions";
import type { Account } from "../lib/types";
import { countOf, fmtDate } from "../lib/fmt";
import { Empty, ListSkeleton, PageHead } from "../ui/bits";

function AccountCard({ account, onChange }: { account: Account; onChange: () => void }) {
  const [busy, setBusy] = useState(false);
  const [confirming, setConfirming] = useState(false);

  const sync = async () => {
    setBusy(true);
    syncNote.value = "Syncing " + account.address + "…";
    try {
      await api("/accounts/" + encodeURIComponent(account.id) + "?op=sync", { method: "POST" });
      showToast("Sync running");
      setTimeout(() => { syncNote.value = ""; onChange(); refreshCounts(); }, 4000);
    } catch (e) {
      syncNote.value = "";
      showError(e instanceof Error ? e.message : "Sync failed");
    } finally {
      setBusy(false);
    }
  };

  const disconnect = async () => {
    setBusy(true);
    try {
      await api("/accounts/" + encodeURIComponent(account.id), { method: "DELETE" });
      showToast(account.address + " disconnected");
      onChange();
      refreshCounts();
    } catch (e) {
      showError(e instanceof Error ? e.message : "Could not disconnect");
    } finally {
      setBusy(false);
      setConfirming(false);
    }
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
          </span>
        )}
      </div>

      <div class="account-btns">
        <button class="btn btn-outline btn-sm" type="button" disabled={busy} onClick={sync}>Sync now</button>
        {confirming ? (
          <>
            {/* Inline confirm: a native confirm() blocks the page and is unstyleable. */}
            <button class="btn btn-danger btn-sm" type="button" disabled={busy} onClick={disconnect}>
              Delete {account.address} and its mirror
            </button>
            <button class="btn btn-ghost btn-sm" type="button" onClick={() => setConfirming(false)}>Cancel</button>
          </>
        ) : (
          <button class="btn btn-ghost btn-sm" type="button" onClick={() => setConfirming(true)}>Disconnect</button>
        )}
      </div>
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
  const { data, loading, error, reload } = useLoad<Account[]>("accounts", (signal) =>
    api<Account[]>("/accounts", { signal })
  );

  useEffect(() => {
    setList({ kind: "none", key: "", loading, error, rows: [], senders: [], origin: null });
  }, [loading, error]);

  return (
    <>
      <PageHead
        kicker="Settings"
        title="Accounts"
        sub="Mailboxes this app mirrors. Credentials are encrypted at rest."
      />
      {loading && !data && <ListSkeleton rows={2} />}
      {error && <Empty title="That didn't load." sub={error} />}
      {data?.map((a) => <AccountCard account={a} onChange={reload} key={a.id} />)}
      {data && data.length === 0 && (
        <Empty title="No mailboxes yet." sub="Connect one below and the Screener starts filling up." />
      )}
      {data && <ConnectForm onDone={reload} />}
    </>
  );
}
