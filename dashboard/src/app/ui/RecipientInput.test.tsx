// @vitest-environment jsdom
import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { RecipientInput, resetCorrespondents, unaddressable } from "./RecipientInput";

const people = vi.hoisted(() => ({ list: [] as { sender: string; allowed: boolean }[], fail: false }));
vi.mock("../lib/api", () => ({
  api: vi.fn(async () => { if (people.fail) throw new Error("offline"); return people.list; }),
}));

let host: HTMLDivElement;
let value = "";
const mount = () => act(() => render(
  <RecipientInput id="to" placeholder="To — comma-separated" value={value}
    onChange={(next) => { value = next; mount(); }} />, host));
const input = () => host.querySelector<HTMLInputElement>("input")!;
const options = () => [...host.querySelectorAll("[role='option']")].map((n) => n.textContent);
const type = async (text: string) => {
  await act(async () => { input().value = text; input().dispatchEvent(new Event("input", { bubbles: true })); await Promise.resolve(); });
};
const key = (name: string) => act(() => { input().dispatchEvent(new KeyboardEvent("keydown", { key: name, bubbles: true, cancelable: true })); });
const focus = async () => { await act(async () => { input().focus(); await Promise.resolve(); await Promise.resolve(); }); };

beforeEach(() => {
  value = ""; people.fail = false;
  people.list = [
    { sender: "Mara Lin <mara@fieldwork.test>", allowed: true },
    { sender: "letters@marginalian.test", allowed: true },
    { sender: "spam@blocked.test", allowed: false },
  ];
  resetCorrespondents();
  host = document.createElement("div"); document.body.append(host);
  mount();
});
afterEach(() => { act(() => render(null, host)); host.remove(); });

describe("unaddressable", () => {
  it("accepts bare and named addresses, and names what cannot be delivered to", () => {
    expect(unaddressable("a@b.co, Mara Lin <mara@fieldwork.test>")).toEqual([]);
    expect(unaddressable("a@b.co, bob, , c@d")).toEqual(["bob", "c@d"]);
    expect(unaddressable("")).toEqual([]);
  });
});

describe("recipient suggestions", () => {
  it("offers people already allowed, matched on the address being typed, and completes with Enter", async () => {
    await focus();
    await type("ma");
    expect(options()).toEqual(["mara@fieldwork.test", "letters@marginalian.test"]);
    expect(input().getAttribute("aria-expanded")).toBe("true");
    key("ArrowDown"); key("ArrowUp"); key("Enter");
    expect(value).toBe("mara@fieldwork.test, ");
    expect(options()).toEqual([]);
  });

  it("completes only the address after the last comma, and never offers one already on the line", async () => {
    await focus();
    await type("mara@fieldwork.test, ma");
    expect(options()).toEqual(["letters@marginalian.test"]);
    key("Tab");
    expect(value).toBe("mara@fieldwork.test, letters@marginalian.test, ");
  });

  it("never suggests a blocked sender, and Escape closes the list without leaving the field", async () => {
    await focus();
    await type("blocked");
    expect(options()).toEqual([]);
    await type("ma");
    const escape = new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true });
    let reachedDocument = false;
    const spy = () => { reachedDocument = true; };
    document.addEventListener("keydown", spy);
    act(() => { input().dispatchEvent(escape); });
    document.removeEventListener("keydown", spy);
    expect(options()).toEqual([]);
    expect(reachedDocument).toBe(false); // the composer must not close with the list
    expect(value).toBe("ma");
  });

  it("works as a plain field when the list cannot be loaded", async () => {
    people.fail = true;
    await focus();
    await type("ma");
    expect(options()).toEqual([]);
    expect(value).toBe("ma");
  });

  it("says what is not an address once the field is left, not while it is being typed", async () => {
    await focus();
    await type("bob");
    expect(host.querySelector(".compose-note")).toBeNull();
    await act(async () => { input().blur(); });
    expect(host.querySelector(".compose-note")!.textContent).toContain("bob");
    expect(input().getAttribute("aria-invalid")).toBe("true");
  });
});
