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

type Zoom = "year" | "month" | "week";

function sameDay(a: Date, b: Date): boolean {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
}

/** Arrows move by whatever the zoom is showing. Month shifts clamp so
    Jan 31 -> Feb 28 instead of spilling into March. */
function shiftDate(d: Date, zoom: Zoom, delta: number): Date {
  if (zoom === "year") return new Date(d.getFullYear() + delta, d.getMonth(), 1);
  if (zoom === "week") {
    const w = new Date(d);
    w.setDate(w.getDate() + 7 * delta);
    return w;
  }
  const target = new Date(d.getFullYear(), d.getMonth() + delta, 1);
  const lastDay = new Date(target.getFullYear(), target.getMonth() + 1, 0).getDate();
  return new Date(target.getFullYear(), target.getMonth(), Math.min(d.getDate(), lastDay));
}

/** Leading blanks + the month's days, Sunday start. */
function monthCells(y: number, m: number): (Date | null)[] {
  const lead = new Date(y, m, 1).getDay();
  const days = new Date(y, m + 1, 0).getDate();
  const out: (Date | null)[] = [];
  for (let i = 0; i < lead; i++) out.push(null);
  for (let d = 1; d <= days; d++) out.push(new Date(y, m, d));
  return out;
}

/** Calendar v0.5: one temporal truth (dated snoozes returning to the Inbox)
    at three zooms — dots on the year, chips on the month, the items
    themselves on the week. Invites and detected dates land here next. */
export function CalendarView() {
  const today = new Date();
  const [zoom, setZoom] = useState<Zoom>("month");
  const [anchor, setAnchor] = useState<Date>(today);

  const { data, loading, error } = useLoad<Row[]>("cal:snoozed", (signal) =>
    api<Row[]>("/buckets/snoozed", { signal }).catch(() => [] as Row[])
  );

  useEffect(() => { resetSelection(); }, []);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.target as HTMLElement)?.closest?.("input, textarea")) return;
      const k = e.key.toLowerCase();
      if (e.key === "ArrowLeft") { e.preventDefault(); setAnchor((a) => shiftDate(a, zoom, -1)); }
      else if (e.key === "ArrowRight") { e.preventDefault(); setAnchor((a) => shiftDate(a, zoom, 1)); }
      else if (k === "t") { e.preventDefault(); setAnchor(new Date()); }
      else if (k === "y") { e.preventDefault(); setZoom("year"); }
      else if (k === "m") { e.preventDefault(); setZoom("month"); }
      else if (k === "w") { e.preventDefault(); setZoom("week"); }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [zoom]);

  const dated = useMemo(
    () => (data || []).filter((r) => r.snooze_until && !isNaN(+new Date(r.snooze_until))),
    [data]
  );
  const someday = useMemo(() => (data || []).filter((r) => !r.snooze_until), [data]);

  const returnsOn = (d: Date) => dated.filter((r) => sameDay(new Date(r.snooze_until!), d));

  const y = anchor.getFullYear();
  const m = anchor.getMonth();

  const weekStart = useMemo(() => {
    const w = new Date(anchor);
    w.setDate(anchor.getDate() - anchor.getDay());
    return w;
  }, [anchor.getFullYear(), anchor.getMonth(), anchor.getDate()]);
  const weekDays = useMemo(
    () => Array.from({ length: 7 }, (_, i) => {
      const d = new Date(weekStart);
      d.setDate(weekStart.getDate() + i);
      return d;
    }),
    [weekStart.getFullYear(), weekStart.getMonth(), weekStart.getDate()]
  );

  const title =
    zoom === "year" ? String(y) :
    zoom === "month" ? MONTHS[m] + " " + y :
    weekStart.getMonth() === new Date(weekStart.getFullYear(), weekStart.getMonth(), weekStart.getDate() + 6).getMonth()
      ? weekStart.toLocaleDateString([], { month: "short", day: "numeric" }) + " – " +
        new Date(weekStart.getFullYear(), weekStart.getMonth(), weekStart.getDate() + 6).getDate()
      : weekStart.toLocaleDateString([], { month: "short", day: "numeric" }) + " – " +
        new Date(weekStart.getFullYear(), weekStart.getMonth(), weekStart.getDate() + 6)
          .toLocaleDateString([], { month: "short", day: "numeric" });

  const sub =
    zoom === "year" ? "The year at a glance — a dot is mail coming back to you." :
    zoom === "month" ? "The month, and the mail that comes back to you — dated snoozes land on their day." :
    "The week, day by day — every return opens its thread.";

  const yearDots = zoom === "year" ? dated.filter((r) => new Date(r.snooze_until!).getFullYear() === y).length : 0;

  return (
    <>
      <PageHead kicker="Calendar" title={title} sub={sub} />

      <div class="cal-topline">
        <button class="btn-icon" type="button" title={"Previous " + zoom + " (←)" } aria-label={"Previous " + zoom}
          onClick={() => setAnchor((a) => shiftDate(a, zoom, -1))}>
          <Icon name="back" size={16} />
        </button>
        <div class="cal-zoom" role="group" aria-label="Zoom">
          {(["year", "month", "week"] as Zoom[]).map((z) => (
            <button
              class={"cal-zoom-btn" + (zoom === z ? " active" : "")} type="button" key={z}
              onClick={() => setZoom(z)}
            >
              {z === "year" ? "Year" : z === "month" ? "Month" : "Week"}
            </button>
          ))}
        </div>
        <span class="spacer" />
        <button class="btn btn-ghost btn-sm" type="button" onClick={() => { setAnchor(new Date()); }}>
          Today <span class="kbd">t</span>
        </button>
        <button class="btn-icon" type="button" title={"Next " + zoom + " (→)"} aria-label={"Next " + zoom}
          onClick={() => setAnchor((a) => shiftDate(a, zoom, 1))}>
          <Icon name="arrow" size={16} />
        </button>
      </div>

      {loading && !data && <ListSkeleton rows={4} />}
      {error && <Empty title="The calendar didn't load." sub={error} />}

      {data && zoom === "year" && (
        <>
          <div class="cal-year">
            {Array.from({ length: 12 }, (_, mm) => {
              const cells = monthCells(y, mm);
              return (
                <div class="cal-mini" key={mm}>
                  <button class="cal-mini-name" type="button"
                    onClick={() => { setAnchor(new Date(y, mm, 1)); setZoom("month"); }}>
                    {MONTHS[mm]}
                  </button>
                  <div class="cal-mini-grid">
                    {cells.map((c, i) =>
                      c ? (
                        <button
                          class={"cal-mini-day" +
                            (sameDay(c, today) ? " today" : "") +
                            (returnsOn(c).length ? " has" : "")}
                          type="button" key={i}
                          title={returnsOn(c).length ? returnsOn(c).length + " returning" : undefined}
                          onClick={() => { setAnchor(c); setZoom("month"); }}
                        >
                          {c.getDate()}
                        </button>
                      ) : (
                        <span key={i} />
                      )
                    )}
                  </div>
                </div>
              );
            })}
          </div>
          {yearDots > 0 && (
            <div class="cal-year-note">{yearDots} dated {yearDots === 1 ? "return" : "returns"} in {y}.</div>
          )}
        </>
      )}

      {data && zoom === "month" && (
        <>
          <div class="cal-weekdays">
            {WEEKDAYS.map((w, i) => <span key={i}>{w}</span>)}
          </div>
          <div class="cal-grid">
            {(() => {
              const cells = monthCells(y, m);
              const lead = cells.length;
              const out: preact.ComponentChild[] = [];
              cells.forEach((c, i) => {
                if (!c) { out.push(<div class="cal-cell blank" key={"b" + i} />); return; }
                const n = returnsOn(c).length;
                out.push(
                  <button
                    class={"cal-cell" + (sameDay(c, today) ? " today" : "") + (sameDay(c, anchor) ? " sel" : "")}
                    type="button" key={c.getTime()}
                    onClick={() => setAnchor(c)}
                  >
                    <span class="cal-daynum">{c.getDate()}</span>
                    {n > 0 && <span class="cal-dots">{n === 1 ? "1 return" : n + " returns"}</span>}
                  </button>
                );
              });
              while (out.length % 7 !== 0) out.push(<div class="cal-cell blank" key={"p" + (out.length - lead)} />);
              return out;
            })()}
          </div>

          <div class="cal-day">
            <div class="cal-day-head">
              <span class="cal-day-title">
                {anchor.toLocaleDateString([], { weekday: "long", month: "long", day: "numeric" })}
                {sameDay(anchor, today) && <span class="cal-day-now"> — today</span>}
              </span>
            </div>
            {returnsOn(anchor).length === 0 && (
              <div class="cal-day-empty">Nothing returns. A quiet day.</div>
            )}
            {returnsOn(anchor).map((r) => {
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
        </>
      )}

      {data && zoom === "week" && (
        <div class="cal-week">
          {weekDays.map((d) => {
            const items = returnsOn(d);
            const isToday = sameDay(d, today);
            return (
              <div class={"cal-wday" + (isToday ? " today" : "")} key={d.getTime()}>
                <div class="cal-wday-head">
                  <span class="cal-wday-name">{d.toLocaleDateString([], { weekday: "long" })}</span>
                  <span class="cal-wday-num">{d.getDate()}</span>
                </div>
                {items.length === 0 && <div class="cal-wday-quiet">—</div>}
                {items.map((r) => {
                  const who = splitFrom(r.from);
                  return (
                    <button class="cal-witem" type="button" key={r.thread_id}
                      onClick={() => openThread(r.thread_id, "snoozed")}>
                      <span class="cal-witem-when">
                        {new Date(r.snooze_until!).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })}
                      </span>
                      <span class="cal-witem-what">{r.subject || "(no subject)"}</span>
                      <span class="cal-witem-who">{who.name || who.email}</span>
                    </button>
                  );
                })}
              </div>
            );
          })}
        </div>
      )}

      {data && someday.length > 0 && (
        <a class="quiet-row" href="/snoozed">
          <span class="quiet-name">{someday.length} for someday</span>
          <span class="quiet-note">snoozed without a date — they wait until you look</span>
          <span class="quiet-go"><Icon name="arrow" size={14} /></span>
        </a>
      )}
    </>
  );
}
