import { useState } from "preact/hooks";
import { closeReader, openCompose, reader } from "../lib/store";
import { BUCKET_LABEL, markDone, markRead, moveTo, snooze } from "../lib/actions";
import type { Bucket, ListBucket, Message, Row } from "../lib/types";
import { countOf, fmtFull, splitFrom } from "../lib/fmt";
import { Avatar } from "../ui/bits";
import { Icon } from "../ui/Icon";
import { SnoozeMenu } from "../ui/SnoozeMenu";
import { MessageBody } from "./Body";
import { Attachments } from "./Attachments";

/** The verbs act on threads; the API acts on messages. The newest message
    carries the thread's bucket, so it is the one to address. */
function rowFor(messages: Message[], threadId: string, bucket: ListBucket | null): Row | null {
  const last = messages[messages.length - 1];
  if (!last) return null;
  return {
    thread_id: threadId,
    message_id: last.id,
    subject: last.subject,
    from: last.from,
    received_at: last.received_at,
    read: true,
    preview: "",
    bucket: (last.bucket as Bucket) || (bucket === "snoozed" ? "set_aside" : bucket) || undefined,
  };
}

function ThreadMessage({ message }: { message: Message }) {
  const who = splitFrom(message.from);
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
      </div>
      <div class="thread-msg-body">
        <MessageBody html={message.html} text={message.body} messageId={message.id} />
        <Attachments messageId={message.id} items={message.attachments || []} />
      </div>
    </article>
  );
}

function Bar({ row }: { row: Row }) {
  const [asideOpen, setAsideOpen] = useState(false);
  const messages = reader.value.messages;
  const last = messages[messages.length - 1];

  const reply = () => {
    const who = splitFrom(last.from);
    openCompose({
      to: who.email,
      subject: /^re:/i.test(last.subject) ? last.subject : "Re: " + (last.subject || ""),
      replyToId: last.id,
      context: "Replying to " + (who.name || who.email),
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

        <div style={{ position: "relative" }}>
          <button class="btn btn-ghost" type="button" onClick={() => setAsideOpen((v) => !v)}>
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
  const row = rowFor(messages, state.threadId || "", state.bucket);
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
        {messages.map((m) => <ThreadMessage message={m} key={m.id} />)}
      </div>
      {row && <Bar row={row} />}
    </>
  );
}
