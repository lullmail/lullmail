// @vitest-environment jsdom
import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { accounts, counts, mailboxes, moreSheet, screeningEnabled } from "./store";
import { path } from "./router";
import { GOTO, PLACES, place, placeCount, placeOffered, placesIn } from "./places";
import { Sidebar } from "../Sidebar";
import { TabBar } from "../ui/TabBar";
import { MoreMenu } from "../ui/MoreMenu";

vi.mock("./actions", () => ({
  markDone: vi.fn(), markRead: vi.fn(), moveTo: vi.fn(), openThread: vi.fn(),
  pinThreads: vi.fn(), snooze: vi.fn(), trashThreads: vi.fn(),
}));

let host: HTMLDivElement;
const mount = (node: preact.ComponentChild) => act(() => render(node, host));
const labels = (selector: string) => [...host.querySelectorAll(selector)].map((el) => el.textContent!.trim());

beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  path.value = "/";
  counts.value = {};
  accounts.value = [];
  mailboxes.value = [];
  moreSheet.value = false;
  screeningEnabled.value = true;
});
afterEach(() => { act(() => render(null, host)); host.remove(); });

describe("the table of places", () => {
  it("names each place once and gives every g-key one destination", () => {
    expect(new Set(PLACES.map((p) => p.nav)).size).toBe(PLACES.length);
    expect(new Set(PLACES.map((p) => p.href)).size).toBe(PLACES.length);
    const keys = PLACES.filter((p) => p.go).map((p) => p.go);
    expect(new Set(keys).size).toBe(keys.length);
    // The letters people have already learned.
    expect(GOTO).toEqual({
      t: "/today", b: "/board", d: "/calendar", n: "/notes", i: "/",
      s: "/screener", r: "/reading", c: "/receipts", z: "/snoozed", p: "/people",
    });
  });

  it("offers a queue only while it holds something", () => {
    for (const nav of ["screener", "snoozed", "outbox", "outreach"]) expect(placeOffered(place(nav))).toBe(false);
    for (const nav of ["today", "imbox", "feed", "paper_trail", "sent", "people", "settings"]) expect(placeOffered(place(nav))).toBe(true);

    counts.value = { screener: 1, snoozed: 2, outbox: 1 };
    accounts.value = [{ id: "a", address: "out@example.test", outreach: true }] as typeof accounts.value;
    for (const nav of ["screener", "snoozed", "outbox", "outreach"]) expect(placeOffered(place(nav))).toBe(true);
  });

  it("keeps a place offered while you are standing in it, so emptying a list does not pull it away", () => {
    expect(placeOffered(place("snoozed"), "snoozed")).toBe(true);
    expect(placeOffered(place("outbox"), "outbox")).toBe(true);
    expect(placesIn("mail", "snoozed").map((p) => p.nav)).toEqual(["imbox", "feed", "paper_trail", "snoozed"]);
  });

  it("never offers the Screener when screening is off, waiting senders or not", () => {
    counts.value = { screener: 4 };
    screeningEnabled.value = false;
    expect(placeOffered(place("screener"))).toBe(false);
  });

  it("gives Receipts no numeral: nothing there is owed", () => {
    counts.value = { imbox: 3, feed: 2, paper_trail: 40, screener: 1 };
    expect(placeCount(place("imbox"))).toBe(3);
    expect(placeCount(place("feed"))).toBe(2);
    expect(placeCount(place("screener"))).toBe(1);
    expect(placeCount(place("paper_trail"))).toBe(0);
    expect(placeCount(place("snoozed"))).toBe(0);
  });
});

describe("the surfaces that draw from it", () => {
  it("sidebar: an empty install shows only the places that always exist", () => {
    mount(<Sidebar />);
    expect(labels(".side-item .side-text")).toEqual(["Today", "Inbox", "Reading", "Receipts", "Sent", "People"]);
  });

  it("sidebar: queues arrive with their contents, and Receipts carries no count", () => {
    counts.value = { imbox: 3, paper_trail: 40, screener: 2, snoozed: 5, outbox: 1 };
    accounts.value = [{ id: "a", address: "out@example.test", outreach: true }] as typeof accounts.value;
    mount(<Sidebar />);
    expect(labels(".side-item .side-text")).toEqual(
      ["Today", "Inbox", "Screener", "Reading", "Receipts", "Snoozed", "Sent", "Outreach", "Outbox", "People"]);
    const row = (label: string) => [...host.querySelectorAll(".side-item")].find((el) => el.querySelector(".side-text")!.textContent === label)!;
    expect(row("Inbox").querySelector(".side-count")!.textContent).toBe("3");
    expect(row("Screener").querySelector(".side-count.urgent")!.textContent).toBe("2");
    expect(row("Receipts").querySelector(".side-count")).toBeNull();
    expect(row("Snoozed").querySelector(".side-count")).toBeNull();
  });

  it("sidebar: the place you are in stays listed and marked even when it is empty", () => {
    path.value = "/outbox";
    mount(<Sidebar />);
    const here = host.querySelector(".side-item[aria-current='page']")!;
    expect(here.textContent).toContain("Outbox");
  });

  it("phone sheet: the same queues come and go, and the fixed tabs are not repeated in it", () => {
    mount(<TabBar />);
    act(() => (host.querySelector(".tabbar button.tab") as HTMLButtonElement).click());
    expect(labels(".sheet-item-label")).toEqual(["Calendar", "Board", "Notes", "Receipts", "Sent", "People", "Settings"]);

    act(() => { counts.value = { snoozed: 1, outbox: 2 }; });
    expect(labels(".sheet-item-label")).toEqual(["Calendar", "Board", "Notes", "Receipts", "Snoozed", "Sent", "Outbox", "People", "Settings"]);
    // The Screener tab holds its slot whether or not anyone waits: a bar that
    // reshuffles under the thumb is worse than a quiet tab.
    expect(labels(".tabbar .tab-label")).toEqual(["Today", "Inbox", "Screener", "Reading", "More"]);
  });

  it("the … menu lists the places the document top bar leaves out, and none in Classic", () => {
    counts.value = { snoozed: 1 };
    mount(<MoreMenu />);
    act(() => (host.querySelector(".more-menu button") as HTMLButtonElement).click());
    expect(labels("[role='menuitem']").slice(0, 4)).toEqual(["Receipts", "Snoozed", "Sent", "People"]);

    act(() => render(null, host));
    mount(<MoreMenu classic />);
    act(() => (host.querySelector(".more-menu button") as HTMLButtonElement).click());
    expect(labels("[role='menuitem']")[0]).toMatch(/^Appearance/);
  });
});
