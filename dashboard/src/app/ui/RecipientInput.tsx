import { useEffect, useRef, useState } from "preact/hooks";
import type { Ref } from "preact";
import { api } from "../lib/api";
import { splitFrom } from "../lib/fmt";
import type { Person } from "../lib/types";

// An address line that knows who you write to. The value stays what it
// always was — one comma-separated string, typed or pasted freely — so
// drafts, replies and the send path are untouched. What this adds is a short
// list of people you have already decided about, matched against the address
// being typed, and a quiet note when something on the line is not an address.

/** People the owner has screened in, loaded once per page. A failure is just
    an empty list: suggestions are a convenience, never a dependency. */
let known: Promise<string[]> | null = null;
function correspondents(): Promise<string[]> {
  known ||= api<Person[]>("/people")
    .then((people) => [...new Set(people.filter((p) => p.allowed).map((p) => splitFrom(p.sender).email.toLowerCase()).filter(Boolean))])
    .catch(() => { known = null; return []; });
  return known;
}

/** Test seam: forget the cached list. */
export function resetCorrespondents() { known = null; }

const ADDRESS = /^[^\s@<>,]+@[^\s@<>,]+\.[^\s@<>,]+$/;

/** Entries on the line that cannot be delivered to as written. "Name <a@b.c>"
    is fine; the address is what is inside the brackets. */
export function unaddressable(value: string): string[] {
  return value.split(",").map((part) => part.trim()).filter(Boolean)
    .filter((part) => !ADDRESS.test(splitFrom(part).email));
}

/** The address being typed: whatever follows the last comma. */
function typing(value: string): { head: string; token: string } {
  const cut = value.lastIndexOf(",") + 1;
  return { head: value.slice(0, cut), token: value.slice(cut).trim() };
}

export function RecipientInput({ id, value, placeholder, onChange, inputRef }: {
  id: string; value: string; placeholder: string;
  onChange: (next: string) => void; inputRef?: Ref<HTMLInputElement>;
}) {
  const [people, setPeople] = useState<string[]>([]);
  const [focused, setFocused] = useState(false);
  const [dismissed, setDismissed] = useState(false);
  const [active, setActive] = useState(0);
  const alive = useRef(true);
  useEffect(() => () => { alive.current = false; }, []);

  const { head, token } = typing(value);
  const already = new Set(value.toLowerCase().split(",").map((part) => splitFrom(part.trim()).email));
  const matches = focused && !dismissed && token.length > 0
    ? people.filter((email) => email.includes(token.toLowerCase()) && email !== token.toLowerCase() && !already.has(email)).slice(0, 6)
    : [];
  const open = matches.length > 0;
  const at = Math.min(active, matches.length - 1);
  const bad = focused ? [] : unaddressable(value);

  const accept = (email: string) => {
    onChange(head + (head ? " " : "") + email + ", ");
    setActive(0);
  };

  return (
    <div class="compose-recipient">
      <input
        ref={inputRef} id={id}
        class="compose-to" type="text" placeholder={placeholder} autocomplete="off"
        autocapitalize="off" spellcheck={false} inputMode="email"
        role="combobox" aria-autocomplete="list" aria-expanded={open}
        aria-controls={open ? id + "-suggest" : undefined}
        aria-activedescendant={open ? id + "-suggest-" + at : undefined}
        aria-invalid={bad.length > 0 || undefined}
        aria-describedby={bad.length > 0 ? id + "-note" : undefined}
        value={value}
        onInput={(e) => { setDismissed(false); setActive(0); onChange((e.target as HTMLInputElement).value); }}
        onFocus={() => {
          setFocused(true);
          correspondents().then((list) => { if (alive.current) setPeople(list); });
        }}
        onBlur={() => setFocused(false)}
        onKeyDown={(ev) => {
          if (!open) return;
          if (ev.key === "ArrowDown") { ev.preventDefault(); setActive((at + 1) % matches.length); }
          else if (ev.key === "ArrowUp") { ev.preventDefault(); setActive((at - 1 + matches.length) % matches.length); }
          else if (ev.key === "Enter" || ev.key === "Tab") { ev.preventDefault(); accept(matches[at]); }
          else if (ev.key === "Escape") { ev.preventDefault(); ev.stopPropagation(); setDismissed(true); }
        }}
      />
      {open && (
        <ul class="compose-suggest" id={id + "-suggest"} role="listbox">
          {matches.map((email, i) => (
            <li
              key={email} id={id + "-suggest-" + i} role="option" aria-selected={i === at}
              class={i === at ? "active" : undefined}
              // mousedown, not click: a click would land after the blur that closes the list.
              onMouseDown={(ev) => { ev.preventDefault(); accept(email); }}
              onMouseEnter={() => setActive(i)}
            >{email}</li>
          ))}
        </ul>
      )}
      {bad.length > 0 && (
        <p class="compose-note" id={id + "-note"} role="status">
          {bad.length === 1 ? <>“{bad[0]}” isn't a full address.</> : <>{bad.length} entries aren't full addresses.</>}
        </p>
      )}
    </div>
  );
}
