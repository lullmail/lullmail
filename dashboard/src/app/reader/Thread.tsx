import { useState } from "preact/hooks";
import { download } from "../lib/api";
import { closeReader, openCompose, reader, readerRow, showError } from "../lib/store";
import { BUCKET_LABEL, loadMessageBody, loadOlderMessages, markDone, markRead, moveTo, pinThreads, snooze } from "../lib/actions";
import type { Bucket, ListBucket, Message, Row } from "../lib/types";
import { countOf, fmtFull, splitFrom } from "../lib/fmt";
import { Avatar } from "../ui/bits";
import { Icon } from "../ui/Icon";
import { SnoozeMenu } from "../ui/SnoozeMenu";
import { MessageBody } from "./Body";
import { Attachments } from "./Attachments";

/** One message. A body that is missing or failed offers its own reload —
    a plain "sync in progress" label promised progress that was not
    happening (audit 4 F19). */
function ThreadMessage({ message }: { message: Message }) {
  const [loadingBody, setLoadingBody] = useState(false);
  const onRetry = async () => {
    if (loadingBody) return;
    setLoadingBody(true);
    try { await loadMessageBody(message); } catch (e) { showError(e instanceof Error ? e.message : "Could not load message"); } finally { setLoadingBody(false); }
  };
  const who = splitFrom(message.from);
  const [exporting, setExporting] = useState(false);
  return (
    <article class="thread-msg">
      <div class="thread-msg-head">
        {/* Avatars belong where a person is the subject. Here they are. */}
        <Avatar email={who.email} name={who.name} size="sm" />
        <div class="thread-msg-names">
          <span class="thread-msg-from">{who.name || who.email}</span>
          <span class="thread-msg-to">to {message.to || "you"}</span>
        </div>
        <span class="thread-msg-date">{fmtFull(message.received_at)}</span>
        <button
          class="btn-icon message-export" type="button"
          title="Download original message (.eml)" aria-label="Download original message"
          disabled={exporting}
          onClick={async () => {
            setExporting(true);
            try {
              await download(
                "/messages/" + encodeURIComponent(message.id) + "/eml?account=" + encodeURIComponent(message.account),
                "message.eml"
              );
            } catch (e) {
              showError(e instanceof Error ? e.message : "Message export failed");
            } finally {
              setExporting(false);
            }
          }}
        >
          <Icon name="download" size={15} />
        </button>
      </div>
      <div class="thread-msg-body">
        <MessageBody
          html={message.html} text={message.body} messageId={message.id} sender={who.email}
          bodyStatus={message.body_status} onRetry={onRetry} account={message.account} inlineParts={message.inline_parts}
        />
        <Attachments account={message.account} messageId={message.id} items={message.attachments || []} />
      </div>
    </article>
  );
}

function Bar({ row }: { row: Row }) {
  const [asideOpen, setAsideOpen] = useState(false);
  const messages = reader.value.messages;
  const last = messages[messages.length - 1];

  const reply = () => {
    // The server computed the default recipients from the stored envelope
    // (Reply-To honored, own sent mail followed up to its recipients —
    // audit 4 F06). Empty means "ask": the composer opens with a blank To.
    openCompose({
      to: last.reply_to || "",
      subject: /^re:/i.test(last.subject) ? last.subject : "Re: " + (last.subject || ""),
      accountId: last.account,
      replyToId: last.id,
      context: "Replying to " + (splitFrom(last.from).name || splitFrom(last.from).email),
    });
  };

  return (
    <div class="thread-bar">
      <div class="column thread-bar-in">
        <button class="btn btn-accent" type="button" onClick={reply}>
          <Icon name="reply" size={14} /> Reply <span class="kbd">r</span>
        </button>
        <button class="btn btn-ghost" type="button" onClick={() => markDone([row])}>
          <Icon name="check" size={14} /> Done <span class="kbd">e</span>
        </button>
        <button class="btn btn-ghost" type="button" onClick={() => pinThreads([row])}>
          <Icon name="pin" size={14} /> Pin <span class="kbd">p</span>
        </button>

        <div style={{ position: "relative" }}>
          <button class="btn btn-ghost" type="button" aria-haspopup="menu" aria-expanded={asideOpen} onClick={() => setAsideOpen((v) => !v)}>
            <Icon name="aside" size={14} /> Snooze <span class="kbd">s</span>
          </button>
          {asideOpen && (
            <SnoozeMenu
              placement="up"
              onPick={(days) => { setAsideOpen(false); snooze([row], days); }}
              onClose={() => setAsideOpen(false)}
            />
          )}
        </div>

        <span class="spacer" />

        {/* Where it files. The bucket it is already in is not offered. */}
        {(["imbox", "feed", "paper_trail"] as Bucket[])
          .filter((b) => b !== row.bucket)
          .map((b) => (
            <button class="btn btn-ghost btn-sm" type="button" key={b} onClick={() => moveTo([row], b)}>
              {BUCKET_LABEL[b]}
            </button>
          ))}
        <button class="btn btn-ghost btn-sm" type="button" onClick={() => markRead([row], false)}>Unread</button>
      </div>
    </div>
  );
}

/** One component, two homes: the whole page in document mode, the third column
    in classic. Only the affordance for leaving it differs. */
export function Thread({ backTo, variant = "page" }: { backTo: string; variant?: "page" | "pane" }) {
  const state = reader.value;

  const back = variant === "page" ? (
    <button class="back" type="button" onClick={closeReader}>
      <Icon name="back" size={14} /> {backTo} <span class="kbd">u</span>
    </button>
  ) : (
    <button class="btn-icon thread-close" type="button" title="Close (u)" aria-label="Close" onClick={closeReader}>
      <Icon name="close" size={16} />
    </button>
  );

  if (state.loading) {
    return (
      <div class="column thread">
        {back}
        <div class="skel" style={{ height: 34, width: "70%", marginTop: 8 }} />
        <div class="skel" style={{ height: 14, width: "30%", marginTop: 14 }} />
      </div>
    );
  }

  if (state.error) {
    return (
      <div class="column thread">
        {back}
        <div class="empty">
          <div class="empty-big">That thread didn't open.</div>
          <div class="empty-sub">{state.error}</div>
        </div>
      </div>
    );
  }

  const messages = state.messages;
  const last = messages[messages.length - 1];
  const row = readerRow(messages, state.threadId, state.bucket);
  const people = new Set(messages.map((m) => splitFrom(m.from).email));

  return (
    <>
      <div class="column thread">
        {back}
        <h1 class="thread-title">{last?.subject || "(no subject)"}</h1>
        <div class="thread-meta">
          {countOf(messages.length, "message")}
          {people.size > 1 && " · " + countOf(people.size, "person", "people")}
        </div>
        {state.nextCursor && <button class="btn btn-outline" disabled={state.loadingOlder} onClick={loadOlderMessages}>{state.loadingOlder ? "Loading…" : "Load older messages"}</button>}
        {messages.map((m) => <ThreadMessage message={m} key={m.account + "\0" + m.id} />)}
      </div>
      {row && <Bar row={row} />}
    </>
  );
}
