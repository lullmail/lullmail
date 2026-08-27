import { useEffect, useState } from "preact/hooks";
import { api, authApi, authed, refreshAuth } from "../lib/api";
import { createPasskey } from "../lib/passkeys";
import { fmtDate } from "../lib/fmt";
import { resetSelection, setList, showError, showToast } from "../lib/store";
import { Empty, ListSkeleton, PageHead } from "../ui/bits";
import { navigate } from "../lib/router";
import { clearOfflineData } from "../lib/offline";

interface Passkey { id: string; name: string; created_at: string; last_used_at: string | null }
interface Security { email: string; passkeys: Passkey[]; totp_enabled: boolean; recovery_codes_remaining: number }
interface Session { id: string; created_at: string; last_seen_at: string; expires_at: string; user_agent: string; current: boolean }
interface PushState { configured: boolean; subscribed: boolean; public_key: string }
interface AgentToken { id: string; name: string; created_at: string; last_used_at: string | null }

function vapidBytes(value: string): Uint8Array {
  const padded = value.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((value.length + 3) % 4);
  const raw = atob(padded); const bytes = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) bytes[i] = raw.charCodeAt(i);
  return bytes;
}

export function SecurityView() {
  const [security, setSecurity] = useState<Security | null>(null);
  const [sessions, setSessions] = useState<Session[]>([]);
  const [busy, setBusy] = useState("");
  const [codes, setCodes] = useState<string[] | null>(null);
  const [totp, setTotp] = useState<{ secret: string; uri: string } | null>(null);
  const [totpCode, setTotpCode] = useState("");
  const [confirm, setConfirm] = useState("");
  const [deleting, setDeleting] = useState(false);
  const [push, setPush] = useState<PushState | null>(null);
  const [agentTokens, setAgentTokens] = useState<AgentToken[]>([]);
  const [newToken, setNewToken] = useState("");

  const load = async () => {
    try {
      const [next, active, pushState, tokens] = await Promise.all([api<Security>("/security"), api<Session[]>("/security/sessions"), api<PushState>("/push"), api<AgentToken[]>("/security/agent-tokens")]);
      setSecurity(next); setSessions(active); setPush(pushState); setAgentTokens(tokens);
    } catch (e) { showError(e instanceof Error ? e.message : "Security settings failed"); }
  };
  useEffect(() => {
    resetSelection();
    setList({ kind: "none", key: "security", loading: false, error: null, rows: [], senders: [], origin: null });
    load();
  }, []);

  const addPasskey = async () => {
    const name = window.prompt("Name this passkey", "This device")?.trim();
    if (!name) return;
    setBusy("passkey");
    try {
      const options = await api<Record<string, unknown>>("/security/passkeys/begin", { method: "POST" });
      const credential = await createPasskey(options);
      await api("/security/passkeys/finish?name=" + encodeURIComponent(name), { body: credential });
      showToast("Passkey added"); await load();
    } catch (e) { showError(e instanceof Error ? e.message : "Could not add passkey"); }
    finally { setBusy(""); }
  };

  const regenerate = async () => {
    if (!window.confirm("Replace every unused recovery code? Existing codes will stop working.")) return;
    setBusy("recovery");
    try { const out = await api<{ recovery_codes: string[] }>("/security/recovery/regenerate", { method: "POST" }); setCodes(out.recovery_codes); await load(); }
    catch (e) { showError(e instanceof Error ? e.message : "Could not create recovery codes"); }
    finally { setBusy(""); }
  };

  const beginTOTP = async () => {
    setBusy("totp");
    try { setTotp(await api("/security/totp/begin", { method: "POST" })); }
    catch (e) { showError(e instanceof Error ? e.message : "Could not start authenticator setup"); }
    finally { setBusy(""); }
  };
  const confirmTOTP = async () => {
    setBusy("totp");
    try { await api("/security/totp/confirm", { body: { code: totpCode } }); setTotp(null); setTotpCode(""); showToast("Authenticator enabled"); await load(); }
    catch (e) { showError(e instanceof Error ? e.message : "Code did not match"); }
    finally { setBusy(""); }
  };

  const logout = async () => {
    await authApi("/auth/logout", { method: "POST" }).catch(() => {});
    authed.value = false; await refreshAuth(); navigate("/today");
  };

  const togglePush = async () => {
    if (!push?.configured || !("serviceWorker" in navigator) || !("PushManager" in window)) return;
    setBusy("push");
    try {
      const registration = await navigator.serviceWorker.ready;
      const current = await registration.pushManager.getSubscription();
      if (current) {
        await api("/push", { method: "DELETE", body: { endpoint: current.endpoint } });
        await current.unsubscribe(); showToast("Notifications disabled");
      } else {
        const permission = await Notification.requestPermission();
        if (permission !== "granted") throw new Error("Notification permission was not granted.");
        const subscription = await registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: vapidBytes(push.public_key) as BufferSource });
        await api("/push", { body: subscription.toJSON() }); showToast("Notifications enabled");
      }
      await load();
    } catch (e) { showError(e instanceof Error ? e.message : "Notification setup failed"); }
    finally { setBusy(""); }
  };

  const createAgentToken = async () => {
    const name = window.prompt("Name this token (what will use it?)", "Purelymail import")?.trim();
    if (!name) return;
    setBusy("agent");
    try {
      const out = await api<{ token: string }>("/security/agent-tokens", { body: { name } });
      setNewToken(out.token); await load();
    } catch (e) { showError(e instanceof Error ? e.message : "Could not create token"); }
    finally { setBusy(""); }
  };

  const copyNewToken = async () => {
    try { await navigator.clipboard.writeText(newToken); showToast("Token copied"); }
    catch { /* selection fallback below */ }
  };


  const deleteEverything = async () => {
    if (!security || confirm.trim().toLowerCase() !== security.email.toLowerCase()) return;
    setDeleting(true);
    try {
      await api("/account", { method: "DELETE", body: { confirmation: confirm } });
      await clearOfflineData();
      authed.value = false; await refreshAuth(); navigate("/today");
    } catch (e) { showError(e instanceof Error ? e.message : "Could not delete account"); setDeleting(false); }
  };

  if (!security) return <><PageHead kicker="Settings" title="Security" sub="Passkeys, recovery, and active sessions." /><ListSkeleton rows={3} /></>;

  return (
    <>
      <PageHead kicker="Settings" title="Security" sub={security.email + " · passkeys are primary; recovery stays in your hands."} />
      <div class="settings-tabs"><a href="/settings">Settings</a><a href="/settings/accounts">Mailboxes</a><a href="/settings/appearance">Appearance</a><a class="active" href="/settings/security">Security</a></div>

      <section class="settings-section">
        <div class="settings-section-head"><div><h2>Passkeys</h2><p>Device-bound credentials with user verification. Add two before you need the second.</p></div>
          <button class="btn btn-primary btn-sm" type="button" disabled={!!busy} onClick={addPasskey}>{busy === "passkey" ? "Waiting…" : "Add passkey"}</button></div>
        {security.passkeys.map((key) => <div class="security-row" key={key.id}><div><strong>{key.name}</strong><span>Added {fmtDate(key.created_at)}{key.last_used_at ? " · used " + fmtDate(key.last_used_at) : " · not used yet"}</span></div>
          <button class="btn btn-quiet-danger btn-sm" type="button" disabled={security.passkeys.length < 2} onClick={async () => { try { await api("/security/passkeys/" + encodeURIComponent(key.id), { method: "DELETE" }); await load(); } catch (e) { showError(e instanceof Error ? e.message : "Could not remove passkey"); } }}>Remove</button></div>)}
      </section>

      <section class="settings-section">
        <div class="settings-section-head"><div><h2>New-mail notifications</h2><p>Private, generic web-push alerts when unread Inbox mail needs you. Message contents never appear on the lock screen.</p></div>
          <button class="btn btn-outline btn-sm" type="button" disabled={!push?.configured || !!busy || !("PushManager" in window)} onClick={togglePush}>{busy === "push" ? "Updating…" : push?.subscribed ? "Disable" : "Enable"}</button></div>
        {push && !push.configured && <p class="settings-callout">Server setup required: add a VAPID key pair and subject. The app keeps this control disabled until delivery can work.</p>}
      </section>

      <section class="settings-section">
        <div class="settings-section-head"><div><h2>Recovery codes</h2><p>{security.recovery_codes_remaining} one-use codes remain. New codes invalidate the old set.</p></div>
          <button class="btn btn-outline btn-sm" type="button" disabled={!!busy} onClick={regenerate}>{busy === "recovery" ? "Creating…" : "Create new set"}</button></div>
        {codes && <div class="recovery-sheet"><div class="recovery-grid">{codes.map((code) => <code key={code}>{code}</code>)}</div><button class="btn btn-ghost btn-sm" type="button" onClick={() => window.print()}>Print codes</button></div>}
      </section>

      <section class="settings-section">
        <div class="settings-section-head"><div><h2>Authenticator app</h2><p>An optional TOTP fallback, encrypted at rest.</p></div>
          {security.totp_enabled ? <button class="btn btn-quiet-danger btn-sm" type="button" onClick={async () => { await api("/security/totp", { method: "DELETE" }); await load(); }}>Disable</button>
            : <button class="btn btn-outline btn-sm" type="button" disabled={!!busy} onClick={beginTOTP}>Set up</button>}</div>
        {totp && <div class="totp-setup"><p>Enter this key in your authenticator app, then verify one code.</p><code>{totp.secret}</code><div class="inline-form"><input value={totpCode} inputMode="numeric" autocomplete="one-time-code" placeholder="6-digit code" onInput={(e) => setTotpCode((e.target as HTMLInputElement).value)} /><button class="btn btn-primary btn-sm" type="button" disabled={totpCode.length < 6 || !!busy} onClick={confirmTOTP}>Verify</button></div></div>}
      </section>

      <section class="settings-section">
        <div class="settings-section-head"><div><h2>Active sessions</h2><p>Thirty-day server-side sessions. Revoke anything you do not recognise.</p></div><button class="btn btn-outline btn-sm" type="button" onClick={logout}>Sign out here</button></div>
        {sessions.length === 0 && <Empty title="No active sessions." />}
        {sessions.map((session) => <div class="security-row" key={session.id}><div><strong>{session.current ? "This session" : "Signed-in device"}</strong><span>{session.user_agent || "Unknown browser"} · seen {fmtDate(session.last_seen_at)}</span></div><button class="btn btn-quiet-danger btn-sm" type="button" onClick={async () => { await api("/security/sessions/" + encodeURIComponent(session.id), { method: "DELETE" }); if (session.current) { authed.value = false; await refreshAuth(); } else await load(); }}>Revoke</button></div>)}
      </section>

      <section class="settings-section">
        <div class="settings-section-head"><div><h2>Agent tokens</h2><p>Let a script or AI agent connect mailboxes and read or send mail on your behalf. Tokens can never touch sign-in or security settings. Shown once — revoke anytime.</p></div>
          <button class="btn btn-outline btn-sm" type="button" disabled={!!busy} onClick={createAgentToken}>{busy === "agent" ? "Creating…" : "Create token"}</button></div>
        {newToken && <div class="recovery-sheet">
          <p>Copy this now — it will not be shown again. Send it as <code>Authorization: Bearer …</code> against <code>/api/…</code>.</p>
          <div class="totp-setup"><code>{newToken}</code></div>
          <div class="inline-form"><button class="btn btn-primary btn-sm" type="button" onClick={copyNewToken}>Copy</button><button class="btn btn-ghost btn-sm" type="button" onClick={() => setNewToken("")}>Done</button></div>
        </div>}
        {agentTokens.length === 0 && !newToken && <Empty title="No agent tokens." />}
        {agentTokens.map((token) => <div class="security-row" key={token.id}><div><strong>{token.name}</strong><span>Created {fmtDate(token.created_at)}{token.last_used_at ? " · used " + fmtDate(token.last_used_at) : " · not used yet"}</span></div>
          <button class="btn btn-quiet-danger btn-sm" type="button" onClick={async () => { try { await api("/security/agent-tokens/" + encodeURIComponent(token.id), { method: "DELETE" }); await load(); } catch (e) { showError(e instanceof Error ? e.message : "Could not revoke token"); } }}>Revoke</button></div>)}
      </section>

      <section class="settings-section danger-zone">
        <h2>Delete everything</h2><p>First export every connected mailbox from Mailboxes. This permanently deletes credentials, local mail mirrors, notes, board cards, sender rules, passkeys, recovery codes, and sessions. It does not delete mail at your provider.</p>
        <div class="inline-form"><input value={confirm} placeholder={security.email} aria-label="Type your email to confirm deletion" onInput={(e) => setConfirm((e.target as HTMLInputElement).value)} /><button class="btn btn-danger" type="button" disabled={deleting || confirm.trim().toLowerCase() !== security.email.toLowerCase()} onClick={deleteEverything}>{deleting ? "Deleting…" : "Delete my account"}</button></div>
      </section>
    </>
  );
}
