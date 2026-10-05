// @vitest-environment jsdom
import "fake-indexeddb/auto";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { api, ApiError, authed, clearMemoryCache, StaleOwnerError } from "./api";

// The offline owner/generation mirror is driven through the same
// localStorage keys the offline module owns; these tests never prepare a
// real owner, so the in-memory `admitted` marker stays unset and the keys
// are the whole state.
const NS = "lull-offline-ns";
const GEN = "lull-offline-gen";
const SNAPSHOTS = "lull-offline-snapshots";

function jsonResponse(body: unknown, status = 200): Response {
  return {
    status,
    ok: status >= 200 && status < 300,
    json: async () => body,
  } as Response;
}

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem(NS, "inst-1/user-1");
  localStorage.setItem(GEN, "1");
  localStorage.setItem(SNAPSHOTS, "1");
  clearMemoryCache();
  authed.value = true;
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("request method inference (audit 6 F1)", () => {
  it("uses GET for a request with no body", async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse({ rows: [] }));
    vi.stubGlobal("fetch", fetchMock);
    await api("/buckets/imbox");
    const init = fetchMock.mock.calls[0][1] as RequestInit;
    expect(init.method).toBe("GET");
    expect(init.body).toBeUndefined();
    expect((init.headers as Record<string, string>)["Content-Type"]).toBeUndefined();
  });

  it("honors an explicit method with no body", async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse({}, 204));
    vi.stubGlobal("fetch", fetchMock);
    await api("/notes/n1", { method: "DELETE" });
    const init = fetchMock.mock.calls[0][1] as RequestInit;
    expect(init.method).toBe("DELETE");
    expect(init.body).toBeUndefined();
  });

  it("infers POST from a null, false, zero, or empty-string payload, not body truthiness", async () => {
    for (const payload of [null, false, 0, ""]) {
      const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse({}));
      vi.stubGlobal("fetch", fetchMock);
      await api("/notes", { body: payload, method: undefined });
      const init = fetchMock.mock.calls[0][1] as RequestInit;
      expect(init.method).toBe("POST");
      expect(init.body).toBe(JSON.stringify(payload));
      expect((init.headers as Record<string, string>)["Content-Type"]).toBe("application/json");
    }
  });

  it("does not queue or key a GET, but mints an idempotency key for a queueable mutation", async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse({}));
    vi.stubGlobal("fetch", fetchMock);
    await api("/buckets/imbox");
    expect(((fetchMock.mock.calls[0][1] as RequestInit).headers as Record<string, string>)["Idempotency-Key"]).toBeUndefined();
    await api("/messages/m1/action", { body: { bucket: "done" } });
    const headers = (fetchMock.mock.calls[1][1] as RequestInit).headers as Record<string, string>;
    expect(typeof headers["Idempotency-Key"]).toBe("string");
    expect(headers["Idempotency-Key"].length).toBeGreaterThan(0);
  });
});

describe("owner and snapshot fencing stays intact (audit 6 F1 regression)", () => {
  it("rejects a private response when the owner generation changes during the flight", async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => {
      localStorage.setItem(GEN, "2");
      return jsonResponse({ secret: true });
    });
    vi.stubGlobal("fetch", fetchMock);
    await expect(api("/buckets/imbox")).rejects.toBeInstanceOf(StaleOwnerError);
  });

  it("rejects a mutation when the owner generation changes during the flight", async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => {
      localStorage.setItem(GEN, "2");
      return jsonResponse({});
    });
    vi.stubGlobal("fetch", fetchMock);
    await expect(api("/messages/m1/action", { body: { bucket: "done" } })).rejects.toBeInstanceOf(StaleOwnerError);
  });

  it("invalidates a GET when the snapshot generation advances mid-flight", async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => {
      localStorage.setItem(SNAPSHOTS, "2");
      return jsonResponse({ rows: [] });
    });
    vi.stubGlobal("fetch", fetchMock);
    await expect(api("/buckets/imbox")).rejects.toBeInstanceOf(StaleOwnerError);
  });

  it("lets a mutation through when only the snapshot generation advances", async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => {
      localStorage.setItem(SNAPSHOTS, "2");
      return jsonResponse({});
    });
    vi.stubGlobal("fetch", fetchMock);
    await expect(api("/messages/m1/action", { body: { bucket: "done" } })).resolves.toEqual({});
  });

  it("signs the shell out on a protected 401", async () => {
    const fetchMock = vi.fn(async (_url: string, _init?: RequestInit) => jsonResponse({ detail: "unauthorized" }, 401));
    vi.stubGlobal("fetch", fetchMock);
    await expect(api("/buckets/imbox")).rejects.toBeInstanceOf(ApiError);
    expect(authed.value).toBe(false);
  });
});
