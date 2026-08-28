import { useEffect, useRef, useState } from "preact/hooks";
import { closeCompose, compose, cycleDraft, draftIndex, draftStack, openCompose, retireDraft, updateDraft, type ComposeState } from "../lib/store";
import { sendMail } from "../lib/actions";

/** One draft in the ring. Remounted per draftIndex so each draft owns its
    fields and its autosave slot — switching carousels nothing between them. */
function DraftForm({ seed }: { seed: ComposeState }) {
  const draftKey = "es-draft-" + seed.id;
  let saved: { to?: string; subject?: string; body?: string } = {};
  try { saved = JSON.parse(localStorage.getItem(draftKey) || "{}"); } catch { /* private mode */ }
  const [to, setTo] = useState(saved.to ?? seed.to ?? "");
  const [subject, setSubject] = useState(saved.subject ?? seed.subject ?? "");
  const [body, setBody] = useState(saved.body ?? seed.body ?? "");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    const timer = setTimeout(() => {
      try { localStorage.setItem(draftKey, JSON.stringify({ to, subject, body })); } catch { /* private mode */ }
    }, 250);
    return () => clearTimeout(timer);
  }, [draftKey, to, subject, body]);

  const send = async () => {
    if (!to.trim() || busy) return;
    setBusy(true);
    const ok = await sendMail({ to: to.trim(), subject, text: body, replyToId: seed.replyToId });
    setBusy(false);
    if (ok) { localStorage.removeItem(draftKey); retireDraft(seed.id); }
  };

  return (
    <>
      <div class="compose-form">
        <div class="compose-kicker">{seed.context || "New message"}</div>
        <input
          class="compose-to" type="email" placeholder="To" autocomplete="off" autofocus={!to}
          value={to} onInput={(e) => { const v = (e.target as HTMLInputElement).value; setTo(v); updateDraft({ to: v }); }}
        />
        <input
          class="compose-subject" type="text" placeholder="Subject"
          value={subject} onInput={(e) => { const v = (e.target as HTMLInputElement).value; setSubject(v); updateDraft({ subject: v }); }}
        />
        <textarea
          class="compose-body" placeholder="Write something worth reading." autofocus={!!to}
          value={body}
          onInput={(e) => { const v = (e.target as HTMLTextAreaElement).value; setBody(v); updateDraft({ body: v }); }}
          onKeyDown={(ev) => {
            if ((ev.metaKey || ev.ctrlKey) && ev.key === "Enter") { ev.preventDefault(); send(); }
          }}
        />
      </div>
      <div class="compose-btns">
        <span class="hint"><span class="kbd">⌘↵</span> send · <span class="kbd">Esc</span> park · <span class="kbd">c</span> new draft · 5s to undo</span>
        <button class="btn btn-ghost btn-sm" type="button" onClick={() => { localStorage.removeItem(draftKey); retireDraft(seed.id); }}>Discard</button>
        <button class="btn btn-accent" type="button" disabled={!to.trim() || busy} onClick={send}>
          {busy ? "Sending…" : "Send"}
        </button>
      </div>
    </>
  );
}

export function Compose() {
  const seed = compose.value;
  const stack = draftStack.value;
  const at = draftIndex.value;
  // Swipe rotates the carousel on touch; the arrows do the same with a mouse.
  const touchX = useRef(0);
  if (!seed) return null;
  return (
    <div class="veil" onClick={(ev) => { if (ev.target === ev.currentTarget) closeCompose(); }}>
      <div
        class="panel panel-narrow" role="dialog" aria-modal="true" aria-label="Compose"
        onTouchStart={(ev) => { touchX.current = ev.touches[0].clientX; }}
        onTouchEnd={(ev) => {
          const dx = ev.changedTouches[0].clientX - touchX.current;
          if (Math.abs(dx) > 60) cycleDraft(dx < 0 ? 1 : -1);
        }}
      >
        <div class="compose-ring">
          <button class="btn btn-ghost btn-sm" type="button" onClick={() => openCompose()}>
            + New draft
          </button>
          {stack.length > 1 && (
            <>
              <span class="compose-ring-count">{at + 1} of {stack.length}</span>
              <button class="btn-icon" type="button" aria-label="Previous draft" onClick={() => cycleDraft(-1)}>‹</button>
              <button class="btn-icon" type="button" aria-label="Next draft" onClick={() => cycleDraft(1)}>›</button>
            </>
          )}
        </div>
        <DraftForm key={seed.id} seed={seed} />
      </div>
    </div>
  );
}
