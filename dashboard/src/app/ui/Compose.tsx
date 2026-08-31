import { useEffect, useRef, useState } from "preact/hooks";
import { closeCompose, compose, cycleDraft, draftIndex, draftStack, newDraft, retireDraft, updateDraft, type ComposeState } from "../lib/store";
import { sendMail } from "../lib/actions";

const previewPolicy = '<meta http-equiv="Content-Security-Policy" content="default-src \'none\'; img-src data: cid:; style-src \'unsafe-inline\'; base-uri \'none\'; form-action \'none\'">';

/** One draft in the ring. Remounted per draftIndex so each draft owns its
    fields and its autosave slot — switching carousels nothing between them. */
function DraftForm({ seed }: { seed: ComposeState }) {
  const draftKey = "es-draft-" + seed.id;
  let saved: { to?: string; subject?: string; body?: string; htmlMode?: boolean } = {};
  try { saved = JSON.parse(localStorage.getItem(draftKey) || "{}"); } catch { /* private mode */ }
  const [to, setTo] = useState(saved.to ?? seed.to ?? "");
  const [subject, setSubject] = useState(saved.subject ?? seed.subject ?? "");
  const [body, setBody] = useState(saved.body ?? seed.body ?? "");
  const [htmlMode, setHtmlMode] = useState(saved.htmlMode ?? seed.htmlMode ?? false);
  const [preview, setPreview] = useState(false);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    const timer = setTimeout(() => {
      try { localStorage.setItem(draftKey, JSON.stringify({ to, subject, body, htmlMode })); } catch { /* private mode */ }
    }, 250);
    return () => clearTimeout(timer);
  }, [draftKey, to, subject, body, htmlMode]);

  const send = async () => {
    if (!to.trim() || busy) return;
    setBusy(true);
    const ok = await sendMail({
      to: to.trim(), subject,
      text: htmlMode ? "" : body,
      html: htmlMode ? body : undefined,
      accountId: seed.accountId,
      replyToId: seed.replyToId,
    });
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
        <div class="compose-modes">
          <button
            class={"btn btn-ghost btn-sm" + (htmlMode ? " lens-on" : "")} type="button"
            aria-pressed={htmlMode}
            title="Toggle HTML source composing: paste or write styled HTML, send it as a rich message"
            onClick={() => { setHtmlMode((v) => !v); setPreview(false); updateDraft({ htmlMode: !htmlMode }); }}
          >HTML</button>
          {htmlMode && (
            <button
              class={"btn btn-ghost btn-sm" + (preview ? " lens-on" : "")} type="button"
              aria-pressed={preview}
              onClick={() => setPreview((v) => !v)}
            >{preview ? "Edit source" : "Preview"}</button>
          )}
          {htmlMode && <span class="compose-modes-note">plain-text readers get an automatic fallback</span>}
        </div>
        {htmlMode && preview ? (
          /* sandbox with no allow-* tokens: styles render, scripts never run. */
          <iframe
            class="compose-preview" title="HTML preview" sandbox=""
            srcDoc={previewPolicy + (body || "<p>(nothing written yet)</p>")}
          />
        ) : (
          <textarea
            class={"compose-body" + (htmlMode ? " html-source" : "")}
            placeholder={htmlMode ? "Write or paste HTML — inline styles travel best in email." : "Write something worth reading."}
            autofocus={!!to && !htmlMode}
            spellcheck={!htmlMode}
            value={body}
            onInput={(e) => { const v = (e.target as HTMLTextAreaElement).value; setBody(v); updateDraft({ body: v }); }}
            onKeyDown={(ev) => {
              if ((ev.metaKey || ev.ctrlKey) && ev.key === "Enter") { ev.preventDefault(); send(); }
            }}
          />
        )}
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
          <button class="btn btn-ghost btn-sm" type="button" onClick={() => newDraft()}>
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
