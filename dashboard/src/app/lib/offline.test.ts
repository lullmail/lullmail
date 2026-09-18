// @vitest-environment jsdom
import { describe, expect, it } from "vitest";

import { canQueue, offlineOwner, queueMutation, replayDecision, replayPlan, retryAfterMs, retryDelay, suspendOfflineStorage } from "./offline";

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
    localStorage.setItem("es-offline-owner", "owner@example.com");
    suspendOfflineStorage();
    await expect(queueMutation("/messages/m1/action", "POST", {})).rejects.toThrow(/suspended/i);
  });
  it("reports suspension state", () => {
    expect(offlineOwner()).toBe("owner@example.com");
  });
});
