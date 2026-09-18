// @vitest-environment jsdom
import { describe, expect, it } from "vitest";

import {
  accountOf, canQueue, generationCurrent, namespaceFor, newMutationKey, offlineOwner,
  queueMutation, replayDecision, replayPlan, replayRequestInit, retryAfterMs, retryDelay,
  suspendOfflineStorage, withReplayLock,
} from "./offline";

describe("replay classification (audit WEB-03)", () => {
  it("counts only 2xx as committed", () => {
    expect(replayDecision(200)).toBe("committed");
    expect(replayDecision(204)).toBe("committed");
    expect(replayDecision(299)).toBe("committed");
  });
  it("treats reauth, throttling and outage as retryable", () => {
    expect(replayDecision(401)).toBe("reauth");
    for (const status of [408, 425, 429, 500, 503]) {
      expect(replayDecision(status)).toBe("retry");
    }
  });
  it("treats permanent 4xx rejections as failed, never committed", () => {
    for (const status of [400, 404, 409, 412, 422]) {
      expect(replayDecision(status)).toBe("failed");
    }
  });
});

describe("queueable mutations", () => {
  it("still matches the supported action surface", () => {
    expect(canQueue("/messages/m1/action", "POST")).toBe(true);
    expect(canQueue("/buckets/imbox", "GET")).toBe(false);
  });
});

describe("retry scheduling (audit 3 WEB-05)", () => {
  it("parses seconds-form and HTTP-date Retry-After", () => {
    const now = Date.parse("2026-09-17T00:00:00Z");
    expect(retryAfterMs(null, now)).toBe(0);
    expect(retryAfterMs("30", now)).toBe(30_000);
    expect(retryAfterMs("2026-09-17T00:00:30Z", now)).toBe(30_000);
    expect(retryAfterMs("garbage", now)).toBe(0);
  });
  it("never backs off shorter than Retry-After", () => {
    for (let i = 0; i < 10; i++) {
      expect(retryDelay(0, "120")).toBeGreaterThanOrEqual(120_000);
    }
  });
  it("bounds exponential backoff with jitter", () => {
    for (let attempts = 0; attempts <= 10; attempts++) {
      const delay = retryDelay(attempts, null);
      const base = Math.min(300_000, 1000 * 2 ** Math.min(attempts, 8));
      expect(delay).toBeGreaterThanOrEqual(base * 0.8);
      expect(delay).toBeLessThanOrEqual(base * 1.2);
    }
  });
});

describe("replay ordering (audit 4 F09)", () => {
  const owner = "owner@example.com";
  it("never lets a newer mutation overtake a backed-off head", () => {
    const plan = replayPlan([
      { owner, queuedAt: 1, nextAttemptAt: 200 },
      { owner, queuedAt: 2 },
    ], owner, 100);
    expect(plan.due).toEqual([]);
    expect(plan.retryAt).toBe(200);
  });
  it("runs due items in queue order and reports the head's deadline", () => {
    const older = { owner, queuedAt: 1, body: "old" };
    const newer = { owner, queuedAt: 2, body: "new" };
    const plan = replayPlan([newer, older, { owner, queuedAt: 3, nextAttemptAt: 150 }], owner, 100);
    expect(plan.due).toEqual([older, newer]);
    expect(plan.retryAt).toBe(150);
  });
  it("ignores other owners and permanently failed entries", () => {
    const plan = replayPlan([
      { owner: "someone-else", queuedAt: 1 },
      { owner, queuedAt: 2, failed: "rejected" },
      { owner, queuedAt: 3 },
    ], owner, 100);
    expect(plan.due).toHaveLength(1);
    expect(plan.due[0].queuedAt).toBe(3);
  });
});

describe("fail-closed suspension (audit 4 F11)", () => {
  it("gates queueMutation with a visible error while suspended", async () => {
    localStorage.setItem("lull-offline-ns", "inst-1/user-1");
    suspendOfflineStorage();
    await expect(queueMutation("/messages/m1/action", "POST", {})).rejects.toThrow(/suspended/i);
  });
  it("reports suspension state", () => {
    expect(offlineOwner()).toBe("inst-1/user-1");
  });
});

describe("namespace + generation fencing (audit WEB-07/R08)", () => {
  it("keys the namespace by installation + user ids, not the email", () => {
    expect(namespaceFor({ installation_id: "abc", user_id: "def", email: "someone@example.com" })).toBe("abc/def");
    expect(namespaceFor({ email: "legacy@example.com" })).toBe("legacy@example.com");
  });
  it("discards results from a stale generation", () => {
    localStorage.setItem("lull-offline-gen", "4");
    expect(generationCurrent(4)).toBe(true);
    expect(generationCurrent(3)).toBe(false);
    localStorage.removeItem("lull-offline-gen");
    expect(generationCurrent(4)).toBe(false);
  });
  it("attributes cached rows to the lensed account", () => {
    expect(accountOf("/buckets/imbox")).toBe("");
    expect(accountOf("/buckets/imbox?account=abc")).toBe("abc");
    expect(accountOf("/buckets/imbox?x=1&account=a%2Fb")).toBe("a/b");
  });
});

describe("idempotency keys (audit WEB-04)", () => {
  it("mints unique keys", () => {
    const seen = new Set<string>();
    for (let i = 0; i < 100; i++) seen.add(newMutationKey());
    expect(seen.size).toBe(100);
  });
  it("sends the queued key as the Idempotency-Key header", () => {
    const init = replayRequestInit({ method: "POST", body: { action: "read" }, key: "key-1" });
    const headers = init.headers as Record<string, string>;
    expect(headers["Idempotency-Key"]).toBe("key-1");
    expect(headers["Content-Type"]).toBe("application/json");
    expect(init.body).toBe(JSON.stringify({ action: "read" }));
  });
  it("omits the header for legacy queue rows minted before the contract", () => {
    const init = replayRequestInit({ method: "DELETE" });
    expect(init.headers).toBeUndefined();
    expect(init.body).toBeUndefined();
  });
});

describe("replay lock (audit WEB-04)", () => {
  it("runs the pass when no lock manager exists", async () => {
    const ran = await withReplayLock(async () => "done");
    expect(ran).toBe("done");
  });
});
