// @vitest-environment jsdom
import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Row } from "../lib/types";
import { SenderMenu } from "./SenderMenu";

const calls = vi.hoisted(() => ({
  route: null as string | null | "fail",
  routeSender: [] as unknown[][],
}));
vi.mock("../lib/actions", () => ({
  BUCKET_LABEL: { imbox: "Inbox", feed: "Reading", paper_trail: "Receipts" },
  senderRoute: vi.fn(async () => { if (calls.route === "fail") throw new Error("offline"); return calls.route; }),
  routeSender: vi.fn((...args: unknown[]) => { calls.routeSender.push(args); }),
}));

const row = { account: "acct", message_id: "m1", thread_id: "t1", bucket: "imbox" } as Row;
let host: HTMLDivElement;
const settle = () => act(async () => { await Promise.resolve(); await Promise.resolve(); });
const trigger = () => host.querySelector<HTMLButtonElement>(".sender-menu > button")!;
const items = () => [...host.querySelectorAll<HTMLButtonElement>("[role='menuitemradio']")];
const open = async () => { act(() => trigger().click()); await settle(); };

beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  calls.route = "imbox";
  calls.routeSender = [];
  act(() => render(<SenderMenu sender="news@letters.test" name="The Letter" rows={[row]} />, host));
});
afterEach(() => { act(() => render(null, host)); host.remove(); vi.clearAllMocks(); });

describe("always send this sender to…", () => {
  it("names the sender it is about, in the button and in the menu", async () => {
    expect(trigger().getAttribute("aria-label")).toBe("Always send mail from The Letter to…");
    expect(trigger().getAttribute("aria-expanded")).toBe("false");
    await open();
    expect(trigger().getAttribute("aria-expanded")).toBe("true");
    const head = host.querySelector(".menu-head")!.textContent!;
    expect(head).toContain("The Letter");
    expect(head).toContain("news@letters.test");
    expect(items().map((i) => i.textContent!.trim())).toEqual(["Inbox", "Reading", "Receipts"]);
  });

  it("ticks where their mail goes today and takes focus", async () => {
    await open();
    expect(items().map((i) => i.getAttribute("aria-checked"))).toEqual(["true", "false", "false"]);
    expect(document.activeElement).toBe(items()[0]);
  });

  it("picking another place sets the rule for this thread's sender and hands over what it was", async () => {
    await open();
    act(() => items()[1].click());
    expect(calls.routeSender).toEqual([["news@letters.test", "The Letter", "feed", [row], "imbox"]]);
    expect(host.querySelector("[role='menu']")).toBeNull();
  });

  it("picking the place they already go to changes nothing", async () => {
    await open();
    act(() => items()[0].click());
    expect(calls.routeSender).toEqual([]);
    expect(host.querySelector("[role='menu']")).toBeNull();
  });

  it("a sender with no rule has nothing ticked, and that is what the undo is told", async () => {
    calls.route = null;
    await open();
    expect(items().map((i) => i.getAttribute("aria-checked"))).toEqual(["false", "false", "false"]);
    act(() => items()[2].click());
    expect(calls.routeSender[0][4]).toBeNull();
  });

  it("still works when the current rule cannot be read, without pretending to know it", async () => {
    calls.route = "fail";
    await open();
    expect(items().map((i) => i.getAttribute("aria-checked"))).toEqual(["false", "false", "false"]);
    act(() => items()[1].click());
    expect(calls.routeSender[0][4]).toBeUndefined();
  });

  it("arrows move, Escape closes and gives focus back to the button", async () => {
    await open();
    const key = (k: string) => act(() => { document.activeElement!.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true })); });
    key("ArrowDown");
    expect(document.activeElement).toBe(items()[1]);
    key("ArrowUp"); key("ArrowUp");
    expect(document.activeElement).toBe(items()[2]);
    key("Escape");
    expect(host.querySelector("[role='menu']")).toBeNull();
    expect(document.activeElement).toBe(trigger());
  });
});
