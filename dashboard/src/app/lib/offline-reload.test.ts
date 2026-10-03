// @vitest-environment jsdom
import "fake-indexeddb/auto";
import { beforeEach, describe, expect, it, vi } from "vitest";
import * as tab from "./offline";

const identity = { installation_id: "inst", user_id: "a", email: "a@example.test" };
const DB = "lullmail-offline-v1";

beforeEach(async () => {
  await new Promise<void>((resolve, reject) => {
    const request = indexedDB.deleteDatabase(DB);
    request.onsuccess = () => resolve();
    request.onerror = request.onblocked = () => reject(request.error ?? new Error("database blocked"));
  });
  localStorage.clear();
  await tab.prepareOfflineOwner(identity);
});

// A page reload starts a fresh module: drafts are hydrated under the
// persisted namespace first, and only afterwards does the auth round trip
// admit the owner. Admission used to drop the revisions hydration had just
// read, so a restored draft (including one carrying the retry key of a send
// whose answer was lost) could never be saved or sent again.
describe("a reloaded page restoring parked drafts", () => {
  async function reload(): Promise<typeof tab> {
    vi.resetModules();
    return import("./offline");
  }

  it("can save a restored draft after the owner is admitted", async () => {
    await tab.saveDraftFields("restored", { body: "kept across reload", sendKey: "key-1" } as never);
    const page = await reload();
    const [restored] = await page.loadDrafts();
    expect(restored.id).toBe("restored");
    await page.prepareOfflineOwner(identity);
    expect(await page.saveDraftFields("restored", { body: "kept across reload", sendKey: "key-1" } as never)).toBe(true);
    expect(await page.saveDraftFields("restored", { body: "edited after reload" })).toBe(true);
    expect((await tab.loadDrafts())[0].body).toBe("edited after reload");
  });

  it("still refuses a restored draft that another tab edited after this page read it", async () => {
    await tab.saveDraftFields("restored", { body: "original" });
    const page = await reload();
    await page.loadDrafts();
    await page.prepareOfflineOwner(identity);
    await tab.saveDraftFields("restored", { body: "newer other-tab edit" });
    await expect(page.saveDraftFields("restored", { body: "stale reloaded edit" })).rejects.toThrow(/changed in another tab/);
    expect((await tab.loadDrafts())[0].body).toBe("newer other-tab edit");
  });
});
