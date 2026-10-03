import { useEffect, useState } from "preact/hooks";
import { api } from "../lib/api";
import { accounts, openCompose, setList, showError, showToast } from "../lib/store";

export interface OutboxEntry {
  id: string; account_id: string;
  status: "pending" | "submitting" | "submitted" | "failed" | "ambiguous" | "cancelled";
  filing_status: string; error_code?: string; created_at: string; undo_until: string; recoverable: boolean; saved_sent_copy: boolean;
}
interface Recovery {
  status: OutboxEntry["status"];
  request: { to: string; cc: string; bcc: string; subject: string; text: string; html: string; account_id: string; reply_to_message_id: string;
    attachments?: Array<{ filename: string; content_type: string; data_base64: string }> };
}

/** What a stopped send means for the person. "Not sent" is only claimed when the
 *  server recorded that the provider provably never accepted the message. */
function failureNote(code?: string): string {
  switch (code) {
    case "not_submitted": return "Not sent: the provider refused it before accepting it, so nothing was delivered.";
    case "payload_key_unavailable": return "Not sent. The saved copy cannot be opened with this server's current encryption key; restore the key it was saved with to recover it.";
    case "payload_corrupt": return "Not sent. The saved copy failed its integrity check and cannot be recovered.";
    default: return "Not sent. Nothing was delivered.";
  }
}

export function OutboxView() {
  const [rows, setRows] = useState<OutboxEntry[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState<string | null>(null);
  const [review, setReview] = useState<{ id: string; recovery: Recovery } | null>(null);
  useEffect(() => {
    setList({ kind: "none", key: "outbox", rows: [], senders: [] });
    let active = true, inFlight = false;
    const refresh = async () => {
      if (inFlight) return; inFlight = true;
      try { const result = await api<OutboxEntry[]>("/outbox", { fresh: true }); if (active) { setRows(result); setError(""); } }
      catch (e) { if (active) setError(e instanceof Error ? e.message : "Outbox could not be loaded"); }
      finally { inFlight = false; if (active) setLoading(false); }
    };
    void refresh(); const timer = window.setInterval(refresh, 3000);
    return () => { active = false; window.clearInterval(timer); };
  }, []);

  const cancel = async (id: string) => {
    if (busy) return; setBusy(id);
    try { await api("/outbox/" + encodeURIComponent(id), { method: "DELETE" }); setRows((old) => old.map((r) => r.id === id ? { ...r, status: "cancelled" } : r)); showToast("Send cancelled; the saved composition is available below"); }
    catch (e) { showError(e instanceof Error ? e.message : "Cancellation could not be confirmed"); }
    finally { setBusy(null); }
  };
  const inspect = async (id: string) => {
    if (busy) return; setBusy(id);
    try { const recovery = await api<Recovery>("/outbox/" + encodeURIComponent(id), { fresh: true }); setReview({ id, recovery }); }
    catch (e) { showError(e instanceof Error ? e.message : "Saved composition could not be opened"); }
    finally { setBusy(null); }
  };
  const discard = async (id: string) => {
    if (busy || !window.confirm("Permanently remove this saved composition and any saved Sent copy? You will not be able to recover these copies. The submission outcome will remain.")) return;
    setBusy(id);
    try { await api("/outbox/" + encodeURIComponent(id) + "/payload", { method: "DELETE" }); setRows((old) => old.map((r) => r.id === id ? { ...r, recoverable: false, saved_sent_copy: false } : r)); setReview(null); showToast("Saved composition removed"); }
    catch (e) { showError(e instanceof Error ? e.message : "Removal could not be confirmed"); }
    finally { setBusy(null); }
  };
  const restore = (item: Recovery) => {
    const r = item.request;
    if (item.status === "ambiguous") showToast("This draft comes from a send whose outcome is unknown. Check Sent and the recipient before sending it again.");
    openCompose({ to: r.to, cc: r.cc, bcc: r.bcc, subject: r.subject, body: r.html || r.text, htmlMode: !!r.html,
      accountId: r.account_id, replyToId: r.reply_to_message_id || undefined,
      attachments: (r.attachments || []).map((a) => ({ filename: a.filename, contentType: a.content_type, dataBase64: a.data_base64 })) });
    setReview(null);
  };
  return <section class="settings-section">
    <h1>Outbox</h1>
    <p>The server saves accepted sends before provider submission. “Submitted” means the provider accepted the message, not that a recipient received it. Saved compositions are removed 30 days after a send settles and records of outcomes after 90 days; disconnecting an account removes its outbox.</p>
    {loading && <p role="status">Loading saved sends…</p>}
    {error && <p role="alert">{error}</p>}
    {!loading && !error && rows.length === 0 && <p>No saved sends</p>}
    <ul>{rows.map((r) => <li key={r.id}>
      <strong>{r.status}</strong> · {accounts.value.find((a) => a.id === r.account_id)?.address || "Connected account"} · <time>{new Date(r.created_at).toLocaleString()}</time>
      {r.status === "failed" && <p>{failureNote(r.error_code)}</p>}
      {r.status === "ambiguous" && <p>The provider may already have sent this message. Check Sent and the recipient before creating another submission.</p>}
      {r.status === "submitted" && r.filing_status === "ambiguous" && <p>The message was submitted, but its Sent copy is unconfirmed. Do not resend it. {r.saved_sent_copy && <><a href={"/api/outbox/" + encodeURIComponent(r.id) + "?format=eml"} download>Download the submitted copy</a> <button type="button" disabled={busy !== null} onClick={() => discard(r.id)}>Remove saved Sent copy</button></>}</p>}
      {r.status === "pending" && Date.parse(r.undo_until) > Date.now() && <button type="button" disabled={busy !== null} onClick={() => cancel(r.id)}>Cancel send</button>}
      {r.recoverable && ["failed", "cancelled", "ambiguous"].includes(r.status) && <>
        <button type="button" disabled={busy !== null} onClick={() => inspect(r.id)}>Review saved composition</button>
        <button type="button" disabled={busy !== null} onClick={() => discard(r.id)}>Remove saved composition</button>
      </>}
    </li>)}</ul>
    {review && <section aria-label="Saved composition review">
      <h2>{review.recovery.request.subject}</h2><p>To: {review.recovery.request.to}</p>
      <pre style={{ whiteSpace: "pre-wrap" }}>{review.recovery.request.text || review.recovery.request.html}</pre>
      <p>{review.recovery.request.attachments?.length || 0} saved attachments</p>
      {review.recovery.status === "ambiguous" && <p role="alert">Creating a new draft can result in a duplicate message if you send it. Confirm the previous outcome first.</p>}
      <button type="button" onClick={() => restore(review.recovery)}>Create a new draft from this copy</button>
      <button type="button" onClick={() => setReview(null)}>Close review</button>
    </section>}
  </section>;
}
