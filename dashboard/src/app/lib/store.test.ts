import { describe, expect, it } from "vitest";
import { rowIdentity } from "./store";

describe("rowIdentity", () => {
  it("keeps provider-local message ids distinct across accounts", () => {
    const first = rowIdentity({ account: "account-a", message_id: "message-1" });
    const second = rowIdentity({ account: "account-b", message_id: "message-1" });
    expect(first).not.toBe(second);
  });
});
