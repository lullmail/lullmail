// @vitest-environment jsdom
import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { installKeys } from "./lib/keys";
import { accounts, checked, resetSelection, setList, toggleChecked } from "./lib/store";
import { AccountPicker } from "./Topline";

vi.mock("./lib/actions", () => ({ refreshAccounts: vi.fn(), refreshCounts: vi.fn(), reload: vi.fn() }));

const rows = [0, 1].map((n) => ({
  account: "a", message_id: String(n), thread_id: "t" + n, from: "s@example.test", subject: "S" + n,
  received_at: "2026-10-01T12:00:00Z", preview: "", read: false,
}));
let host: HTMLDivElement;
let off: () => void;
const key = (value: string) => {
  const ev = new KeyboardEvent("keydown", { key: value, bubbles: true, cancelable: true });
  act(() => { (document.activeElement || document).dispatchEvent(ev); });
};

beforeEach(() => {
  accounts.value = [{ id: "a", address: "a@example.test" }, { id: "b", address: "b@example.test" }];
  host = document.createElement("div");
  document.body.append(host);
  act(() => render(<AccountPicker />, host));
  setList({ kind: "rows", key: "t", rows, senders: [], origin: "imbox", loading: false, error: null });
  off = installKeys();
});
afterEach(() => {
  off(); act(() => render(null, host)); host.remove(); resetSelection();
});

describe("account picker keyboard", () => {
  it("takes focus on open, and Escape closes just the menu, not the selection behind it", () => {
    act(() => toggleChecked(rows[0]));
    expect(checked.value.size).toBe(1);
    act(() => { host.querySelector<HTMLButtonElement>(".acct-toggle")!.click(); });
    const first = host.querySelector<HTMLButtonElement>("[role='menuitem']")!;
    expect(document.activeElement).toBe(first);
    key("Escape");
    expect(host.querySelector("[role='menu']")).toBeNull();
    expect(checked.value.size).toBe(1);
    expect(document.activeElement).toBe(host.querySelector(".acct-toggle"));
    key("Escape");
    expect(checked.value.size).toBe(0);
  });

  it("moves between choices with the arrow keys", () => {
    act(() => { host.querySelector<HTMLButtonElement>(".acct-toggle")!.click(); });
    const items = [...host.querySelectorAll<HTMLButtonElement>("[role='menuitem']")];
    key("ArrowDown");
    expect(document.activeElement).toBe(items[1]);
    key("ArrowUp");
    key("ArrowUp");
    expect(document.activeElement).toBe(items[items.length - 1]);
  });
});
