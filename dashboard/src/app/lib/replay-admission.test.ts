// @vitest-environment jsdom
import "fake-indexeddb/auto";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// LUL-D02: offline display admission must not be reused as confirmed
// replay admission. On a reload whose /auth/status read fails, the shell
// synthesizes an authenticated display session from the last known owner
// — the mailbox stays viewable — but the replay driver requires a
// confirmed {owner, generation, epoch} admission published only after a
// successful status read plus committed offline preparation. Queued work
// waits instead of being applied under whatever cookie the browser holds.

const identity = { installation_id: "inst", user_id: "owner-a", email: "a@example.test" };
const DB = "lullmail-offline-v1";
let offline: typeof import("./offline");
let api: typeof import("./api");

const statusResponse = (authenticated: boolean) => new Response(JSON.stringify({
  configured: true,
  authenticated,
  email: authenticated ? identity.email : "",
  bootstrap_available: false,
  passkey_supported: true,
  ...(authenticated ? { installation_id: identity.installation_id, user_id: identity.user_id } : {}),
}), { headers: { "Content-Type": "application/json" } });

async function freshModules(): Promise<void> {
  vi.resetModules();
  offline = await import("./offline");
  api = await import("./api");
}

async function dropDatabase(): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    const request = indexedDB.deleteDatabase(DB);
    request.onsuccess = () => resolve();
    request.onerror = request.onblocked = () => reject(request.error ?? new Error("database blocked"));
  });
}

beforeEach(async () => {
  await freshModules();
  await dropDatabase();
  localStorage.clear();
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("confirmed replay admission", () => {
  it("replayAdmitted requires owner, generation and session epoch to match", () => {
    const admission = { owner: "inst/a", generation: 2, sessionEpoch: 3 };
    expect(offline.replayAdmitted(admission, "inst/a", 2, 3)).toBe(true);
    expect(offline.replayAdmitted(admission, "inst/b", 2, 3)).toBe(false);
    expect(offline.replayAdmitted(admission, "inst/a", 1, 3)).toBe(false);
    expect(offline.replayAdmitted(admission, "inst/a", 2, 4)).toBe(false);
    expect(offline.replayAdmitted(undefined, "inst/a", 2, 3)).toBe(false);
  });

  it("keeps offline display but replays nothing when the status read fails", async () => {
    // A previously confirmed session left account-independent work
    // queued on this device.
    await offline.prepareOfflineOwner(identity);
    await offline.queueMutation("/screener/decide", "POST", { sender: "x@example.test", allow: true }, "key-1");
    expect(offline.replayConfirmed()).toBe(true);

    // The reload: a fresh page whose /auth/status fails at the network
    // level while the browser itself remains online.
    await freshModules();
    const applied = vi.fn();
    vi.stubGlobal("fetch", vi.fn(async (input: unknown) => {
      if (String(input).includes("/auth/status")) throw new TypeError("connection reset");
      applied(String(input));
      return new Response(null, { status: 204 });
    }));
    await expect(api.refreshAuth()).rejects.toThrow();

    // The offline display fallback keeps the mailbox viewable...
    expect(api.unreachable.value).toBe(true);
    expect(api.authed.value).toBe(true);
    // ...but it grants no replay authority: the queued decision is not
    // applied to whoever the cookie now identifies.
    expect(offline.replayConfirmed()).toBe(false);
    expect(await offline.replayMutations()).toEqual({ committed: 0, rejected: 0 });
    expect(applied).not.toHaveBeenCalled();
  });

  it("replays under a freshly confirmed same-owner session", async () => {
    await offline.prepareOfflineOwner(identity);
    await offline.queueMutation("/notes/n1", "PUT", { text: "v" }, "key-2");
    const sent: string[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: unknown) => {
      const url = String(input);
      if (url.includes("/auth/status")) return statusResponse(true);
      sent.push(url);
      return new Response(null, { status: 204 });
    }));
    await api.refreshAuth();
    expect(offline.replayConfirmed()).toBe(true);
    expect(await offline.replayMutations()).toEqual({ committed: 1, rejected: 0 });
    expect(sent).toEqual(["/api/notes/n1"]);
  });

  it("sends the expected-owner namespace on every replayed mutation", async () => {
    await offline.prepareOfflineOwner(identity);
    await offline.queueMutation("/notes/n1", "PUT", { text: "v" }, "key-3");
    const headers: Array<Record<string, string>> = [];
    vi.stubGlobal("fetch", vi.fn(async (input: unknown, init?: RequestInit) => {
      if (String(input).includes("/auth/status")) return statusResponse(true);
      headers.push(init?.headers as Record<string, string>);
      return new Response(null, { status: 204 });
    }));
    expect(await offline.replayMutations()).toEqual({ committed: 1, rejected: 0 });
    expect(headers[0]["X-Lullmail-Owner"]).toBe("inst/owner-a");
  });

  it("retracts admission when the session is rejected (401)", async () => {
    await offline.prepareOfflineOwner(identity);
    expect(offline.replayConfirmed()).toBe(true);
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ title: "Unauthorized" }), { status: 401 })));
    await expect(api.api("/screener")).rejects.toMatchObject({ status: 401 });
    expect(offline.replayConfirmed()).toBe(false);
  });

  it("retracts admission when another tab's owner transition invalidates this one", async () => {
    await offline.prepareOfflineOwner(identity);
    expect(offline.replayConfirmed()).toBe(true);
    vi.stubGlobal("fetch", vi.fn(async () => statusResponse(false)));
    window.dispatchEvent(new Event("lullmail-offline-invalidated"));
    expect(offline.replayConfirmed()).toBe(false);
    expect(api.authed.value).toBe(false);
  });

  it("advances the session epoch on every preparation, so an earlier token cannot authorize a later session", async () => {
    await offline.prepareOfflineOwner(identity);
    const firstConfirmed = offline.replayConfirmed();
    expect(firstConfirmed).toBe(true);
    // Any suspension (a wipe, a failed prepare) revokes admission; the
    // next preparation publishes a NEW epoch-matched token.
    offline.suspendOfflineStorage();
    expect(offline.replayConfirmed()).toBe(false);
    await offline.prepareOfflineOwner(identity);
    expect(offline.replayConfirmed()).toBe(true);
  });
});
