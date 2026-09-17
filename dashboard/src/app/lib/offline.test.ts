import { describe, expect, it } from "vitest";
import { canQueue, replayDecision } from "./offline";

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
