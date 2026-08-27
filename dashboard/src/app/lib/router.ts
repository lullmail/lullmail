// Client-side routing inside one island.
//
// The static build still emits a real HTML file per route, so a cold load or a
// pasted URL works exactly as before. This layer only stops *in-app* navigation
// from tearing the document down — which is what used to destroy the open
// reader, the list scroll position and the keyboard selection on every click.
import { signal } from "@preact/signals";
import type { ListBucket } from "./types";
import { query } from "./store";

export type PageKind = "today" | "board" | "notes" | "calendar" | "bucket" | "screener" | "people" | "accounts" | "security" | "appearance" | "search";

export interface Route {
  kind: PageKind;
  bucket?: ListBucket;
  /** Sidebar highlight key, matching the bucket names the API already uses. */
  nav: string;
  title: string;
}

const ROUTES: Record<string, Route> = {
  "/": { kind: "bucket", bucket: "imbox", nav: "imbox", title: "Inbox" },
  "/today": { kind: "today", nav: "today", title: "Today" },
  "/board": { kind: "board", nav: "board", title: "Board" },
  "/notes": { kind: "notes", nav: "notes", title: "Notes" },
  "/calendar": { kind: "calendar", nav: "calendar", title: "Calendar" },
  "/screener": { kind: "screener", nav: "screener", title: "The Screener" },
  "/reading": { kind: "bucket", bucket: "feed", nav: "feed", title: "Reading" },
  "/receipts": { kind: "bucket", bucket: "paper_trail", nav: "paper_trail", title: "Receipts" },
  // One destination, two storage values: a dated snooze and a someday one.
  "/snoozed": { kind: "bucket", bucket: "snoozed", nav: "snoozed", title: "Snoozed" },
  "/people": { kind: "people", nav: "people", title: "People" },
  "/settings/accounts": { kind: "accounts", nav: "accounts", title: "Accounts" },
  "/settings/security": { kind: "security", nav: "security", title: "Security" },
  "/settings/appearance": { kind: "appearance", nav: "appearance", title: "Appearance" },
};

function normalise(p: string): string {
  if (p.length > 1 && p.endsWith("/")) p = p.slice(0, -1);
  return p || "/";
}

export function routeFor(pathname: string): Route {
  return ROUTES[normalise(pathname)] || ROUTES["/"];
}

// Empty means "not resolved yet". Preact's hydration pass deliberately does not
// patch attributes, so if the prerendered HTML committed to a route the sidebar
// would keep the build-time highlight forever. startRouter fills this in.
export const path = signal<string>("");

// Navigation leaves search: the palette's query is a takeover, and any route
// change — link, g-key, back — is the user saying "show me this instead".
export function navigate(to: string) {
	const next = normalise(to);
	query.value = "";
	if (next === path.value) return;
	window.history.pushState({}, "", next);
	path.value = next;
}

/** Intercepts same-origin left-clicks so anchors stay real links but stop reloading. */
export function startRouter() {
  if (typeof window === "undefined") return;
  path.value = normalise(window.location.pathname);
  window.addEventListener("popstate", () => {
    query.value = "";
    path.value = normalise(window.location.pathname);
  });
  document.addEventListener("click", (ev) => {
    if (ev.defaultPrevented || ev.button !== 0) return;
    if (ev.metaKey || ev.ctrlKey || ev.shiftKey || ev.altKey) return;
    const a = (ev.target as HTMLElement | null)?.closest?.("a");
    if (!a) return;
    const href = a.getAttribute("href");
    if (!href || !href.startsWith("/")) return;
    if (a.hasAttribute("download") || a.getAttribute("target") === "_blank") return;
    if (!ROUTES[normalise(href)]) return; // attachments and other real files
    ev.preventDefault();
    navigate(href);
  });
}
