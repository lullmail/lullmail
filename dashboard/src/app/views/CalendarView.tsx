import { useEffect, useMemo, useState } from "preact/hooks";
import { api } from "../lib/api";
import { useLoad } from "../lib/useLoad";
import { resetSelection } from "../lib/store";
import { openThread } from "../lib/actions";
import type { Row } from "../lib/types";
import { Empty, ListSkeleton, PageHead } from "../ui/bits";
import { Icon } from "../ui/Icon";
import { splitFrom } from "../lib/fmt";

const WEEKDAYS = ["S", "M", "T", "W", "T", "F", "S"];
const MONTHS = [
  "January", "February", "March", "April", "May", "June",
  "July", "August", "September", "October", "November", "December",
];

function sameDay(a: Date, b: Date): boolean {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
}

/** Calendar v0: the month grid over the one temporal truth the system
    already has — dated snoozes returning to the Inbox. Invites and detected
    dates land on this same surface next. */
export function CalendarView() {
  const today = new Date();
  const [ym, setYm] = useState({ y: today.getFullYear(), m: today.getMonth() });
  const [selected, setSelected] = useState<Date>(today);

  const { data, loading, error } = useLoad<Row[]>("cal:snoozed", (signal) =>
    api<Row[]>("/buckets/snoozed", { signal }).catch(() => [] as Row[])
  );

  useEffect(() => { resetSelection(); }, []);

  // ← → walks months, t comes back to today.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.target as HTMLElement)?.closest?.("input, textarea")) return;
      if (e.key === "ArrowLeft") { e.preventDefault(); shift(-1); }
      else if (e.key === "ArrowRight") { e.preventDefault(); shift(1); }
      else if (e.key === "t") { e.preventDefault(); setYm({ y: today.getFullYear(), m: today.getMonth() }); setSelected(today); }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [ym]);

  function shift(delta: number) {
    setYm(({ y, m }) => {
      const d = new Date(y, m + delta, 1);
      return { y: d.getFullYear(), m: d.getMonth() };
    });
  }

  const dated = useMemo(() => {
    return (data || []).filter((r) => r.snooze_until && !isNaN(+new Date(r.snooze_until)));
  }, [data]);
  const someday = useMemo(() => (data || []).filter((r) => !r.snooze_until), [data]);

  const cells = useMemo(() => {
    const first = new Date(ym.y, ym.m, 1);
    const lead = first.getDay(); // Sunday start
    const days = new Date(ym.y, ym.m + 1, 0).getDate();
    const out: { date: Date | null; inMonth: boolean }[] = [];
    for (let i = 0; i < lead; i++) out.push({ date: null, inMonth: false });
    for (let d = 1; d <= days; d++) out.push({ date: new Date(ym.y, ym.m, d), inMonth: true });
    while (out.length % 7 !== 0) out.push({ date: null, inMonth: false });
    return out;
  }, [ym]);

  const returnsOn = (d: Date) =>
    dated.filter((r) => sameDay(new Date(r.snooze_until!), d));

  const selectedReturns = returnsOn(selected);
  const isToday = sameDay(selected, today);

  return (
    <>
      <PageHead
        kicker="Calendar"
        title={MONTHS[ym.m] + " " + ym.y}
        sub="The month, and the mail that comes back to you — dated snoozes land on their day."
      />

      <div class="cal-topline">
        <button class="btn-icon" type="button" title="Previous month (←)" aria-label="Previous month"
          onClick={() => shift(-1)}>
          <Icon name="back" size={16} />
        </button>
        <button class="btn btn-ghost btn-sm" type="button"
          onClick={() => { setYm({ y: today.getFullYear(), m: today.getMonth() }); setSelected(today); }}>
          Today <span class="kbd">t</span>
        </button>
        <button class="btn-icon" type="button" title="Next month (→)" aria-label="Next month"
          onClick={() => shift(1)}>
          <Icon name="arrow" size={16} />
        </button>
      </div>

      {loading && !data && <ListSkeleton rows={4} />}
      {error && <Empty title="The month didn't load." sub={error} />}

      {data && (
        <>
          <div class="cal-weekdays">
            {WEEKDAYS.map((w, i) => <span key={i}>{w}</span>)}
          </div>
          <div class="cal-grid">
            {cells.map((c, i) => {
              if (!c.date) return <div class="cal-cell blank" key={i} />;
              const n = returnsOn(c.date).length;
              const isSel = sameDay(c.date, selected);
              const cls =
                "cal-cell" +
                (sameDay(c.date, today) ? " today" : "") +
                (isSel ? " sel" : "");
              return (
                <button class={cls} type="button" key={i} onClick={() => setSelected(c.date!)}>
                  <span class="cal-daynum">{c.date.getDate()}</span>
                  {n > 0 && <span class="cal-dots">{n === 1 ? "1 return" : n + " returns"}</span>}
                </button>
              );
            })}
          </div>

          <div class="cal-day">
            <div class="cal-day-head">
              <span class="cal-day-title">
                {selected.toLocaleDateString([], { weekday: "long", month: "long", day: "numeric" })}
                {isToday && <span class="cal-day-now"> — today</span>}
              </span>
            </div>
            {selectedReturns.length === 0 && (
              <div class="cal-day-empty">Nothing returns. A quiet day.</div>
            )}
            {selectedReturns.map((r) => {
              const who = splitFrom(r.from);
              return (
                <button class="cal-return" type="button" key={r.thread_id}
                  onClick={() => openThread(r.thread_id, "snoozed")}>
                  <span class="cal-return-when">
                    {new Date(r.snooze_until!).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })}
                  </span>
                  <span class="cal-return-what">{r.subject || "(no subject)"}</span>
                  <span class="cal-return-who">{who.name || who.email}</span>
                </button>
              );
            })}
          </div>

          {someday.length > 0 && (
            <a class="quiet-row" href="/snoozed">
              <span class="quiet-name">{someday.length} for someday</span>
              <span class="quiet-note">snoozed without a date — they wait until you look</span>
              <span class="quiet-go"><Icon name="arrow" size={14} /></span>
            </a>
          )}
        </>
      )}
    </>
  );
}
