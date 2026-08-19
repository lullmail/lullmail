// Formatting and identity helpers. Pure — safe to call during prerender.

export interface Who {
  name: string;
  email: string;
}

/** Splits `Ada Lovelace <ada@example.com>` into its parts; tolerates bare addresses. */
export function splitFrom(raw: string | undefined): Who {
  const m = /^([^<]*)<(.+)>$/.exec(raw || "");
  if (m) return { name: m[1].trim().replace(/^"|"$/g, ""), email: m[2].trim() };
  const s = (raw || "").trim();
  return { name: s, email: s };
}

export function fmtDate(iso: string | null | undefined): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (isNaN(+d)) return "";
  const now = new Date();
  if (d.toDateString() === now.toDateString()) {
    return d.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
  }
  if (d.getFullYear() === now.getFullYear()) {
    return d.toLocaleDateString([], { month: "short", day: "numeric" });
  }
  return d.toLocaleDateString([], { month: "short", day: "numeric", year: "numeric" });
}

export function fmtFull(iso: string | null | undefined): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (isNaN(+d)) return "";
  return d.toLocaleString([], {
    weekday: "short", month: "short", day: "numeric",
    hour: "numeric", minute: "2-digit",
  });
}

/** Whole days elapsed; used for the "you're waiting" ageing emphasis. */
export function daysSince(iso: string | null | undefined): number {
  if (!iso) return 0;
  const d = new Date(iso);
  if (isNaN(+d)) return 0;
  return Math.floor((Date.now() - +d) / 86400000);
}

export function relativeAge(iso: string | null | undefined): string {
  const days = daysSince(iso);
  if (days <= 0) return "today";
  if (days === 1) return "1 day";
  if (days < 7) return days + " days";
  if (days < 14) return "1 week";
  if (days < 60) return Math.floor(days / 7) + " weeks";
  return Math.floor(days / 30) + " months";
}

export function dayLabel(iso: string | null | undefined): string {
  if (!iso) return "Earlier";
  const d = new Date(iso);
  if (isNaN(+d)) return "Earlier";
  const now = new Date();
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const that = new Date(d.getFullYear(), d.getMonth(), d.getDate());
  const days = Math.round((+today - +that) / 86400000);
  if (days <= 0) return "Today";
  if (days === 1) return "Yesterday";
  if (days < 7) return "This week";
  if (days < 31) return "This month";
  return "Earlier";
}

export function fmtBytes(n: number): string {
  if (!n) return "";
  if (n < 1024) return n + " B";
  if (n < 1024 * 1024) return Math.round(n / 1024) + " KB";
  return (n / 1048576).toFixed(1) + " MB";
}

/** Stable per-address colour from a curated hue set — never a muddy random hue. */
const HUES = [4, 24, 42, 88, 152, 172, 194, 218, 246, 276, 310, 340];

export function hueFor(s: string): number {
  let h = 0;
  for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) >>> 0;
  return HUES[h % HUES.length];
}

export function plural(n: number, one: string, many?: string): string {
  return n === 1 ? one : many || one + "s";
}

/** `3 things` — the count and its noun together, since separating them is how the old header lost its number. */
export function countOf(n: number, one: string, many?: string): string {
  return n + " " + plural(n, one, many);
}
