import { useEffect, useState } from "preact/hooks";
import { api } from "../lib/api";
import { useLoad } from "../lib/useLoad";
import { accountFilter, accountQS, cursor, resetSelection, setList } from "../lib/store";
import { markDone, openThread, sendMail } from "../lib/actions";
import type { Briefing, BriefThread, Row, ScreenerSender } from "../lib/types";
import { countOf, daysSince, fmtDate, relativeAge, splitFrom } from "../lib/fmt";
import { Empty, ListSkeleton, LoadError, PageHead, SectionHead } from "../ui/bits";
import { ScreenerCard } from "../ui/ScreenerCard";
import { Icon } from "../ui/Icon";

/** Briefing threads are always unread Inbox mail, so they act like rows. */
function asRow(t: BriefThread): Row {
  return { ...t, read: false, bucket: "imbox" };
}

function InlineReply({ thread, onDone }: { thread: BriefThread; onDone: () => void }) {
  const who = splitFrom(thread.from);
  const [text, setText] = useState("");
  const [sending, setSending] = useState(false);

  const send = async () => {
    if (!text.trim() || sending) return;
    setSending(true);
    const ok = await sendMail({
      to: who.email, subject: thread.subject || "", text, replyToId: thread.message_id,
    });
    setSending(false);
    if (ok) onDone();
  };

  return (
    <div class="inline-reply">
      <textarea
        class="inline-ta" rows={3} autofocus
        placeholder={"Reply to " + (who.name || who.email) + "…"}
        value={text}
        onInput={(ev) => setText((ev.target as HTMLTextAreaElement).value)}
        onKeyDown={(ev) => {
          if ((ev.metaKey || ev.ctrlKey) && ev.key === "Enter") { ev.preventDefault(); send(); }
          if (ev.key === "Escape") onDone();
        }}
      />
      <div class="inline-btns">
        <span class="inline-hint"><span class="kbd">⌘↵</span> to send</span>
        <button class="btn btn-ghost btn-sm" type="button" onClick={onDone}>Cancel</button>
        <button class="btn btn-accent btn-sm" type="button" disabled={!text.trim() || sending} onClick={send}>
          {sending ? "Sending…" : "Send"}
        </button>
      </div>
    </div>
  );
}

function NeedsRow({ thread, index }: { thread: BriefThread; index: number }) {
  const who = splitFrom(thread.from);
  const [replying, setReplying] = useState(false);

  return (
    <div
      class={"digest" + (cursor.value === index ? " cursor" : "")}
      data-cursor-index={index}
      onClick={() => { cursor.value = index; }}
    >
      <div class="digest-top">
        <span class="digest-sender">{who.name || who.email}</span>
        <span class="digest-date">{fmtDate(thread.received_at)}</span>
      </div>
      <div class="digest-subject">{thread.subject || "(no subject)"}</div>
      {thread.preview && <div class="digest-preview">{thread.preview}</div>}

      {replying ? (
        <InlineReply thread={thread} onDone={() => setReplying(false)} />
      ) : (
        <div class="digest-acts">
          <button class="btn btn-outline btn-sm" type="button" onClick={() => setReplying(true)}>
            <Icon name="reply" size={13} /> Reply
          </button>
          <button class="btn btn-ghost btn-sm" type="button" onClick={() => openThread(thread.thread_id, thread.account, "imbox")}>
            Read it
          </button>
          <button class="btn btn-ghost btn-sm" type="button" onClick={() => markDone([asRow(thread)])}>
            <Icon name="check" size={13} /> Done
          </button>
        </div>
      )}
    </div>
  );
}

function WaitingRow({ thread }: { thread: BriefThread }) {
  const who = splitFrom(thread.from);
  const age = daysSince(thread.received_at);
  return (
    <div class="wait-row" role="button" tabIndex={0} onClick={() => openThread(thread.thread_id, thread.account, "imbox")} onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); openThread(thread.thread_id, thread.account, "imbox"); } }}>
      <span class="wait-who">{who.name || who.email}</span>
      <span class="wait-what">{thread.subject || "(no subject)"}</span>
      {/* Ageing is the point of this section: a three-day silence is the nudge. */}
      <span class={"wait-age" + (age >= 3 ? " stale" : "")}>{relativeAge(thread.received_at)}</span>
    </div>
  );
}

export function TodayView() {
  const lens = accountFilter.value;
  const { data, loading, error, reload } = useLoad<{ brief: Briefing; senders: ScreenerSender[]; sendersError: string | null }>("today:" + lens, async (signal) => {
    const brief = await api<Briefing>(accountQS("/briefing"), { signal });
    try {
      return { brief, senders: await api<ScreenerSender[]>(accountQS("/screener"), { signal }), sendersError: null };
    } catch (e) {
      return { brief, senders: [], sendersError: e instanceof Error ? e.message : "New senders did not load." };
    }
  });

  const brief = data?.brief;
  const senders = data?.senders || [];
  const needs = brief?.needs_you || [];
  const waiting = brief?.waiting_on || [];

  useEffect(() => { resetSelection(); }, []);

  // j/k and the row verbs drive the "needs you" list — the actual daily loop.
  useEffect(() => {
    setList({ kind: "rows", key: "today", loading, error, rows: needs.map(asRow), senders: [], origin: "imbox" });
  }, [data, loading, error]);

  const today = new Date().toLocaleDateString([], { weekday: "long", month: "long", day: "numeric" });

  const parts: string[] = [];
  if (needs.length) parts.push(countOf(needs.length, "thing") + " " + (needs.length === 1 ? "needs" : "need") + " you");
  if (senders.length) parts.push(countOf(senders.length, "sender") + " to screen");
  const sub = parts.length ? parts.join(", ") + "." : "Everything's handled. This is the whole picture.";

  const nothingAtAll =
    !loading && !error && !data?.sendersError && !needs.length && !senders.length && !waiting.length &&
    !brief?.feed_unread && !brief?.paper_unread;

  return (
    <>
      <PageHead kicker="Today" title={today} sub={sub} />

      {/* The day surfaces live here, not in the topline — Today is their
          front door. Quiet text links, same register as the rows below. */}
      <div class="day-row">
        <a class="day-link" href="/board">Board</a>
        <a class="day-link" href="/calendar">Calendar</a>
        <a class="day-link" href="/notes">Notes</a>
      </div>

      {loading && !data && <ListSkeleton rows={3} />}
      {error && <LoadError title="The briefing didn't load." error={error} retry={reload} />}
      {data?.sendersError && <div class="empty" role="alert"><div class="empty-sub">New senders are unavailable: {data.sendersError}</div><button class="btn btn-outline btn-sm" type="button" onClick={reload}>Try again</button></div>}

      {needs.length > 0 && (
        <>
          <SectionHead title="Needs you" count={needs.length} />
          {needs.map((t, i) => <NeedsRow thread={t} index={i} key={t.thread_id} />)}
        </>
      )}

      {senders.length > 0 && (
        <>
          <SectionHead title="New senders" count={senders.length} />
          <p class="explainer">
            These people have never written to you before, so nothing of theirs has reached your
            Inbox. Pick where their mail should go and every message they ever send follows that
            rule — you can change it later from People.
          </p>
          {/* Same card as /screener. The keys are not bound here, so they are not advertised. */}
          {senders.slice(0, 4).map((s, i) => (
            <ScreenerCard sender={s} index={-1 - i} key={s.sender} />
          ))}
          {senders.length > 4 && (
            <a class="more-link" href="/screener">Screen all {senders.length} senders →</a>
          )}
        </>
      )}

      {waiting.length > 0 && (
        <>
          <SectionHead title="You're waiting" count={waiting.length} />
          {waiting.map((t) => <WaitingRow thread={t} key={t.thread_id} />)}
        </>
      )}

      {(brief?.feed_unread || brief?.paper_unread) ? (
        <>
          <SectionHead title="Quiet" />
          {!!brief.feed_unread && (
            <a class="quiet-row" href="/reading">
              <span class="quiet-name">Reading</span>
              <span class="quiet-stat">{brief.feed_unread} new</span>
              <span class="quiet-note">skim it when you feel like it</span>
              <span class="quiet-go"><Icon name="arrow" size={14} /></span>
            </a>
          )}
          {!!brief.paper_unread && (
            <a class="quiet-row" href="/receipts">
              <span class="quiet-name">Receipts</span>
              <span class="quiet-stat">{brief.paper_unread} new</span>
              <span class="quiet-note">filed, and searchable when you need one</span>
              <span class="quiet-go"><Icon name="arrow" size={14} /></span>
            </a>
          )}
        </>
      ) : null}

      {nothingAtAll && (
        <Empty title="You're done." sub="Nothing needs you, nobody's waiting, every sender is screened." />
      )}
    </>
  );
}
