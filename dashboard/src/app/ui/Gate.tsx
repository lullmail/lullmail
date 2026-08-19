import { useState } from "preact/hooks";
import { setToken } from "../lib/api";

/** Dev auth v0 until passkeys land (TASKS 1.2). Still deserves to look like a
    front door rather than a blanked-out page. */
export function Gate() {
  const [value, setValue] = useState("");
  return (
    <div class="gate-wrap">
      <div class="gate">
        <div class="gate-brand">email-soft</div>
        <div class="gate-sub">Your mail, behind one key.</div>
        <form onSubmit={(ev) => { ev.preventDefault(); if (value.trim()) setToken(value.trim()); }}>
          <input
            type="password" placeholder="Access token" autocomplete="current-password" autofocus
            aria-label="Access token"
            value={value} onInput={(e) => setValue((e.target as HTMLInputElement).value)}
          />
          <button class="btn btn-accent" type="submit" disabled={!value.trim()}>Let me in</button>
        </form>
      </div>
    </div>
  );
}
