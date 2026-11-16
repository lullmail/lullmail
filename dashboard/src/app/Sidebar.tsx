import { computed, signal } from "@preact/signals";
import { mailboxes } from "./lib/store";
import { place, placeCount, placesIn } from "./lib/places";
import { folderLabel, folderPath, path, routeFor } from "./lib/router";
import { Icon } from "./ui/Icon";

// Classic's nav: one list of places, grouped by what you came to do.
//
// It used to be two systems stacked — every folder every connected mailbox
// reports, then Lull's own places under a second heading. With a Gmail
// account beside thirteen others that was two Drafts, two Sents, two
// Trashes, a Junk and a Spam, and then an "Inbox" (the provider's) above a
// "Focused" (ours) that were nearly the same list. Now:
//
//   Mail     where mail lands — the Inbox and the places it files itself
//   Sent     what you wrote, what a tool sent for you, what is still going out
//   Folders  the provider's housekeeping, one row per KIND of folder
//            (the server folds every spelling of Trash into "Trash"),
//            closed until wanted
//
// The provider's own Inbox and Sent folders are not listed: the Inbox and
// Sent above are those, sorted.
//
// Which places exist, what they are called and when a queue is worth a row
// (the Screener, Snoozed and the Outbox only while they hold something) is
// lib/places — this file only arranges them.
const FOLDER_ORDER = ["drafts", "archive", "junk", "trash", "all mail"];

const folders = computed(() => {
  const rank = (n: string) => {
    const i = FOLDER_ORDER.indexOf(n);
    return i === -1 ? FOLDER_ORDER.length : i;
  };
  return mailboxes.value
    .filter((m) => m.role !== "inbox" && m.role !== "sent" && m.name !== "inbox")
    .sort((a, b) => rank(a.name) - rank(b.name) || a.name.localeCompare(b.name));
});

const FOLDERS_KEY = "es-side-folders";
const foldersOpen = signal<boolean>((() => {
  try { return typeof localStorage !== "undefined" && localStorage.getItem(FOLDERS_KEY) === "open"; }
  catch { return false; }
})());
function toggleFolders() {
  foldersOpen.value = !foldersOpen.value;
  try { localStorage.setItem(FOLDERS_KEY, foldersOpen.value ? "open" : "closed"); } catch { /* private mode */ }
}

function Item({ href, nav, label, count, here, title, urgent }: {
  href: string; nav: string; label: string; count?: number; here: string; title?: string; urgent?: boolean;
}) {
  const active = here === nav;
  return (
    <a href={href} class={"side-item" + (active ? " active" : "")} title={title} aria-current={active ? "page" : undefined}>
      <span class="side-text">{label}</span>
      {!!count && <span class={"side-count" + (urgent ? " urgent" : "")}>{count}</span>}
    </a>
  );
}

export function Sidebar() {
  const here = path.value ? routeFor(path.value).nav : "";
  const list = folders.value;
  // Sitting in a folder keeps the group open, however it was left.
  const open = foldersOpen.value || here.startsWith("folder:");

  return (
    <aside class="sidebar">
      <nav aria-label="Places">
        <Item href="/today" nav="today" label="Today" here={here} />

        <div class="side-label">Mail</div>
        {placesIn("mail", here).map((p) => (
          <Item key={p.nav} href={p.href} nav={p.nav} label={p.label} title={p.sub} here={here}
            urgent={p.nav === "screener"} count={placeCount(p)} />
        ))}

        <div class="side-label">Sent</div>
        {placesIn("sent", here).map((p) => (
          <Item key={p.nav} href={p.href} nav={p.nav} label={p.label} title={p.sub} here={here} />
        ))}

        {list.length > 0 && (
          <>
            <button class={"side-label side-toggle" + (open ? " open" : "")} type="button"
              aria-expanded={open} aria-controls="side-folders" onClick={toggleFolders}>
              Folders <Icon name="chevron" size={11} />
            </button>
            {open && (
              <div id="side-folders" class="side-group">
                {list.map((f) => (
                  <Item key={f.name} href={folderPath(f.name)} nav={"folder:" + f.name.toLowerCase()}
                    label={folderLabel(f.name)} here={here} />
                ))}
              </div>
            )}
          </>
        )}

        <div class="side-label">People</div>
        <Item href="/people" nav="people" label={place("people").label} title={place("people").sub} here={here} />
      </nav>
    </aside>
  );
}
