// Browse and jump. Deliberately not a second mail-search surface: typing a
// query here offers to run it in the list column, so results always land in
// the same place instead of two rival result views.
import { useEffect, useMemo, useState } from "preact/hooks";
import { api } from "../lib/api";
import { layout, palette, query, theme, toggleLayout, toggleTheme } from "../lib/store";
import { openThread } from "../lib/actions";
import { navigate } from "../lib/router";
import { fmtDate, splitFrom } from "../lib/fmt";
import type { Mailbox, Row } from "../lib/types";
import { Icon } from "./Icon";

interface Item {
  key: string;
  label: string;
  sub?: string;
  note?: string;
  run: () => void;
}

const JUMPS: [string, string][] = [
  ["Today", "/today"],
  ["Board", "/board"],
  ["Calendar", "/calendar"],
  ["Notes", "/notes"],
  ["Inbox", "/"],
  ["Screener", "/screener"],
  ["Reading", "/reading"],
  // Not in the nav — this is how you actually reach receipts.
  ["Receipts", "/receipts"],
  ["Snoozed", "/snoozed"],
  ["People", "/people"],
  ["Accounts", "/settings/accounts"],
  ["Security", "/settings/security"],
];

function titleCase(s: string): string {
  return s.replace(/\b\w/g, (c) => c.toUpperCase());
}

export function Palette() {
  const [q, setQ] = useState("");
  const [folder, setFolder] = useState<string | null>(null);
  const [mailboxes, setMailboxes] = useState<Mailbox[]>([]);
  const [recent, setRecent] = useState<Row[]>([]);
  const [folderRows, setFolderRows] = useState<Row[] | null>(null);
  const [cursor, setCursor] = useState(0);

  const close = () => { palette.value = false; };

  useEffect(() => {
    api<Mailbox[]>("/mailboxes").then(setMailboxes).catch(() => {});
    api<Row[]>("/recent").then(setRecent).catch(() => {});
  }, []);

  useEffect(() => {
    if (!folder) { setFolderRows(null); return; }
    setFolderRows(null);
    api<Row[]>("/folder?name=" + encodeURIComponent(folder))
      .then(setFolderRows)
      .catch(() => setFolderRows([]));
  }, [folder]);

  const openRow = (row: Row) => { close(); openThread(row.thread_id, null); };

  const sections = useMemo(() => {
    const needle = q.trim().toLowerCase();
    const out: { title: string; items: Item[] }[] = [];

    if (folder) {
      const rows = (folderRows || []).filter(
        (r) => !needle || (r.subject + " " + r.from).toLowerCase().includes(needle)
      );
      out.push({
        title: titleCase(folder),
        items: rows.map((r) => ({
          key: r.message_id,
          label: r.subject || "(no subject)",
          sub: splitFrom(r.from).name || splitFrom(r.from).email,
          note: fmtDate(r.received_at),
          run: () => openRow(r),
        })),
      });
      return out;
    }

    const jumps = JUMPS.filter(([label]) => !needle || label.toLowerCase().includes(needle));
    if (jumps.length) {
      out.push({
        title: "Go to",
        items: jumps.map(([label, href]) => ({
          key: href,
          label,
          run: () => { close(); navigate(href); },
        })),
      });
    }

    const folders = mailboxes.filter((m) => !needle || m.name.includes(needle));
    if (folders.length) {
      out.push({
        title: "Provider folders",
        items: folders.map((m) => ({
          key: "mb:" + m.name,
          label: titleCase(m.name),
          note: "browse",
          run: () => { setFolder(m.name); setQ(""); setCursor(0); },
        })),
      });
    }

    const threads = recent.filter(
      (r) => !needle || (r.subject + " " + r.from).toLowerCase().includes(needle)
    );
    if (threads.length) {
      out.push({
        title: needle ? "Matching threads" : "Recent",
        items: threads.slice(0, 8).map((r) => ({
          key: r.message_id,
          label: r.subject || "(no subject)",
          sub: splitFrom(r.from).name || splitFrom(r.from).email,
          note: fmtDate(r.received_at),
          run: () => openRow(r),
        })),
      });
    }

    // Settings that used to hold permanent chrome. Rare actions belong to the
    // command surface, not the topline.
    const settings: [Item, string][] = [
      [{
        key: "theme",
        label: theme.value === "dark"
          ? "Switch to sepia theme"
          : theme.value === "sepia"
            ? "Switch to light theme"
            : "Switch to dark theme",
        run: () => { close(); toggleTheme(); },
      }, "theme appearance sepia dark light mode"],
      [{
        key: "layout",
        label: layout.value === "classic"
          ? "Switch to the single-column layout"
          : "Switch to the classic three-column layout",
        note: layout.value === "classic" ? "classic" : "document",
        run: () => { close(); toggleLayout(); },
      }, "layout classic columns document view pane reader"],
    ];
    const matchedSettings = settings
      .filter(([item, alias]) => !needle || item.label.toLowerCase().includes(needle) || alias.includes(needle))
      .map(([item]) => item);
    if (matchedSettings.length) out.push({ title: "Settings", items: matchedSettings });

    if (needle) {
      out.push({
        title: "Search",
        items: [{
          key: "search",
          label: "Search all mail for “" + q.trim() + "”",
          run: () => { close(); query.value = q.trim(); },
        }],
      });
    }
    return out;
  }, [q, folder, folderRows, mailboxes, recent, theme.value, layout.value]);

  const flat = sections.flatMap((s) => s.items);
  useEffect(() => { setCursor(0); }, [q, folder]);

  const onKey = (ev: KeyboardEvent) => {
    if (ev.key === "ArrowDown") { ev.preventDefault(); setCursor((c) => Math.min(flat.length - 1, c + 1)); }
    else if (ev.key === "ArrowUp") { ev.preventDefault(); setCursor((c) => Math.max(0, c - 1)); }
    else if (ev.key === "Enter") { ev.preventDefault(); flat[cursor]?.run(); }
    else if (ev.key === "Backspace" && !q && folder) { ev.preventDefault(); setFolder(null); }
  };

  let index = -1;

  return (
    <div class="veil veil-top" onClick={(ev) => { if (ev.target === ev.currentTarget) close(); }}>
      <div class="panel" role="dialog" aria-modal="true" aria-label="Browse">
        <div class="palette-input-row">
          <Icon name="search" size={16} />
          <input
            class="palette-input" autofocus placeholder={folder ? "Filter " + folder + "…" : "Search the mail, or jump anywhere…"}
            value={q} onInput={(e) => setQ((e.target as HTMLInputElement).value)} onKeyDown={onKey}
          />
          {folder && (
            <button class="btn btn-ghost btn-sm" type="button" onClick={() => { setFolder(null); setQ(""); }}>
              Back
            </button>
          )}
        </div>

        <div class="palette-body">
          {folder && folderRows === null && <div class="palette-empty">Loading {folder}…</div>}
          {!flat.length && !(folder && folderRows === null) && (
            <div class="palette-empty">Nothing here.</div>
          )}
          {sections.map((section) => (
            <div key={section.title}>
              <div class="palette-section">{section.title}</div>
              {section.items.map((item) => {
                index++;
                const at = index;
                return (
                  <div
                    class={"palette-row" + (at === cursor ? " cursor" : "")}
                    key={item.key}
                    onMouseEnter={() => setCursor(at)}
                    onClick={item.run}
                  >
                    <div class="palette-row-main">
                      <span class="palette-row-label">{item.label}</span>
                      {item.sub && <span class="palette-row-sub">{item.sub}</span>}
                    </div>
                    {item.note && <span class="palette-row-note">{item.note}</span>}
                  </div>
                );
              })}
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}
