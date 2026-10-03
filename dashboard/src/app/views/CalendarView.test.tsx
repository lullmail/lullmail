// @vitest-environment jsdom
import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { palette } from "../lib/store";
import { CalendarView } from "./CalendarView";

vi.mock("../lib/api", async (original) => ({ ...(await original<typeof import("../lib/api")>()), api: vi.fn().mockResolvedValue({ rows: [], has_more: false }) }));
vi.mock("../lib/actions", () => ({ openThread: vi.fn(), setReloader: vi.fn() }));

let host: HTMLDivElement;
const title = () => host.querySelector(".page-title")!.textContent;
const zoom = () => host.querySelector(".cal-zoom-btn.active")!.textContent;
function key(value: string, init: KeyboardEventInit = {}, target: EventTarget = document.body) {
  const ev = new KeyboardEvent("keydown", { key: value, bubbles: true, cancelable: true, ...init });
  act(() => { target.dispatchEvent(ev); });
  return ev;
}

beforeEach(async () => {
  palette.value = false;
  host = document.createElement("div");
  document.body.append(host);
  await act(async () => { render(<CalendarView />, host); });
});
afterEach(() => { act(() => render(null, host)); host.remove(); });

describe("calendar keys", () => {
  it("y m w switch the zoom and the arrows move by it", () => {
    expect(zoom()).toBe("Month");
    key("y");
    expect(zoom()).toBe("Year");
    key("w");
    expect(zoom()).toBe("Week");
    key("m");
    const before = title();
    expect(key("ArrowRight").defaultPrevented).toBe(true);
    expect(title()).not.toBe(before);
    key("ArrowLeft");
    expect(title()).toBe(before);
  });

  it.each(["ctrlKey", "metaKey", "altKey"])("ignores every calendar key with %s held", (modifier) => {
    const before = title();
    for (const k of ["y", "w", "t", "ArrowRight", "ArrowLeft"]) expect(key(k, { [modifier]: true }).defaultPrevented).toBe(false);
    expect(zoom()).toBe("Month");
    expect(title()).toBe(before);
  });

  it("ignores keys typed into any field, and while an overlay is open", () => {
    for (const html of ["<input>", "<textarea></textarea>", "<select><option>a</option></select>", "<div contenteditable='true'></div>", "<div role='textbox' tabindex='0'></div>"]) {
      const holder = document.createElement("div");
      holder.innerHTML = html;
      document.body.append(holder);
      key("y", {}, holder.firstElementChild!);
      key("ArrowRight", {}, holder.firstElementChild!);
      holder.remove();
    }
    expect(zoom()).toBe("Month");
    const before = title();
    palette.value = true;
    key("y");
    key("ArrowRight");
    palette.value = false;
    expect(zoom()).toBe("Month");
    expect(title()).toBe(before);
  });
});
