// One stroke-weight, one grid, no emoji anywhere (see the project's standards).
import type { JSX } from "preact";

const PATHS: Record<string, JSX.Element> = {
  today: <><circle cx="12" cy="12" r="9" /><path d="M12 7v5l3 2" /></>,
  imbox: <><path d="M3 12h5l2 3h4l2-3h5" /><path d="M5 5h14l2 7v7H3v-7z" /></>,
  screener: <><path d="M4 5h16l-6.5 8v6l-3 1.5V13z" /></>,
  feed: <><path d="M4 11a9 9 0 0 1 9 9M4 4a16 16 0 0 1 16 16" /><circle cx="5" cy="19" r="1.4" fill="currentColor" /></>,
  paper: <><path d="M6 2h9l5 5v15H6z" /><path d="M15 2v5h5" /><path d="M9 13h6M9 17h5" /></>,
  aside: <><circle cx="12" cy="12" r="9" /><path d="M12 7v5l3 3" /></>,
  later: <><path d="M3 6h18M3 12h12M3 18h8" /></>,
  people: <><circle cx="9" cy="8" r="3.2" /><path d="M3 20a6 6 0 0 1 12 0" /><path d="M16 5.5a3.2 3.2 0 0 1 0 5M17.5 20a6 6 0 0 0-2-4.4" /></>,
  settings: <><circle cx="12" cy="12" r="3" /><path d="M19.4 15a1.6 1.6 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.6 1.6 0 0 0-2.7 1.1V21a2 2 0 1 1-4 0v-.1A1.6 1.6 0 0 0 7 19.4a1.6 1.6 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1A1.6 1.6 0 0 0 3 15a1.6 1.6 0 0 0-1.5-1H1a2 2 0 1 1 0-4h.1A1.6 1.6 0 0 0 2.6 9a1.6 1.6 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1A1.6 1.6 0 0 0 7 4.6h.1A1.6 1.6 0 0 0 8.6 3V3a2 2 0 1 1 4 0v.1A1.6 1.6 0 0 0 15 4.6a1.6 1.6 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.6 1.6 0 0 0-.3 1.8v.1a1.6 1.6 0 0 0 1.5 1.5H21a2 2 0 1 1 0 4h-.1a1.6 1.6 0 0 0-1.5 1.5z" /></>,
  search: <><circle cx="11" cy="11" r="7" /><path d="m20 20-3.8-3.8" /></>,
  clip: <><path d="M21.4 11 12.3 20a6 6 0 0 1-8.5-8.5l9.2-9.2a4 4 0 0 1 5.7 5.7l-9.2 9.2a2 2 0 0 1-2.8-2.8l8.5-8.5" /></>,
  moon: <><path d="M21 12.8A9 9 0 1 1 11.2 3 7 7 0 0 0 21 12.8z" /></>,
  sun: <><circle cx="12" cy="12" r="4" /><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M6.3 17.7l-1.4 1.4M19.1 4.9l-1.4 1.4" /></>,
  check: <><path d="m4 12 5 5L20 6" /></>,
  reply: <><path d="M9 17 4 12l5-5" /><path d="M4 12h10a6 6 0 0 1 6 6v2" /></>,
  close: <><path d="M6 6l12 12M18 6 6 18" /></>,
  arrow: <><path d="M5 12h14M13 6l6 6-6 6" /></>,
  shield: <><path d="M12 3l8 3v6c0 5-3.4 8.4-8 9-4.6-.6-8-4-8-9V6z" /></>,
  compose: <><path d="M4 20h16" /><path d="M14.5 4.5a2.1 2.1 0 0 1 3 3L8 17l-4 1 1-4z" /></>,
  block: <><circle cx="12" cy="12" r="9" /><path d="m6 6 12 12" /></>,
  chevron: <><path d="m9 6 6 6-6 6" /></>,
  back: <><path d="M19 12H5M11 6l-6 6 6 6" /></>,
  more: <><circle cx="12" cy="5" r="1.4" fill="currentColor" /><circle cx="12" cy="12" r="1.4" fill="currentColor" /><circle cx="12" cy="19" r="1.4" fill="currentColor" /></>,
  keyboard: <><rect x="2" y="6" width="20" height="12" rx="2" /><path d="M6 10h.01M10 10h.01M14 10h.01M18 10h.01M8 14h8" /></>,
  classic: <><rect x="3" y="4" width="18" height="16" rx="2" /><path d="M9 4v16M15 4v16" /></>,
  document: <><rect x="3" y="4" width="18" height="16" rx="2" /><path d="M8 9h8M8 13h8M8 17h5" /></>,
  undo: <><path d="M4 9h11a5 5 0 0 1 0 10h-6" /><path d="m8 5-4 4 4 4" /></>,
  pin: <><path d="M12 17v4" /><path d="M7 4h10l-1.5 6.5 2.5 3v1.5H6V13l2.5-3z" /></>,
  plus: <><path d="M12 5v14M5 12h14" /></>,
};

export type IconName = keyof typeof PATHS;

export function Icon({ name, size = 16 }: { name: IconName; size?: number }) {
  return (
    <svg
      viewBox="0 0 24 24" width={size} height={size} fill="none" stroke="currentColor"
      stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"
    >
      {PATHS[name]}
    </svg>
  );
}
