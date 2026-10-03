// @vitest-environment jsdom
import { render } from "preact";
import { Suspense } from "preact/compat";
import { act } from "preact/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { accountCount, accountsFailed } from "./lib/store";
import { path } from "./lib/router";
import { refreshAccounts } from "./lib/actions";
import { RouteSkeleton } from "./ui/bits";

// The Notes chunk is held open by the test so "chunk still loading" is a
// state the test controls, exactly like a cold load over a slow connection.
const chunk = vi.hoisted(() => {
  let release!: () => void;
  const ready = new Promise<void>((resolve) => { release = resolve; });
  return { ready, release, requested: false };
});
const settings = vi.hoisted(() => ({ requested: false }));
const state = vi.hoisted(() => ({ api: vi.fn() }));
vi.mock("./views/NotesView", async () => {
  chunk.requested = true;
  await chunk.ready;
  return { NotesView: () => <div class="wall">the wall</div> };
});
vi.mock("./views/SecurityView", () => {
  settings.requested = true;
  return { SecurityView: () => <div class="security">security</div> };
});
vi.mock("./lib/api", async (original) => ({ ...(await original<typeof import("./lib/api")>()), api: state.api }));

import { CurrentView } from "./App";

let host: HTMLDivElement;
beforeEach(() => {
  path.value = "/notes";
  accountCount.value = null;
  accountsFailed.value = false;
  host = document.createElement("div");
  document.body.append(host);
});
afterEach(() => { act(() => render(null, host)); host.remove(); });

const mount = () => act(async () => { render(<Suspense fallback={<RouteSkeleton />}><CurrentView /></Suspense>, host); });
const settle = () => act(async () => { await new Promise((r) => setTimeout(r, 20)); });

describe("CurrentView cold load of a lazy, welcome-gated route", () => {
  // Regression: the column stayed empty because the lazy view had already
  // suspended when the zero count arrived and swapped it for <Welcome/>.
  it("shows Welcome when the mailbox count resolves to 0 while the chunk is still loading", async () => {
    await mount();
    expect(host.querySelector(".welcome")).toBeNull();
    expect(host.querySelector('[aria-label="Loading page"]')).not.toBeNull();
    expect(chunk.requested).toBe(false); // nothing lazy starts before the count is known
    await act(async () => { accountCount.value = 0; });
    expect(host.querySelector(".welcome")).not.toBeNull();
    chunk.release();
    await settle();
    expect(host.querySelector(".welcome")).not.toBeNull();
    expect(host.querySelector(".wall")).toBeNull();
  });

  it("renders the view once the count says a mailbox exists", async () => {
    await mount();
    await act(async () => { accountCount.value = 2; });
    await vi.waitFor(() => expect(host.querySelector(".wall")).not.toBeNull());
    expect(host.querySelector(".welcome")).toBeNull();
  });

  it("does not wait forever when the mailbox list cannot be fetched", async () => {
    state.api.mockRejectedValueOnce(new Error("network down"));
    await mount();
    expect(host.querySelector('[aria-label="Loading page"]')).not.toBeNull();
    await act(async () => { await refreshAccounts(); });
    expect(accountCount.value).toBeNull();
    expect(accountsFailed.value).toBe(true);
    await vi.waitFor(() => expect(host.querySelector(".wall")).not.toBeNull());
    expect(host.querySelector('[aria-label="Loading page"]')).toBeNull();
  });

  it("never gates settings routes on the count", async () => {
    path.value = "/settings/security";
    await mount();
    await vi.waitFor(() => expect(host.querySelector(".security")).not.toBeNull());
    expect(accountCount.value).toBeNull();
    expect(settings.requested).toBe(true);
  });
});
