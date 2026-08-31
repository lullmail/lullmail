import { useEffect, useState } from "preact/hooks";
import { api } from "../lib/api";
import { useLoad } from "../lib/useLoad";
import { accountFilter, accountQS, cursor, resetSelection, setList } from "../lib/store";
import { addCard, markDone, openThread, removeCard, setCardDone } from "../lib/actions";
import type { Board, BoardCard, Row } from "../lib/types";
import { daysSince, fmtDate, relativeAge, splitFrom } from "../lib/fmt";
import { Empty, ListSkeleton, PageHead } from "../ui/bits";
import { Icon } from "../ui/Icon";

/** A card the keyboard can act on is one with a message behind it. Manual
    notes stay button-only; the server keeps them last in needs_you so the
    column's render order and the keyboard list's indices agree. */
function asRow(c: BoardCard): Row {
  return {
    account: c.account || "",
    thread_id: c.thread_id || "",
    message_id: c.message_id || "",
    subject: c.subject,
    from: c.from || "",
    received_at: c.received_at || "",
    read: false,
    preview: c.preview || "",
    bucket: "imbox",
  };
}

function NeedsCard({ card, index }: { card: BoardCard; index: number }) {
  const who = card.from ? splitFrom(card.from) : null;
  const openable = !!card.thread_id && !card.manual;
  return (
    <div
      class={"board-card" + (cursor.value === index ? " cursor" : "")}
      data-cursor-index={openable ? index : undefined}
      onClick={() => { if (openable) cursor.value = index; }}
    >
      <div class="board-card-top">
        <span class="board-card-who">
          {card.manual ? "Note" : who?.name || who?.email || "Pinned"}
        </span>
        <span class="board-card-date">{card.received_at ? fmtDate(card.received_at) : ""}</span>
      </div>
      <div class="board-card-subject">{card.subject || "(no subject)"}</div>
      {(card.note || card.preview) && (
        <div class="board-card-preview">{card.note || card.preview}</div>
      )}
      <div class="board-card-acts">
        {openable && (
          <button class="btn btn-outline btn-sm" type="button" onClick={() => openThread(card.thread_id!, card.account!, null)}>
            Open
          </button>
        )}
        {card.card_id ? (
          <>
            <button class="btn btn-ghost btn-sm" type="button" onClick={() => setCardDone(card, true)}>
              <Icon name="check" size={13} /> Done
            </button>
            <button class="btn btn-ghost btn-sm" type="button" onClick={() => removeCard(card)}>
              {card.manual ? "Delete" : "Unpin"}
            </button>
          </>
        ) : (
          <button class="btn btn-ghost btn-sm" type="button" onClick={() => markDone([asRow(card)])}>
            <Icon name="check" size={13} /> Done
          </button>
        )}
      </div>
    </div>
  );
}

function WaitCard({ card }: { card: BoardCard }) {
  const who = splitFrom(card.from || "");
  const age = daysSince(card.received_at);
  return (
    <div class="board-card wait" onClick={() => card.thread_id && card.account && openThread(card.thread_id, card.account, null)}>
      <div class="board-card-top">
        <span class="board-card-who">{who.name || who.email}</span>
        <span class={"wait-age" + (age >= 3 ? " stale" : "")}>{relativeAge(card.received_at)}</span>
      </div>
      <div class="board-card-subject">{card.subject || "(no subject)"}</div>
    </div>
  );
}

function SnoozeCard({ row }: { row: Row }) {
  const who = splitFrom(row.from);
  return (
    <div class="board-card snoozed" onClick={() => openThread(row.thread_id, row.account, "snoozed")}>
      <div class="board-card-subject">{row.subject || "(no subject)"}</div>
      <div class="board-card-top">
        <span class="board-card-who">{who.name || who.email}</span>
        <span class="board-card-date">{row.snooze_until ? "back " + fmtDate(row.snooze_until) : "someday"}</span>
      </div>
    </div>
  );
}

function NoteComposer() {
  const [open, setOpen] = useState(false);
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);

  const save = async () => {
    if (!text.trim() || busy) return;
    setBusy(true);
    const ok = await addCard(text.trim(), "");
    setBusy(false);
    if (ok) { setText(""); setOpen(false); }
  };

  if (!open) {
    return (
      <button class="btn btn-ghost btn-sm" type="button" onClick={() => setOpen(true)}>
        <Icon name="plus" size={13} /> Note
      </button>
    );
  }
  return (
    <div class="board-note-composer">
      <input
        class="board-note-input" autofocus
        placeholder="Call the dentist…"
        value={text}
        onInput={(ev) => setText((ev.target as HTMLInputElement).value)}
        onKeyDown={(ev) => {
          if (ev.key === "Enter") { ev.preventDefault(); save(); }
          if (ev.key === "Escape") setOpen(false);
        }}
      />
      <div class="inline-btns">
        <button class="btn btn-ghost btn-sm" type="button" onClick={() => setOpen(false)}>Cancel</button>
        <button class="btn btn-accent btn-sm" type="button" disabled={!text.trim() || busy} onClick={save}>
          {busy ? "Adding…" : "Add"}
        </button>
      </div>
    </div>
  );
}

function DonePile({ done }: { done: BoardCard[] }) {
  const [open, setOpen] = useState(false);
  if (!done.length) return null;
  return (
    <div class="done-pile">
      <button class="done-pile-head" type="button" onClick={() => setOpen((v) => !v)}>
        <Icon name="check" size={13} /> Done — {done.length}
        <span class="done-pile-note">{open ? "hide" : "show"}</span>
      </button>
      {open && done.map((c) => (
        <div class="done-row" key={c.card_id}>
          <span class="done-what">{c.subject || "(no subject)"}</span>
          <span class="done-acts">
            {!c.manual && c.thread_id ? (
              <button class="btn btn-ghost btn-sm" type="button" onClick={() => c.thread_id && c.account && openThread(c.thread_id, c.account, null)}>Open</button>
            ) : null}
            <button class="btn btn-ghost btn-sm" type="button" onClick={() => setCardDone(c, false)}>Restore</button>
            <button class="btn btn-quiet-danger btn-sm" type="button" onClick={() => removeCard(c)}>Delete</button>
          </span>
        </div>
      ))}
    </div>
  );
}

function Column({ title, count, sub, children }: {
  title: string; count: number; sub?: string; children: preact.ComponentChildren;
}) {
  return (
    <section class="board-col">
      <div class="board-col-head">
        <span class="board-col-title">{title}</span>
        <span class="board-col-count">{count}</span>
      </div>
      {sub && <div class="board-col-sub">{sub}</div>}
      {children}
    </section>
  );
}

export function BoardView() {
  const lens = accountFilter.value;
  const { data, loading, error } = useLoad<[Board, Row[]]>("board:" + lens, async (signal) =>
    Promise.all([
      api<Board>(accountQS("/board"), { signal }),
      api<Row[]>(accountQS("/buckets/snoozed"), { signal }).catch(() => [] as Row[]),
    ])
  );

  const board = data?.[0];
  const snoozed = data?.[1] || [];
  const needs = board?.needs_you || [];
  const waiting = board?.waiting_on || [];
  const done = board?.done || [];

  useEffect(() => { resetSelection(); }, []);

  // Only openable cards join the keyboard list; the server's order (derived,
  // pins, notes) keeps indices aligned with the rendered column.
  useEffect(() => {
    setList({
      kind: "rows", key: "board", loading, error,
      rows: needs.filter((c) => c.thread_id && !c.manual).map(asRow),
      senders: [], origin: null,
    });
  }, [data, loading, error]);

  return (
    <>
      <PageHead
        kicker="Board"
        title="The board your mail writes"
        sub="Cards fill themselves: unread Inbox mail needs you, threads you answered are waiting on the other side, snoozed mail comes back on its day. Pin anything to keep it here."
      />

      {loading && !data && <ListSkeleton rows={4} />}
      {error && <Empty title="The board didn't load." sub={error} />}

      {data && (
        <>
          <div class="board-head-row">
            <NoteComposer />
          </div>
          <div class="board">
            <Column title="Needs you" count={needs.length}>
              {needs.length === 0 && (
                <div class="board-col-empty">Nothing needs you. Reading a card is finishing it.</div>
              )}
              {needs.map((c, i) => <NeedsCard card={c} index={i} key={c.card_id || c.thread_id} />)}
            </Column>

            <Column
              title="You're waiting" count={waiting.length}
              sub={waiting.length ? "each card clears itself when they reply" : undefined}
            >
              {waiting.length === 0 && <div class="board-col-empty">Nobody's waiting on you.</div>}
              {waiting.map((c) => <WaitCard card={c} key={c.thread_id} />)}
            </Column>

            <Column title="Snoozed" count={snoozed.length}>
              {snoozed.length === 0 && (
                <div class="board-col-empty">Nothing snoozed. Dated ones return here on their day.</div>
              )}
              {snoozed.map((r) => <SnoozeCard row={r} key={r.thread_id} />)}
            </Column>
          </div>
          <DonePile done={done} />
        </>
      )}
    </>
  );
}
