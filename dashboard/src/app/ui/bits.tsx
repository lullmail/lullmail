// Small shared pieces. Everything here is presentational — no fetching, no store.
import type { ComponentChildren, JSX } from "preact";
import { hueFor } from "../lib/fmt";

export function Avatar({ email, name, size }: { email: string; name?: string; size?: "sm" | "lg" }) {
  const cls = "avatar" + (size ? " avatar-" + size : "");
  const initial = (name || email || "?").trim().charAt(0).toUpperCase() || "?";
  return (
    <div class={cls} style={{ background: "hsl(" + hueFor(email || "?") + ", 58%, 46%)" }} aria-hidden="true">
      {initial}
    </div>
  );
}

export function Kbd({ children }: { children: ComponentChildren }) {
  return <span class="kbd">{children}</span>;
}

export function PageHead({ kicker, title, sub }: { kicker?: string; title: string; sub?: string }) {
  return (
    <div class="page-head">
      {kicker && <div class="page-kicker">{kicker}</div>}
      <h1 class="page-title">{title}</h1>
      {sub && <div class="page-sub">{sub}</div>}
    </div>
  );
}

export function SectionHead({ title, count }: { title: string; count?: number }) {
  return (
    <div class="section-head">
      <span class="section-title">{title}</span>
      {count !== undefined && <span class="section-count">{count}</span>}
      <span class="section-rule" />
    </div>
  );
}

export function Empty({ title, sub }: { title: string; sub?: string }) {
  return (
    <div class="empty">
      <div class="empty-big">{title}</div>
      {sub && <div class="empty-sub">{sub}</div>}
    </div>
  );
}

/** Keeps the list's shape while it loads, instead of collapsing to blank. */
export function ListSkeleton({ rows = 6 }: { rows?: number }) {
  return (
    <div class="msg-list" aria-busy="true" aria-label="Loading">
      {Array.from({ length: rows }, (_, i) => (
        <div class="skel-row" key={i}>
          <div class="skel" style={{ height: 10, width: 110 }} />
          <div class="skel" style={{ height: 17, width: 55 + ((i * 37) % 40) + "%" }} />
          <div class="skel" style={{ height: 10, width: 40 + ((i * 23) % 35) + "%" }} />
        </div>
      ))}
    </div>
  );
}

/** A stable page silhouette while a secondary route's code arrives. */
export function RouteSkeleton() {
  return (
    <div class="route-skeleton" aria-busy="true" aria-label="Loading page">
      <div class="skel" style={{ height: 10, width: 72 }} />
      <div class="skel" style={{ height: 34, width: "42%" }} />
      <div class="skel" style={{ height: 13, width: "64%" }} />
      <div class="route-skeleton-body"><ListSkeleton rows={4} /></div>
    </div>
  );
}

/** Highlights the first case-insensitive occurrence of `q` inside `text`. */
export function Highlight({ text, q }: { text: string; q?: string }): JSX.Element {
  if (!q) return <>{text}</>;
  const idx = text.toLowerCase().indexOf(q.toLowerCase());
  if (idx < 0) return <>{text}</>;
  return (
    <>
      {text.slice(0, idx)}
      <mark class="hit">{text.slice(idx, idx + q.length)}</mark>
      {text.slice(idx + q.length)}
    </>
  );
}
