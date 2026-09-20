import { useEffect, useRef, useState } from "preact/hooks";
import { accounts, closeCompose, compose, cycleDraft, draftIndex, draftsUnsaved, draftStack, newDraft, retireDraft, showToast, undoSeconds, updateDraft, updateDraftById, type ComposeState } from "../lib/store";
import { sendMail, type SendAttachment } from "../lib/actions";
import { deleteDraft, saveDraftFields } from "../lib/offline";

const previewPolicy = '<meta http-equiv="Content-Security-Policy" content="default-src \'none\'; img-src data: cid:; style-src \'unsafe-inline\'; base-uri \'none\'; form-action \'none\'">';

const MAX_FILE_BYTES = 15 << 20;

async function fileToAttachment(file: File): Promise<SendAttachment | null> {
  if (file.size > MAX_FILE_BYTES) {
    showToast(`"${file.name}" is over 15 MiB and was skipped`);
    return null;
  }
  const buf = await file.arrayBuffer();
  const bytes = new Uint8Array(buf);
  let binary = "";
  const CHUNK = 0x8000;
  for (let i = 0; i < bytes.length; i += CHUNK) {
    binary += String.fromCharCode(...bytes.subarray(i, i + CHUNK));
  }
  return { filename: file.name, contentType: file.type || "application/octet-stream", dataBase64: btoa(binary) };
}

function formatBytes(n: number): string {
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + " MB";
  if (n >= 1 << 10) return Math.round(n / (1 << 10)) + " KB";
  return n + " B";
}

/** Wrap the textarea's current selection with an HTML tag pair (or an
    <a href> shell the cursor lands inside). */
function wrapSelection(el: HTMLTextAreaElement, before: string, after: string, cursorOffset?: number) {
  const start = el.selectionStart;
  const end = el.selectionEnd;
  const value = el.value;
  const next = value.slice(0, start) + before + value.slice(start, end) + after + value.slice(end);
  el.value = next;
  const caret = start + before.length + (end - start) + (cursorOffset ?? 0);
  el.setSelectionRange(caret, caret);
  el.focus();
  el.dispatchEvent(new Event("input", { bubbles: true }));
}

/** One draft in the ring. Remounted per draftIndex so each draft owns its
    fields and its autosave slot — switching carousels nothing between them.
    offline-v2 (audit F05/WEB-07): the seed IS the persisted record
    (hydrateDrafts loads whole records, attachments included), so fields
    initialize from it directly and every save targets the one
    IndexedDB record that holds this draft. */
function DraftForm({ seed }: { seed: ComposeState }) {
  const [to, setTo] = useState(seed.to ?? "");
  const [cc, setCc] = useState(seed.cc ?? "");
  const [bcc, setBcc] = useState(seed.bcc ?? "");
  const [showCc, setShowCc] = useState(!!(seed.cc || seed.bcc));
  const [subject, setSubject] = useState(seed.subject ?? "");
  const [body, setBody] = useState(seed.body ?? "");
  const [htmlMode, setHtmlMode] = useState(seed.htmlMode ?? false);
  const [preview, setPreview] = useState(false);
  const [busy, setBusy] = useState(false);
  const [accountId, setAccountId] = useState(seed.accountId ?? "");
  const [attachments, setAttachments] = useState<SendAttachment[]>(seed.attachments ? [...seed.attachments] : []);
  const bodyRef = useRef<HTMLTextAreaElement>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  // Retirement fence: once a draft is sent or discarded, the unmount
  // autosave and any queued attachment save must not resurrect its slot
  // (audit 4-F04).
  const retired = useRef(false);
  // Closes the same-tick double-activation window between the button and
  // the keyboard shortcut, which funnel through one send() (audit 4 F03).
  const sending = useRef(false);
  // Serializes attachment writes with the final deletion so an in-flight
  // save cannot land after deleteDraft and resurrect the draft.
  const saves = useRef<Promise<void>>(Promise.resolve());
  const queueSave = (run: () => Promise<void>) => {
    saves.current = saves.current.then(run, run);
  };

  const fileBytes = attachments.reduce((n, a) => n + Math.round(a.dataBase64.length * 3 / 4), 0);

  // Pending file reads for THIS draft (audit 5 DRAFT-02): a read that
  // started while the composer was idle used to complete after send or a
  // draft switch — the message left without the file, or the completion
  // wrote into a retired slot. Send eligibility requires zero pending.
  const [pendingReads, setPendingReads] = useState(0);

  const addFiles = async (files: FileList | null) => {
    // No additions while submitting — the send already captured its set
    // (audit 4 F03).
    if (!files || busy || sending.current) return;
    const next: SendAttachment[] = [];
    setPendingReads((n) => n + files.length);
    try {
      for (const f of files) {
        const att = await fileToAttachment(f);
        if (att) { next.push(att); }
      }
    } finally {
      setPendingReads((n) => Math.max(0, n - files.length));
    }
    if (!next.length) return;
    if (retired.current) return; // the draft ended mid-read: discard
    // Functional merge: two selections decoding concurrently must not
    // overwrite one another (audit WEB-06). The merged set lands in the
    // draft stack — the single authoritative document — so the carousel
    // seed, the flush, and this component can never disagree about what
    // the draft carries (audit 5 DRAFT-01).
    setAttachments((current) => {
      const merged = [...current, ...next];
      updateDraftById(seed.id, { attachments: merged });
      return merged;
    });
  };

  const removeAttachment = (i: number) => {
    if (busy || sending.current) return;
    setAttachments((current) => {
      const next = current.filter((_, idx) => idx !== i);
      updateDraftById(seed.id, { attachments: next });
      return next;
    });
  };

  // An undo-restored seed carries its attachments in memory; they are
  // written into the draft's single record here. Every other case
  // hydrates from that record already (no second engine to wait for).
  // The stack patch and the record write carry the same payload — the
  // stack is what later flushes read (audit 5 DRAFT-01).
  useEffect(() => {
    const seeded = seed.attachments;
    if (!seeded) return;
    const payload = [...seeded];
    updateDraftById(seed.id, { attachments: payload });
    queueSave(() => (retired.current ? Promise.resolve() : saveDraftFields(seed.id, { attachments: payload })));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Attachment persistence rides the ONE flush path: attachment changes
  // patch the draft stack (updateDraftById), the stack subscription
  // schedules the debounced flush, and flushDrafts writes the whole
  // record — fields AND attachments together. A separate per-change
  // attachment writer here is what let a stale whole-draft flush
  // overwrite the saved attachment list (audit 5 DRAFT-01).

  // Debounced autosave that FLUSHES on unmount: clearing the timer and
  // dropping a pending write left the per-draft snapshot older than the
  // draft stack, so switching back resurrected stale text (audit WEB-05).
  // The write is fenced by retirement so a sent/discarded draft's slot is
  // not recreated after its deletion (audit 4-F04).
  const latest = useRef({ to, cc, bcc, subject, body, htmlMode, accountId });
  latest.current = { to, cc, bcc, subject, body, htmlMode, accountId };
  useEffect(() => {
    const write = () => {
      if (retired.current) return;
      saveDraftFields(seed.id, latest.current).catch(() => { /* store.ts surfaces ring-wide failures */ });
    };
    const timer = setTimeout(write, 250);
    return () => { clearTimeout(timer); write(); };
  }, [seed.id, to, cc, bcc, subject, body, htmlMode, accountId]);

  // Send and discard both pass through here: fence first, then remove the
  // record, and only then retire the ring entry (audit 4-F04). The queued
  // delete runs after every pending save, so deletion is the last write.
  const retireLocalDraft = () => {
    retired.current = true;
    queueSave(() => deleteDraft(seed.id));
    retireDraft(seed.id);
  };

  const send = async () => {
    // The guard lives in send(), not only on the button: the keyboard path
    // reaches here directly (audit 4 F03). Pending file reads block the
    // send — a message that leaves without its attachment is silent data
    // loss (audit 5 DRAFT-02).
    if (!to.trim() || busy || pendingReads > 0 || sending.current) return;
    sending.current = true;
    setBusy(true);
    let ok = false;
    try {
      ok = await sendMail({
        to: to.trim(), cc: cc.trim(), bcc: bcc.trim(), subject,
        text: htmlMode ? "" : body,
        html: htmlMode ? body : undefined,
        // A new message with no account chosen sends from the account the
        // From menu is showing. Left empty, the server picks its own "first",
        // which is not the menu's first. A reply stays empty: the server
        // answers from the account that received the parent.
        accountId: accountId || seed.accountId || (seed.replyToId ? undefined : accounts.value[0]?.id),
        replyToId: seed.replyToId,
        attachments,
      });
    } finally {
      sending.current = false;
      setBusy(false);
    }
    if (ok) retireLocalDraft();
  };

  const accountList = accounts.value;
  const fromLabel = accountList.find((a) => a.id === (accountId || seed.accountId))?.address || accountList[0]?.address || "";

  return (
    <>
      <div class="compose-form">
        <div class="compose-kicker">{seed.context || "New message"}</div>
        <div class="compose-head-row">
          <select
            class="compose-from" aria-label="Send from account"
            value={accountId || seed.accountId || ""}
            onChange={(e) => { const v = (e.target as HTMLSelectElement).value; setAccountId(v); updateDraft({ accountId: v }); }}
          >
            {accountList.length === 0 && <option value="">{fromLabel || "First connected account"}</option>}
            {accountList.map((a) => <option value={a.id}>{a.address}</option>)}
          </select>
          <button
            class={"btn btn-ghost btn-sm" + (showCc ? " lens-on" : "")} type="button"
            aria-pressed={showCc}
            title="Show or hide the Cc and Bcc fields"
            onClick={() => setShowCc((v) => !v)}
          >Cc/Bcc</button>
        </div>
        <input
          class="compose-to" type="text" placeholder="To — comma-separated" autocomplete="off" autofocus={!to}
          value={to} onInput={(e) => { const v = (e.target as HTMLInputElement).value; setTo(v); updateDraft({ to: v }); }}
        />
        {showCc && (
          <>
            <input
              class="compose-to" type="text" placeholder="Cc" autocomplete="off"
              value={cc} onInput={(e) => { const v = (e.target as HTMLInputElement).value; setCc(v); updateDraft({ cc: v }); }}
            />
            <input
              class="compose-to" type="text" placeholder="Bcc" autocomplete="off"
              value={bcc} onInput={(e) => { const v = (e.target as HTMLInputElement).value; setBcc(v); updateDraft({ bcc: v }); }}
            />
          </>
        )}
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
          {htmlMode && !preview && (
            <>
              <button class="btn btn-ghost btn-sm" type="button" title="Bold" onClick={() => bodyRef.current && wrapSelection(bodyRef.current, "<b>", "</b>")}>B</button>
              <button class="btn btn-ghost btn-sm compose-tool-i" type="button" title="Italic" onClick={() => bodyRef.current && wrapSelection(bodyRef.current, "<i>", "</i>")}>I</button>
              <button class="btn btn-ghost btn-sm" type="button" title="Link" onClick={() => bodyRef.current && wrapSelection(bodyRef.current, '<a href="', '"></a>', -6)}>Link</button>
            </>
          )}
          {htmlMode && (
            <button
              class={"btn btn-ghost btn-sm" + (preview ? " lens-on" : "")} type="button"
              aria-pressed={preview}
              onClick={() => setPreview((v) => !v)}
            >{preview ? "Edit source" : "Preview"}</button>
          )}
          <button
            class="btn btn-ghost btn-sm" type="button"
            title="Attach files (they upload with the message, not before)"
            onClick={() => fileInput.current?.click()}
          >Attach</button>
          <input
            ref={fileInput} type="file" multiple hidden
            onChange={(e) => { const el = e.target as HTMLInputElement; addFiles(el.files); el.value = ""; }}
          />
          {htmlMode && !preview && <span class="compose-modes-note">plain-text readers get an automatic fallback</span>}
        </div>
        {attachments.length > 0 && (
          <div class="compose-files">
            {attachments.map((a, i) => (
              <span class="compose-file" key={i}>
                <span class="compose-file-name">{a.filename}</span>
                <span class="compose-file-size">{formatBytes(Math.round(a.dataBase64.length * 3 / 4))}</span>
                <button class="btn-icon" type="button" aria-label={"Remove " + a.filename} onClick={() => removeAttachment(i)}>×</button>
              </span>
            ))}
            <span class="compose-files-total">{formatBytes(fileBytes)} attached</span>
          </div>
        )}
        {htmlMode && preview ? (
          /* sandbox with no allow-* tokens: styles render, scripts never run. */
          <iframe
            class="compose-preview" title="HTML preview" sandbox=""
            srcDoc={previewPolicy + (body || "<p>(nothing written yet)</p>")}
          />
        ) : (
          <textarea
            ref={bodyRef}
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
        <span class="hint"><span class="kbd">⌘↵</span> send · <span class="kbd">Esc</span> park · <span class="kbd">c</span> new draft · {undoSeconds}s to undo</span>
        <button class="btn btn-ghost btn-sm" type="button" onClick={retireLocalDraft}>Discard</button>
        <button class="btn btn-accent" type="button" disabled={!to.trim() || busy || pendingReads > 0} onClick={send}>
          {busy ? "Sending…" : pendingReads > 0 ? "Reading files…" : "Send"}
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
    <div class="veil compose-veil" onClick={(ev) => { if (ev.target === ev.currentTarget) closeCompose(); }}>
      <div
        class="panel panel-narrow compose-panel" role="dialog" aria-modal="true" aria-label="Compose"
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
          {draftsUnsaved.value && (
            <span class="compose-ring-count" role="status">drafts are NOT saved on this device</span>
          )}
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
