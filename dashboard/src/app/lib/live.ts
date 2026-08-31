// Live updates over Server-Sent Events. The server publishes a hint after a
// sync finishes; the browser responds by re-reading authoritative state —
// the event itself is never treated as mailbox data. Disconnects are
// expected and silent: native reconnect plus the 30-second visible-tab poll
// cover any gap.
import { refreshAccounts, refreshCounts, reload } from "./actions";

/** Several accounts can finish within milliseconds of each other; one
 *  coalesced re-read serves them all. */
const SYNC_HINT_DEBOUNCE_MS = 150;

let source: EventSource | null = null;
let timer: number | undefined;

/** One hint arrived: schedule the re-read. Exported for testing. */
export function onSyncHint() {
  window.clearTimeout(timer);
  timer = window.setTimeout(() => {
    refreshCounts();
    refreshAccounts();
    reload();
  }, SYNC_HINT_DEBOUNCE_MS);
}

export function startLive() {
  if (source) return;
  source = new EventSource("/api/events");
  source.addEventListener("sync", onSyncHint);
  // No onerror handling by design: reconnect is automatic and the fallback
  // poll keeps the tab correct; an error toast would only add noise.
}

export function stopLive() {
  source?.close();
  source = null;
  window.clearTimeout(timer);
}
