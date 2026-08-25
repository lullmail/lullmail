import { useEffect, useState } from "preact/hooks";
import { compose } from "../lib/store";
import { sendMail } from "../lib/actions";

export function Compose() {
  const seed = compose.value;
  const draftKey = "es-draft-" + (seed?.replyToId || "new");
  let saved: { to?: string; subject?: string; body?: string } = {};
  try { saved = JSON.parse(localStorage.getItem(draftKey) || "{}"); } catch { /* private mode */ }
  const [to, setTo] = useState(saved.to ?? seed?.to ?? "");
  const [subject, setSubject] = useState(saved.subject ?? seed?.subject ?? "");
  const [body, setBody] = useState(saved.body ?? seed?.body ?? "");
  const [busy, setBusy] = useState(false);
  if (!seed) return null;

  useEffect(() => {
    const timer = setTimeout(() => {
      try { localStorage.setItem(draftKey, JSON.stringify({ to, subject, body })); } catch { /* private mode */ }
    }, 250);
    return () => clearTimeout(timer);
  }, [draftKey, to, subject, body]);

  const close = () => { compose.value = null; };
  const discard = () => { localStorage.removeItem(draftKey); close(); };

  const send = async () => {
    if (!to.trim() || busy) return;
    setBusy(true);
    const ok = await sendMail({ to: to.trim(), subject, text: body, replyToId: seed.replyToId });
    setBusy(false);
    if (ok) { localStorage.removeItem(draftKey); close(); }
  };

  return (
    <div class="veil" onClick={(ev) => { if (ev.target === ev.currentTarget) close(); }}>
      <div class="panel" role="dialog" aria-modal="true" aria-label="Compose">
        <div class="compose-form">
          <div class="compose-kicker">{seed.context || "New message"}</div>
          <input
            class="compose-to" type="email" placeholder="To" autocomplete="off" autofocus={!to}
            value={to} onInput={(e) => setTo((e.target as HTMLInputElement).value)}
          />
          <input
            class="compose-subject" type="text" placeholder="Subject"
            value={subject} onInput={(e) => setSubject((e.target as HTMLInputElement).value)}
          />
          <textarea
            class="compose-body" placeholder="Write something worth reading." autofocus={!!to}
            value={body}
            onInput={(e) => setBody((e.target as HTMLTextAreaElement).value)}
            onKeyDown={(ev) => {
              if ((ev.metaKey || ev.ctrlKey) && ev.key === "Enter") { ev.preventDefault(); send(); }
            }}
          />
        </div>
        <div class="compose-btns">
          <span class="hint"><span class="kbd">⌘↵</span> send · <span class="kbd">Esc</span> close · 5s to undo</span>
          <button class="btn btn-ghost btn-sm" type="button" onClick={discard}>Discard</button>
          <button class="btn btn-accent" type="button" disabled={!to.trim() || busy} onClick={send}>
            {busy ? "Sending…" : "Send"}
          </button>
        </div>
      </div>
    </div>
  );
}
