import { dismissHints, list, showHints, shortcuts } from "../lib/store";
import { path, routeFor } from "../lib/router";
import { Icon } from "./Icon";

/** The interaction model is otherwise entirely invisible: fifteen keys, none of
    them advertised. This teaches the three that matter for whatever is on
    screen, then retires itself once the user clearly knows them. */
function hintsFor(): [string, string][] {
  if (list.value.kind === "senders") {
    return [["j k", "move"], ["1", "Inbox"], ["2", "Reading"], ["3", "Receipts"], ["0", "block"]];
  }
  if (list.value.kind === "rows") {
    return [["j k", "move"], ["↵", "open"], ["e", "done"], ["s", "snooze"], ["x", "select"]];
  }
  const kind = routeFor(path.value).kind;
  if (kind === "notes") return [["2×click", "new note"], ["drag", "move"], ["⌘K", "jump"]];
  if (kind === "calendar") return [["y m w", "year, month, week"], ["← →", "move"], ["t", "today"]];
  if (kind === "accounts" || kind === "people") return [["⌘K", "search & jump"], ["c", "compose"]];
  return [["⌘K", "search & jump"], ["c", "compose"], ["/", "search"]];
}

export function KeyHints() {
  if (!showHints.value) return null;
  return (
    <div class="hints">
      <div class="hints-in">
        {hintsFor().map(([k, label]) => (
          <span class="hint" key={k}>
            <span class="kbd">{k}</span> {label}
          </span>
        ))}
        <button class="hint-link" type="button" onClick={() => { shortcuts.value = true; }}>
          all shortcuts
        </button>
        <button class="btn-icon hint-close" type="button" aria-label="Hide shortcut hints"
          title="Hide these hints" onClick={dismissHints}>
          <Icon name="close" size={13} />
        </button>
      </div>
    </div>
  );
}
